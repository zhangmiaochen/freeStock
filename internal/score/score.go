// Package score 实现股票评分与市场状态判定。
//
// 逻辑 1:1 移植自 tushare-stock-tonyz：
//   - 指标与基础分：portfolio_service._build_strength_history_frame（627 行）
//   - portfolio_strategy.apply_strategy_scores
//   - 市场状态：volume_price_market_state.build_market_state_cache
//   - 策略参数：portfolio_strategy.build_strategy_config
//
// 复刻时**如实照搬**的几处反直觉之处（不要"顺手改好"）：
//
//  1. **全程不复权**。原项目直读 bars_daily 的原始 OHLC，
//     adjustflag 字段被 SELECT 但从不使用。MA / 收益 / 回撤 / RS 全部基于不复权价。
//  2. **市场状态是全市场宽度**，不是基准指数的趋势。原实现按日聚合全市场的
//     平均涨跌幅、上涨家数占比、成交额，再合成一个"代理指数"来判断牛熊震荡。
//  3. **repair_* 有 41 日延迟入账且是累计值**（防未来函数），
//     事件在第 i 天发生，要到第 i+41 天才计入统计。
//  4. **修复能力分用 0..1 的原始比例**（未乘 100）。
//  5. **过热冷却只作用于 trend/rs 两个基础分**。
package score

