package service

import (
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// planHasExecutableChange：建仓/清仓/换入换出/减仓/加层都算，只要层数真变。
func TestPlanHasExecutableChange(t *testing.T) {
	cases := []struct {
		name string
		rows []score.RotationRow
		want bool
	}{
		{"空计划", nil, false},
		{"建仓", []score.RotationRow{{Action: score.ActionAdd, IsExecutable: true, CurrentLayers: 0, TargetLayers: 3}}, true},
		{"清仓", []score.RotationRow{{Action: score.ActionClear, IsExecutable: true, CurrentLayers: 2, TargetLayers: 0}}, true},
		{"换入", []score.RotationRow{{Action: score.ActionReplaceIn, IsExecutable: true, CurrentLayers: 0, TargetLayers: 2}}, true},
		{"减仓（权重调整也算）", []score.RotationRow{{Action: score.ActionReduce, IsExecutable: true, CurrentLayers: 5, TargetLayers: 4}}, true},
		{"加层（权重调整也算）", []score.RotationRow{{Action: score.ActionAddLayer, IsExecutable: true, CurrentLayers: 4, TargetLayers: 5}}, true},
		{"层数没变", []score.RotationRow{{Action: score.ActionAdd, IsExecutable: true, CurrentLayers: 3, TargetLayers: 3}}, false},
		{"不可执行", []score.RotationRow{{Action: score.ActionAdd, IsExecutable: false, CurrentLayers: 0, TargetLayers: 3}}, false},
	}
	for _, tc := range cases {
		plan := &PlanResult{Plan: score.RotationPlan{Rows: tc.rows}}
		if got := planHasExecutableChange(plan); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if planHasExecutableChange(nil) {
		t.Error("nil 计划不应触发")
	}
}

// shouldAutoApply：只有自动组合 + 有变化才应用；手动/半自动永远不应用。
func TestShouldAutoApply(t *testing.T) {
	svc := &PortfolioService{}
	change := &PlanResult{StrengthDate: 20261030, Plan: score.RotationPlan{Rows: []score.RotationRow{
		{Action: score.ActionAdd, IsExecutable: true, CurrentLayers: 0, TargetLayers: 3},
	}}}
	noChange := &PlanResult{StrengthDate: 20261030, Plan: score.RotationPlan{Rows: []score.RotationRow{
		{Action: score.ActionAdd, IsExecutable: true, CurrentLayers: 3, TargetLayers: 3},
	}}}

	if svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeManual}, change) {
		t.Error("手动组合不应自动应用")
	}
	if svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeSemiAuto}, change) {
		t.Error("半自动组合不应自动应用")
	}
	if svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeAuto}, noChange) {
		t.Error("没有真实变化时不应自动应用")
	}
	// 低吸类策略：有变化就应用
	if !svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeAuto, StrategyID: portfolio.StrategyTrendLowBuy}, change) {
		t.Error("自动组合 + 有变化 应自动应用")
	}
	// 质量成长：月末（2026-10-30 是当月最后一个工作日）且有建仓 → 应用
	if !svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeAuto, StrategyID: portfolio.StrategyQualityGrowth}, change) {
		t.Error("质量成长月末 + 有建仓 应自动应用")
	}
	// 质量成长：非月末（11-04）→ 不应用
	midMonth := &PlanResult{StrengthDate: 20261104, Plan: score.RotationPlan{Rows: []score.RotationRow{
		{Action: score.ActionAdd, IsExecutable: true, CurrentLayers: 0, TargetLayers: 3},
	}}}
	if svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeAuto, StrategyID: portfolio.StrategyQualityGrowth}, midMonth) {
		t.Error("质量成长非月末不应自动应用")
	}
	// 质量成长：只有清仓、没有建仓 → 不应用
	clearOnly := &PlanResult{StrengthDate: 20261030, Plan: score.RotationPlan{Rows: []score.RotationRow{
		{Action: score.ActionClear, IsExecutable: true, CurrentLayers: 8, TargetLayers: 0},
	}}}
	if svc.shouldAutoApply(portfolio.Portfolio{Type: portfolio.TypeAuto, StrategyID: portfolio.StrategyQualityGrowth}, clearOnly) {
		t.Error("只有清仓没有建仓时不应自动应用")
	}
}
