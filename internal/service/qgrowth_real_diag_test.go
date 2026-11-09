package service

import (
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/config"
	"freestock/internal/stockdb"
)

// 诊断：真实组合在最新交易日的质量成长建议（候选数、计划行）。
func TestRealPlanDiag(t *testing.T) {
	if os.Getenv("FREESTOCK_DIAG") == "" {
		t.Skip("设 FREESTOCK_DIAG=1 才跑")
	}
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	_ = os.MkdirAll(cfgDir, 0o755)
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", os.Getenv("FREESTOCK_QG_LAKE")); err != nil {
		t.Fatal(err)
	}
	svc := NewPortfolioServiceAt(stock, filepath.Join("..", "..", "bin", "portfolio.db"))
	t.Cleanup(svc.Close)
	plan, err := svc.BuildPlan(os.Getenv("FREESTOCK_QG_PORTFOLIO"))
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	t.Logf("PDIAG date=%d candidates=%d rows=%d", plan.StrengthDate, len(plan.Candidates), len(plan.Plan.Rows))
	for i, r := range plan.Plan.Rows {
		if i >= 12 {
			break
		}
		t.Logf("PROW %s %s cur=%d target=%d", r.Action, r.Code, r.CurrentLayers, r.TargetLayers)
	}
}
