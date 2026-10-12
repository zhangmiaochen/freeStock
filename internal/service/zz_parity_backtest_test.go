package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 回测一致性对照台（诊断用）。
//
// 为什么需要它：原版 Python 的 `create_backtest` 与 Go 的 `CreateBacktest`
// 在**同一区间、同一组合**上到底差多少，只能把两边都跑一遍逐笔比。
// 这个测试把 Go 的完整回测路径（取评分 → 贴信号 → 取价 → 逐日生成计划 → 执行）
// 暴露出来，并且允许**用原版算出来的市场状态覆盖库里的状态** ——
// 用来量化"市场状态口径差异"对回测结果的影响（A/B 对照）。
//
// 跑法（只动副本，绝不写真实库）：
//
//	$env:FREESTOCK_BT_DB="...\bin\portfolio.db"
//	$env:FREESTOCK_BT_LAKE="D:\workspace\stockTool\CNEquity\data\cnequity"
//	$env:FREESTOCK_BT_PORTFOLIO="pf_..."
//	$env:FREESTOCK_BT_START="20251013"; $env:FREESTOCK_BT_END="20260918"
//	可选：$env:FREESTOCK_BT_STATE_OVERRIDE="20251013:bull,20251014:range,..."
//	go test ./internal/service/ -run TestBacktestParityHarness -v -timeout 120m
func TestBacktestParityHarness(t *testing.T) {
	srcDB := os.Getenv("FREESTOCK_BT_DB")
	lakeRoot := os.Getenv("FREESTOCK_BT_LAKE")
	portfolioID := os.Getenv("FREESTOCK_BT_PORTFOLIO")
	if srcDB == "" || lakeRoot == "" || portfolioID == "" {
		t.Skip("未设置 FREESTOCK_BT_DB / FREESTOCK_BT_LAKE / FREESTOCK_BT_PORTFOLIO，跳过")
	}
	start := envInt(t, "FREESTOCK_BT_START", 0)
	end := envInt(t, "FREESTOCK_BT_END", 0)
	if start <= 0 || end <= 0 {
		t.Skip("需要 FREESTOCK_BT_START / FREESTOCK_BT_END")
	}
	capital := 100000.0
	if raw := os.Getenv("FREESTOCK_BT_CAPITAL"); raw != "" {
		capital = float64(envInt(t, "FREESTOCK_BT_CAPITAL", 100000))
	}

	overrides := map[string]string{}
	if raw := strings.TrimSpace(os.Getenv("FREESTOCK_BT_STATE_OVERRIDE")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
			if len(kv) == 2 && kv[0] != "" && kv[1] != "" {
				overrides[kv[0]] = kv[1]
			}
		}
	}

	dstDB := filepath.Join(t.TempDir(), "portfolio.db")
	copyFile(t, srcDB, dstDB)
	stock, _ := newIsolatedService(t)
	if _, err := stock.SetDataSource("cnlake", lakeRoot); err != nil {
		t.Fatalf("切数据源失败：%v", err)
	}
	svc := NewPortfolioServiceAt(stock, dstDB)
	t.Cleanup(svc.Close)

	if _, err := stock.GetStockUniverse(true); err != nil {
		t.Fatalf("股票列表失败：%v", err)
	}
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(portfolioID)
	if err != nil {
		t.Fatalf("读组合失败：%v", err)
	}
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(start), int64(end))
	if err != nil {
		t.Fatal(err)
	}
	days, codes, _, readyByDate := svc.buildStrengthDays(store, strengths, item)
	if len(days) == 0 {
		t.Fatal("区间内没有可用评分交易日")
	}
	days = svc.attachSignalsToDays(store, days)

	applied := 0
	for i := range days {
		if state, ok := overrides[days[i].Date]; ok {
			days[i].MarketState = state
			applied++
		}
	}
	t.Logf("评分 %d 行 / %d 天 / %d 只；市场状态覆盖 %d 天（提供 %d 天）",
		len(strengths), len(days), len(codes), applied, len(overrides))

	bars, err := svc.loadBacktestBars(codes, start, end)
	if err != nil {
		t.Fatal(err)
	}
	benchmark := map[string]float64{}
	if closes, err := stock.loadBenchmark(score.LowBuyIndexBenchmarkCode, start, end); err == nil {
		for date, close := range closes {
			benchmark[fmt.Sprintf("%d", date)] = close
		}
	}

	targetCount := item.TargetPositionCount
	maxStocks := item.MaxStockCount
	if maxStocks < targetCount {
		maxStocks = targetCount
	}
	cfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		BenchmarkCode:       item.BenchmarkCode,
		StartDate:           fmt.Sprintf("%d", start),
		EndDate:             fmt.Sprintf("%d", end),
		TargetPositionCount: targetCount,
		MaxStockCount:       maxStocks,
		TotalLayers:         item.TotalLayers,
		MaxLayersPerStock:   maxLayersForBacktest(item.StrategyID, item.TotalLayers),
		InitialCapital:      capital,
		MarketScope:         scopeOfPortfolio(item),
	}
	indexGates := map[string]score.LowBuyIndexGate{}
	if item.StrategyID == portfolio.StrategyTrendLowBuy {
		dates := make([]string, 0, len(days))
		for _, day := range days {
			dates = append(dates, day.Date)
		}
		indexGates = stock.lowBuyIndexGatesForRange(dates)
	}
	engine := backtest.New(cfg, days, bars, benchmark)
	planBuilder := svc.backtestPlanBuilder(item, store, marketStateByDate(store, days),
		targetCount, maxStocks, indexGates, readyByDate)
	result, err := engine.Run(planBuilder)
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	metrics := backtest.CalculateMetrics(result.Snapshots, result.Trades, cfg.InitialCapital)

	t.Logf("RESULT total_return=%.2f annual=%.2f max_dd=%.2f sharpe=%.2f snapshots=%d trades=%d",
		metrics.TotalReturn, metrics.AnnualReturn, metrics.MaxDrawdown, metrics.SharpeRatio,
		len(result.Snapshots), len(result.Trades))
	if len(result.Snapshots) > 0 {
		last := result.Snapshots[len(result.Snapshots)-1]
		t.Logf("RESULT final_value=%.6f cash=%.2f positions=%d", last.TotalValue, last.Cash, last.PositionCount)
	}
	for _, trade := range result.Trades {
		t.Logf("TRADE %s %-5s %-6s px=%.4f sh=%d layers=%d pl%%=%.2f reason=%s",
			trade.TradeDate, trade.Action, trade.Code, trade.Price, trade.Shares,
			trade.PositionLayers, trade.ProfitLossPct, trade.Reason)
	}
	states := make([]string, 0, len(days))
	for _, day := range days {
		states = append(states, day.Date+":"+day.MarketState)
	}
	sort.Strings(states)
	t.Logf("STATES %s", strings.Join(states, ","))
}
