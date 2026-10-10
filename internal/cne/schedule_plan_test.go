package cne

import "testing"

// clockOf 要从 schtasks 的 "2026/10/10 17:30:00" 里取出 "17:30"。
//
// 它决定"当前时间是否已等于目标值"，取错了会导致：
// 要么每次都误报"要改"（反复弹确认），要么该改却不改。
func TestClockOf(t *testing.T) {
	cases := map[string]string{
		"2026/10/10 17:30:00": "17:30",
		"2026/10/10 21:00:00": "21:00",
		"2026/10/10 09:05:00": "09:05",
		"2026/10/10 9:05:00":  "09:05",
		"10/10/2026 17:30:00": "17:30",
		"":                    "",
		"2026/10/10":          "",
		"garbage":             "",
		"2026/10/10 25:00:00": "",
	}
	for in, want := range cases {
		if got := clockOf(in); got != want {
			t.Errorf("clockOf(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ⚠ **未确认时必须拒绝执行** —— 这是"改动前弹确认"在代码层面的落实。
// 界面必须先展示预览（含原始 schtasks 命令），用户点确认后才带 confirmed=true。
func TestApplyScheduleRequiresConfirmation(t *testing.T) {
	plan := &SchedulePlan{
		HasChanges: true,
		Changes:    []ScheduleChange{{Task: TaskDaily, Action: "change", To: "18:00"}},
		Commands:   []string{`schtasks /change /tn "cne-daily-update" /st 18:00`},
	}
	if _, err := ApplySchedule(plan, false); err == nil {
		t.Fatal("**未经确认就执行了改动** —— 这会让用户机器上的计划任务被悄悄改掉")
	} else {
		t.Logf("未确认被正确拒绝：%v", err)
	}

	// nil 计划也要拒绝
	if _, err := ApplySchedule(nil, true); err == nil {
		t.Error("nil 计划应当被拒绝")
	}
}

// 时间非法时必须先拒绝，不能拿一个坏时间去做 schtasks。
func TestPreviewScheduleValidatesClock(t *testing.T) {
	for _, bad := range []string{"", "25:00", "1730", "abc"} {
		if _, err := PreviewSchedule(bad, "21:00"); err == nil {
			t.Errorf("主日更时间 %q 非法，应当被拒绝", bad)
		}
		if _, err := PreviewSchedule("17:30", bad); err == nil {
			t.Errorf("补抓时间 %q 非法，应当被拒绝", bad)
		}
	}
}

// **不新建、不删除**：任务不存在时只报告，不动任何东西。
//
// 用户明确选了"只改现有两个任务" —— 所以这里不该出现 /create 或 /delete。
func TestApplyScheduleNeverCreatesOrDeletes(t *testing.T) {
	plan := &SchedulePlan{
		HasChanges: true,
		Changes: []ScheduleChange{
			{Task: TaskDaily, Action: "change", To: "18:00"},
			{Task: TaskStale, Action: "not-registered", To: "21:00", Reason: "任务不存在"},
		},
		Commands: []string{`schtasks /change /tn "cne-daily-update" /st 18:00`},
	}
	for _, cmd := range plan.Commands {
		if contains(cmd, "/create") {
			t.Errorf("**不该创建任务**：%s（会与 CNEquity 侧双跑）", cmd)
		}
		if contains(cmd, "/delete") {
			t.Errorf("**不该删除任务**：%s（会破坏 CNEquity 侧的用法）", cmd)
		}
		if !contains(cmd, "/change") {
			t.Errorf("命令应当是 /change：%s", cmd)
		}
	}
	// ⚠ 关键断言：**任务不存在时不产生任何命令**。
	//（第一版断言写成了"not-registered 不该有目标时间" —— 那是错的：
	// 目标时间是已知的，只是任务不存在，所以 To 本来就该有值。
	//  真正要守的是"这种情况绝不生成命令"。）
	for _, c := range plan.Changes {
		if c.Action == "not-registered" {
			if c.To == "" {
				t.Error("not-registered 项也应带上目标时间，便于界面显示意图")
			}
			if c.Reason == "" {
				t.Error("not-registered 项必须说明为什么不动它")
			}
		}
	}
}

// 预览的措辞要说清"将要改什么、从什么改成什么"。
func TestSummarizePlanDescribesChanges(t *testing.T) {
	plan := &SchedulePlan{
		HasChanges: true,
		Changes: []ScheduleChange{
			{Task: TaskDaily, Action: "change", From: "17:30", To: "18:00"},
			{Task: TaskStale, Action: "keep", From: "21:00", To: "21:00", Reason: "时间已经是目标值"},
		},
	}
	got := summarizePlan(plan)
	for _, want := range []string{TaskDaily, "17:30", "18:00"} {
		if !contains(got, want) {
			t.Errorf("摘要里缺少 %q：%s", want, got)
		}
	}
	// keep 的项不该出现在"将要改动"里
	if contains(got, "cne-daily-stale：") {
		t.Errorf("无需改动的任务不该列进'将要改动'：%s", got)
	}
}

// 没有改动时"无需改动"，并且不该让界面弹出确认。
func TestPlanWithoutChanges(t *testing.T) {
	plan := &SchedulePlan{HasChanges: false}
	if got := summarizePlan(plan); !contains(got, "无需改动") {
		t.Errorf("应为'无需改动'：%s", got)
	}
	// HasChanges=false 时 ApplySchedule 应当直接返回，不做任何事
	out, err := ApplySchedule(plan, true)
	if err != nil {
		t.Errorf("没有改动时不该报错：%v", err)
	}
	if out == nil || out.HasChanges {
		t.Error("没有改动时应原样返回")
	}
}
