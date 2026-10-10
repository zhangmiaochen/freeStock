package volprice

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Bar 是一根已归一的日线（对应 `prepare_bars` 的输出列）。
type Bar struct {
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// VectorBars 是"已附加全部 vp_* 派生列"的 K 线帧。
//
// 对应 `volume_price_features._attach_vector_bars_columns` 出来的 DataFrame：
// 原实现是把 58 个派生列挂回同一个 frame，这里显式建模，避免再解析列名。
type VectorBars struct {
	Date   StrSeries
	Open   Series
	High   Series
	Low    Series
	Close  Series
	Volume Series

	PrevOpen  Series
	PrevHigh  Series
	PrevLow   Series
	PrevClose Series

	CloseMa5      Series
	CloseMa10     Series
	CloseMa20     Series
	CloseMa60     Series
	CloseMax5     Series
	CloseMedian60 Series

	HighRoll20 Series
	LowRoll10  Series
	LowRoll20  Series

	Prev20High        Series
	Prev20Low         Series
	Prev20VolumeMean  Series
	Prev30High        Series
	Prev30Low         Series
	Prev40High        Series
	Prev40Low         Series
	Prev60High        Series
	Prev60Low         Series
	Prev2VolumeMean   Series
	Prev3VolumeMean   Series
	Prev5VolumeMean   Series
	Prev8VolumeMean   Series
	Prev8LowMin       Series
	Prev8HighMax      Series
	Prev8CloseMin     Series
	Prev8DownCloseCnt Series
	Prev10HighMax     Series
	Prev5Close        Series
	Prev10Close       Series
	Prev20Close       Series
	Prev60Close       Series

	ClosePos         Series
	LowerShadowRatio Series
	UpperShadowRatio Series
	VolumeDecayScore Series
	ThreeDayDrift    BoolSeries

	PlatformRangePct Series
	VolumeRatio20    Series
	PullbackRatio    Series
	HigherHighFlag   BoolSeries
	HigherLowFlag    BoolSeries
	SupportHoldFlag  BoolSeries
	BreakoutFlag     BoolSeries
	PanicVolumeFlag  BoolSeries
	TrendScore       Series
	ReversalFlag     BoolSeries

	VolumeBehavior         StrSeries
	VolumeBehaviorLabel    StrSeries
	AcceptanceQuality      StrSeries
	AcceptanceQualityLabel StrSeries
	UpperShadowBehavior    StrSeries
	UpperShadowBehaviorLbl StrSeries
	BehaviorQualityScore   Series
	DeathVolumeRisk        BoolSeries
}

// Len 返回 K 线根数。
func (v *VectorBars) Len() int { return len(v.Close) }

// Columns 把全部派生列导出成"列名 → 值"，供黄金值比对与后续模型使用。
//
// 列名与原实现的 `_VECTOR_BARS_COLUMNS` 完全一致。
func (v *VectorBars) Columns() (map[string]Series, map[string]BoolSeries, map[string]StrSeries) {
	numeric := map[string]Series{
		"vp_prev_open":              v.PrevOpen,
		"vp_prev_high":              v.PrevHigh,
		"vp_prev_low":               v.PrevLow,
		"vp_prev_close":             v.PrevClose,
		"vp_close_ma5":              v.CloseMa5,
		"vp_close_ma10":             v.CloseMa10,
		"vp_close_ma20":             v.CloseMa20,
		"vp_close_ma60":             v.CloseMa60,
		"vp_close_max5":             v.CloseMax5,
		"vp_close_median60":         v.CloseMedian60,
		"vp_high_roll20":            v.HighRoll20,
		"vp_low_roll10":             v.LowRoll10,
		"vp_low_roll20":             v.LowRoll20,
		"vp_prev20_high":            v.Prev20High,
		"vp_prev20_low":             v.Prev20Low,
		"vp_prev20_volume_mean":     v.Prev20VolumeMean,
		"vp_prev30_high":            v.Prev30High,
		"vp_prev30_low":             v.Prev30Low,
		"vp_prev40_high":            v.Prev40High,
		"vp_prev40_low":             v.Prev40Low,
		"vp_prev60_high":            v.Prev60High,
		"vp_prev60_low":             v.Prev60Low,
		"vp_prev2_volume_mean":      v.Prev2VolumeMean,
		"vp_prev3_volume_mean":      v.Prev3VolumeMean,
		"vp_prev5_volume_mean":      v.Prev5VolumeMean,
		"vp_prev8_volume_mean":      v.Prev8VolumeMean,
		"vp_prev8_low_min":          v.Prev8LowMin,
		"vp_prev8_high_max":         v.Prev8HighMax,
		"vp_prev8_close_min":        v.Prev8CloseMin,
		"vp_prev8_down_close_count": v.Prev8DownCloseCnt,
		"vp_prev10_high_max":        v.Prev10HighMax,
		"vp_prev5_close":            v.Prev5Close,
		"vp_prev10_close":           v.Prev10Close,
		"vp_prev20_close":           v.Prev20Close,
		"vp_prev60_close":           v.Prev60Close,
		"vp_close_pos":              v.ClosePos,
		"vp_lower_shadow_ratio":     v.LowerShadowRatio,
		"vp_upper_shadow_ratio":     v.UpperShadowRatio,
		"vp_volume_decay_score":     v.VolumeDecayScore,
		"vp_platform_range_pct":     v.PlatformRangePct,
		"vp_volume_ratio_20":        v.VolumeRatio20,
		"vp_pullback_ratio":         v.PullbackRatio,
		"vp_trend_score":            v.TrendScore,
		"vp_behavior_quality_score": v.BehaviorQualityScore,
	}
	boolean := map[string]BoolSeries{
		"vp_higher_high_flag":  v.HigherHighFlag,
		"vp_higher_low_flag":   v.HigherLowFlag,
		"vp_support_hold_flag": v.SupportHoldFlag,
		"vp_breakout_flag":     v.BreakoutFlag,
		"vp_panic_volume_flag": v.PanicVolumeFlag,
		"vp_reversal_flag":     v.ReversalFlag,
		"vp_three_day_drift":   v.ThreeDayDrift,
		"vp_death_volume_risk": v.DeathVolumeRisk,
	}
	text := map[string]StrSeries{
		"vp_volume_behavior":             v.VolumeBehavior,
		"vp_volume_behavior_label":       v.VolumeBehaviorLabel,
		"vp_acceptance_quality":          v.AcceptanceQuality,
		"vp_acceptance_quality_label":    v.AcceptanceQualityLabel,
		"vp_upper_shadow_behavior":       v.UpperShadowBehavior,
		"vp_upper_shadow_behavior_label": v.UpperShadowBehaviorLbl,
	}
	return numeric, boolean, text
}

// floatingOrDefault 对应原实现的 `_float_or_default`（NaN → default）。
func floatingOrDefault(v float64, def float64) float64 {
	if isNaN(v) {
		return def
	}
	return v
}

// optionalFloat 对应 `_optional_float`：NaN → nil（这里用 ok 表示）。
func optionalFloat(v float64) (float64, bool) {
	if isNaN(v) {
		return 0, false
	}
	return v, true
}

// AttachVectorBarsColumns 对应 `_attach_vector_bars_columns`（1:1）。
//
// 输入必须是已按 trade_date 升序、无重复日期的 K 线（与 `prepare_bars` 的输出一致）。
func AttachVectorBarsColumns(bars []Bar) *VectorBars {
	n := len(bars)
	out := &VectorBars{
		Date:   make(StrSeries, n),
		Open:   make(Series, n),
		High:   make(Series, n),
		Low:    make(Series, n),
		Close:  make(Series, n),
		Volume: make(Series, n),
	}
	for i, bar := range bars {
		out.Date[i] = bar.Date
		out.Open[i] = bar.Open
		out.High[i] = bar.High
		out.Low[i] = bar.Low
		out.Close[i] = bar.Close
		out.Volume[i] = bar.Volume
	}
	if n == 0 {
		return out
	}

	openS, highS, lowS, closeS, volumeS := out.Open, out.High, out.Low, out.Close, out.Volume

	// down_close = close.diff().lt(0).astype("float64")
	downClose := closeS.Diff().LT(0).Float()

	// row_low / row_high = 每行 min/max(open, close)（pandas min(axis=1) 跳过 NaN）
	rowLow := make(Series, n)
	rowHigh := make(Series, n)
	for i := range openS {
		rowLow[i] = pandasRowMin(openS[i], closeS[i])
		rowHigh[i] = pandasRowMax(openS[i], closeS[i])
	}

	// body = |close - open|；body_base = body.where(body>0, |close|*0.002).clip(lower=0.01)
	body := make(Series, n)
	bodyFloor := make(Series, n)
	for i := range closeS {
		body[i] = math.Abs(closeS[i] - openS[i])
		bodyFloor[i] = math.Abs(closeS[i]) * 0.002
	}
	bodyBase := body.WhereOther(body.GT(0), bodyFloor).Clip(0.01, nan())

	// rng = (high - low).where((high - low) > 0)
	rawRange := highS.Sub(lowS)
	rng := rawRange.Where(rawRange.GT(0))

	out.PrevOpen = openS.Shift(1)
	out.PrevHigh = highS.Shift(1)
	out.PrevLow = lowS.Shift(1)
	out.PrevClose = closeS.Shift(1)

	out.CloseMa5 = closeS.RollingMean(5, 5)
	out.CloseMa10 = closeS.RollingMean(10, 10)
	out.CloseMa20 = closeS.RollingMean(20, 10)
	out.CloseMa60 = closeS.RollingMean(60, 20)
	out.CloseMax5 = closeS.RollingMax(5, 1)
	out.CloseMedian60 = closeS.RollingMedian(60, 20)
	out.HighRoll20 = highS.RollingMax(20, 5)
	out.LowRoll10 = lowS.RollingMin(10, 5)
	out.LowRoll20 = lowS.RollingMin(20, 5)
	out.Prev20High = highS.Shift(1).RollingMax(20, 10)
	out.Prev20Low = lowS.Shift(1).RollingMin(20, 10)
	out.Prev20VolumeMean = volumeS.Shift(1).RollingMean(20, 10)
	out.Prev30High = highS.Shift(1).RollingMax(30, 20)
	out.Prev30Low = lowS.Shift(1).RollingMin(30, 20)
	out.Prev40High = highS.Shift(21).RollingMax(20, 10)
	out.Prev40Low = lowS.Shift(21).RollingMin(20, 10)
	out.Prev60High = highS.Shift(1).RollingMax(60, 45)
	out.Prev60Low = lowS.Shift(1).RollingMin(60, 45)
	out.Prev2VolumeMean = volumeS.Shift(1).RollingMean(2, 2)
	out.Prev3VolumeMean = volumeS.Shift(1).RollingMean(3, 3)
	out.Prev5VolumeMean = volumeS.Shift(1).RollingMean(5, 5)
	out.Prev8VolumeMean = volumeS.Shift(1).RollingMean(8, 5)
	out.Prev8LowMin = lowS.Shift(1).RollingMin(8, 5)
	out.Prev8HighMax = highS.Shift(1).RollingMax(8, 5)
	out.Prev8CloseMin = closeS.Shift(1).RollingMin(8, 5)
	out.Prev8DownCloseCnt = Series(downClose).Shift(1).RollingSum(8, 5)
	out.Prev10HighMax = highS.Shift(1).RollingMax(10, 5)
	out.Prev5Close = closeS.Shift(5)
	out.Prev10Close = closeS.Shift(10)
	out.Prev20Close = closeS.Shift(20)
	out.Prev60Close = closeS.Shift(59)

	out.ClosePos = closeS.Sub(lowS).Div(rng).FillNA(0.5)
	out.LowerShadowRatio = rowLow.Sub(lowS).Div(bodyBase)
	out.UpperShadowRatio = highS.Sub(rowHigh).Div(bodyBase)

	// volume_decay：两级 mask，最后 fillna(0.0)
	volumeDecay := make(Series, n)
	for i := range volumeDecay {
		volumeDecay[i] = 0.0
	}
	v1, v2 := volumeS.Shift(1), volumeS.Shift(2)
	firstCond := boolAnd(volumeS.LT2(v1), v1.LT2(v2))
	volumeDecay = volumeDecay.MaskValue(firstCond, 100.0)
	avgPrev2 := v1.Add(v2).DivScalar(2)
	secondCond := boolAnd(volumeDecay.EQ(0.0), volumeS.LT2(avgPrev2))
	volumeDecay = volumeDecay.MaskValue(secondCond, 60.0)
	out.VolumeDecayScore = volumeDecay.FillNA(0.0)

	// three_day_drift
	out.ThreeDayDrift = boolAndAll(
		closeS.LT2(closeS.Shift(1)),
		closeS.Shift(1).LT2(closeS.Shift(2)),
		lowS.LE2(lowS.Shift(1)),
		lowS.Shift(1).LE2(lowS.Shift(2)),
	)

	platformHigh := out.Prev20High
	platformLow := out.Prev20Low
	prev20AvgVolume := out.Prev20VolumeMean
	prev40High := out.Prev40High
	prev40Low := out.Prev40Low
	high20 := out.HighRoll20
	low20 := out.LowRoll20
	prev60Close := out.Prev60Close
	closeMedian60 := out.CloseMedian60
	prevOpen := out.PrevOpen
	prevHigh := out.PrevHigh
	prevClose := out.PrevClose
	prevLow := out.PrevLow
	closePos := out.ClosePos.FillNA(0.5)
	lowerShadow := out.LowerShadowRatio.FillNA(0.0)
	upperShadow := out.UpperShadowRatio.FillNA(0.0)
	volumeDecayFlag := out.VolumeDecayScore.GE(60.0)
	platformValid := boolAnd(platformHigh.GT(0), platformLow.GT(0))
	avg20Valid := prev20AvgVolume.GT(0)

	out.PlatformRangePct = platformHigh.Div(platformLow).SubScalar(1.0).MulScalar(100.0).Where(platformValid)
	volumeRatio20 := volumeS.Div(prev20AvgVolume).Where(boolAnd(platformValid, avg20Valid))
	out.VolumeRatio20 = volumeRatio20
	out.HigherHighFlag = boolAndAll(platformValid, prev40High.GT(0), platformHigh.GT2(prev40High))
	out.HigherLowFlag = boolAndAll(platformLow.GT(0), prev40Low.GT(0), platformLow.GT2(prev40Low))
	out.PullbackRatio = high20.Sub(closeS).Div(high20).MulScalar(100.0).Where(boolAnd(high20.GT(0), low20.GT(0)))
	out.SupportHoldFlag = boolAndAll(high20.GT(0), low20.GT(0), closeS.GE2(low20), lowS.GE2(low20.MulScalar(0.99)))
	out.BreakoutFlag = boolAndAll(platformValid, avg20Valid, closeS.GT2(platformHigh), volumeRatio20.GE(2.0))
	out.PanicVolumeFlag = boolAndAll(platformValid, avg20Valid, volumeRatio20.GE(3.0), closeS.LT2(openS))

	// effective_prev60_close：先 where(>0)，再把"前 20 根之后仍缺失"的补成首根收盘价
	effectivePrev60Close := prev60Close.Where(prev60Close.GT(0))
	firstClose := floatingOrDefault(closeS[0], 0.0)
	if firstClose > 0 {
		patch := make(BoolSeries, n)
		isna := effectivePrev60Close.IsNA()
		for i := 0; i < n; i++ {
			patch[i] = isna[i] && i >= 19
		}
		effectivePrev60Close = effectivePrev60Close.MaskValue(patch, firstClose)
	}
	trendValid := boolAnd(effectivePrev60Close.GT(0), closeMedian60.GT(0))
	retScore := closeS.Div(effectivePrev60Close).SubScalar(1.0).MulScalar(100.0).Clip(-30.0, 60.0)
	structureScore := out.HigherHighFlag.Float().MulScalar(25.0).Add(out.HigherLowFlag.Float().MulScalar(25.0))
	closeScore := closeS.GE2(closeMedian60).Float().MulScalar(20.0)
	// `.where(trend_valid, 0.0)`：条件为真保留计算值，否则取 0.0
	out.TrendScore = retScore.Add(structureScore).Add(closeScore).AddScalar(30.0).
		Clip(0.0, 100.0).WhereOther(trendValid, zeroSeries(n))

	// reversal_flag
	bullish := closeS.GT2(openS)
	engulf := boolAnd(closeS.GT2(prevOpen.FillNARef(openS)), openS.LE2(prevClose.FillNARef(closeS)))
	recoverHigh := closeS.GT2(prevHigh.FillNARef(highS))
	out.ReversalFlag = boolAnd(bullish, boolOrAll(engulf, recoverHigh, lowerShadow.GE(2.0)))

	// 量价行为分类
	volumeRatioBase := volumeRatio20.FillNA(1.0)
	prevCloseBase := prevClose.FillNARef(closeS)
	prevLowBase := prevLow.FillNARef(lowS)
	closeUp := closeS.GE2(prevCloseBase)
	lowHold := lowS.GE2(prevLowBase.MulScalar(0.995))
	weakClose := boolAndAll(closeS.LT2(openS), closeS.LT2(prevCloseBase), closePos.LT(0.45))
	lowBreak := lowS.LT2(prevLowBase.MulScalar(0.995))
	strongAccept := boolAndAll(
		closePos.GE(0.62),
		boolOrAll(lowerShadow.GE(1.0), closeUp),
		boolOrAll(lowHold, out.SupportHoldFlag),
	)
	weakAccept := boolOrAll(closePos.GE(0.45), lowerShadow.GE(0.6), closeUp, lowHold)
	noAccept := boolOr(boolNot(weakAccept), boolAnd(weakClose, lowBreak))
	deathVolume := boolAndAll(
		volumeDecayFlag,
		volumeRatioBase.LE(0.75),
		noAccept,
		boolOrAll(weakClose, lowBreak, out.ThreeDayDrift),
	)
	distributionVolume := boolAndAll(volumeRatioBase.GE(1.8), upperShadow.GE(1.5), closePos.LT(0.5))
	strongDecay := boolAnd(volumeDecayFlag, strongAccept)
	activeVolume := volumeRatioBase.GE(1.2)

	volumeBehavior := selectStr([]BoolSeries{
		deathVolume, out.BreakoutFlag, out.PanicVolumeFlag,
		distributionVolume, strongDecay, volumeDecayFlag, activeVolume,
	}, []string{
		"exhaustion_decay", "breakout_volume", "panic_volume",
		"distribution_volume", "strong_decay", "neutral_decay", "active_volume",
	}, "normal_volume")
	acceptanceQuality := selectStr([]BoolSeries{
		boolOr(deathVolume, noAccept), strongAccept,
	}, []string{"no_acceptance", "strong_acceptance"}, "weak_acceptance")
	upperShadowBehavior := selectStr([]BoolSeries{
		upperShadow.LT(1.0),
		boolAnd(volumeRatioBase.GE(1.6), closePos.LE(0.45)),
		boolAnd(closePos.GE(0.55), closeS.GE2(openS)),
	}, []string{"none", "selling_pressure", "probe"}, "failed_wash")

	behaviorScore := make(Series, n)
	for i := range behaviorScore {
		behaviorScore[i] = 50.0
	}
	behaviorScore = behaviorScore.Add(selectFloat([]BoolSeries{
		strEq(acceptanceQuality, "strong_acceptance"),
		strEq(acceptanceQuality, "weak_acceptance"),
	}, []float64{25.0, 8.0}, -22.0))
	behaviorScore = behaviorScore.Add(selectFloat([]BoolSeries{
		strEq(volumeBehavior, "strong_decay"),
		strEq(volumeBehavior, "neutral_decay"),
		strEq(volumeBehavior, "exhaustion_decay"),
		strEq(volumeBehavior, "breakout_volume"),
		strEq(volumeBehavior, "panic_volume"),
		strEq(volumeBehavior, "distribution_volume"),
	}, []float64{15.0, 4.0, -28.0, 12.0, -4.0, -18.0}, 0.0))
	behaviorScore = behaviorScore.Add(out.SupportHoldFlag.Float().MulScalar(8.0))
	behaviorScore = behaviorScore.Add(lowHold.Float().MulScalar(5.0))
	behaviorScore = behaviorScore.Sub(closePos.LT(0.35).Float().MulScalar(8.0))
	behaviorScore = behaviorScore.Sub(strEq(upperShadowBehavior, "selling_pressure").Float().MulScalar(8.0))

	out.VolumeBehavior = volumeBehavior
	out.VolumeBehaviorLabel = strReplace(volumeBehavior, volumeBehaviorLabels)
	out.AcceptanceQuality = acceptanceQuality
	out.AcceptanceQualityLabel = strReplace(acceptanceQuality, acceptanceLabels)
	out.UpperShadowBehavior = upperShadowBehavior
	out.UpperShadowBehaviorLbl = strReplace(upperShadowBehavior, upperShadowLabels)
	out.BehaviorQualityScore = behaviorScore.Clip(0.0, 100.0)
	out.DeathVolumeRisk = deathVolume
	return out
}

var volumeBehaviorLabels = map[string]string{
	"exhaustion_decay":    "衰竭缩量",
	"breakout_volume":     "突破放量",
	"panic_volume":        "恐慌放量",
	"distribution_volume": "出货放量",
	"strong_decay":        "强势缩量",
	"neutral_decay":       "中性缩量",
	"active_volume":       "温和放量",
	"normal_volume":       "常态量",
}

var acceptanceLabels = map[string]string{
	"no_acceptance":     "无承接",
	"strong_acceptance": "强承接",
	"weak_acceptance":   "弱承接",
}

var upperShadowLabels = map[string]string{
	"none":             "无明显上影",
	"selling_pressure": "抛压上影",
	"probe":            "试盘上影",
	"failed_wash":      "洗盘失败",
}

func zeroSeries(n int) Series {
	out := make(Series, n)
	for i := range out {
		out[i] = 0.0
	}
	return out
}

// pandasRowMin / pandasRowMax 对应 `pd.concat([a,b],axis=1).min/max(axis=1)`：
// 跳过 NaN；两个都是 NaN 时结果 NaN。
func pandasRowMin(a, b float64) float64 {
	if isNaN(a) {
		return b
	}
	if isNaN(b) {
		return a
	}
	return math.Min(a, b)
}

func pandasRowMax(a, b float64) float64 {
	if isNaN(a) {
		return b
	}
	if isNaN(b) {
		return a
	}
	return math.Max(a, b)
}

func strEq(s StrSeries, want string) BoolSeries {
	out := make(BoolSeries, len(s))
	for i := range s {
		out[i] = s[i] == want
	}
	return out
}

func strReplace(s StrSeries, mapping map[string]string) StrSeries {
	out := make(StrSeries, len(s))
	for i, v := range s {
		if replaced, ok := mapping[v]; ok {
			out[i] = replaced
		} else {
			out[i] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 特征快照（`calculate_feature_snapshot_prepared`）
// ---------------------------------------------------------------------------

// Feature 对应原实现的 `VolumePriceFeature` dataclass。
//
// 指针字段对应 Python 的 Optional[float]（None 表示缺失）。
type Feature struct {
	TradeDate string
	Close     float64

	TrendScore   float64
	HigherHigh   bool
	HigherLow    bool
	PlatformHigh *float64
	PlatformLow  *float64

	PlatformRangePct *float64
	VolumeRatio20    *float64
	VolumeDecayScore float64
	PullbackRatio    *float64
	SupportPrice     *float64
	SupportHold      bool
	LowerShadowRatio *float64
	UpperShadowRatio *float64
	BreakoutFlag     bool
	PanicVolumeFlag  bool
	ReversalFlag     bool

	VolumeBehavior         string
	VolumeBehaviorLabel    string
	AcceptanceQuality      string
	AcceptanceQualityLabel string
	UpperShadowBehavior    string
	UpperShadowBehaviorLbl string
	BehaviorQualityScore   float64
	DeathVolumeRisk        bool
}

// Snapshot 对应 `calculate_feature_snapshot_prepared`：
// 把已附加派生列的最后一根 K 线投影成 Feature。
func (v *VectorBars) Snapshot() Feature {
	if v == nil || v.Len() == 0 {
		return Feature{}
	}
	i := v.Len() - 1
	last := func(s Series) float64 { return s[i] }

	feature := Feature{
		TradeDate: v.Date[i],
		Close:     last(v.Close),
	}

	platformHigh, highOK := optionalFloat(last(v.Prev20High))
	platformLow, lowOK := optionalFloat(last(v.Prev20Low))
	if highOK && lowOK && platformHigh > 0 && platformLow > 0 {
		feature.PlatformHigh = &platformHigh
		feature.PlatformLow = &platformLow
	}
	feature.PlatformRangePct = optPtr(last(v.PlatformRangePct))
	feature.VolumeRatio20 = optPtr(last(v.VolumeRatio20))
	feature.VolumeDecayScore = floatingOrDefault(last(v.VolumeDecayScore), 0.0)

	feature.PullbackRatio = optPtr(last(v.PullbackRatio))
	if feature.PullbackRatio == nil {
		if high20, ok := optionalFloat(last(v.HighRoll20)); ok && high20 > 0 {
			value := (high20 - feature.Close) / high20 * 100.0
			feature.PullbackRatio = &value
		}
	}
	if support, ok := optionalFloat(last(v.LowRoll20)); ok && support > 0 {
		feature.SupportPrice = &support
	}
	feature.SupportHold = v.SupportHoldFlag[i]
	feature.LowerShadowRatio = optPtr(last(v.LowerShadowRatio))
	feature.UpperShadowRatio = optPtr(last(v.UpperShadowRatio))
	feature.BreakoutFlag = v.BreakoutFlag[i]
	feature.PanicVolumeFlag = v.PanicVolumeFlag[i]
	feature.ReversalFlag = v.ReversalFlag[i]
	feature.HigherHigh = v.HigherHighFlag[i]
	feature.HigherLow = v.HigherLowFlag[i]
	feature.TrendScore = floatingOrDefault(last(v.TrendScore), 0.0)
	feature.VolumeBehavior = v.VolumeBehavior[i]
	feature.VolumeBehaviorLabel = v.VolumeBehaviorLabel[i]
	feature.AcceptanceQuality = v.AcceptanceQuality[i]
	feature.AcceptanceQualityLabel = v.AcceptanceQualityLabel[i]
	feature.UpperShadowBehavior = v.UpperShadowBehavior[i]
	feature.UpperShadowBehaviorLbl = v.UpperShadowBehaviorLbl[i]
	feature.BehaviorQualityScore = floatingOrDefault(last(v.BehaviorQualityScore), 0.0)
	feature.DeathVolumeRisk = v.DeathVolumeRisk[i]
	return feature
}

func optPtr(v float64) *float64 {
	value, ok := optionalFloat(v)
	if !ok {
		return nil
	}
	return &value
}

// FormatFloat 把浮点按原实现 `round(v, 4)` 的风格输出（供调试与快照文本）。
func FormatFloat(v float64) string {
	if isNaN(v) {
		return "nan"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// TrimDate 去掉日期里的连字符（对应 `str.replace("-", "")`）。
func TrimDate(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "-", "")
}

// sortBarsByDate 按日期升序排序，同日保留**最后**一条（对应
// `sort_values("trade_date").drop_duplicates(subset=["trade_date"], keep="last")`）。
func sortBarsByDate(bars []Bar) []Bar {
	out := append([]Bar(nil), bars...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	dedup := make([]Bar, 0, len(out))
	for i, bar := range out {
		if i+1 < len(out) && out[i+1].Date == bar.Date {
			continue
		}
		dedup = append(dedup, bar)
	}
	return dedup
}
