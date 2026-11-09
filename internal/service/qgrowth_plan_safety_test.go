package service

import (
	"testing"

	"freestock/internal/score"
)

// 调仓日 = 当月最后一个工作日（与回测"每月最后一个交易日"对齐）。
func TestIsRebalanceWindowDate(t *testing.T) {
	cases := []struct {
		date int
		want bool
	}{
		{20261104, false}, // 月中
		{20261126, false}, // 月末前，但不是最后一个工作日
		{20261127, false},
		{20261130, true},  // 11-30 周一
		{20261128, false}, // 周六
		{20261101, false},
		{20261030, true}, // 10 月末（周五）
	}
	for _, tc := range cases {
		if got := isRebalanceWindowDate(tc.date); got != tc.want {
			t.Errorf("%d: got %v want %v", tc.date, got, tc.want)
		}
	}
}

func TestNextRebalanceDate(t *testing.T) {
	cases := map[int]string{
		20261104: "2026-11-30",
		20261130: "2026-12-31", // 当天已是调仓日 → 返回下一个
		20261030: "2026-11-30",
	}
	for date, want := range cases {
		if got := nextRebalanceDate(date); got != want {
			t.Errorf("%d: got %s want %s", date, got, want)
		}
	}
}

// 没有候选时**清仓**是策略口径（回测同样如此）。
// 防止数据缺口误清仓的责任在调用方：非调仓日只展示持仓、自动应用要求有建仓动作。
func TestQGrowthPlanRowsNoCandidatesClears(t *testing.T) {
	held := []score.Row{
		{Code: "600938", Name: "中国海油", PositionLayers: 2},
		{Code: "603259", Name: "药明康德", PositionLayers: 8},
	}
	plan := qGrowthPlanRows(held, nil, map[string]int{})
	if len(plan.Rows) != 2 {
		t.Fatalf("应有 2 条清仓行，实际 %d 条：%+v", len(plan.Rows), plan.Rows)
	}
	for _, row := range plan.Rows {
		if row.Action != score.ActionClear {
			t.Errorf("%s 应为 clear（与回测一致），实际 %s", row.Code, row.Action)
		}
	}
}
