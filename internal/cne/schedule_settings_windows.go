//go:build windows

package cne

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ⚠ **`StartWhenAvailable` 是"自动更新能不能真的生效"的关键。**
//
// 实测本机两个任务（`cne-daily-update` / `cne-daily-stale`）的 XML 里
// **没有这个元素**。后果：
//
//	17:30 时电脑关着或休眠 → 那天直接跳过，**永远不会补跑**
//
// 而任务是 `Enabled` / `Ready`、时间也正确 —— 表面完全正常，
// 所以这种"静默不跑"极难发现。对一台会关机的台式机来说这是致命的。
//
// 默认值说明：Task Scheduler 里 StartWhenAvailable 默认是 **false**，
// 即"错过了就算了"。设成 true 后，错过的运行会在机器可用时补跑
// （配合下面的 IgnoreNew，不会和正在跑的任务叠加）。
//
// 注意：`schtasks /change` **没有**改这个的开关，必须走 PowerShell 的
// `Set-ScheduledTask`。
const taskSettingsScript = `
$ErrorActionPreference = 'Stop'
$name = $env:FREESTOCK_TASK_NAME
$task = Get-ScheduledTask -TaskName $name
$s = $task.Settings
$s.StartWhenAvailable = $true
$s.DisallowStartIfOnBatteries = $false
$s.StopIfGoingOnBatteries = $false
$s.MultipleInstances = 'IgnoreNew'
Set-ScheduledTask -TaskName $name -Settings $s | Out-Null
`

// ApplyRecommendedSettings 把两个任务改成"错过也能补跑"。
//
// ⚠ 这**只改上面四项设置**，不动触发器（时间）、不动要执行的命令 ——
// 用户选定的边界是"只改现有两个任务"，这里仍然遵守：
// 不新建、不删除，也不改它的时间与命令行（时间另有 ChangeTaskTime）。
func ApplyRecommendedSettings(taskName string) error {
	if !isKnownTask(taskName) {
		return fmt.Errorf("只允许改本项目的两个任务，收到 %q", taskName)
	}
	cmd := hiddenCommand("powershell", "-NoProfile", "-NonInteractive",
		"-Command", taskSettingsScript)
	cmd.Env = append(buildTaskEnv(), "FREESTOCK_TASK_NAME="+taskName)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	// ⚠ 与 runSchtasks 同样的完整性预检：低完整性下 COM 不可用，
	// Get-ScheduledTask 会报"系统找不到指定的路径"，完全看不出真实原因。
	if lv := integrityLevel(); strings.Contains(strings.ToLower(lv), "low") {
		return fmt.Errorf(
			"调整 %s 的设置失败，而且**根因很可能是完整性级别**：\n"+
				"  当前进程完整性级别 = %s（低完整性无法激活 COM 服务器）\n"+
				"  修：icacls \"<项目目录>\" /setintegritylevel (OI)(CI)M /T\n"+
				"  原始错误：%v（%s）",
			taskName, lv, err, strings.TrimSpace(decodeConsole(out)))
	}

	// 与 runSchtasks 同样加一次**裸调用**兜底（不设 Dir、不设标志、不碰 Env）。
	// PowerShell 的 Get-ScheduledTask 之前也报 ERROR_PATH_NOT_FOUND(0x80070003)，
	// 所以它和 schtasks 很可能是同一个原因。
	plain := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", taskSettingsScript)
	plain.Env = append(os.Environ(), "FREESTOCK_TASK_NAME="+taskName)
	if out2, err2 := plain.CombinedOutput(); err2 == nil {
		return nil
	} else if strings.TrimSpace(decodeConsole(out2)) != "" {
		return fmt.Errorf("调整 %s 的设置失败：\n  ① hiddenCommand: %v（%s）\n  ② 裸调用: %v（%s）",
			taskName, err, strings.TrimSpace(decodeConsole(out)),
			err2, strings.TrimSpace(decodeConsole(out2)))
	}

	return fmt.Errorf("调整 %s 的设置失败：%v（%s）\n\n%s",
		taskName, err, strings.TrimSpace(decodeConsole(out)), Diagnose())
}

