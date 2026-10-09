package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
)

// 第 4 项的等价性验证：**复用回填缓存**跑出的回测，
// 必须和"老老实实重新取价"跑出的回测**完全一样**。
//
// 这是"提速但不改结果"的判决性测试 —— 只要净值曲线、成交明细、
// 指标里有任何一项不同，这个优化就不能要。
func TestBacktestCacheReuseIsIdentical(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("要回填真实数据（约 1 分钟）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用：%v", err)
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -12)
	const limit = 200

	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil || bf.Scored == 0 {
		t.Skipf("回填失败：%v", err)
	}
	dates := bf.Dates
	if len(dates) < 2 {
		t.Skip("交易日不足")
	}

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "缓存等价性", Type: portfolio.TypeSemiAuto,
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(5),
		MaxStockCount:       intPtr(8),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	seed, err := store.LoadStrength(int64(dates[len(dates)-1]), portfolio.StrategyTrendGrowth, 5)
	if err != nil || len(seed) == 0 {
		t.Skipf("评分表没有可用股票：%v", err)
	}
	for _, row := range seed {
		_, _ = svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: row.Code, Name: row.Name,
			EntryPrice: row.Close, EntryDate: "20260101", Layers: intPtr(1),
		})
	}

	req := BacktestRequest{
		PortfolioID: created.ID, StartDate: dates[0],
		EndDate: dates[len(dates)-1], InitialCapital: 100000,
	}

	// ① 带缓存复用（回填刚做完，缓存是热的）
	began := time.Now()
	withCache, err := svc.CreateBacktest(req)
	if err != nil {
		t.Fatalf("回测（带缓存）失败：%v", err)
	}
	cachedElapsed := time.Since(began)

	// ② 清掉缓存，逼它重新取价
	svc.setBackfillCache(nil, nil)
	began = time.Now()
	noCache, err := svc.CreateBacktest(req)
	if err != nil {
		t.Fatalf("回测（不带缓存）失败：%v", err)
	}
	freshElapsed := time.Since(began)

	t.Logf("带缓存 %s / 不带缓存 %s（快 %.1f 倍）",
		cachedElapsed.Round(time.Millisecond), freshElapsed.Round(time.Millisecond),
		float64(freshElapsed)/float64(cachedElapsed))

	// ---- 逐项比对 ----
	compareBacktestMetrics(t, withCache.Metrics, noCache.Metrics)

	a, err := svc.GetBacktestPayload(withCache.RunID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetBacktestPayload(noCache.RunID)
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Daily) != len(b.Daily) {
		t.Fatalf("净值曲线天数不同：%d vs %d", len(a.Daily), len(b.Daily))
	}
	for i := range a.Daily {
		x, y := a.Daily[i], b.Daily[i]
		if x.TradeDate != y.TradeDate || x.TotalValue != y.TotalValue ||
			x.Cash != y.Cash || x.PositionValue != y.PositionValue ||
			x.DailyReturn != y.DailyReturn || x.CumulativeReturn != y.CumulativeReturn ||
			x.PositionCount != y.PositionCount {
			t.Errorf("第 %d 天净值不同：\n  带缓存 %+v\n  不带   %+v", i, x, y)
		}
	}
	if len(a.Trades) != len(b.Trades) {
		t.Fatalf("成交笔数不同：%d vs %d", len(a.Trades), len(b.Trades))
	}
	for i := range a.Trades {
		x, y := a.Trades[i], b.Trades[i]
		if x.TradeDate != y.TradeDate || x.Code != y.Code || x.Action != y.Action ||
			x.Price != y.Price || x.Shares != y.Shares || x.Amount != y.Amount ||
			x.Commission != y.Commission || x.Tax != y.Tax {
			t.Errorf("第 %d 笔成交不同：\n  带缓存 %+v\n  不带   %+v", i, x, y)
		}
	}
	t.Logf("净值 %d 天、成交 %d 笔、指标全部一致", len(a.Daily), len(a.Trades))
}

// compareBacktestMetrics 逐字段比对两组指标 —— 指标也算"结果"，必须一致。
func compareBacktestMetrics(t *testing.T, a, b backtest.Metrics) {
	t.Helper()
	type item struct {
		name string
		x, y float64
	}
	nums := []item{
		{"TotalReturn", a.TotalReturn, b.TotalReturn},
		{"AnnualReturn", a.AnnualReturn, b.AnnualReturn},
		{"MaxDrawdown", a.MaxDrawdown, b.MaxDrawdown},
		{"Volatility", a.Volatility, b.Volatility},
		{"SharpeRatio", a.SharpeRatio, b.SharpeRatio},
		{"CalmarRatio", a.CalmarRatio, b.CalmarRatio},
		{"SortinoRatio", a.SortinoRatio, b.SortinoRatio},
		{"WinRate", a.WinRate, b.WinRate},
		{"AvgHoldingDays", a.AvgHoldingDays, b.AvgHoldingDays},
		{"AvgPositionRatio", a.AvgPositionRatio, b.AvgPositionRatio},
		{"AvgProfit", a.AvgProfit, b.AvgProfit},
		{"AvgLoss", a.AvgLoss, b.AvgLoss},
	}
	for _, it := range nums {
		if it.x != it.y {
			t.Errorf("指标 %s 不同：带缓存 %v，不带 %v", it.name, it.x, it.y)
		}
	}
	ints := []struct {
		name string
		x, y int
	}{
		{"TotalTrades", a.TotalTrades, b.TotalTrades},
		{"WinCount", a.WinCount, b.WinCount},
		{"LoseCount", a.LoseCount, b.LoseCount},
		{"MaxPositionCount", a.MaxPositionCount, b.MaxPositionCount},
		{"TradingDays", a.TradingDays, b.TradingDays},
		{"ProfitDays", a.ProfitDays, b.ProfitDays},
		{"LossDays", a.LossDays, b.LossDays},
	}
	for _, it := range ints {
		if it.x != it.y {
			t.Errorf("指标 %s 不同：带缓存 %d，不带 %d", it.name, it.x, it.y)
		}
	}
}
