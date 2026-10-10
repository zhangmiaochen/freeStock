//go:build windows

package cne

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 计划任务名。
//
// ⚠ **单一所有者原则**（方案 §5.1）：CNEquity 侧**已经装好这两个任务**，
// 形态与本方案一致。freeStock 接管时应当**改这两个，而不是再新建一套** ——
// 两套并存 = 双跑 = 重复取数 + 抢锁。
//
// 同时必须确保 CNEquity 控制台里的「数据自动更新」卡片保持关闭，
// 那是 cne 自带调度器（方案 §2.5 实测它在这台机器上会自我暂停，且会双跑）。
const (
	TaskDaily = "cne-daily-update" // 盘后主日更
	TaskStale = "cne-daily-stale"  // 稍后补抓落后
)

// TaskInfo 是一个计划任务的状态。
type TaskInfo struct {
	Name string `json:"name"`
	// Registered 表示任务是否存在
	Registered bool `json:"registered"`
	// NextRun 是下次运行时间（原样字符串，来自 schtasks）
	NextRun string `json:"nextRun"`
	// Status 是 Ready / Running / Disabled 之类
	Status string `json:"status"`
	// LastResult 是上次运行结果码
	LastResult string `json:"lastResult"`
	// LastRun 是上次运行时间
	LastRun string `json:"lastRun"`
	// Command 是任务实际执行的命令行
	Command string `json:"command"`
	// Enabled 表示任务是启用还是禁用 —— 也就是"自动更新开没开"
	Enabled bool `json:"enabled"`
	// ScheduledState 是原始的 "Scheduled Task State" 字段
	ScheduledState string `json:"scheduledState"`
	// StartWhenAvailable 表示"错过计划时间后是否补跑"。
	// ⚠ 它缺失时任务会在错过时间的那天**静默不跑**（见 schedule_settings_windows.go）
	StartWhenAvailable bool `json:"startWhenAvailable"`
	// StartWhenAvailableKnown 表示上面那个值是否真的读到了
	StartWhenAvailableKnown bool `json:"startWhenAvailableKnown"`
}

// Query 读取一个计划任务的状态。
//
// **只读**，不修改任何东西 —— 界面靠它显示"确实排上了"，
// 而不是只给用户一个开关（方案 §8.1 第 4 点）。
//
// 用 `schtasks /query` 而不是手写 XML：cne 就是栽在
// "自己拼 XML 再回读校验"上（方案 §4.4）。Windows 会省略等于默认值的
// XML 元素，自己拼的 XML 回读时校验不过。
func Query(name string) (*TaskInfo, error) {
	info := &TaskInfo{Name: name}
	out, err := runSchtasks("/query", "/tn", name, "/fo", "LIST", "/v")
	text := decodeConsole(out)
	if err != nil {
		// 找不到任务不是错误 —— 就是"未注册"
		if isNotFound(text) {
			return info, nil
		}
		return info, fmt.Errorf("schtasks query 失败：%v（%s）", err, strings.TrimSpace(text))
	}
	info.Registered = true
	info.NextRun = fieldOf(text, "Next Run Time", "下次运行时间")
	info.Status = fieldOf(text, "Status", "状态")
	info.LastResult = fieldOf(text, "Last Result", "上次运行结果")
	info.LastRun = fieldOf(text, "Last Run Time", "上次运行时间")
	info.Command = fieldOf(text, "Task To Run", "要运行的任务")
	info.ScheduledState = fieldOf(text, "Scheduled Task State", "计划任务状态")
	state := strings.ToLower(strings.TrimSpace(info.ScheduledState))
	info.Enabled = !strings.Contains(state, "disabled") && !strings.Contains(state, "禁用")
	// StartWhenAvailable 的读取不可靠：schtasks 不一定输出该字段，
	// 所以单独标一个 known，让界面区分"关着"和"读不到"
	info.StartWhenAvailable, info.StartWhenAvailableKnown = StartWhenAvailable(name)
	return info, nil
}