// SetTaskEnabled 启用/禁用一个任务。
//
// 关了之后应用不运行时也不会自动更新 —— 这就是"自动更新开关"的实现。
func SetTaskEnabled(taskName string, enabled bool) error {
	if !isKnownTask(taskName) {
		return fmt.Errorf("只允许改本项目的两个任务，收到 %q", taskName)
	}
	verb := "/disable"
	if enabled {
		verb = "/enable"
	}
	out, err := runSchtasks("/change", "/tn", taskName, verb)
	if err != nil {
		return fmt.Errorf("%s %s 失败：%v（%s）", verb, taskName, err,
			strings.TrimSpace(decodeConsole(out)))
	}
	return nil
}

// TaskEnabled 判断任务当前是启用还是禁用。
//
// `schtasks /query /fo LIST /v` 的 "Scheduled Task State" 字段是
// Enabled / Disabled —— 这是"自动更新开没开"的直接依据。
func TaskEnabled(taskName string) (bool, error) {
	info, err := queryVerbose(taskName)
	if err != nil {
		return false, err
	}
	if !info.registered {
		return false, fmt.Errorf("任务 %s 不存在", taskName)
	}
	state := strings.ToLower(strings.TrimSpace(info.scheduledState))
	switch {
	case strings.Contains(state, "disabled") || strings.Contains(state, "禁用"):
		return false, nil
	case strings.Contains(state, "enabled") || strings.Contains(state, "启用"):
		return true, nil
	}
	return false, fmt.Errorf("读不出 %s 的启用状态（Scheduled Task State=%q）", taskName, info.scheduledState)
}

// taskState 是 queryVerbose 的原始结果（比 TaskInfo 多几个字段）。
type taskState struct {
	registered     bool
	scheduledState string
	startWhenAvail string
}

func queryVerbose(taskName string) (*taskState, error) {
	out, err := runSchtasks("/query", "/tn", taskName, "/fo", "LIST", "/v")
	text := decodeConsole(out)
	state := &taskState{}
	if err != nil {
		if isNotFound(text) {
			return state, nil
		}
		return state, fmt.Errorf("schtasks query 失败：%v（%s）", err, strings.TrimSpace(text))
	}
	state.registered = true
	state.scheduledState = fieldOf(text, "Scheduled Task State", "计划任务状态")
	state.startWhenAvail = fieldOf(text, "Start When Available", "如果错过计划开始时间则尽快启动")
	return state, nil
}

// StartWhenAvailable 读回"错过计划时间后是否补跑"。
//
// ⚠ **必须从任务的 XML 读，不能从 `schtasks /query /fo LIST /v` 读** ——
// 实测后者根本不输出这个字段（返回空），会被误判成"未设置"或"已设置"。
// 而这一位恰恰是"自动更新会不会静默不跑"的关键。
//
// 第二个返回值恒为 true：只要能读到 XML，结论就是确定的 ——
// `<StartWhenAvailable>` 元素**不存在就等于默认值 false**（Windows 会省略
// 等于默认值的元素，这也是方案 §4.4 里 cne 栽跟头的原因）。
func StartWhenAvailable(taskName string) (value bool, known bool) {
	out, err := runSchtasks("/query", "/tn", taskName, "/xml")
	if err != nil {
		return false, false
	}
	xml := decodeConsole(out)
	if strings.TrimSpace(xml) == "" || isNotFound(xml) {
		return false, false
	}
	// 元素不存在 = 默认 false（不是"读不到"）
	lower := strings.ToLower(xml)
	idx := strings.Index(lower, "<startwhenavailable>")
	if idx < 0 {
		return false, true
	}
	rest := lower[idx+len("<startwhenavailable>"):]
	end := strings.Index(rest, "</startwhenavailable>")
	if end < 0 {
		return false, true
	}
	return strings.Contains(rest[:end], "true"), true
}

func isKnownTask(name string) bool {
	return name == TaskDaily || name == TaskStale
}

// buildTaskEnv 给 PowerShell 子进程的环境。
//
// 复用 BuildEnv（它负责清代理变量），但**不要**加 PYTHONUTF8 那三个 ——
// 那是给 cne 的，对 PowerShell 无害但没意义；这里保持最小侵入。
func buildTaskEnv() []string {
	return BuildEnv()
}
