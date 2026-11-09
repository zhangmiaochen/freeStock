package service

import (
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/stockdb"
)

// 历史持仓的成本价曾是复权价（603259 建仓价 350.93），现价是原始价 135.28，
// 导致盈亏 -61%。这里验证：一次性修正后，成本价/现价/盈亏同口径。
func TestEntryPriceMigrationProbe(t *testing.T) {
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	if lakeRoot == "" {
		t.Skip("设 FREESTOCK_QG_LAKE 才跑")
	}
	dir := t.TempDir()
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	_ = os.MkdirAll(cfgDir, 0o755)
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatal(err)
	}
	svc := NewPortfolioServiceAt(stock, filepath.Join(dir, "portfolio.db"))
	t.Cleanup(svc.Close)

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(portfolio.PortfolioInput{
		Name: "迁移测试", Type: portfolio.TypeAuto,
		StrategyID: portfolio.StrategyQualityGrowth, TotalLayers: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO portfolio_position
		 (portfolio_id, ts_code, name, entry_price, entry_date, position_layers, position_weight,
		  source_type, last_action, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, "603259", "药明康德", 350.93, "20261103", 8, 8, "auto", "add", "x", "x"); err != nil {
		t.Fatal(err)
	}
	// 中国海油：复权 45.8382 / 原始 34.21，比值 1.34（旧规则会漏掉，新规则要能识别）
	if _, err := store.DB().Exec(
		`INSERT INTO portfolio_position
		 (portfolio_id, ts_code, name, entry_price, entry_date, position_layers, position_weight,
		  source_type, last_action, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, "600938", "中国海油", 45.8382, "20261103", 2, 2, "system", "add", "x", "x"); err != nil {
		t.Fatal(err)
	}

	detail, err := svc.GetPortfolioDetail(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Positions) != 2 {
		t.Fatalf("应有 2 个持仓，实际 %d", len(detail.Positions))
	}
	byCode := map[string]float64{}
	for _, p := range detail.Positions {
		t.Logf("POS %s entry=%.2f cur=%.2f pnl=%.1f", p.Code, p.EntryPrice, p.CurrentPrice, p.ProfitLossPct)
		byCode[p.Code] = p.EntryPrice
		if p.EntryPrice > 200 {
			t.Errorf("%s 成本价应被修正为原始价（<200），实际 %.2f", p.Code, p.EntryPrice)
		}
		if p.ProfitLossPct < -5 || p.ProfitLossPct > 5 {
			t.Errorf("%s 同口径下盈亏应接近 0，实际 %.1f%%", p.Code, p.ProfitLossPct)
		}
	}
	if byCode["600938"] > 40 {
		t.Errorf("中国海油成本价应修正为约 34.21，实际 %.2f", byCode["600938"])
	}
}
