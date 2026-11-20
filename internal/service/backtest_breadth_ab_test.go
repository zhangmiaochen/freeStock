package service

import (
	"path/filepath"
	"sort"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 方案 C 的 A/B 回测对比：同一份回填数据、同一个组合、同一区间，
// 只切换"广度闸门口径"，比较回测指标。
//
//	C  ：原始口径就绪数 + 原始阈值（1000/1500/2000/2600）
//	旧 ：复权就绪数 × 1.04（1040/1560/2080/2704）
func TestBacktestABByBreadthBasis(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要回填真实数据）")
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

	// ① 回填分红季（6~8 月，两种口径差别最大）
	if _, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "AB-全市场", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendLowBuy,
	}); err != nil {
		t.Fatal(err)
	}
	origRaw := breadthUseRaw
	origScale := score.LowBuyBreadthAdjScale
	defer func() {
		breadthUseRaw = origRaw
		score.LowBuyBreadthAdjScale = origScale
	}()
	breadthUseRaw = true
	score.LowBuyBreadthAdjScale = 1.0
	if _, err := svc.RefreshBackfill(portfolio.StrategyTrendLowBuy, 20240102, 20240628, 0, true); err != nil {
		t.Fatalf("回填失败：%v", err)
	}

	// ② 取 40 只做"指定股票"组合（回测只取这 40 只的日线，秒级）
	analytics, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	codeSet, err := topScoredCodes(analytics, 20240415, portfolio.StrategyTrendLowBuy, 40)
	if err != nil || len(codeSet) == 0 {
		t.Fatalf("没有评分数据：%v", err)
	}
	codes := codeSet
	pf, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "AB-40只", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendLowBuy,
		ScopeType: portfolio.ScopeManualCodes, ScopeValue: &portfolio.ScopeValue{Codes: codes},
	})
	if err != nil {
		t.Fatal(err)
	}

	// ③ 先给证据：区间内两种口径的就绪数
	states, err := analytics.LoadMarketStateRange(20240101, 20240630)
	if err != nil {
		t.Fatal(err)
	}
	shown := 0
	for _, state := range states {
		if shown >= 8 {
			break
		}
		shown++
		adjusted, err := analytics.LowBuyBreadthReadyCount(state.TradeDate, portfolio.StrategyTrendLowBuy)
		if err != nil {
			continue
		}
		t.Logf("  %d  复权就绪=%4d  原始就绪=%4d", state.TradeDate, adjusted, state.RawBreadthReady)
	}

	// ④ 两次回测
	run := func(cMode bool) (float64, float64, float64, float64, int, int) {
		breadthUseRaw = cMode
		if cMode {
			score.LowBuyBreadthAdjScale = 1.0
		} else {
			score.LowBuyBreadthAdjScale = 1.04
		}
		result, err := svc.CreateBacktest(BacktestRequest{
			PortfolioID: pf.ID, StartDate: 20240201, EndDate: 20240628,
		})
		if err != nil {
			t.Fatalf("回测失败（C=%v）：%v", cMode, err)
		}
		m := result.Metrics
		return m.TotalReturn, m.MaxDrawdown, m.SharpeRatio, m.AnnualReturn, m.TotalTrades, m.TradingDays
	}

	retC, ddC, sharpeC, annC, tradesC, daysC := run(true)
	retOld, ddOld, sharpeOld, annOld, tradesOld, _ := run(false)

	// ⚠ Metrics 里的收益/回撤**本身就是百分数**，不要再 ×100
	t.Logf("【C  原始口径+原始阈值】总收益=%.2f%% 年化=%.2f%% 回撤=%.2f%% 夏普=%.2f 成交=%d 天=%d",
		retC, annC, ddC, sharpeC, tradesC, daysC)
	t.Logf("【旧 复权口径×1.04 】总收益=%.2f%% 年化=%.2f%% 回撤=%.2f%% 夏普=%.2f 成交=%d",
		retOld, annOld, ddOld, sharpeOld, tradesOld)
	t.Logf("差异：总收益 %+.2f 个百分点，回撤 %+.2f 个百分点，成交 %+d 笔",
		retC-retOld, ddC-ddOld, tradesC-tradesOld)
}

// topScoredCodes 取某天评分最高的 N 只（回测候选更可能成交）。
func topScoredCodes(store *portfolio.Store, date int64, strategyID string, limit int) ([]string, error) {
	rows, err := store.DB().Query(
		`SELECT ts_code FROM stock_strength WHERE trade_date=? AND strategy_id=? ORDER BY total_score DESC LIMIT ?`,
		date, strategyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, limit)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		out = append(out, code)
	}
	sort.Strings(out)
	return out, rows.Err()
}