// ScheduleState 是界面上要显示的整体状态。
type ScheduleState struct {
	Daily TaskInfo `json:"daily"`
	Stale TaskInfo `json:"stale"`
	// WrapperPath 是任务实际调用的包装脚本（从命令行里解析出来）
	WrapperPath string `json:"wrapperPath"`
	// Summary 是人类可读的一句话
	Summary string `json:"summary"`
	// OwnedByFreeStock 表示这两个任务是否由本项目的设置管理。
	//
	// ⚠ 无法从 schtasks 直接判断"谁创建的"，所以这里只报告**观察到的形态**
	// （任务名与包装脚本），由用户确认 —— 不假装知道所有权。
	Note string `json:"note"`
}

// QuerySchedule 读回两个任务的状态。
func QuerySchedule() (*ScheduleState, error) {
	daily, err := Query(TaskDaily)
	if err != nil {
		return nil, err
	}
	stale, err := Query(TaskStale)
	if err != nil {
		return nil, err
	}
	state := &ScheduleState{Daily: *daily, Stale: *stale}

	// 从命令行里取包装脚本路径（形如 `cmd /c "D:\...\cne-daily.cmd" --stale-only`）
	for _, candidate := range []string{daily.Command, stale.Command} {
		if p := extractScriptPath(candidate); p != "" {
			state.WrapperPath = p
			break
		}
	}
	state.Summary = summarizeSchedule(state)
	state.Note = "这两个任务是 CNEquity 侧已注册的；freeStock 接管时应当**修改它们**，" +
		"而不是新建一套（两套并存会双跑、抢 cne 的写入锁）。" +
		"同时请确认 CNEquity 控制台里的「数据自动更新」保持关闭。"
	return state, nil
}

