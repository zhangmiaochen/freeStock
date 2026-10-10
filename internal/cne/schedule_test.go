//go:build windows

package cne

import (
	"strings"
	"testing"
)

// **用真实计划任务验证 Query**。
//
// 这台机器上 CNEquity 侧已经注册好两个任务（方案 §12）：
//
//	cne-daily-update   每天 17:30
//	cne-daily-stale    每天 21:00
//
// 这个用例直接读它们 —— 只读，不改任何东西。
func TestQueryRealSchedule(t *testing.T) {
	if testing.Short() {
		t.Skip("要调 schtasks，短模式跳过")
	}
	state, err := QuerySchedule()
	if err != nil {
		t.Fatalf("读计划任务失败：%v", err)
	}

	t.Logf("包装脚本：%s", orDash(state.WrapperPath))
	for _, ti := range []TaskInfo{state.Daily, state.Stale} {
		if !ti.Registered {
			t.Logf("  %-18s 未注册", ti.Name)
			continue
		}
		t.Logf("  %-18s 下次 %s  状态 %s  上次结果 %s",
			ti.Name, orDash(ti.NextRun), orDash(ti.Status), orDash(ti.LastResult))
		if ti.Command != "" {
			t.Logf("  %-18s 命令 %s", "", ti.Command)
		}
	}
	t.Logf("结论：%s", state.Summary)
	t.Logf("说明：%s", state.Note)

	// 硬断言：读出来的必须是**可解析**的内容，不能是乱码或空
	if state.Daily.Registered {
		if strings.TrimSpace(state.Daily.NextRun) == "" {
			t.Error("任务已注册却取不到「下次运行时间」—— 字段解析或编码有问题（" +
				"中文 Windows 上 schtasks 按 GBK 输出，已用 chcp 65001 强制 UTF-8）")
		}
		if strings.Contains(state.Daily.NextRun, "锟") || strings.Contains(state.Daily.NextRun, "鍛") {
			t.Errorf("「下次运行时间」出现乱码：%q —— 编码处理没生效", state.Daily.NextRun)
		}
		if !strings.Contains(state.Daily.NextRun, ":") {
			t.Errorf("「下次运行时间」不像时间：%q", state.Daily.NextRun)
		}
	}
	if state.WrapperPath == "" && (state.Daily.Registered || state.Stale.Registered) {
		t.Error("任务已注册却解析不出包装脚本路径 —— extractScriptPath 没匹配上")
	}
	if state.WrapperPath != "" && !strings.Contains(strings.ToLower(state.WrapperPath), ".cmd") &&
		!strings.Contains(strings.ToLower(state.WrapperPath), ".bat") {
		t.Logf("注意：包装脚本不是 .cmd/.bat：%s", state.WrapperPath)
	}
}

// 查一个不存在的任务：应当返回"未注册"，而不是报错。
//
// 这条很重要 —— 界面靠它区分"没排上"和"查询失败"，
// 如果把两者都当错误弹出来，用户会以为功能坏了。
func TestQueryMissingTaskIsNotAnError(t *testing.T) {
	info, err := Query("cne-definitely-not-registered-xyz")
	if err != nil {
		t.Fatalf("查不存在的任务不该报错：%v", err)
	}
	if info.Registered {
		t.Error("不存在的任务不该报告已注册")
	}
}

// 时间校验：非法值必须被拒 —— 计划任务对格式敏感，注册失败会很难查。
func TestValidClockRejectsBadInput(t *testing.T) {
	good := []string{"00:00", "17:30", "23:59", "9:05"}
	bad := []string{"", "1730", "24:00", "12:60", "-1:00", "abc", "12:34:56", "12"}
	for _, v := range good {
		if !validClock(v) {
			t.Errorf("validClock(%q) 应为 true", v)
		}
	}
	for _, v := range bad {
		if validClock(v) {
			t.Errorf("validClock(%q) 应为 false", v)
		}
	}
}

// 从命令行里解析脚本路径。
func TestExtractScriptPath(t *testing.T) {
	cases := map[string]string{
		`cmd /c "D:\workspace\stockTool\CNEquity\cne-daily.cmd"`: `D:\workspace\stockTool\CNEquity\cne-daily.cmd`,
		`cmd /c "D:\a b\cne-daily.cmd" --stale-only`:             `D:\a b\cne-daily.cmd`,
		`"D:\softInstal\python3.11.3\Scripts\cne.exe" run daily`: `D:\softInstal\python3.11.3\Scripts\cne.exe`,
		``:                  "",
		`cmd /c echo hello`: "",
	}
	for in, want := range cases {
		if got := extractScriptPath(in); got != want {
			t.Errorf("extractScriptPath(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 字段解析要能吃下中英文两种字段名。
func TestFieldOfHandlesBothLanguages(t *testing.T) {
	en := "TaskName:      \\cne-daily-update\r\nNext Run Time: 2026/10/10 17:30:00\r\nStatus:        Ready\r\n"
	if got := fieldOf(en, "Next Run Time", "下次运行时间"); got != "2026/10/10 17:30:00" {
		t.Errorf("英文字段解析失败：%q", got)
	}
	if got := fieldOf(en, "Status", "状态"); got != "Ready" {
		t.Errorf("Status 解析失败：%q", got)
	}
	zh := "任务名:      \\cne-daily-update\r\n下次运行时间: 2026/10/10 17:30:00\r\n状态:         就绪\r\n"
	if got := fieldOf(zh, "Next Run Time", "下次运行时间"); got != "2026/10/10 17:30:00" {
		t.Errorf("中文字段解析失败：%q", got)
	}
}
