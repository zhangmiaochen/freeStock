package backtest

import (
	"math"
	"testing"

	"freestock/internal/score"
)

// 造一个固定计划的 PlanBuilder：只在指定日期返回指定行。
func fixedPlan(byDate map[string][]score.RotationRow) PlanBuilder {
	return func(_ []score.Row, day StrengthDay) score.RotationPlan {
		return score.RotationPlan{Rows: byDate[day.Date]}
	}
}

func bar(date, code string, open, close float64) PriceBar {
	return PriceBar{Date: date, Code: code, Open: open, Close: close, Name: code}
}

// **核心时序**：T 日收盘后生成的计划，在 **T+1 日开盘**执行。
func TestEngineExecutesNextDayOpen(t *testing.T) {
	cfg := Config{
		StartDate: "20260105", EndDate: "20260107",
		TargetPositionCount: 1, MaxStockCount: 1, TotalLayers: 2, MaxLayersPerStock: 2,
		InitialCapital: 100000,
		// 关掉滑点与手续费，便于精确核对金额
		SlippagePct: 0.0001, CommissionRate: 0.0001, MinCommission: 0,
	}
	bars := []PriceBar{
		bar("20260105", "600519", 100, 100),
		bar("20260106", "600519", 110, 120), // 次日开盘 110
		bar("20260107", "600519", 130, 140),
	}
	days := []StrengthDay{
		{Date: "20260105", MarketState: score.StateRange},
		{Date: "20260106", MarketState: score.StateRange},
		{Date: "20260107", MarketState: score.StateRange},
	}
	// 1/5 收盘后给"建仓 1 层"的计划
	plan := fixedPlan(map[string][]score.RotationRow{
		"20260105": {{
			Code: "600519", Name: "贵州茅台", Action: score.ActionAdd,
			TargetLayers: 1, LayersDelta: 1, RankNo: 1, Score: 80,
		}},
	})

	engine := New(cfg, days, bars, nil)
	result, err := engine.Run(plan)
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	if len(result.Snapshots) != 3 {
		t.Fatalf("应有 3 天快照，实际 %d", len(result.Snapshots))
	}

	// 1/5：还没执行（计划是收盘后才生成的）→ 全现金
	if result.Snapshots[0].PositionCount != 0 {
		t.Errorf("1/5 不该有持仓（计划收盘后才生成），实际 %d", result.Snapshots[0].PositionCount)
	}
	// 1/6：在**开盘 110** 买入，收盘 120 盯市
	if result.Snapshots[1].PositionCount != 1 {
		t.Fatalf("1/6 应有 1 只持仓，实际 %d", result.Snapshots[1].PositionCount)
	}
	if len(result.Trades) != 1 {
		t.Fatalf("应有 1 笔成交，实际 %d", len(result.Trades))
	}
	trade := result.Trades[0]
	if trade.TradeDate != "20260106" {
		t.Errorf("成交应发生在 1/6（T+1），实际 %s", trade.TradeDate)
	}
	if trade.SignalDate != "20260105" {
		t.Errorf("信号日应为 1/5，实际 %s", trade.SignalDate)
	}
	// 滑点极小，成交价约等于开盘价
	if math.Abs(trade.Price-110) > 0.5 {
		t.Errorf("成交价应在开盘价 110 附近，实际 %.2f", trade.Price)
	}
	// 1 层 = 总资产的 1/2 = 50000；买在 110 → 约 454 股（向下取整）
	if trade.Shares <= 0 || trade.Amount > 55000 {
		t.Errorf("买入金额应在半仓（~5万）以内，实际 股数 %d 金额 %.2f", trade.Shares, trade.Amount)
	}
	t.Logf("1/6 开盘买入 %d 股 @ %.2f，金额 %.2f", trade.Shares, trade.Price, trade.Amount)

	// 1/6 收盘盯市：现金 + 股数×120
	expected := result.Snapshots[1].Cash + float64(trade.Shares)*120
	if math.Abs(result.Snapshots[1].TotalValue-expected) > 0.01 {
		t.Errorf("1/6 总资产应为 %.2f，实际 %.2f", expected, result.Snapshots[1].TotalValue)
	}
}