func summarizeSchedule(state *ScheduleState) string {
	switch {
	case state.Daily.Registered && state.Stale.Registered:
		return fmt.Sprintf("两个任务都已注册：%s 下次 %s；%s 下次 %s",
			state.Daily.Name, orDash(state.Daily.NextRun),
			state.Stale.Name, orDash(state.Stale.NextRun))
	case state.Daily.Registered:
		return fmt.Sprintf("只注册了 %s（下次 %s）；「补抓落后」的任务 %s 未注册",
			state.Daily.Name, orDash(state.Daily.NextRun), state.Stale.Name)
	case state.Stale.Registered:
		return fmt.Sprintf("只注册了 %s；主日更任务 %s 未注册", state.Stale.Name, state.Daily.Name)
	default:
		return "两个计划任务都未注册 —— 应用关着时不会自动更新"
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// ---------------------------------------------------------------------------
// 增删改（副作用操作，只在用户显式调用 ApplySchedule 时执行）
// ---------------------------------------------------------------------------

// CreateTask 注册一个每天固定时间运行包装脚本的计划任务。
//
// cmd 应当指向**固定的包装脚本**（方案 §6），而不是把参数直接塞进任务命令行 ——
// 脚本负责清环境、写日志、解析时间戳，任务定义保持稳定、可读、可复现。
func CreateTask(name, scriptPath, at string) error {
	if strings.TrimSpace(scriptPath) == "" {
		return errors.New("缺少包装脚本路径")
	}
	if !validClock(at) {
		return fmt.Errorf("时间格式应为 HH:MM，实际 %q", at)
	}
	// /tr 里的路径含空格时必须整体加引号
	tr := fmt.Sprintf(`cmd /c "%s"`, scriptPath)
	out, err := runSchtasks("/create", "/tn", name,
		"/tr", tr, "/sc", "DAILY", "/st", at, "/f")
	if err != nil {
		return fmt.Errorf("注册计划任务 %s 失败：%v（%s）", name, err, strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// ChangeTaskTime 改一个已有任务的运行时间。
func ChangeTaskTime(name, at string) error {
	if !validClock(at) {
		return fmt.Errorf("时间格式应为 HH:MM，实际 %q", at)
	}
	out, err := runSchtasks("/change", "/tn", name, "/st", at)
	if err != nil {
		return fmt.Errorf("修改计划任务 %s 失败：%v（%s）", name, err, strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// DeleteTask 删除一个计划任务。任务本来不存在时不算失败。
func DeleteTask(name string) error {
	out, err := runSchtasks("/delete", "/tn", name, "/f")
	if err != nil {
		if isNotFound(decodeConsole(out)) {
			return nil
		}
		return fmt.Errorf("删除计划任务 %s 失败：%v（%s）", name, err, strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// runSchtasks 调用 schtasks。
//
// ⚠ **刻意不经过 `cmd /c`** —— 这是两轮排查的最终结论。
//
// 一开始为了强制 UTF-8 输出，用的是
// `cmd /c "chcp 65001 >nul && schtasks ..."`。它在命令行里跑得好好的，
// 但在 GUI 应用里报：
//
//	/enable cne-daily-update 失败：exit status 1
//	（ERROR: The system cannot find the path specified.）
//
// 同一条命令在 PowerShell 里是 exit 0，所以问题在环境：`cmd.exe` 对
// **当前工作目录**敏感。GUI 进程继承来的 CWD 不可控，而错误信息里
// 完全看不出是这个原因，极难定位。
//
// 中途我试过多种"绕过"办法（设 Dir、加兜底、按有无 schtasks 特征判断
// 是不是启动失败），**全都治标**：真正的问题是我一直在绕 GBK 编码，
// 而项目里本来就有 `golang.org/x/text`。
//
// 现在的做法：**直连 schtasks，把 GBK 正确解码**（见 decodeConsole）。
// 这样既不需要 cmd，也不需要 chcp，那条失败路径就不存在了。
func runSchtasks(args ...string) ([]byte, error) {
	out, err := hiddenCommand("schtasks", args...).CombinedOutput()
	if err == nil {
		return out, nil
	}

	// ⚠ **自愈兜底 + 一次决定性的实验。**
	//
	// 实测：任务确实存在、`schtasks` 与 `Get-ScheduledTask` 在命令行里都正常，
	// 但从应用进程里调用就报 "系统找不到指定的路径"（ERROR_PATH_NOT_FOUND）。
	// 差异只能在应用进程自身 —— 而我给子进程加了两样东西：Dir 和 CREATE_NO_WINDOW。
	//
	// 所以这里用**完全裸的 exec.Command**（不设 Dir、不设任何标志、
	// 不碰 Env）再试一次：
	//   - 能成功 → 功能立刻可用，同时说明问题就出在那两样设置上
	//   - 仍失败 → 说明是进程环境本身（PATH/权限/令牌），此时报错里
	//     带上够定位的事实
	//
	// 这不是"再猜一次"，而是把可能性收敛到一个二选一上。
	plain := exec.Command("schtasks", args...)
	out2, err2 := plain.CombinedOutput()
	if err2 == nil {
		return out2, nil
	}

	// ⚠ **先查一个非常隐蔽、但完全能解释这种现象的原因：完整性级别。**
	//
	// 实测（用户机器）：整个项目目录被打上了 "Low Mandatory Level" 标签，
	// 于是从它启动的进程都是低完整性；而低完整性进程**无法激活需要 Medium
	// 完整性的 COM 服务器** —— schtasks / Get-ScheduledTask 走的就是 COM，
	// 报错恰好是 `ERROR_PATH_NOT_FOUND`（"系统找不到指定的路径"）。
	//
	// 那种报错**完全看不出是权限标签问题**，我为此排查了六轮。
	// 所以在报错前先看一眼：如果是 Low，直接把原因说出来。
	if lv := integrityLevel(); strings.Contains(strings.ToLower(lv), "low") {
		return out, fmt.Errorf(
			"schtasks 失败，而且**根因很可能是完整性级别**：\n"+
				"  当前进程完整性级别 = %s\n"+
				"  低完整性进程无法激活需要 Medium 完整性的 COM 服务器，"+
				"而 schtasks / Get-ScheduledTask 走的正是 COM —— 报错却只是"+
				"「系统找不到指定的路径」，看不出来是这个问题。\n"+
				"  常见原因：**exe 所在目录或 exe 本身被打上了 Low 完整性标签**。\n"+
				"  查：icacls \"<目录>\" | findstr Mandatory\n"+
				"  修：icacls \"<目录>\" /setintegritylevel (OI)(CI)M /T\n"+
				"  （另外：低完整性下 COM、WMI、部分 RPC 都会失败，不只是计划任务）\n"+
				"  原始错误：%v（%s）",
			lv, err, strings.TrimSpace(decodeConsole(out)))
	}
	dir := neutralDir()
	_, statErr := os.Stat(dir)
	lookPath, lookErr := exec.LookPath("schtasks")
	// ⚠ **失败时自动附上完整诊断**。
	//
	// 这个错修了四版都没修对，每一版都在"命令行里能跑、应用里不能跑"之间打转。
	// 根因只能从**应用进程内部**看 —— 所以不再让用户额外点一次「诊断」，
	// 直接把报告塞进错误信息里。
	return out, fmt.Errorf(
		"schtasks 两种调用方式都失败：\n"+
			"  ① hiddenCommand（Dir + CREATE_NO_WINDOW）: %v（%s）\n"+
			"  ② 裸 exec.Command（不设 Dir/标志）: %v（%s）\n"+
			"  Dir=%q（存在性 err=%v），LookPath(schtasks)=%q（err=%v）\n"+
			"\n%s",
		err, strings.TrimSpace(decodeConsole(out)),
		err2, strings.TrimSpace(decodeConsole(out2)),
		dir, statErr, lookPath, lookErr,
		Diagnose())
}

// decodeConsole 把子进程输出转成 UTF-8 字符串。
//
// ⚠ **中文 Windows 上 schtasks 按 GBK（OEM 代码页 936）输出** ——
// 而且不只是"值"是中文，**连字段名都是中文**（`任务名:` / `下次运行时间:` /
// `计划任务状态:`），所以不解码就根本取不到值。
//
// 之前我用 `cmd /c chcp 65001` 强制它输出 UTF-8，结果引出了 cmd 对
// 工作目录敏感那一堆问题（见 runSchtasks 的说明）。现在改成**正确解码 GBK**：
// `golang.org/x/text` 本来就在依赖里（indirect），不需要新增任何东西。
//
// 判断顺序：先看是不是合法 UTF-8（英文系统、或已经转过码的情况），
// 不是再按 GBK 解。
func decodeConsole(raw []byte) string {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if utf8.ValidString(s) {
		return s
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw); err == nil {
		return strings.TrimPrefix(string(decoded), "\ufeff")
	}
	// 兜底：丢掉无法解码的字节，至少保住 ASCII
	return strings.ToValidUTF8(s, "")
}
func isNotFound(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "cannot find") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(text, "找不到") ||
		strings.Contains(text, "不存在")
}

// fieldOf 从 `schtasks /fo LIST /v` 的输出里取一个字段值。
//
// 支持中英文字段名 —— 系统语言不同字段名也不同。
func fieldOf(text, en, zh string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if strings.EqualFold(key, en) || key == zh {
			return strings.TrimSpace(line[idx+1:])
		}
	}
	return ""
}

var scriptPathRe = regexp.MustCompile(`(?i)"?([A-Za-z]:\\[^"]*?\.(?:cmd|bat|exe))"?`)

// extractScriptPath 从任务命令行里取出脚本/可执行文件路径。
func extractScriptPath(command string) string {
	m := scriptPathRe.FindStringSubmatch(command)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// validClock 校验 HH:MM（计划任务对格式敏感，非法值会让注册失败）。
func validClock(v string) bool {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return false
	}
	hour, minute := 0, 0
	if _, err := fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return false
	}
	return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
}
