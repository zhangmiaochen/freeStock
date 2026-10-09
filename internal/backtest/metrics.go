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
	// ---- 相对基准的指标（原实现 _calculate_relative_metrics）----
	//
	// 这几项此前**整体缺失**：原版会算超额收益、Beta、Alpha（Jensen's）、
	// 信息比率，而我这边 Metrics 里连字段都没有。
	ExcessReturn        float64 `json:"excessReturn"`
	Beta                float64 `json:"beta"`
	Alpha               float64 `json:"alpha"`
	InformationRatio    float64 `json:"informationRatio"`
	IsShortPeriod       bool    `json:"isShortPeriod"`
	AnnualReturnWarning string  `json:"annualReturnWarning"`
	// ScopeWarning 提示"回填范围远小于全市场" —— 这会让广度闸门误判，
	// 进而删光所有买入信号（回测零成交的常见原因）。
	// 见 service 侧的 backtestScopeWarning。
	ScopeWarning string `json:"scopeWarning"`
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

	// 相对基准的指标（原实现 `_calculate_relative_metrics` :206-252）
	calculateRelativeMetrics(&out, snapshots, annual)

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

// calculateRelativeMetrics 算相对基准的四个指标。
//
// 对应原实现 `MetricsCalculator._calculate_relative_metrics`（:206-252）：
//
//	excess_return      = total_return - 末个基准收益率
//	beta               = cov(策略日收益, 基准日收益) / var(基准日收益)
//	alpha (Jensen's)   = 年化 - (无风险 + beta × (基准年化 - 无风险))
//	information_ratio  = mean(超额日收益) × 252 / (std(超额日收益) × √252)
//
// ⚠ 协方差与标准差都按**样本口径（ddof=1）**，与 `np.cov` / `pandas.std()` 一致。
// ⚠ 基准数据缺失（首日基准净值为 0）时**整体跳过** —— 原实现也是直接 return。
func calculateRelativeMetrics(out *Metrics, snapshots []Snapshot, annualReturn float64) {
	if len(snapshots) < 2 {
		return
	}
	// 原实现：`if benchmark_values.iloc[0] > 0` 才算，否则 return
	if snapshots[0].BenchmarkValue <= 0 {
		return
	}

	finalBenchmarkReturn := snapshots[len(snapshots)-1].BenchmarkReturn
	out.ExcessReturn = round2(out.TotalReturn - finalBenchmarkReturn)

	// 基准日收益：`benchmark_values.pct_change().fillna(0)`
	n := len(snapshots)
	strategy := make([]float64, n)
	benchmark := make([]float64, n)
	for i, snap := range snapshots {
		strategy[i] = snap.DailyReturn / 100
		if i == 0 {
			benchmark[i] = 0
			continue
		}
		prev := snapshots[i-1].BenchmarkValue
		if prev > 0 {
			benchmark[i] = snap.BenchmarkValue/prev - 1
		}
	}

	// beta = cov01 / cov11（样本协方差，(n-1) 会约掉）
	cov01, cov11 := sampleCovariance(strategy, benchmark), sampleVariance(benchmark)
	if cov11 > 0 {
		out.Beta = round2(cov01 / cov11)
	}

	// alpha：注意基准年化用的是**末个基准收益率**（原实现 :242）
	benchmarkAnnual := finalBenchmarkReturn / 100
	expected := riskFreeRate + out.Beta*(benchmarkAnnual-riskFreeRate)
	out.Alpha = round2((annualReturn - expected) * 100)

	// 信息比率
	excess := make([]float64, n)
	for i := range strategy {
		excess[i] = strategy[i] - benchmark[i]
	}
	trackingError := sampleStd(excess) * math.Sqrt(tradingDaysPerYear)
	if trackingError > 0 {
		avgExcess := mean(excess) * tradingDaysPerYear
		out.InformationRatio = round2(avgExcess / trackingError)
	}
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// sampleVariance 是样本方差（ddof=1），与 `np.cov` 的对角元一致。
func sampleVariance(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	sum := 0.0
	for _, v := range values {
		sum += (v - m) * (v - m)
	}
	return sum / float64(len(values)-1)
}

// sampleCovariance 是样本协方差（ddof=1），与 `np.cov(x,y)[0][1]` 一致。
func sampleCovariance(x, y []float64) float64 {
	if len(x) < 2 || len(x) != len(y) {
		return 0
	}
	mx, my := mean(x), mean(y)
	sum := 0.0
	for i := range x {
		sum += (x[i] - mx) * (y[i] - my)
	}
	return sum / float64(len(x)-1)
}