// 卖出时收印花税与手续费，且**卖出用开盘价**。
func TestEngineSellChargesCommissionAndTax(t *testing.T) {
	cfg := Config{
		StartDate: "20260105", EndDate: "20260107",
		TargetPositionCount: 1, MaxStockCount: 1, TotalLayers: 1, MaxLayersPerStock: 1,
		InitialCapital: 100000,
		CommissionRate: 0.001, MinCommission: 0, TaxRate: 0.001, SlippagePct: 0.0001,
	}
	bars := []PriceBar{
		bar("20260105", "600519", 100, 100),
		bar("20260106", "600519", 100, 100),
		bar("20260107", "600519", 200, 200),
	}
	days := []StrengthDay{
		{Date: "20260105"}, {Date: "20260106"}, {Date: "20260107"},
	}
	plan := fixedPlan(map[string][]score.RotationRow{
		"20260105": {{
			Code: "600519", Action: score.ActionAdd, TargetLayers: 1, LayersDelta: 1,
			RankNo: 1, Score: 80,
		}},
		// 1/6 收盘后全部清仓 → 1/7 开盘执行
		"20260106": {{
			Code: "600519", Action: score.ActionClear, TargetLayers: 0, LayersDelta: -1,
			RankNo: 1, Score: 10,
		}},
	})

	engine := New(cfg, days, bars, nil)
	result, err := engine.Run(plan)
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	if len(result.Trades) != 2 {
		t.Fatalf("应有买、卖各一笔，实际 %d", len(result.Trades))
	}
	buy, sell := result.Trades[0], result.Trades[1]
	if sell.Action != "sell" || sell.TradeDate != "20260107" {
		t.Fatalf("卖出应发生在 1/7，实际 %s %s", sell.Action, sell.TradeDate)
	}
	// 卖出价 = 开盘 200（滑点极小）
	if math.Abs(sell.Price-200) > 0.5 {
		t.Errorf("卖出应用开盘价 200，实际 %.2f", sell.Price)
	}
	// 买入不收印花税；卖出要收
	if buy.Tax != 0 {
		t.Errorf("买入不该收印花税，实际 %.4f", buy.Tax)
	}
	if sell.Tax <= 0 {
		t.Error("卖出应收印花税")
	}
	if sell.Commission <= 0 {
		t.Error("卖出应收手续费")
	}
	// 卖完后无持仓、总资产 = 现金
	if result.Snapshots[2].PositionCount != 0 {
		t.Errorf("1/7 卖完应无持仓，实际 %d", result.Snapshots[2].PositionCount)
	}
	if math.Abs(result.Snapshots[2].TotalValue-result.Snapshots[2].Cash) > 0.01 {
		t.Errorf("清仓后总资产应等于现金")
	}
	t.Logf("买 %.2f@%d 股（费 %.2f）→ 卖 %.2f@%d 股（费 %.2f 税 %.2f），净得 %.2f",
		buy.Price, buy.Shares, buy.Commission, sell.Price, sell.Shares, sell.Commission, sell.Tax, sell.TotalCost)
}

// 指标：样本标准差（ddof=1）而非总体标准差。
func TestSampleStdUsesDDOF1(t *testing.T) {
	values := []float64{1, 2, 3, 4}
	// 均值 2.5，离差平方和 5 → 总体 std = sqrt(5/4)=1.1180，样本 std = sqrt(5/3)=1.2910
	got := sampleStd(values)
	want := math.Sqrt(5.0 / 3.0)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("应为样本标准差(ddof=1) %.6f，实际 %.6f", want, got)
	}
	if sampleStd([]float64{5}) != 0 {
		t.Error("单点应返回 0（与 pandas 一致）")
	}
}

// 指标：总收益/最大回撤/年化。
func TestCalculateMetricsBasic(t *testing.T) {
	snapshots := []Snapshot{
		{Date: "20260105", TotalValue: 100000, Cash: 100000, CumulativeReturn: 0, DailyReturn: 0},
		{Date: "20260106", TotalValue: 110000, Cash: 0, PositionValue: 110000, CumulativeReturn: 10, DailyReturn: 10},
		{Date: "20260107", TotalValue: 88000, Cash: 0, PositionValue: 88000, CumulativeReturn: -12, DailyReturn: -20},
	}
	got := CalculateMetrics(snapshots, nil, 100000)
	if got.TotalReturn != -12 {
		t.Errorf("总收益应为 -12%%，实际 %.2f", got.TotalReturn)
	}
	// 峰值 110000 → 回撤 (110000-88000)/110000 = 20%
	if math.Abs(got.MaxDrawdown-20) > 0.01 {
		t.Errorf("最大回撤应为 20%%，实际 %.2f", got.MaxDrawdown)
	}
	if got.TradingDays != 3 {
		t.Errorf("交易日数应为 3，实际 %d", got.TradingDays)
	}
	// 不足 60 天要有短期警告
	if !got.IsShortPeriod || got.AnnualReturnWarning == "" {
		t.Error("不足 60 个交易日应给出短期回测警告")
	}
	t.Logf("总收益 %.2f%% 年化 %.2f%% 回撤 %.2f%% 波动 %.2f%% 夏普 %.2f",
		got.TotalReturn, got.AnnualReturn, got.MaxDrawdown, got.Volatility, got.SharpeRatio)
}
