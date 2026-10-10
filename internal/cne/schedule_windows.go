//go:build windows

package cne

import (
	"errors"
	"fmt"
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

// runSchtasks 调用 schtasks，并**强制它输出 UTF-8**。
//
// ⚠ 中文 Windows 上 schtasks 按 **OEM 代码页（GBK）** 输出字段名与值，
// 直接当 UTF-8 读会变乱码（实测出现 `鍛ㄥ叚` 那种）。
// 纯 Go 标准库没有 GBK 解码器，而为此引一个依赖不值得 ——
// 直接用 `chcp 65001` 让它输出 UTF-8 即可（方案 §6 的包装脚本也是这么做的）。
//
// 注意：`chcp` 的输出本身也要一起丢掉，只取 schtasks 的部分。
func runSchtasks(args ...string) ([]byte, error) {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, a)
	}
	// cmd /c "chcp 65001 >nul && schtasks <args...>"
	full := "chcp 65001 >nul && schtasks " + strings.Join(quoted, " ")
	return exec.Command("cmd", "/c", full).CombinedOutput()
}

// decodeConsole 把输出规整成字符串。
//
// 已经用 chcp 65001 强制 UTF-8，所以这里基本是恒等；仍保留一层兜底 ——
// 万一某台机器上 chcp 没生效，至少不会 panic，只是字段可能取不到。
func decodeConsole(raw []byte) string {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if utf8.ValidString(s) {
		return s
	}
	// 非 UTF-8：丢掉无法解码的字节，保住 ASCII 字段名（Next Run Time 等）
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
