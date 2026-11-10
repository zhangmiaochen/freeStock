package cne

import (
	"errors"
	"fmt"
	"strings"
)

// ScheduleChange 是一处计划任务改动。
type ScheduleChange struct {
	Task string `json:"task"`
	// Action: change（要改时间）/ keep（时间已对）/ not-registered（任务不存在）
	Action string `json:"action"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// SchedulePlan 是**将要执行的改动预览**。
//
// ⚠ 用户明确要求："只改现有两个任务，且改动前弹确认"。
// 所以这里分成两步：
//
//	PreviewSchedule —— 只算、只查，绝不执行，把要跑的命令原样摆出来给人看
//	ApplySchedule   —— 只有 confirmed=true 才执行，且只执行 Action=="change"
//
// **不新建、不删除**（因此没有 CreateTask/DeleteTask 的调用点）。
// 理由（方案 §5.1）：CNEquity 侧已经装好这两个任务，再建一套就是双跑 +
// 抢 cne 的写入锁；而删掉它则会破坏 CNEquity 自己的用法。
type SchedulePlan struct {
	Changes []ScheduleChange `json:"changes"`
	// Commands 是**将要执行的原始 schtasks 命令**，给用户在确认框里看。
	// 界面上原样展示 —— 让用户知道自己机器上会发生什么。
	Commands []string `json:"commands"`
	// Warnings 是不阻塞但需要知道的问题
	Warnings []string `json:"warnings"`
	// HasChanges 表示是否有实际改动要做
	HasChanges bool `json:"hasChanges"`
	// Summary 是人类可读的一句话
	Summary string `json:"summary"`
}

// PreviewSchedule 算出把两个任务改到目标时间需要做什么，**不执行任何操作**。
func PreviewSchedule(dailyAt, staleAt string) (*SchedulePlan, error) {
	plan := &SchedulePlan{}
	if !validClock(dailyAt) {
		return nil, fmt.Errorf("主日更时间格式应为 HH:MM，实际 %q", dailyAt)
	}
	if !validClock(staleAt) {
		return nil, fmt.Errorf("补抓落后时间格式应为 HH:MM，实际 %q", staleAt)
	}

	targets := []struct {
		task string
		at   string
		desc string
	}{
		{TaskDaily, dailyAt, "主日更"},
		{TaskStale, staleAt, "补抓落后"},
	}

	for _, t := range targets {
		info, err := Query(t.task)
		if err != nil {
			return nil, err
		}
		change := ScheduleChange{Task: t.task, To: t.at}

		if !info.Registered {
			// ⚠ **不新建**。任务不存在时只报告 —— 建一套新的会和
			// CNEquity 侧的安排冲突，而且要由人来决定该不该建。
			change.Action = "not-registered"
			change.Reason = fmt.Sprintf("任务不存在。本功能**不会创建**它 —— "+
				"请先用 CNEquity 侧或 schtasks 建好%s任务，或改这里的设置去匹配已有任务。", t.desc)
			plan.Changes = append(plan.Changes, change)
			plan.Warnings = append(plan.Warnings, change.Reason)
			continue
		}

		current := clockOf(info.NextRun)
		change.From = current
		if current == "" {
			// 取不到当前时间 → 不敢盲改
			change.Action = "keep"
			change.Reason = "读不到当前运行时间，为避免误改而不动它"
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s：%s", t.task, change.Reason))
			plan.Changes = append(plan.Changes, change)
			continue
		}
		if current == t.at {
			change.Action = "keep"
			change.Reason = "时间已经是目标值"
			plan.Changes = append(plan.Changes, change)
			continue
		}

		change.Action = "change"
		plan.Changes = append(plan.Changes, change)
		// ⚠ 这里显示的是**该平台真正会发生什么**，不是硬编码的 schtasks：
		//   Windows → 一条 schtasks /change 命令
		//   Linux/NAS → 写回 config.json 的 dailyTime/staleTime
		// 用户在确认框里看到的东西必须和实际行为一致，否则"确认"就没有意义。
		plan.Commands = append(plan.Commands, changeTaskHint(t.task, t.at))
		plan.HasChanges = true
	}

	plan.Summary = summarizePlan(plan)
	return plan, nil
}

// ApplySchedule 执行预览里的改动。
//
// ⚠ **必须显式传 confirmed=true** —— 这是"改动前弹确认"在代码层面的落实：
// 界面必须先把 PreviewSchedule 的结果（含原始命令）展示给用户，
// 用户点确认后才带着 confirmed=true 调这里。
//
// 只执行 Action=="change" 的项；不会创建、不会删除任何任务。
func ApplySchedule(plan *SchedulePlan, confirmed bool) (*SchedulePlan, error) {
	if plan == nil {
		return nil, errors.New("没有要执行的计划")
	}
	if !confirmed {
		return nil, errors.New("未经确认：请先把改动预览展示给用户，确认后再执行")
	}
	if !plan.HasChanges {
		return plan, nil
	}

	applied := &SchedulePlan{
		Changes:    append([]ScheduleChange(nil), plan.Changes...),
		Commands:   append([]string(nil), plan.Commands...),
		Warnings:   append([]string(nil), plan.Warnings...),
		HasChanges: true,
	}
	var failed []string
	for i := range applied.Changes {
		c := &applied.Changes[i]
		if c.Action != "change" {
			continue
		}
		if err := ChangeTaskTime(c.Task, c.To); err != nil {
			c.Action = "failed"
			c.Reason = err.Error()
			failed = append(failed, c.Task)
			continue
		}
		c.Reason = "已改为 " + c.To
	}
	if len(failed) > 0 {
		applied.Summary = fmt.Sprintf("有 %d 个任务改动失败：%s。其余已生效。",
			len(failed), strings.Join(failed, "、"))
		return applied, fmt.Errorf("部分计划任务改动失败：%s", strings.Join(failed, "、"))
	}
	applied.Summary = "计划任务时间已更新：" + joinChanges(applied.Changes)
	return applied, nil
}

func summarizePlan(plan *SchedulePlan) string {
	if !plan.HasChanges {
		if len(plan.Warnings) > 0 {
			return "没有需要改的地方，但有以下问题：" + strings.Join(plan.Warnings, "；")
		}
		return "两个任务的时间都已经是目标值，无需改动。"
	}
	var parts []string
	for _, c := range plan.Changes {
		if c.Action == "change" {
			parts = append(parts, fmt.Sprintf("%s：%s → %s", c.Task, c.From, c.To))
		}
	}
	text := "将要改动 " + strings.Join(parts, "；")
	if len(plan.Warnings) > 0 {
		text += "。另外：" + strings.Join(plan.Warnings, "；")
	}
	return text
}

func joinChanges(changes []ScheduleChange) string {
	var parts []string
	for _, c := range changes {
		if c.Action == "change" || c.Action == "failed" {
			parts = append(parts, fmt.Sprintf("%s=%s", c.Task, c.To))
		}
	}
	return strings.Join(parts, "、")
}

// clockOf 从 "2026/10/10 17:30:00" 里取出 "17:30"。
func clockOf(nextRun string) string {
	fields := strings.Fields(nextRun)
	if len(fields) < 2 {
		return ""
	}
	timePart := fields[len(fields)-1] // 17:30:00
	parts := strings.Split(timePart, ":")
	if len(parts) < 2 {
		return ""
	}
	hour, minute := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if len(hour) == 1 {
		hour = "0" + hour
	}
	if len(minute) == 1 {
		minute = "0" + minute
	}
	if !validClock(hour + ":" + minute) {
		return ""
	}
	return hour + ":" + minute
}
