package backtest

import "math"

// 绩效指标。1:1 对照 `backtest_metrics.py`。
//
// 常量（`backtest_metrics.py:6-8`）：
//
//	RISK_FREE_RATE = 0.03         无风险利率（年化 3%）
//	TRADING_DAYS_PER_YEAR = 252
const (
	tradingDaysPerYear = 252
	riskFreeRate       = 0.03
)

// Metrics 是一次回测的绩效指标。
type Metrics struct {
	TotalReturn      float64 `json:"totalReturn"`
	AnnualReturn     float64 `json:"annualReturn"`
	MaxDrawdown      float64 `json:"maxDrawdown"`
	Volatility       float64 `json:"volatility"`
	SharpeRatio      float64 `json:"sharpeRatio"`
	CalmarRatio      float64 `json:"calmarRatio"`
	SortinoRatio     float64 `json:"sortinoRatio"`
	TotalTrades      int     `json:"totalTrades"`
	WinCount         int     `json:"winCount"`
	LoseCount        int     `json:"loseCount"`
	WinRate          float64 `json:"winRate"`
	AvgHoldingDays   float64 `json:"avgHoldingDays"`
	MaxPositionCount int     `json:"maxPositionCount"`
	AvgPositionRatio float64 `json:"avgPositionRatio"`
	TradingDays      int     `json:"tradingDays"`
	ProfitDays       int     `json:"profitDays"`
	LossDays         int     `json:"lossDays"`
	AvgProfit        float64 `json:"avgProfit"`
	AvgLoss          float64 `json:"avgLoss"`
	// 短期回测警告（少于 60 个交易日时年化会被显著放大）
	IsShortPeriod       bool   `json:"isShortPeriod"`
	AnnualReturnWarning string `json:"annualReturnWarning"`
}

// CalculateMetrics 计算全部指标。
func CalculateMetrics(snapshots []Snapshot, trades []Trade, initialCapital float64) Metrics {
	out := Metrics{TradingDays: len(snapshots)}
	if len(snapshots) < 2 {
		return out
	}

	// 总收益 = 最后一天的累计收益（原实现 `cumulative_return.iloc[-1]`）
	out.TotalReturn = round2(snapshots[len(snapshots)-1].CumulativeReturn)

	// 年化：`(1 + total/100)^(252/days) - 1`
	days := len(snapshots)
	totalDecimal := out.TotalReturn / 100
	annual := math.Pow(1+totalDecimal, float64(tradingDaysPerYear)/float64(days)) - 1
	out.AnnualReturn = round2(annual * 100)

	switch {
	case days < 60:
		out.IsShortPeriod = true
		out.AnnualReturnWarning = "注意：回测期较短，年化收益率可能被显著放大，仅供参考"
	case days < 120:
		out.AnnualReturnWarning = "提示：回测期偏短，建议延长回测期以获得更可靠的年化收益率"
	}

	// 最大回撤：`max((cummax - total_value) / cummax * 100)`
	peak := snapshots[0].TotalValue
	maxDD := 0.0
	drawdownDuration, maxDuration := 0, 0
	for _, snap := range snapshots {
		if snap.TotalValue > peak {
			peak = snap.TotalValue
		}
		if peak > 0 {
			dd := (peak - snap.TotalValue) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
			if dd > 0 {
				drawdownDuration++
				if drawdownDuration > maxDuration {
					maxDuration = drawdownDuration
				}
			} else {
				drawdownDuration = 0
			}
		}
	}
	out.MaxDrawdown = round2(maxDD)

	// 波动率：日收益的**样本**标准差（pandas `.std()` 默认 ddof=1）× sqrt(252) × 100
	daily := make([]float64, 0, len(snapshots))
	for _, snap := range snapshots {
		daily = append(daily, snap.DailyReturn/100)
	}
	std := sampleStd(daily)
	out.Volatility = round2(std * math.Sqrt(tradingDaysPerYear) * 100)

	// 夏普 / 卡玛
	if out.Volatility > 0 {
		out.SharpeRatio = round2((annual - riskFreeRate) / (out.Volatility / 100))
	}
	if out.MaxDrawdown > 0 {
		out.CalmarRatio = round2(annual / (out.MaxDrawdown / 100))
	}
	// 索提诺：只考虑下行波动
	downside := make([]float64, 0, len(daily))
	for _, v := range daily {
		if v < 0 {
			downside = append(downside, v)
		}
	}
	if len(downside) > 0 {
		downsideStd := sampleStd(downside) * math.Sqrt(tradingDaysPerYear)
		if downsideStd > 0 {
			out.SortinoRatio = round2((annual - riskFreeRate) / downsideStd)
		}
	}

	// 交易日统计
	for _, snap := range snapshots {
		switch {
		case snap.DailyReturn > 0:
			out.ProfitDays++
		case snap.DailyReturn < 0:
			out.LossDays++
		}
		if snap.PositionCount > out.MaxPositionCount {
			out.MaxPositionCount = snap.PositionCount
		}
		out.AvgPositionRatio += positionRatio(snap)
	}
	out.AvgPositionRatio = round2(out.AvgPositionRatio / float64(days) * 100)

	// 成交统计：只统计**卖出**（有已实现盈亏的那一侧）
	out.TotalTrades = len(trades)
	profitSum, lossSum := 0.0, 0.0
	holdingSum := 0.0
	holdingCount := 0
	for _, trade := range trades {
		if trade.Action != "sell" {
			continue
		}
		if trade.ProfitLoss > 0 {
			out.WinCount++
			profitSum += trade.ProfitLossPct
		} else if trade.ProfitLoss < 0 {
			out.LoseCount++
			lossSum += trade.ProfitLossPct
		}
		if trade.HoldingDays > 0 {
			holdingSum += float64(trade.HoldingDays)
			holdingCount++
		}
	}
	closed := out.WinCount + out.LoseCount
	if closed > 0 {
		out.WinRate = round2(float64(out.WinCount) / float64(closed) * 100)
	}
	if out.WinCount > 0 {
		out.AvgProfit = round2(profitSum / float64(out.WinCount))
	}
	if out.LoseCount > 0 {
		out.AvgLoss = round2(lossSum / float64(out.LoseCount))
	}
	if holdingCount > 0 {
		out.AvgHoldingDays = round2(holdingSum / float64(holdingCount))
	}
	return out
}

// positionRatio 是仓位比例（持仓市值 / 总资产）。
func positionRatio(snap Snapshot) float64 {
	if snap.TotalValue <= 0 {
		return 0
	}
	return snap.PositionValue / snap.TotalValue
}

// sampleStd 是**样本**标准差（ddof=1），与 pandas `.std()` 的默认行为一致。
//
// 用总体标准差（ddof=0）会让波动率偏小、夏普偏大 —— 这是很容易踩的坑。
func sampleStd(values []float64) float64 {
	n := len(values)
	if n < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(n)
	sum := 0.0
	for _, v := range values {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(n-1))
}
