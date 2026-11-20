package service

import (
	"path/filepath"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
)

// 方案 C 的落库与读取链路（不跑回测引擎，只验数据）：
//
//	回填 → market_state.raw_breadth_ready 被写入 → rawBreadthReadyAt 能读到。
//
// 全市场回填太重，这里用 limit=400 只：广度计数只统计"当天有评分的代码"，
// 链路本身（计算→落库→读取）与全市场完全一致。
func TestRawBreadthPersistedAndReadable(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过")
	}
	lake := cnlake.Open("")
	if lake == nil || !lake.Available() || !lake.HasDataset("daily_bars") {
		t.Skip("数据湖不可用")
	}
	dir := t.TempDir()
	cfg := config.NewStoreAt(filepath.Join(dir, "config.json"))
	cfg.SetDataSource("", config.KindCnLake)
	cfg.SetCnLakeRoot(lake.Root())
	stock := NewStockService(cfg)
	svc := NewPortfolioServiceAt(stock, filepath.Join(dir, "portfolio.db"))
	defer svc.Close()

	latest := svc.latestTradeDate()
	if latest <= 0 {
		t.Skip("拿不到最新交易日")
	}
	if _, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "C 链路", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendLowBuy,
	}); err != nil {
		t.Fatal(err)
	}
	start := latest - 10
	res, err := svc.RefreshBackfill(portfolio.StrategyTrendLowBuy, start, latest, 400, true)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	if res.Scored == 0 {
		t.Skip("没有成功回填任何交易日")
	}

	analytics, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	states, err := analytics.LoadMarketStateRange(int64(start), int64(latest))
	if err != nil {
		t.Fatal(err)
	}
	withRaw := 0
	for _, state := range states {
		if state.RawBreadthReady >= 0 {
			withRaw++
			got, ok := svc.rawBreadthReadyAt(analytics, state.TradeDate)
			if !ok || got != state.RawBreadthReady {
				t.Fatalf("%d 的原始口径就绪数读取不一致：库里=%d 读到=%d(ok=%v)",
					state.TradeDate, state.RawBreadthReady, got, ok)
			}
		}
	}
	if withRaw == 0 {
		t.Fatalf("回填 %d 天，market_state 里没有任何 raw_breadth_ready —— 方案 C 没落库", len(states))
	}
	t.Logf("回填 %d 天（成功 %d），其中 %d 天已物化原始口径就绪数", res.Days, res.Scored, withRaw)
	for i, state := range states {
		if i >= 3 {
			break
		}
		t.Logf("  %d raw=%d", state.TradeDate, state.RawBreadthReady)
	}
}

// 关闭方案 C 时，读取必须返回 false（回退到旧行为）。
func TestRawBreadthIgnoredWhenDisabled(t *testing.T) {
	orig := breadthUseRaw
	breadthUseRaw = false
	defer func() { breadthUseRaw = orig }()

	svc := &PortfolioService{}
	defer svc.Close()
	if _, ok := svc.rawBreadthReadyAt(nil, 20261119); ok {
		t.Fatal("关闭 C 时不应读取原始口径计数")
	}
}
