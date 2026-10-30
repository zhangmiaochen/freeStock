package service

import (
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/config"
	"freestock/internal/qgrowth"
	"freestock/internal/score"
	"freestock/internal/stockdb"
)

// 质量成长策略的"最新交易日建议"真实数据测试。
//
//	$env:FREESTOCK_QG_EXP="1"
//	$env:FREESTOCK_QG_DB="...\portfolio.db"
//	$env:FREESTOCK_QG_LAKE="D:\...\cnequity"
//	$env:FREESTOCK_QG_PORTFOLIO="pf_..."
//	go test ./internal/service -run TestQualityGrowthPlanReal -v -timeout 30m
func TestQualityGrowthPlanReal(t *testing.T) {
	if os.Getenv("FREESTOCK_QG_EXP") == "" {
		t.Skip("设 FREESTOCK_QG_EXP=1 才跑（真实数据、只读副本库）")
	}
	dbPath := os.Getenv("FREESTOCK_QG_DB")
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	portfolioID := os.Getenv("FREESTOCK_QG_PORTFOLIO")
	if dbPath == "" || lakeRoot == "" || portfolioID == "" {
		t.Fatal("需要 FREESTOCK_QG_DB / FREESTOCK_QG_LAKE / FREESTOCK_QG_PORTFOLIO")
	}
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dbPath)
	t.Cleanup(svc.Close)

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		"UPDATE portfolio SET strategy_id=? WHERE portfolio_id=?",
		"quality_growth_pullback", portfolioID); err != nil {
		t.Fatalf("切换策略失败：%v", err)
	}

	plan, err := svc.BuildPlan(portfolioID)
	if err != nil {
		t.Fatalf("BuildPlan 失败：%v", err)
	}
	t.Logf("建议日=%d 市场=%s 健康度=%s 组合分=%.1f 候选=%d 计划行=%d",
		plan.StrengthDate, plan.MarketState.Label, plan.HealthLabel,
		plan.PortfolioScore, len(plan.Candidates), len(plan.Plan.Rows))
	for i, row := range plan.Candidates {
		if i >= 8 {
			break
		}
		t.Logf("  候选%d %s %s 评分=%.1f", i+1, row.Code, row.Name, row.TotalScore)
	}
	for _, row := range plan.Plan.Rows {
		t.Logf("  计划 %s %s %s 层 %d→%d (%+d) %s",
			row.Action, row.Code, row.Name, row.CurrentLayers, row.TargetLayers, row.LayersDelta, row.Reason)
	}
	if plan.StrengthDate == 0 {
		t.Fatal("建议日为空")
	}
	if len(plan.Candidates) == 0 {
		t.Fatal("最新交易日没有候选（可能数据未更新）")
	}

	// ---- 一致性检查：回测的计划构建器与实盘建议必须是同一套目标层数 ----
	item, err := store.Get(portfolioID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := qgrowth.DefaultConfig()
	days, _, bench, err := svc.buildQGrowthDays(plan.StrengthDate, plan.StrengthDate, cfg)
	if err != nil || len(days) == 0 {
		t.Fatalf("回测侧组装失败：%v", err)
	}
	btPlan := svc.qGrowthPlanBuilder(item, days, bench, cfg)(nil, days[0])
	btTarget := make(map[string]int, len(btPlan.Rows))
	for _, r := range btPlan.Rows {
		if r.Action == score.ActionAdd && r.TargetLayers > 0 {
			btTarget[r.Code] = r.TargetLayers
		}
	}
	liveTarget := make(map[string]int, len(plan.Plan.Rows))
	for _, r := range plan.Plan.Rows {
		if r.Action == score.ActionAdd && r.TargetLayers > 0 {
			liveTarget[r.Code] = r.TargetLayers
		}
	}
	if len(btTarget) != len(liveTarget) {
		t.Fatalf("回测目标 %d 只 vs 建议目标 %d 只", len(btTarget), len(liveTarget))
	}
	for code, layers := range btTarget {
		if liveTarget[code] != layers {
			t.Fatalf("%s 目标层数不一致：回测 %d vs 建议 %d", code, layers, liveTarget[code])
		}
	}
	t.Logf("一致性检查通过：回测与建议的目标层数完全一致（%d 只）", len(btTarget))
}