import (
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 数值工具（对齐 pandas 的 rolling + min_periods 语义）
// ---------------------------------------------------------------------------

// mean 计算 values[from:to] 的均值。
func mean(values []float64, from, to int) float64 {
	if from < 0 || to > len(values) || from >= to {
		return 0
	}
	sum := 0.0
	for _, v := range values[from:to] {
		sum += v
	}
	return sum / float64(to-from)
}

// rollingMean 取最近 period 个值的均值；不足 minPeriods 时返回 (0,false)。
//
// 对应 pandas 的 rolling(period, min_periods=minPeriods).mean()。
func rollingMean(values []float64, period, minPeriods int) (float64, bool) {
	if period <= 0 || minPeriods <= 0 || len(values) < minPeriods {
		return 0, false
	}
	if len(values) < period {
		// pandas 里 min_periods <= n < period 时会用现有的 n 个值算
		if len(values) < minPeriods {
			return 0, false
		}
		return mean(values, 0, len(values)), true
	}
	return mean(values, len(values)-period, len(values)), true
}

// rollingMax / rollingMin 取最近 period 个值的极值。
func rollingMax(values []float64, period, minPeriods int) (float64, bool) {
	return rollingExtreme(values, period, minPeriods, true)
}

func rollingMin(values []float64, period, minPeriods int) (float64, bool) {
	return rollingExtreme(values, period, minPeriods, false)
}

func rollingExtreme(values []float64, period, minPeriods int, wantMax bool) (float64, bool) {
	if period <= 0 || minPeriods <= 0 || len(values) < minPeriods {
		return 0, false
	}
	start := len(values) - period
	if start < 0 {
		start = 0
	}
	result := values[start]
	for _, v := range values[start:] {
		if (wantMax && v > result) || (!wantMax && v < result) {
			result = v
		}
	}
	return result, true
}

// pctChange 计算 N 期涨幅（%）：(close[t] / close[t-N] - 1) * 100。
func pctChange(values []float64, periods int) (float64, bool) {
	if periods <= 0 || len(values) < periods+1 {
		return 0, false
	}
	base := values[len(values)-1-periods]
	if base == 0 {
		return 0, false
	}
	return (values[len(values)-1]/base - 1) * 100, true
}

func clamp(value, low, high float64) float64 {
	switch {
	case math.IsNaN(value):
		// NaN 说明上游算不出来，归到下限（不能让它污染后续计算）
		return low
	case math.IsInf(value, 1):
		return high
	case math.IsInf(value, -1):
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func round(value float64, digits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}

// Bool 把布尔转成 0/1，便于和原项目的 `8*(close>ma20)` 写法逐字对应。
func boolToFloat(condition bool) float64 {
	if condition {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// 个股指标
// ---------------------------------------------------------------------------

// Bar 是一根不复权日线。
type Bar struct {
	Date   int64 // YYYYMMDD
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// Indicators 是单只股票在最新一根 K 线上的指标快照。
//
// 字段名与原项目 stock_strength_history 的列名一一对应。
type Indicators struct {
	Date   int64   `json:"date"`
	Close  float64 `json:"closePrice"`
	Open   float64 `json:"openPrice"`
	High   float64 `json:"highPrice"`
	Low    float64 `json:"lowPrice"`
	PctChg float64 `json:"pctChg"`
	Amount float64 `json:"amountYuan"`

	Ma5   float64 `json:"ma5Price"`
	Ma10  float64 `json:"ma10Price"`
	Ma20  float64 `json:"ma20Price"`
	Ma30  float64 `json:"ma30Price"`
	Ma60  float64 `json:"ma60Price"`
	Ma120 float64 `json:"ma120Price"`
	Ma250 float64 `json:"ma250Price"`

	Ma5Prev   float64 `json:"ma5PrevPrice"`
	Ma10Prev  float64 `json:"ma10PrevPrice"`
	Ma250Prev float64 `json:"ma250PrevPrice"`

	VolumeRatio20  float64 `json:"volumeRatio20"`
	VolumeRatio520 float64 `json:"volumeRatio520"`

	StockReturn3D  float64 `json:"stockReturn3d"`
	StockReturn5D  float64 `json:"stockReturn5d"`
	StockReturn10D float64 `json:"stockReturn10d"`
	StockReturn20D float64 `json:"stockReturn20d"`

	DrawdownFromHigh20  float64 `json:"drawdownFromHigh20"`
	DrawdownFromHigh120 float64 `json:"drawdownFromHigh120"`
	ReboundFromLow60    float64 `json:"reboundFromLow60"`

	// 以下为横截面/外部输入，由调用方填充
	BenchmarkReturn20D  float64 `json:"benchmarkReturn20d"`
	RelativeStrength20D float64 `json:"relativeStrength20d"`
	IndustryReturn20D   float64 `json:"industryReturn20d"`

	// 表外字段（原项目不入库、运行时算）
	RangePosition120    float64 `json:"rangePosition120"`
	AmountRankPct       float64 `json:"amountRankPct"`
	TurnRankPct         float64 `json:"turnRankPct"`
	IndustryStrengthPct float64 `json:"industryStrengthPct"`
}

// ComputeIndicators 从升序日线算出指标。
//
// 只取**最后一根** K 线上的快照 —— 原项目是逐日整表算，
// 这里按需算某一天，语义一致（滚动窗口只看历史）。
func ComputeIndicators(bars []Bar) (Indicators, bool) {
	if len(bars) < 2 {
		return Indicators{}, false
	}
	opens := make([]float64, len(bars))
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	volumes := make([]float64, len(bars))
	for i, b := range bars {
		opens[i], highs[i], lows[i], closes[i], volumes[i] = b.Open, b.High, b.Low, b.Close, b.Volume
	}
	last := bars[len(bars)-1]

	out := Indicators{
		Date:   last.Date,
		Open:   last.Open,
		High:   last.High,
		Low:    last.Low,
		Close:  last.Close,
		Amount: last.Amount,
	}
	if prev := closes[len(closes)-2]; prev > 0 {
		out.PctChg = round((last.Close/prev-1)*100, 4)
	}

	// 均线：rolling(N, min_periods=N).mean()
	for _, item := range []struct {
		period int
		dest   *float64
	}{
		{5, &out.Ma5}, {10, &out.Ma10}, {20, &out.Ma20}, {30, &out.Ma30},
		{60, &out.Ma60}, {120, &out.Ma120}, {250, &out.Ma250},
	} {
		if v, ok := rollingMean(closes, item.period, item.period); ok {
			*item.dest = round(v, 4)
		}
	}
	// 前一日均线：shift(1) 后再滚动
	if v, ok := rollingMean(closes[:len(closes)-1], 5, 5); ok {
		out.Ma5Prev = round(v, 4)
	}
	if v, ok := rollingMean(closes[:len(closes)-1], 10, 10); ok {
		out.Ma10Prev = round(v, 4)
	}
	if v, ok := rollingMean(closes[:len(closes)-1], 250, 250); ok {
		out.Ma250Prev = round(v, 4)
	}

	// 量比
	if avg, ok := rollingMean(volumes, 20, 10); ok && avg > 0 {
		out.VolumeRatio20 = round(last.Volume/avg, 4)
	}
	v5, ok5 := rollingMean(volumes, 5, 3)
	v20, ok20 := rollingMean(volumes, 20, 8)
	if ok5 && ok20 && v20 > 0 {
		out.VolumeRatio520 = round(v5/v20, 4)
	}

	// 区间收益
	for _, item := range []struct {
		period int
		dest   *float64
	}{
		{3, &out.StockReturn3D}, {5, &out.StockReturn5D},
		{10, &out.StockReturn10D}, {20, &out.StockReturn20D},
	} {
		if v, ok := pctChange(closes, item.period); ok {
			*item.dest = round(v, 4)
		}
	}

	// 回撤与反弹
	high20, ok20h := rollingMax(highs, 20, 8)
	high60, ok60h := rollingMax(highs, 60, 20)
	high120, ok120h := rollingMax(highs, 120, 40)
	low60, ok60l := rollingMin(lows, 60, 20)
	low120, ok120l := rollingMin(lows, 120, 40)

	if ok20h && high20 > 0 {
		out.DrawdownFromHigh20 = round((last.Close/high20-1)*100, 4)
	}
	if ok120h && high120 > 0 {
		out.DrawdownFromHigh120 = round((last.Close/high120-1)*100, 4)
	}
	if ok60h && ok60l && high60-low60 > 0 {
		out.ReboundFromLow60 = round((last.Close-low60)/(high60-low60), 4)
	}
	if ok120h && ok120l {
		out.RangePosition120 = round(clamp((last.Close-low120)/math.Max(high120-low120, 1e-9), 0, 1), 4)
	}

	// 相对强度需要一个基准收益，这里先留空由调用方填
	out.RelativeStrength20D = out.StockReturn20D
	return out, true
}

// PrevHigh60 返回**前一日**的 60 日最高价（对应 prev_high60 = high.shift(1).rolling(60).max()）。
//
// 用在结构分的 breakout_gap 上：判断今日是否突破了「昨天为止的 60 日高点」。
func PrevHigh60(bars []Bar) float64 {
	if len(bars) < 2 {
		return 0
	}
	highs := make([]float64, len(bars)-1)
	for i := 0; i < len(bars)-1; i++ {
		highs[i] = bars[i].High
	}
	if v, ok := rollingMax(highs, 60, 20); ok {
		return v
	}
	return 0
}

// ---------------------------------------------------------------------------
// 基础分（trend_growth 直接使用的五个）
// ---------------------------------------------------------------------------

// BaseScores 是五个基础分。
//
// 原项目里这五项对四个策略**各有各的算法**；这里实现的是 trend_growth 的口径
// （其余三个策略的公式差异很大，且互相之间无法统一抽象，见 docs 说明）。
type BaseScores struct {
	Trend          float64 `json:"trendScore"`
	Structure      float64 `json:"structureScore"`
	RS             float64 `json:"rsScore"`
	Capital        float64 `json:"capitalScore"`
	RepairAbility  float64 `json:"repairAbilityScore"`
	NearTermRepair float64 `json:"nearTermRepairScore"`

	// Total 是加权总分（权重恒定 30/20/20/15/15）
	Total float64 `json:"totalScore"`
}

// 五个基础分的**上限**，相加恰好是 100。
//
// ⚠ 这**不是**加权系数：原项目总分是 `clip(五项和 + 调整分, 0, 100)`，
// 也就是五项直接相加。规格里"权重恒定 30/20/20/15/15"描述的是
// **每一项自己能贡献多少分**，不是"各占多少百分比"。
//
// 曾经把这里实现成 `trend*0.30 + structure*0.20 + ...`，结果是总分上限只有
// 21.5，而参考实现的建仓线是 66、清仓线是 45 —— 阈值永远够不到。
// 用真实数据跑评分时（最高分只有 18）才暴露出这个问题。
const (
	maxTrend     = 30.0
	maxStructure = 20.0
	maxRS        = 20.0
	maxCapital   = 15.0
	maxRepair    = 15.0
)

// scoreWindow 对应原项目的 score_0_1(value, low, high)：把 value 线性映射到 0..1。
func scoreWindow(value, low, high float64) float64 {
	if high <= low {
		return 0
	}
	return clamp((value-low)/(high-low), 0, 1)
}

// TrendBase 趋势基础分（上限 30）。
//
//	8*(close>ma20) + 8*(ma20>ma60) + 6*(ma60>ma120) + 4*(ma120>ma250)
//	+ 2*(ma20>ma20_lag5) + 2*(ma60>ma60_lag10)
func TrendBase(ind Indicators, ma20Lag5, ma60Lag10 float64) float64 {
	score := 8*boolToFloat(ind.Close > ind.Ma20 && ind.Ma20 > 0) +
		8*boolToFloat(ind.Ma20 > ind.Ma60 && ind.Ma60 > 0) +
		6*boolToFloat(ind.Ma60 > ind.Ma120 && ind.Ma120 > 0) +
		4*boolToFloat(ind.Ma120 > ind.Ma250 && ind.Ma250 > 0) +
		2*boolToFloat(ind.Ma20 > ma20Lag5 && ma20Lag5 > 0) +
		2*boolToFloat(ind.Ma60 > ma60Lag10 && ma60Lag10 > 0)
	return round(clamp(score, 0, 30), 4)
}

// StructureBase 结构基础分（上限 20）。
//
// 位置分档 + 突破缺口分档 + 均线量能配合 + 模型胜率。
func StructureBase(ind Indicators, prevHigh60, winRate float64) float64 {
	score := 0.0
	// 位置：range_position_120 落在舒服区间得分最高
	switch {
	case ind.RangePosition120 >= 0.35 && ind.RangePosition120 <= 0.75:
		score += 8
	case ind.RangePosition120 >= 0.2 && ind.RangePosition120 <= 0.9:
		score += 4
	default:
		score += 1
	}
	// 突破缺口
	if prevHigh60 > 0 {
		gap := (ind.Close - prevHigh60) / prevHigh60 * 100
		switch {
		case gap >= -2.5 && gap <= 3:
			score += 6
		case gap > 3 && gap <= 8:
			score += 3
		}
	}
	// 均线上方 + 量能温和 + 有回撤（三者同时）
	if ind.Close >= ind.Ma20 && ind.Ma20 > 0 &&
		ind.VolumeRatio20 <= 1.15 &&
		ind.DrawdownFromHigh120 <= -3 && ind.DrawdownFromHigh120 >= -18 {
		score += 4
	}
	score += clamp(winRate/100, 0, 1) * 2
	return round(clamp(score, 0, 20), 4)
}

// RSBase 相对强度基础分（上限 20），含过热冷却。
//
//	clip((rs20+10)/20,0,1)*14 + clip((industry_edge+10)/20,0,1)*6
//	过热冷却：rs20>=35 且 dd120>=-6 → -1；rs20>=50 且 dd120>=-3 → -2
func RSBase(ind Indicators) float64 {
	industryEdge := ind.IndustryReturn20D - ind.BenchmarkReturn20D
	score := scoreWindow(ind.RelativeStrength20D, -10, 10)*14 +
		scoreWindow(industryEdge, -10, 10)*6

	// 过热冷却（只作用于 trend/rs 两个基础分）
	if ind.RelativeStrength20D >= 35 && ind.DrawdownFromHigh120 >= -6 {
		score -= 1
	}
	if ind.RelativeStrength20D >= 50 && ind.DrawdownFromHigh120 >= -3 {
		score -= 2
	}
	return round(clamp(score, 0, 20), 4)
}

// CapitalBase 资金基础分（上限 15）。
//
//	amount_rank*7 + turn_rank*4 + clip((vr20-0.8)/1.2,0,1)*2 + industry_strength*2
func CapitalBase(ind Indicators) float64 {
	score := clamp(ind.AmountRankPct, 0, 1)*7 +
		clamp(ind.TurnRankPct, 0, 1)*4 +
		clamp((ind.VolumeRatio20-0.8)/1.2, 0, 1)*2 +
		clamp(ind.IndustryStrengthPct, 0, 1)*2
	return round(clamp(score, 0, 15), 4)
}

// NearTermRepairScore 近期修复分（上限 15）。
//
//	clip(rebound60,0,1)*6 + clip((20+dd120)/20,0,1)*4
//	+ 2*(close>ma20) + 1.5*(close>ma60)
//	+ clip((close/prev_high60-0.95)/0.08,0,1)*1.5
func NearTermRepairScore(ind Indicators, prevHigh60 float64) float64 {
	score := clamp(ind.ReboundFromLow60, 0, 1)*6 +
		clamp((20+ind.DrawdownFromHigh120)/20, 0, 1)*4 +
		2*boolToFloat(ind.Close > ind.Ma20 && ind.Ma20 > 0) +
		1.5*boolToFloat(ind.Close > ind.Ma60 && ind.Ma60 > 0)
	if prevHigh60 > 0 {
		score += clamp((ind.Close/prevHigh60-0.95)/0.08, 0, 1) * 1.5
	}
	return round(clamp(score, 0, 15), 4)
}

// TotalScore 合成总分：`clip(五项和 + 调整分, 0, 100)`。
//
// 五项**直接相加**（各自上限 30/20/20/15/15，合计 100），不是加权平均。
// 调整分是 ±修正项，可以是负数。
//
// 注意：原项目 trend_growth 的第五项用的是 **repair_ability_score**，
// 而其他策略用 near_term_repair_score —— 两者语义不同，这里按 trend_growth 口径。
func TotalScore(trend, structure, rs, capital, repair, adjustment float64) float64 {
	total := trend + structure + rs + capital + repair + adjustment
	return round(clamp(total, 0, 100), 2)
}

// ---------------------------------------------------------------------------
// 修复能力（repair_*）
// ---------------------------------------------------------------------------

// RepairParams 是修复能力的参数（原项目为模块级常量）。
const (
	RepairThreshold = -10.0 // 触发事件的回撤阈值 %
	RepairWindow    = 40    // 事件后的观察窗口（交易日）
	RepairGap       = 10    // 两次事件的最小间隔（交易日）
	RepairKnownLag  = 41    // 事件后多少天才计入统计（窗口 + 1，防未来函数）
)

// RepairFeatures 是修复能力的统计结果。
type RepairFeatures struct {
	EventCount          int     `json:"repairEventCount"`
	RecoveryProbability float64 `json:"repairRecoveryProbability"`
	AvgRecoveryDays     float64 `json:"repairAvgRecoveryDays"`
	NewHighProbability  float64 `json:"repairNewHighProbability"`
	AbilityScore        float64 `json:"repairAbilityScore"`
}

// BuildRepairFeatures 计算修复能力。
//
// 事件定义：当日 `close/high120 - 1 <= -10%`，且与上一次事件间隔 >= 10 个交易日。
// 事件发生后 40 日窗口内：
//   - `close >= high120[i]*0.98` 记 recovered
//   - `max(窗口内 close) >= high120[i]*1.02` 记 new_high
//
// **关键：结果在 known_at = i + 41 才累计入账**，即算第 i 天的事件时
// 只统计 `i + RepairKnownLag <= 最后一天` 的事件 —— 这是原实现防未来函数的做法，
// 也意味着最近 41 天内的事件还不计入。
//
// 统计是**累计值**（只增不减）。
func BuildRepairFeatures(bars []Bar) RepairFeatures {
	var out RepairFeatures
	if len(bars) < RepairWindow+2 {
		// 原实现：n <= 41 的股票全为 0
		return out
	}

	highs := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	for i, b := range bars {
		highs[i], closes[i] = b.High, b.Close
	}

	var (
		events     int
		recovered  int
		newHighs   int
		totalDays  float64
		recoverCnt int
		lastEvent  = -RepairGap - 1
	)

	last := len(bars) - 1
	for i := 0; i <= last; i++ {
		// 事件条件：相对 120 日高点回撤 <= -10%
		high120, ok := rollingMax(highs[:i+1], 120, 40)
		if !ok || high120 <= 0 {
			continue
		}
		if (closes[i]/high120-1)*100 > RepairThreshold {
			continue
		}
		// 最小间隔
		if i-lastEvent < RepairGap {
			continue
		}
		// **防未来函数**：事件后 41 天内不能入账
		if i+RepairKnownLag > last {
			continue
		}
		lastEvent = i
		events++

		// 观察窗口（不越过数据末尾）
		end := i + RepairWindow
		if end > last {
			end = last
		}
		recoveredFlag := false
		newHighFlag := false
		recoverDay := 0
		for j := i + 1; j <= end; j++ {
			if closes[j] >= high120*0.98 && !recoveredFlag {
				recoveredFlag = true
				recoverDay = j - i
			}
			if closes[j] >= high120*1.02 {
				newHighFlag = true
			}
		}
		if recoveredFlag {
			recovered++
			totalDays += float64(recoverDay)
			recoverCnt++
		}
		if newHighFlag {
			newHighs++
		}
	}

	out.EventCount = events
	if events == 0 {
		return out
	}

	// 注意：这里用的是 0..1 的**原始比例**（未乘 100），与原实现一致
	out.RecoveryProbability = round(float64(recovered)/float64(events), 4)
	out.NewHighProbability = round(float64(newHighs)/float64(events), 4)
	if recoverCnt > 0 {
		out.AvgRecoveryDays = round(totalDays/float64(recoverCnt), 4)
	}

	// repair_ability_score = clip(prob*8 + speed*4 + newhigh*3, 0, 15)
	// speed = clip((40 - avg_days) / 32, 0, 1)
	speed := clamp((float64(RepairWindow)-out.AvgRecoveryDays)/32, 0, 1)
	out.AbilityScore = round(clamp(
		out.RecoveryProbability*8+speed*4+out.NewHighProbability*3, 0, 15), 4)
	return out
}

// ---------------------------------------------------------------------------
// 横截面分位
// ---------------------------------------------------------------------------

// RankPct 计算 value 在同组里的百分位（0..1，越大越高）。
//
// 对应 pandas 的 rank(pct=True)：并列取平均名次。
func RankPct(values []float64, value float64) float64 {
	if len(values) == 0 {
		return 0
	}
	less, equal := 0, 0
	for _, v := range values {
		switch {
		case v < value:
			less++
		case v == value:
			equal++
		}
	}
	// pandas 默认 method='average'：并列占用的名次取平均
	if equal == 0 {
		return 0
	}
	avgRank := float64(less) + (float64(equal)+1)/2
	return round(avgRank/float64(len(values)), 4)
}

// Median 返回中位数（同 industry 收益均值等场景用）。
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
