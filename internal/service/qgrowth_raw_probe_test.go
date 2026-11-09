package service

import (
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/config"
	"freestock/internal/stockdb"
)

// 临时探针：确认 rawCloseFor 取到的是原始价。
func TestRawCloseProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_QG_EXP") == "" {
		t.Skip("设 FREESTOCK_QG_EXP=1 才跑")
	}
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	_ = os.MkdirAll(cfgDir, 0o755)
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatal(err)
	}
	svc := NewPortfolioServiceAt(stock, filepath.Join("..", "..", ".gotmp", "gate_exp", "portfolio.db"))
	t.Cleanup(svc.Close)
	for _, code := range []string{"603259", "002648", "300628"} {
		v, ok := svc.rawCloseFor(code, 20261029)
		t.Logf("RAW %s close=%.2f ok=%v", code, v, ok)
	}

	// 直接调计划构建，确认候选价是原始价
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(os.Getenv("FREESTOCK_QG_PORTFOLIO"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PSTRAT %s layers=%d", item.StrategyID, item.TotalLayers)
	plan, err := svc.buildQualityGrowthPlanAt(item, store, 20261029)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range plan.Candidates {
		if i >= 3 {
			break
		}
		t.Logf("PCAND %s close=%.2f", row.Code, row.Close)
	}
}
