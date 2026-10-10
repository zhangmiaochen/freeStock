package backtest

import (
	"fmt"
	"math"
)

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
	// MaxDrawdownDuration 是最长回撤持续天数（原实现 `max_drawdown_duration`）。
	// 此前这里算了却丢掉，摘要里也就永远没有这个数。
	MaxDrawdownDuration int `json:"maxDrawdownDuration"`
	// MaxProfit / MaxLoss 是单笔最大盈利 / 最大亏损百分比（原实现同名指标）。
	MaxProfit float64 `json:"maxProfit"`
	MaxLoss   float64 `json:"maxLoss"`
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
	// ⚠ 后续风险调整指标要用**四舍五入后**的年化收益。
	// 原实现先 `metrics.annual_return = round(..., 2)`，随后
	// `_calculate_risk_adjusted` 里 `annual_return = metrics.annual_return / 100`
	//（backtest_metrics.py:145,185）—— 用未取整的值会让夏普/卡玛/索提诺/Alpha
	// 在小数点后两位上与原版不同。
	annual = out.AnnualReturn / 100

	switch {
	case days < 60:
		out.IsShortPeriod = true
		out.AnnualReturnWarning = fmt.Sprintf(
			"注意: 回测期仅%d个交易日，年化收益率%.1f%%可能被显著放大，仅供参考",
			days, out.AnnualReturn)
	case days < 120:
		out.AnnualReturnWarning = fmt.Sprintf(
			"提示: 回测期%d个交易日，建议延长回测期以获得更可靠的年化收益率", days)
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
	out.MaxDrawdownDuration = maxDuration

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

	// 成交统计：**只统计卖出**（有已实现盈亏的那一侧）。
	//
	// ⚠ 原实现 `_calculate_trade_stats`（backtest_metrics.py:253-269）：
	//
	//	sell_trades = [t for t in self.trades if t.action.value == 'sell']
	//	if not sell_trades: return          # ← 一笔卖出都没有时，
	//	                                    #   total_trades 保持 0
	//	metrics.total_trades = len(sell_trades)
	//
	// 此前这里写的是 `len(trades)`（买 + 卖都算），界面上"成交笔数"
	// 直接是原版的约两倍；而且只有买入、没有卖出时原版报 0、这里报买入数。
	sellTrades := make([]Trade, 0, len(trades))
	for _, trade := range trades {
		if trade.Action == "sell" {
			sellTrades = append(sellTrades, trade)
		}
	}
	if len(sellTrades) == 0 {
		return out
	}
	out.TotalTrades = len(sellTrades)

	// 盈亏口径：`> 0` 算赢，**`<= 0` 一律算输**（原实现 :262-263）。
	// 此前 `== 0` 既不算赢也不算输 —— 恰好 0% 的卖出会让 lose_count 偏小、
	// 胜率偏高。胜率的分母是 **total_trades**（全部卖出），不是 win+lose。
	profitSum, lossSum := 0.0, 0.0
	holdingSum := 0.0
	holdingCount := 0
	for _, trade := range sellTrades {
		if trade.ProfitLossPct > 0 {
			out.WinCount++
			profitSum += trade.ProfitLossPct
			if trade.ProfitLossPct > out.MaxProfit {
				out.MaxProfit = trade.ProfitLossPct
			}
		} else {
			out.LoseCount++
			lossSum += trade.ProfitLossPct
			if trade.ProfitLossPct < out.MaxLoss {
				out.MaxLoss = trade.ProfitLossPct
			}
		}
		if trade.HoldingDays > 0 {
			holdingSum += float64(trade.HoldingDays)
			holdingCount++
		}
	}
	if out.TotalTrades > 0 {
		out.WinRate = round2(float64(out.WinCount) / float64(out.TotalTrades) * 100)
	}
	if out.WinCount > 0 {
		out.AvgProfit = round2(profitSum / float64(out.WinCount))
	}
	// 原实现是 `abs(mean(losses))` —— 平均亏损取绝对值
	if out.LoseCount > 0 {
		out.AvgLoss = round2(math.Abs(lossSum / float64(out.LoseCount)))
	}
	// 原实现 `round(np.mean(holding_days), 1)` —— **1 位小数**，不是 2 位
	if holdingCount > 0 {
		out.AvgHoldingDays = math.Round(holdingSum/float64(holdingCount)*10) / 10
	}
	if out.MaxProfit != 0 {
		out.MaxProfit = round2(out.MaxProfit)
	}
	if out.MaxLoss != 0 {
		out.MaxLoss = round2(out.MaxLoss)
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
