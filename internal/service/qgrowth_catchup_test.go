package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/stockdb"
)

// 补做机制依赖的两个判定：月末最后交易日、是否已应用过。
func TestLastTradingDayOfMonth(t *testing.T) {
	lakeRoot := os.Getenv("FREESTOCK_QG_LAKE")
	if lakeRoot == "" {
		t.Skip("设 FREESTOCK_QG_LAKE 才跑")
	}
	cfgDir := filepath.Join("..", "..", ".gotmp", "qg_test_cfg")
	_ = os.MkdirAll(cfgDir, 0o755)
	stock := NewStockService(config.NewStoreAt(filepath.Join(cfgDir, "config.json")))
	stock.applySource(config.KindStockDB, stockdb.DefaultLocalURL)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatal(err)
	}
	svc := NewPortfolioServiceAt(stock, filepath.Join(t.TempDir(), "portfolio.db"))
	t.Cleanup(svc.Close)

	if got := svc.lastTradingDayOfMonth(2026, time.October); got != 20261030 {
		t.Errorf("2026-10 最后交易日 = %d，期望 20261030", got)
	}
	// 当月（未结束）只能取到最新有数据的一天，不是"月末"
	if got := svc.lastTradingDayOfMonth(2026, time.November); got == 0 {
		t.Errorf("2026-11 至少应返回一个已有数据的交易日")
	}
}

// 没有流水历史 → 不补做；有流水且当日之后没应用 → 判定为"未应用"。
func TestAppliedSince(t *testing.T) {
	svc := NewPortfolioServiceAt(nil, filepath.Join(t.TempDir(), "portfolio.db"))
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(portfolio.PortfolioInput{
		Name: "补做测试", Type: portfolio.TypeAuto,
		StrategyID: portfolio.StrategyQualityGrowth, TotalLayers: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc.hasAnyTxn(item.ID) {
		t.Fatal("新组合不应有流水")
	}
	if svc.appliedSince(item.ID, 20261030) {
		t.Fatal("没有流水时不应判定为已应用")
	}
	if _, err := store.DB().Exec(
		`INSERT INTO portfolio_position_txn
		 (txn_id, portfolio_id, ts_code, name, trade_date, action, price, layers_delta, weight_delta, reason, source_signal_id, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		"txn_test", item.ID, "600938", "中国海油", "20261103", "add", 34.21, 2, 2.0,
		applyTxnReasonText, "sig", "x"); err != nil {
		t.Fatal(err)
	}
	if !svc.hasAnyTxn(item.ID) {
		t.Fatal("插入流水后应有历史")
	}
	if !svc.appliedSince(item.ID, 20261030) {
		t.Fatal("11-03 应用过，应判定为已应用")
	}
	if svc.appliedSince(item.ID, 20261104) {
		t.Fatal("11-04 之后没有应用，应判定为未应用")
	}
}
