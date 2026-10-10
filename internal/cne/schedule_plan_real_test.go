//go:build windows

package cne

import "testing"

// **用真实任务验证预览**（只读 —— 不会改动任何东西）。
//
// 目标是确认：能读出两个任务的当前时间、并且"已等于目标值"时
// 报告"无需改动"（否则界面会每次都弹确认，用户很快就烦了）。
func TestPreviewRealTasks(t *testing.T) {
	// 先读出当前实际时间
	state, err := QuerySchedule()
	if err != nil {
		t.Skipf("读不到计划任务：%v", err)
	}
	if !state.Daily.Registered || !state.Stale.Registered {
		t.Skip("两个任务没有同时注册，跳过")
	}
	currentDaily := clockOf(state.Daily.NextRun)
	currentStale := clockOf(state.Stale.NextRun)
	if currentDaily == "" || currentStale == "" {
		t.Fatalf("读不出当前时间：%q / %q", state.Daily.NextRun, state.Stale.NextRun)
	}

	// ① 用**当前时间**当目标 → 应当报告"无需改动"，且没有任何命令
	same, err := PreviewSchedule(currentDaily, currentStale)
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	t.Logf("目标=当前时间：%s", same.Summary)
	if same.HasChanges {
		t.Errorf("目标时间与现状相同，不该报告有改动（否则界面每次都弹确认）")
	}
	if len(same.Commands) != 0 {
		t.Errorf("无需改动时不该产生命令：%v", same.Commands)
	}

	// ② 换一个**肯定不同的**时间 → 应当报告 2 处改动，并给出 /change 命令
	other := "03:33"
	diff, err := PreviewSchedule(other, other)
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	t.Logf("目标=%s：%s", other, diff.Summary)
	t.Logf("将要执行的命令：%v", diff.Commands)
	if !diff.HasChanges {
		t.Error("目标时间与现状不同，应当报告有改动")
	}
	if len(diff.Commands) != 2 {
		t.Errorf("两个任务都要改，应有 2 条命令，实际 %v", diff.Commands)
	}
	for _, cmd := range diff.Commands {
		if !contains(cmd, "/change") || contains(cmd, "/create") || contains(cmd, "/delete") {
			t.Errorf("命令必须是 /change 且不含 create/delete：%s", cmd)
		}
	}
	for _, c := range diff.Changes {
		if c.Action == "change" {
			if c.From == "" || c.To == "" {
				t.Errorf("change 项必须给出 from/to：%+v", c)
			}
			t.Logf("  %s：%s → %s", c.Task, c.From, c.To)
		}
	}
	// ⚠ 全程只调了 PreviewSchedule —— 一个字节都没改
	t.Log("（以上全部是只读预览，没有执行任何改动）")
}
