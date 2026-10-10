//go:build windows

package cne

import (
	"strings"
	"testing"
)

// **用真实任务验证"自动更新到底开没开、错过会不会补跑"**（只读）。
//
// 这两条是"自动更新能不能真的生效"的关键：
//   - Enabled 决定任务会不会跑
//   - StartWhenAvailable 决定**错过时间的那天会不会补跑**
//
// 实测本机 XML 里**没有** StartWhenAvailable —— 后果是 17:30 时电脑
// 关着就永远跳过那天。而任务显示 Enabled / Ready、时间也对，极难发现。
func TestRealTaskEnabledAndStartWhenAvailable(t *testing.T) {
	state, err := QuerySchedule()
	if err != nil {
		t.Skipf("读不到计划任务：%v", err)
	}
	if !state.Daily.Registered && !state.Stale.Registered {
		t.Skip("两个任务都没注册")
	}

	for _, ti := range []TaskInfo{state.Daily, state.Stale} {
		if !ti.Registered {
			t.Logf("%-18s 未注册", ti.Name)
			continue
		}
		t.Logf("%-18s Enabled=%v  ScheduledState=%q  StartWhenAvailable=%v(known=%v)",
			ti.Name, ti.Enabled, ti.ScheduledState, ti.StartWhenAvailable, ti.StartWhenAvailableKnown)
		if !ti.Enabled {
			t.Errorf("**%s 是禁用的 —— 自动更新不会发生**", ti.Name)
		}
		if !ti.StartWhenAvailableKnown {
			t.Logf("  注意：读不到 StartWhenAvailable（schtasks 不保证输出该字段）")
		} else if !ti.StartWhenAvailable {
			t.Logf("  ⚠ **错过时间后不会补跑** —— 17:30/21:00 时机器关着就会跳过那天")
		}
	}
}

// TaskEnabled 直接读启用状态，与 TaskInfo.Enabled 应当一致。
func TestTaskEnabledConsistent(t *testing.T) {
	state, err := QuerySchedule()
	if err != nil {
		t.Skipf("读不到计划任务：%v", err)
	}
	if !state.Daily.Registered {
		t.Skip("主日更任务未注册")
	}
	enabled, err := TaskEnabled(TaskDaily)
	if err != nil {
		t.Fatalf("读启用状态失败：%v", err)
	}
	if enabled != state.Daily.Enabled {
		t.Errorf("两处读到的启用状态不一致：TaskEnabled=%v TaskInfo.Enabled=%v",
			enabled, state.Daily.Enabled)
	}
	t.Logf("%s 当前：%s", TaskDaily, map[bool]string{true: "已启用", false: "已禁用"}[enabled])
}

// ⚠ **只允许改本项目的两个任务** —— 保护用户机器上其他 200+ 个任务。
func TestMutatingHelpersRejectForeignTasks(t *testing.T) {
	foreign := []string{
		"", "cne-daily-update2", "\\cne-daily-update",
		"Microsoft\\Windows\\Defrag\\ScheduledDefrag",
		"cne-daily-update; whoami",
	}
	for _, name := range foreign {
		if err := SetTaskEnabled(name, false); err == nil {
			t.Errorf("SetTaskEnabled(%q) 应当被拒绝 —— 否则可能误改用户的其它任务", name)
		}
		if err := ApplyRecommendedSettings(name); err == nil {
			t.Errorf("ApplyRecommendedSettings(%q) 应当被拒绝", name)
		}
	}
	// 本项目的两个任务应当被接受（这里只校验"不被拒绝为非法名字"，
	// 不实际执行 —— 那会改用户机器）
	for _, name := range []string{TaskDaily, TaskStale} {
		if !isKnownTask(name) {
			t.Errorf("%q 应当被认作本项目的任务", name)
		}
	}
}

// 状态字段解析：Enabled / Disabled 两种写法都要认。
func TestScheduledStateParsing(t *testing.T) {
	cases := map[string]bool{
		"Enabled":  true,
		"Disabled": false,
		"启用":       true,
		"禁用":       false,
	}
	for raw, wantEnabled := range cases {
		state := strings.ToLower(raw)
		got := !strings.Contains(state, "disabled") && !strings.Contains(state, "禁用")
		if got != wantEnabled {
			t.Errorf("state %q 解析成 enabled=%v，期望 %v", raw, got, wantEnabled)
		}
	}
}
