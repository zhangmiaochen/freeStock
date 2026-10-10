package volprice

import (
	"fmt"
	"math"
)

// 对应 `volume_price_models.py` 的"确认信号"一族：
//
//	_trend_risk_sell_prepared            （趋势票的风险卖出）
//	_eval_power_breakout_reentry_prepared
//	_eval_early_maintrend_start_prepared
//	_eval_trend_platform_reentry_prepared
//	_eval_trend_continuation_reentry_prepared
//	_eval_trend_pullback_prepared        （trend_pullback 的确认入口）
//	_eval_box_low_prepared               （box_range 的确认入口）
//	_eval_oversold_rebound_prepared      （oversold_rebound 的确认入口）
//
// ⚠ 前四个**是被 `_eval_trend_pullback_prepared` 调的**（`trend_reentry` 那一路），
// 不是死代码 —— 用调用图解析确认过，它们确实在链路里。
//
// "未命中"一律用 `(Signal{}, false)` 表示，对应原实现的 `return None`。

// TrendRiskSellPrepared 对应 `_trend_risk_sell_prepared`。
func TrendRiskSellPrepared(
	v *VectorBars, marketState string, f Feature, lowSince, highSince float64,
) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 2 {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	if quality.UpperShadowBehavior != "selling_pressure" {
		return Signal{}, false
	}
	if quality.AcceptanceQuality == "strong_acceptance" {
		return Signal{}, false
	}
	volumeRatio := 0.0
	if f.VolumeRatio20 != nil {
		volumeRatio = *f.VolumeRatio20
	}
	if volumeRatio < 2.0 {
		return Signal{}, false
	}
	n := v.Len()
	closeP := v.Close[n-1]
	high := v.High[n-1]
	low := v.Low[n-1]
	openP := v.Open[n-1]
	prevClose := v.Close[n-2]
	closePos := 0.5
	if high > low {
		closePos = (closeP - low) / (high - low)
	}
	weakClose := closePos <= 0.38 && closeP <= math.Max(openP, prevClose*1.01)
	if !weakClose {
		return Signal{}, false
	}
	sell := BaseSignal(ModelTrendPullback, marketState, "sell", v.Date[n-1])
	sell.EntryPrice = roundPtr(FloatPtr(closeP))
	sell.StopPrice = roundPtr(FloatPtr(lowSince))
	sell.TargetPrice = roundPtr(FloatPtr(highSince))
	sell.Score = 74.0
	sell.RiskLevel = "high"
	sell.ConditionHits = []string{"放量冲高回落", "承接转弱，存在短线抛压"}
	sell.ConditionMisses = []string{}
	sell.Explanation = "趋势票出现放量冲高回落且承接转弱，优先按风险信号退出观察。"
	sell.Feature = f
	AttachBehaviorQuality(&sell, f)
	return sell, true
}

