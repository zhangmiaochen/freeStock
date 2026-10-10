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
// ⚠ 这里有三个坑，全是实测踩出来的：
//
// **坑一：必须设一个有效的工作目录。**
// 用户点「开启自动更新」时报过：
//
//	/enable cne-daily-update 失败：exit status 1
//	（ERROR: The system cannot find the path specified.）
//
// 而**同一条命令在 PowerShell 里是 exit 0** —— 问题在环境不在命令：
// `cmd.exe` 对**当前工作目录**敏感，目录无效时报的正是这句话。
// GUI 进程继承来的 CWD 不可控（快捷方式的"起始位置"、被删的目录、
// 构建期间的 bin/）。→ `hiddenCommand` 统一设 Dir。
//
// **坑二：Dir 不能是 System32。**
// 实测 Dir=System32 时 schtasks 输出**中文 GBK**，而 decodeConsole 对非
// UTF-8 是"剥掉非法字节"，中文被剥光后字段全空。→ 用 neutralDir()。
//
// **坑三：不能只依赖 `cmd /c`。**（用户第二次报同样的错之后加的）
// 既然在用户机器上 `cmd /c` 这条路仍然会报"找不到路径"，就不能把功能
// 压在它上面。所以：
//   - 先按老路 `cmd /c "chcp 65001 >nul && schtasks ..."` 试一次（能拿到 UTF-8）
//   - **失败就直接调 schtasks**（不经 cmd，绕开 cmd 对 CWD 的敏感性）
//   - 两条都失败，才把**两条路各自的事实**一起报出来
//
// 最后这点是刻意的：这个错我修过一次没修好，第二次不能再让用户只看到
// 一句"找不到路径" —— 错误里必须带上足够定位的信息。
func runSchtasks(args ...string) ([]byte, error) {
	// ① 经 cmd /c（能强制 UTF-8 输出）
	full := "chcp 65001 >nul && schtasks " + strings.Join(args, " ")
	cmd := hiddenCommand("cmd", "/c", full)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}
	// ⚠ **不是所有非零退出都是"调用失败"。**
	// 查一个不存在的任务时 schtasks 本来就返回非零（"找不到该任务"），
	// 那是**正常的业务结果**，要原样交给上层用 isNotFound 判断。
	// 只有"cmd 根本没跑起来"才该走兜底。
	//（第一版没区分这两者，把"任务不存在"也当成调用失败去重试，
	//  结果 TestQueryMissingTaskIsNotAnError 直接挂了。）
	if !cmdFailedToStart(out) {
		return out, err
	}
	first := strings.TrimSpace(decodeConsole(out))

	// ② 直接调 schtasks（绕开 cmd）
	out2, err2 := hiddenCommand("schtasks", args...).CombinedOutput()
	if err2 == nil {
		return out2, nil
	}
	if !cmdFailedToStart(out2) {
		return out2, err2
	}

	// ③ 两条都失败 —— 把定位所需的事实全放进错误里
	return out2, fmt.Errorf(
		"schtasks 两种调用方式都失败：\n"+
			"  ① cmd /c: %v（%s）\n"+
			"  ② 直接调用: %v（%s）\n"+
			"  诊断：子进程工作目录=%q，进程自身工作目录=%q，cmd 解析为=%q，schtasks 解析为=%q，SystemRoot=%q",
		err, first,
		err2, strings.TrimSpace(decodeConsole(out2)),
		cmd.Dir, selfWorkingDir(),
		lookPathOrEmpty("cmd"), lookPathOrEmpty("schtasks"),
		os.Getenv("SystemRoot"))
}

// cmdFailedToStart 判断一次失败是不是"命令根本没跑起来"。
//
// 只认**启动阶段**的错误：
//   - "The system cannot find the path specified." / 中文版
//   - 或输出完全为空（进程没来得及产出任何东西就死了）
//
// ⚠ 不能简单地用"退出码非零"当判据：schtasks 查不到任务时也返回非零。
func cmdFailedToStart(out []byte) bool {
	text := decodeConsole(out)
	if strings.TrimSpace(text) == "" {
		return true
	}
	lower := strings.ToLower(text)
	// ⚠ 只认**路径**。schtasks 自己的业务错误里也有"系统找不到"，
	// 例如"系统找不到指定的**文件**。"（任务/文件不存在）——
	// 那说明 schtasks 已经跑起来了，不该走兜底重试。
	//（第一版用宽泛的"系统找不到"匹配，把这种也被判成启动失败，
	//  是测试把它抓出来的。）
	return strings.Contains(lower, "cannot find the path") ||
		strings.Contains(text, "找不到指定的路径")
}

// selfWorkingDir 返回本进程的当前工作目录（可能已失效）。
func selfWorkingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "<取不到: " + err.Error() + ">"
	}
	return dir
}

// lookPathOrEmpty 解析可执行文件位置，失败时返回空串。
//
// 放进诊断里是因为：如果应用进程的 PATH 有问题（GUI 启动的进程 PATH 可能
// 不完整），`cmd` / `schtasks` 会解析不到 —— 那是另一类根因。
func lookPathOrEmpty(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return "<找不到: " + err.Error() + ">"
	}
	return p
}

// decodeConsole 把输出规整成字符串。
//
// 走 `cmd /c chcp 65001` 时输出已是 UTF-8，这里是恒等。
// 走了兜底（直接调 schtasks）时输出可能是 **OEM 代码页（GBK）** ——
// 纯 Go 标准库没有 GBK 解码器，所以只能把无法解码的字节丢掉，
// **ASCII 字段名（TaskName / Next Run Time / Status）仍能保住**，
// 中文值（例如"就绪"）会丢。这是兜底路径的已知代价 ——
// 界面上的时间与状态仍可用，只是个别中文值可能为空。
func decodeConsole(raw []byte) string {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if utf8.ValidString(s) {
		return s
	}
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
