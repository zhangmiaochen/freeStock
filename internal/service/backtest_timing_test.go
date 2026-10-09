package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/portfolio"
)

// 第 4 项：回测到底慢在哪一段。
//
// 候选池只有几十只时看不出问题（一次批量就取完），所以要放到**几百只**的规模。
// 用 FREESTOCK_TIMING=1 开启（回填 + 回测合计约 1~2 分钟）。
func TestBacktestPhaseTiming(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("计时（约 1~2 分钟）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用：%v", err)
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -12)
	const limit = 300

	// ① 回填
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	backfillElapsed := time.Since(began)
	if bf.Scored == 0 {
		t.Skipf("回填没出数据：%v", bf.Skipped)
	}
	t.Logf("① 回填 %d 天 × %d 只：%s（%.0f ms/天·只）",
		bf.Days, limit, backfillElapsed.Round(time.Millisecond),
		float64(backfillElapsed.Milliseconds())/float64(bf.Days*limit))

	// ② 组合 + 持仓
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "阶段计时", Type: portfolio.TypeSemiAuto,
		StrategyID:          portfolio.StrategyTrendGrowth,
		TargetPositionCount: intPtr(5),
		MaxStockCount:       intPtr(8),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	dates := bf.Dates
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

	// ③ 回测（阶段日志由 CreateBacktest 自己打）
	began = time.Now()
	result, err := svc.CreateBacktest(BacktestRequest{
		PortfolioID: created.ID, StartDate: dates[0], EndDate: dates[len(dates)-1],
		InitialCapital: 100000,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	elapsed := time.Since(began)

	// ④ 读回结果（这一步也走 DB，顺带计时）
	began = time.Now()
	payload, err := svc.GetBacktestPayload(result.RunID)
	if err != nil {
		t.Fatalf("读回测结果失败：%v", err)
	}
	readElapsed := time.Since(began)

	t.Logf("② 回测（%d 天）：%s", len(dates), elapsed.Round(time.Millisecond))
	t.Logf("③ 读回结果：%s（净值 %d 天，成交 %d 笔）",
		readElapsed.Round(time.Millisecond), len(payload.Daily), len(payload.Trades))
	t.Logf("指标：总收益 %.2f%%，成交 %d 笔", result.Metrics.TotalReturn, result.Metrics.TotalTrades)
}