// EvalPowerBreakoutReentry 对应 `_eval_power_breakout_reentry_prepared`。
func EvalPowerBreakoutReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	metrics, ok := PowerBreakoutMetricsOf(v)
	if !ok {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	behaviorOK := BehaviorQualityOK(f, 50.0)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	pullbackOK := metrics.PullbackPct >= 1.0 && metrics.PullbackPct <= 12.0
	accelerationOK := !metrics.AccelerationRisk
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{f.HigherHigh && f.HigherLow && f.TrendScore >= 68.0, "20-60日趋势结构保持强势"},
		{metrics.BreakoutRetPct >= 7.0 && metrics.BreakoutVolume >= 2.0, "近期出现高质量放量大阳突破"},
		{metrics.HoldMidOK, "突破后回踩不破突破日中枢"},
		{pullbackOK, fmt.Sprintf("突破后回踩幅度%.2f%%处于再入场区", metrics.PullbackPct)},
		{accelerationOK, PowerBreakoutAccelerationText(metrics)},
		{metrics.TurnStrong, "回踩后再次放量转强"},
		{strongAcceptance, "二次确认承接达到强承接"},
		{behaviorOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if len(misses) > 0 {
		return Signal{}, false
	}
	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.2
	signal := BaseSignal(ModelTrendPullback, marketState, "buy", v.Date[v.Len()-1])
	score := 66.0 + math.Min(10.0, metrics.BreakoutVolume*1.5) +
		math.Min(10.0, f.TrendScore/10.0) + BehaviorScoreAdjust(f)
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	signal.Score = round2(math.Max(0.0, math.Min(95.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.Feature = f
	signal.SignalSubtype = "power_breakout_reentry"
	signal.SignalSubtypeLabel = "强突破后二次确认"
	signal.Explanation = fmt.Sprintf(
		"%s出现放量大阳突破，之后回踩未破突破日中枢，今日再次放量转强，"+
			"属于强突破后的二次确认再入场。%s。", metrics.BreakoutDate, BehaviorConditionText(f))
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalEarlyMainTrendStart 对应 `_eval_early_maintrend_start_prepared`。
func EvalEarlyMainTrendStart(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	metrics, ok := EarlyMainTrendMetricsOf(v)
	if !ok {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	behaviorOK := BehaviorQualityOK(f, 50.0)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"
	volumeOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{metrics.LowBaseOK, "近60日仍处于低位或中低位启动区"},
		{metrics.PlatformOK, fmt.Sprintf("20/30日平台蓄势振幅%.2f%%", metrics.PlatformHeight)},
		{metrics.TrendTurnOK, "价格开始脱离20/60日中期结构"},
		{metrics.ConfirmBreak, "当日放量突破低位平台上沿且收盘强"},
		{volumeOK, "主升启动量能为有效放量"},
		{strongAcceptance, "主升启动承接达到强承接"},
		{behaviorOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
		{!metrics.ChaseRisk, "突破位置未明显过热或追高"},
	})
	if len(misses) > 0 {
		return Signal{}, false
	}
	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.04)
	target := closeP + risk*2.2
	signal := BaseSignal(ModelTrendPullback, marketState, "buy", v.Date[v.Len()-1])
	score := 64.0 + math.Min(12.0, metrics.VolumeRatio*2.2) +
		math.Min(8.0, metrics.DayRetPct*0.7) + BehaviorScoreAdjust(f)
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	signal.Score = round2(math.Max(0.0, math.Min(95.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.Feature = f
	signal.EarlyMainTrend = &metrics
	signal.SignalSubtype = "early_maintrend_start"
	signal.SignalSubtypeLabel = "主升启动早期确认"
	trigger := round4(metrics.BreakoutPrice)
	signal.CandidateTriggerPrice = &trigger
	signal.Explanation = "低位平台蓄势后首次有效放量突破上沿，收盘位置强且承接达标，" +
		"属于主升启动早期确认。" + BehaviorConditionText(f) + "。"
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalTrendPlatformReentry 对应 `_eval_trend_platform_reentry_prepared`。
func EvalTrendPlatformReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 60 {
		return Signal{}, false
	}
	metrics, ok := TrendPlatformMetricsOf(v)
	cont, contOK := TrendContinuationMetricsOf(v)
	if !ok || !contOK {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	behaviorOK := BehaviorQualityOK(f, 50.0)
	volumeOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume" ||
		quality.VolumeBehavior == "strong_decay" || quality.VolumeBehavior == "neutral_decay"
	acceptanceOK := quality.AcceptanceQuality == "strong_acceptance"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	trendOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 70.0
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendOK, "20-60日趋势持续抬高且强度达标"},
		{cont.TrendStack, "价格仍在10/20日趋势结构上方"},
		{cont.RecentStrength, "近期仍保持强趋势新高结构"},
		{metrics.SetupOK, fmt.Sprintf("近期平台整理幅度%.2f%%且完成消化", metrics.PlatformHeight)},
		{metrics.SupportHold, "平台整理期间没有明显跌穿中期支撑"},
		{metrics.BreakoutTurn, "今日放量突破近期整理平台上沿"},
		{!metrics.ChaseRisk, "当前突破未明显脱离合理追踪区"},
		{volumeOK, "平台再启动量能质量有效"},
		{acceptanceOK, "平台再启动承接达到强承接"},
		{behaviorOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if len(misses) > 0 {
		return Signal{}, false
	}
	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.1
	signal := BaseSignal(ModelTrendPullback, marketState, "buy", v.Date[v.Len()-1])
	signal.Feature = f
	signal.TrendPlatform = &metrics
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	score := 65.0 + math.Min(12.0, f.TrendScore/8.0) + BehaviorScoreAdjust(f)
	signal.Score = round2(math.Max(0.0, math.Min(95.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.SignalSubtype = "trend_platform_reentry"
	signal.SignalSubtypeLabel = "趋势平台再启动"
	signal.Explanation = "强趋势在近期平台整理后重新放量突破上沿，平台内部承接未坏，" +
		"属于趋势中继后的再启动买点。" + BehaviorConditionText(f) + "。"
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalTrendContinuationReentry 对应 `_eval_trend_continuation_reentry_prepared`。
func EvalTrendContinuationReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 60 {
		return Signal{}, false
	}
	metrics, ok := TrendContinuationMetricsOf(v)
	if !ok {
		return Signal{}, false
	}
	behaviorOK := BehaviorQualityOK(f, 50.0)
	quality := BehaviorQualityOf(f)
	volumeOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume" ||
		quality.VolumeBehavior == "strong_decay" || quality.VolumeBehavior == "neutral_decay"
	acceptanceOK := quality.AcceptanceQuality == "strong_acceptance"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{f.HigherHigh && f.HigherLow && f.TrendScore >= 70.0, "20-60日趋势持续抬高且强度达标"},
		{metrics.TrendStack, "价格站上10/20日线且20日线保持中期趋势"},
		{metrics.NotFarFromHigh, "距离20日高点不过远，仍处于趋势续航区"},
		{metrics.RecentStrength, "近期创新高或接近20日新高"},
		{metrics.Trigger, "当日放量转强或回踩10日线后收复"},
		{metrics.DigestionOK, "趋势续航已有回踩/缩量消化"},
		{!metrics.OverheatRisk, "短线未明显过热，避免高位追强"},
		{metrics.ReentryQuality, "价格未明显追高，具备回踩后再上车质量"},
		{volumeOK, "趋势续航量能质量有效"},
		{acceptanceOK, "趋势续航承接达到强承接"},
		{behaviorOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if len(misses) > 0 {
		return Signal{}, false
	}
	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.2
	signal := BaseSignal(ModelTrendPullback, marketState, "buy", v.Date[v.Len()-1])
	signal.Feature = f
	triggerText := "回踩10日线后收复"
	if metrics.BreakoutTurn {
		triggerText = "放量突破前高"
	}
	score := 64.0 + math.Min(15.0, f.TrendScore/8.0) + BehaviorScoreAdjust(f)
	if metrics.BreakoutTurn {
		score += 4.0
	}
	if metrics.Ma10Reclaim {
		score += 3.0
	}
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	signal.Score = round2(math.Max(0.0, math.Min(95.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.SignalSubtype = "trend_continuation_reentry"
	signal.SignalSubtypeLabel = "趋势续航再上车"
	signal.Explanation = fmt.Sprintf("强趋势已脱离早期回踩区，今日%s，价格仍贴近中短期均线，"+
		"按10/20日结构支撑重新定义防守位，属于趋势续航再上车买点。%s。",
		triggerText, BehaviorConditionText(f))
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalTrendPullback 对应 `_eval_trend_pullback_prepared`。
func EvalTrendPullback(v *VectorBars, marketState string) (Signal, bool) {
	if v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	f := v.Snapshot()
	n := v.Len()
	last := v.rawLast()
	signal := BaseSignal(ModelTrendPullback, marketState, "buy", last.Date)
	signal.Feature = f
	var hits, misses []string

	if marketState == "bull" {
		hits = append(hits, "市场状态匹配牛市/趋势扩张")
	} else {
		misses = append(misses, "市场状态不是牛市/趋势扩张")
	}
	trendOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 65.0
	if trendOK {
		hits = append(hits, "20-60日高低点抬高且趋势强度达标")
	} else {
		misses = append(misses, "20-60日高低点抬高且趋势强度达标")
	}

	breakout, breakoutOK := RecentBreakoutOf(v)
	if breakoutOK {
		hits = append(hits, "60日内出现过放量突破平台")
	} else {
		misses = append(misses, "60日内出现过放量突破平台")
	}

	// trend_reentry = 四个"再入场"判定按顺序取第一个非 None
	var trendReentry Signal
	reentryOK := false
	for _, candidate := range []struct {
		run func() (Signal, bool)
	}{
		{func() (Signal, bool) { return EvalTrendContinuationReentry(v, marketState, f) }},
		{func() (Signal, bool) { return EvalTrendPlatformReentry(v, marketState, f) }},
		{func() (Signal, bool) { return EvalPowerBreakoutReentry(v, marketState, f) }},
		{func() (Signal, bool) { return EvalEarlyMainTrendStart(v, marketState, f) }},
	} {
		if got, ok := candidate.run(); ok {
			trendReentry, reentryOK = got, true
			break
		}
	}

	if !breakoutOK {
		return trendReentry, reentryOK
	}

	sub := v.Slice(breakout.Idx, n)
	highSince := maxSeries(Series(sub.High))
	lowSince := minSeries(Series(sub.Low))
	closeP := last.Close
	pullback := 0.0
	if highSince > 0 {
		pullback = (highSince - closeP) / highSince * 100.0
	}
	pullbackOK := pullback >= 3.0 && pullback <= 12.0
	if pullbackOK {
		hits = append(hits, fmt.Sprintf("回踩幅度%.2f%%位于3%%-12%%", pullback))
	} else {
		misses = append(misses, fmt.Sprintf("回踩幅度%.2f%%位于3%%-12%%", pullback))
	}

	avg3 := meanNonNaN(v.Volume[n-3:])
	volumeDecayOK := avg3 < breakout.Volume*0.5
	if volumeDecayOK {
		hits = append(hits, "最近3日均量低于启动日成交量50%")
	} else {
		misses = append(misses, "最近3日均量低于启动日成交量50%")
	}

	support := breakout.PlatformHigh
	supportOK := lowSince >= support*0.98 && closeP >= support
	if supportOK {
		hits = append(hits, "回踩未有效跌破突破平台")
	} else {
		misses = append(misses, "回踩未有效跌破突破平台")
	}
	entryNearSupportOK := closeP <= support*1.10
	if entryNearSupportOK {
		hits = append(hits, "买点仍贴近突破平台支撑")
	} else {
		misses = append(misses, "买点仍贴近突破平台支撑")
	}

	prev2Vol := meanNonNaN(v.Volume[n-3 : n-1])
	prevHigh := v.High[n-2]
	restrengthOK := closeP > prevHigh && last.Volume > prev2Vol
	if restrengthOK {
		hits = append(hits, "当日收盘高于前高且成交量回升")
	} else {
		misses = append(misses, "当日收盘高于前高且成交量回升")
	}

	behaviorOK := BehaviorQualityOK(f, 45.0)
	behaviorText := BehaviorConditionText(f)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}
	quality := BehaviorQualityOf(f)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"
	if strongAcceptance {
		hits = append(hits, "回踩承接达到强承接")
	} else {
		misses = append(misses, "回踩承接达到强承接")
	}

	riskSell, riskSellOK := TrendRiskSellPrepared(v, marketState, f, lowSince, highSince)

	allOK := marketState == "bull" && trendOK && breakoutOK && pullbackOK && volumeDecayOK &&
		supportOK && entryNearSupportOK && restrengthOK && behaviorOK && strongAcceptance
	if !allOK {
		if reentryOK {
			return trendReentry, true
		}
		return riskSell, riskSellOK
	}

	stop := math.Max(lowSince, support*0.99)
	risk := math.Max(closeP-stop, closeP*0.03)
	target := closeP + risk*2.0
	score := 55.0 + math.Min(15.0, f.TrendScore/10.0) +
		math.Min(12.0, breakout.VolumeRatio*3.0) +
		math.Min(12.0, 12.0-pullback) + BehaviorScoreAdjust(f)
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	signal.Score = round2(math.Max(0.0, math.Min(96.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.Explanation = "放量突破后的缩量回踩没有破位，今日重新放量转强，属于趋势延续买点。" +
		behaviorText + "。"
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalBoxLow 对应 `_eval_box_low_prepared`。
func EvalBoxLow(v *VectorBars, marketState string) (Signal, bool) {
	if v == nil || v.Len() < 35 {
		return Signal{}, false
	}
	f := v.Snapshot()
	quality := BehaviorQualityOf(f)
	last := v.rawLast()
	boxMetrics, boxOK := BoxWindowMetricsOf(v)
	if !boxOK {
		return Signal{}, false
	}
	support := boxMetrics.Support
	resistance := boxMetrics.Resistance
	closeP := last.Close
	heightPct := boxMetrics.HeightPct
	nearSupport := boxMetrics.NearSupportConfirm
	volumeDecay := boxMetrics.VolumeDecay
	lowerShadow := 0.0
	if f.LowerShadowRatio != nil {
		lowerShadow = *f.LowerShadowRatio
	}
	accept := lowerShadow >= 1.5 || closeP > (last.High+last.Low)/2.0
	boxQuality := BoxStructureQualityOf(v, f)
	behaviorText := BehaviorConditionText(f)

	var hits, misses []string
	if marketState == "range" {
		hits = append(hits, "市场状态匹配震荡/轮动")
	} else {
		misses = append(misses, "市场状态匹配震荡/轮动")
	}
	heightOK := heightPct >= 8.0 && heightPct <= 25.0
	heightText := fmt.Sprintf("30日箱体振幅%.2f%%", heightPct)
	if heightOK {
		hits = append(hits, heightText)
	} else {
		misses = append(misses, heightText)
	}
	if nearSupport {
		hits = append(hits, "价格接近箱体下沿3%以内")
	} else {
		misses = append(misses, "价格接近箱体下沿3%以内")
	}
	if volumeDecay {
		hits = append(hits, "下跌到支撑附近时成交量连续收缩")
	} else {
		misses = append(misses, "下跌到支撑附近时成交量连续收缩")
	}
	if accept {
		hits = append(hits, "出现下影线或收盘回升承接")
	} else {
		misses = append(misses, "出现下影线或收盘回升承接")
	}
	behaviorOK := BehaviorQualityOK(f, 45.0)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}
	boxQualityOK := boxQuality.Tradeable
	qualityText := fmt.Sprintf("箱体质量评分%.0f且无破位/下降通道风险", boxQuality.Score)
	if boxQualityOK {
		hits = append(hits, qualityText)
	} else {
		misses = append(misses, qualityText)
	}

	upperShadow := 0.0
	if f.UpperShadowRatio != nil {
		upperShadow = *f.UpperShadowRatio
	}
	upperSell := closeP >= resistance*0.97 && upperShadow >= 1.0
	if marketState == "range" && upperSell {
		sell := BaseSignal(ModelBoxRange, marketState, "sell", last.Date)
		sell.EntryPrice = roundPtr(FloatPtr(closeP))
		sell.StopPrice = roundPtr(FloatPtr(support))
		sell.TargetPrice = roundPtr(FloatPtr(resistance))
		sell.Score = 76.0
		sell.RiskLevel = "medium"
		sell.ConditionHits = []string{"接近箱体压力位", "上影线显示冲高抛压"}
		sell.ConditionMisses = []string{}
		sell.Explanation = "震荡市接近箱体上沿不恋战，优先兑现。"
		sell.Feature = f
		AttachBehaviorQuality(&sell, f)
		return sell, true
	}

	if !(marketState == "range" && heightOK && nearSupport && volumeDecay && accept &&
		behaviorOK && boxQualityOK) {
		return Signal{}, false
	}

	stop := support * 0.99
	target := resistance * 0.97
	signal := BaseSignal(ModelBoxRange, marketState, "buy", last.Date)
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	score := 62.0 + (25.0-heightPct)*0.4 + math.Min(12.0, lowerShadow*3.0) + behaviorScoreAdjust(quality)
	signal.Score = round2(math.Max(0.0, math.Min(92.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.Explanation = fmt.Sprintf("有效震荡箱体下沿出现缩量和强承接，箱体质量评分%.0f，"+
		"适合按区间交易低吸，高抛目标看箱体上沿。%s。", boxQuality.Score, behaviorText)
	signal.Feature = f
	signal.BoxQuality = &boxQuality
	// 原实现把 box_quality 的**数值/布尔键**合并进 behavior_quality（去掉 block_reasons）
	signal.BehaviorQuality = BehaviorQualityOf(f)
	mergeExtras(&signal.BehaviorQuality, boxQuality.AsFloatMap())
	signal.QualityBlockReasons = append([]string(nil), boxQuality.BlockReasons...)
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvalOversoldRebound 对应 `_eval_oversold_rebound_prepared`。
func EvalOversoldRebound(v *VectorBars, marketState string) (Signal, bool) {
	if v == nil || v.Len() < 25 {
		return Signal{}, false
	}
	f := v.Snapshot()
	n := v.Len()
	last := v.rawLast()
	closeP := last.Close
	close5 := closeP
	if n >= 6 {
		close5 = v.Close[n-6]
	}
	fall5 := 0.0
	if close5 > 0 {
		fall5 = (closeP/close5 - 1.0) * 100.0
	}
	prev20Vol := v.Volume[n-21 : n-1]
	avg20 := 0.0
	if len(prev20Vol) >= 10 {
		avg20 = meanNonNaN(prev20Vol)
	}
	panicFlag := avg20 > 0 && maxSeries(v.Volume[n-7:]) >= avg20*3.0
	stopDays := v.Volume[n-3 : n-1]
	volumeStop := len(stopDays) >= 2 && stopDays[len(stopDays)-1] < stopDays[0]
	prevHigh := v.High[n-2]
	prevVolume := v.Volume[n-2]
	rebound := closeP > prevHigh && last.Volume > prevVolume
	lowerShadow := 0.0
	if f.LowerShadowRatio != nil {
		lowerShadow = *f.LowerShadowRatio
	}
	reversal := f.ReversalFlag || lowerShadow >= 2.0
	reboundQuality := OversoldReboundQualityOf(v, f, fall5, panicFlag, rebound)

	var hits, misses []string
	if marketState == "bear" {
		hits = append(hits, "市场状态匹配熊市/风险释放")
	} else {
		misses = append(misses, "市场状态匹配熊市/风险释放")
	}
	fallText := fmt.Sprintf("5日跌幅%.2f%%达到超跌", fall5)
	if fall5 <= -20.0 {
		hits = append(hits, fallText)
	} else {
		misses = append(misses, fallText)
	}
	if panicFlag {
		hits = append(hits, "近5日出现3倍均量恐慌释放")
	} else {
		misses = append(misses, "近5日出现3倍均量恐慌释放")
	}
	if volumeStop || reversal {
		hits = append(hits, "出现缩量止跌或长下影")
	} else {
		misses = append(misses, "出现缩量止跌或长下影")
	}
	if rebound {
		hits = append(hits, "首次收复前一日高点且成交量回升")
	} else {
		misses = append(misses, "首次收复前一日高点且成交量回升")
	}
	behaviorOK := BehaviorQualityOK(f, 38.0)
	behaviorText := BehaviorConditionText(f)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}
	reboundQualityOK := reboundQuality.Tradeable
	qualityText := fmt.Sprintf("恐慌释放%.0f且反抽质量%.0f",
		reboundQuality.PanicReleaseScore, reboundQuality.ReboundScore)
	if reboundQualityOK {
		hits = append(hits, qualityText)
	} else {
		misses = append(misses, qualityText)
	}

	if !(marketState == "bear" && fall5 <= -20.0 && panicFlag && (volumeStop || reversal) &&
		rebound && behaviorOK && reboundQualityOK) {
		return Signal{}, false
	}

	stop := minSeries(v.Low[n-5:])
	risk := math.Max(closeP-stop, closeP*0.03)
	target := math.Min(closeP*1.15, closeP+risk*2.0)
	signal := BaseSignal(ModelOversoldRebound, marketState, "buy", last.Date)
	signal.EntryPrice = roundPtr(FloatPtr(closeP))
	signal.StopPrice = roundPtr(FloatPtr(stop))
	signal.TargetPrice = roundPtr(FloatPtr(target))
	score := 62.0 + math.Min(15.0, math.Abs(fall5)*0.4) +
		math.Min(10.0, lowerShadow*2.0) + BehaviorScoreAdjust(f)
	signal.Score = round2(math.Max(0.0, math.Min(90.0, score)))
	signal.RiskLevel = RiskLevel(closeP, stop)
	signal.ConditionHits = hits
	signal.ConditionMisses = misses
	signal.Explanation = "熊市连续急跌后完成恐慌释放，首次放量修复质量达标，" +
		"定位为短线反抽而非趋势反转。" + behaviorText + "。"
	signal.Feature = f
	signal.OversoldQuality = &reboundQuality
	signal.BehaviorQuality = BehaviorQualityOf(f)
	mergeExtras(&signal.BehaviorQuality, reboundQuality.AsFloatMap())
	signal.QualityBlockReasons = append([]string(nil), reboundQuality.BlockReasons...)
	AttachBehaviorQuality(&signal, f)
	return signal, true
}

// EvaluateConfirmedPrepared 对应 `_evaluate_confirmed_prepared`（分派）。
func EvaluateConfirmedPrepared(modelID string, v *VectorBars, marketState string) (Signal, bool) {
	switch NormalizeModelID(modelID) {
	case ModelTrendPullback:
		return EvalTrendPullback(v, marketState)
	case ModelBoxRange:
		return EvalBoxLow(v, marketState)
	case ModelOversoldRebound:
		return EvalOversoldRebound(v, marketState)
	}
	return Signal{}, false
}

// EvaluateModel 对应 `evaluate_model`（先 prepare 再分派）。
func EvaluateModel(modelID string, bars []Bar, marketState, currentDate string) (Signal, bool) {
	state := NormalizeMarketState(marketState, "range")
	return EvaluateConfirmedPrepared(modelID, PrepareBars(bars, currentDate), state)
}

// mergeExtras 把子度量的数值键合并进 behavior_quality（对应原实现的 dict.update）。
func mergeExtras(quality *BehaviorQuality, values map[string]float64) {
	if quality.Extras == nil {
		quality.Extras = map[string]float64{}
	}
	for key, value := range values {
		quality.Extras[key] = value
	}
}

// PrepareBars 对应 `prepare_bars`：过滤到 currentDate、按日期排序、同日保留最后一条，
// 然后附加派生列。
func PrepareBars(bars []Bar, currentDate string) *VectorBars {
	td := TrimDate(currentDate)
	filtered := make([]Bar, 0, len(bars))
	for _, bar := range bars {
		date := TrimDate(bar.Date)
		if date == "" {
			continue
		}
		if td != "" && date > td {
			continue
		}
		bar.Date = date
		filtered = append(filtered, bar)
	}
	return AttachVectorBarsColumns(sortBarsByDate(filtered))
}
