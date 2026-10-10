package volprice

import (
	"fmt"
	"math"
	"strings"
)

// 对应 `volume_price_models.py` 的"候选信号"一族：
//
//	_candidate_power_breakout_reentry_prepared
//	_candidate_early_maintrend_start_prepared
//	_candidate_trend_platform_reentry_prepared
//	_candidate_trend_continuation_reentry_prepared
//	_candidate_trend_pullback_prepared   （trend_pullback 的候选入口）
//	_candidate_box_low_prepared          （box_range 的候选入口）
//	_candidate_oversold_rebound_prepared （oversold_rebound 的候选入口）
//
// 前四个是被 `_candidate_trend_pullback_prepared` 调的（用调用图确认过）。
//
// 候选信号与确认信号的关键差别：
//   - 打分上限 89（确认是 92~96），级别 "candidate"/"候选"；
//   - 额外带 `loose_entry_score` / `loose_entry_allowed` / `loose_entry_block_reasons`
//     / `confirmation_hits` / `confirmation_misses` / `missing_core`；
//   - "软原因"只降低可放行性（loose_entry_allowed），不直接否决信号。

// countTrue 对应 Python 的 `sum([a, b, c]) >= n`（bool 当 int 相加）。
func countTrue(values ...bool) int {
	total := 0
	for _, value := range values {
		if value {
			total++
		}
	}
	return total
}

// hitRatio 对应 `len(hits) / max(1, len(hits) + len(misses))`。
func hitRatio(hits, misses []string) float64 {
	total := len(hits) + len(misses)
	if total < 1 {
		total = 1
	}
	return float64(len(hits)) / float64(total)
}

// CandidatePowerBreakoutReentry 对应 `_candidate_power_breakout_reentry_prepared`。
func CandidatePowerBreakoutReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	metrics, ok := PowerBreakoutMetricsOf(v)
	if !ok {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	behaviorWatchOK := BehaviorQualityOK(f, 38.0)
	behaviorConfirmOK := BehaviorQualityOK(f, 50.0)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	trendWatchOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 62.0
	trendConfirmOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 68.0
	pullbackWatchOK := metrics.PullbackPct >= 0.0 && metrics.PullbackPct <= 15.0
	pullbackConfirmOK := metrics.PullbackPct >= 1.0 && metrics.PullbackPct <= 12.0
	accelerationOK := !metrics.AccelerationRisk
	volumeOK := quality.VolumeBehavior == "breakout_volume" ||
		quality.VolumeBehavior == "active_volume" || quality.VolumeBehavior == "strong_decay"
	turnWatchOK := metrics.TurnStrong || (metrics.ClosePos >= 0.58 && volumeOK)
	breakoutStrong := metrics.BreakoutRetPct >= 7.0 && metrics.BreakoutVolume >= 2.0

	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendWatchOK, "20-60日趋势结构保持偏强"},
		{breakoutStrong, "近期出现高质量放量大阳突破"},
		{metrics.HoldMidOK, "突破后回踩不破突破日中枢"},
		{pullbackWatchOK, fmt.Sprintf("突破后回踩幅度%.2f%%处于观察区", metrics.PullbackPct)},
		{accelerationOK, PowerBreakoutAccelerationText(metrics)},
		{turnWatchOK, "回踩后已有再次转强迹象"},
		{behaviorWatchOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if !(trendWatchOK && metrics.HoldMidOK && pullbackWatchOK && accelerationOK &&
		turnWatchOK && behaviorWatchOK && upperOK) {
		return Signal{}, false
	}

	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendConfirmOK, "20-60日趋势结构保持强势"},
		{breakoutStrong, "近期出现高质量放量大阳突破"},
		{metrics.HoldMidOK, "突破后回踩不破突破日中枢"},
		{pullbackConfirmOK, fmt.Sprintf("突破后回踩幅度%.2f%%处于再入场区", metrics.PullbackPct)},
		{accelerationOK, PowerBreakoutAccelerationText(metrics)},
		{metrics.TurnStrong, "回踩后再次放量转强"},
		{strongAcceptance, "二次确认承接达到强承接"},
		{behaviorConfirmOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})

	var hard, soft []string
	if !trendConfirmOK {
		soft = append(soft, "趋势结构未达到强突破再入场确认强度")
	}
	if !pullbackConfirmOK {
		hard = append(hard, "强突破后回踩幅度不在再入场区")
	}
	if !metrics.TurnStrong {
		soft = append(soft, "尚未再次放量转强")
	}
	if !strongAcceptance {
		hard = append(hard, "二次确认承接未达到强承接")
	}
	if !upperOK {
		hard = append(hard, "存在明显放量抛压上影")
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 50.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 78.0, MaxSoftMisses: 1, RequireStrongAcceptance: true,
	})

	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.0
	score := 50.0 + hitRatio(hits, misses)*25.0 +
		math.Min(10.0, metrics.BreakoutVolume*1.2) +
		math.Min(8.0, f.TrendScore/12.0) + BehaviorScoreAdjust(f)
	return BuildCandidateSignal(ModelTrendPullback, marketState, v.Date[v.Len()-1], f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "近期强突破后未破中枢，当前进入二次确认观察区，仍需价格和承接继续确认。" +
			BehaviorConditionText(f) + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      confirmMisses,
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		SignalSubtype: "power_breakout_reentry", SignalSubtypeLabel: "强突破后二次确认",
	}), true
}

// CandidateEarlyMainTrendStart 对应 `_candidate_early_maintrend_start_prepared`。
func CandidateEarlyMainTrendStart(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	metrics, ok := EarlyMainTrendMetricsOf(v)
	if !ok {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	behaviorWatchOK := BehaviorQualityOK(f, 38.0)
	behaviorConfirmOK := BehaviorQualityOK(f, 50.0)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"
	volumeConfirmOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume"
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	watchOK := metrics.LowBaseOK && metrics.PlatformOK && metrics.WatchReady &&
		behaviorWatchOK && upperOK && !metrics.ChaseRisk

	platformText := fmt.Sprintf("20/30日平台蓄势振幅%.2f%%", metrics.PlatformHeight)
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{metrics.LowBaseOK, "近60日仍处于低位或中低位启动区"},
		{metrics.PlatformOK, platformText},
		{metrics.TrendTurnOK, "价格开始脱离20/60日中期结构"},
		{metrics.WatchReady, "价格已经靠近低位平台上沿"},
		{behaviorWatchOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
		{!metrics.ChaseRisk, "当前未进入明显追高区"},
	})
	if !watchOK {
		return Signal{}, false
	}

	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{metrics.LowBaseOK, "近60日仍处于低位或中低位启动区"},
		{metrics.PlatformOK, platformText},
		{metrics.TrendTurnOK, "价格开始脱离20/60日中期结构"},
		{metrics.ConfirmBreak, "当日放量突破低位平台上沿且收盘强"},
		{volumeConfirmOK, "主升启动量能为有效放量"},
		{strongAcceptance, "主升启动承接达到强承接"},
		{behaviorConfirmOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
		{!metrics.ChaseRisk, "突破位置未明显过热或追高"},
	})

	var hard, soft []string
	if !metrics.TrendTurnOK {
		soft = append(soft, "价格尚未完全脱离20/60日结构")
	}
	if !metrics.ConfirmBreak {
		soft = append(soft, "尚未放量突破低位平台上沿")
	}
	if !volumeConfirmOK {
		soft = append(soft, "主升启动量能仍需进一步放大")
	}
	if !strongAcceptance {
		hard = append(hard, "主升启动承接未达到强承接")
	}
	if metrics.ChaseRisk {
		hard = append(hard, "突破位置过热，存在追高风险")
	}
	if !upperOK {
		hard = append(hard, "存在明显放量抛压上影")
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 50.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 75.0, MaxSoftMisses: 2, RequireStrongAcceptance: true,
	})

	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.04)
	target := closeP + risk*2.0
	score := 48.0 + hitRatio(hits, misses)*28.0 +
		math.Min(10.0, metrics.VolumeRatio*2.0) + BehaviorScoreAdjust(f)
	signal := BuildCandidateSignal(ModelTrendPullback, marketState, v.Date[v.Len()-1], f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "低位平台已接近突破触发区，属于主升启动早期观察位，明日需放量站上平台上沿确认。" +
			BehaviorConditionText(f) + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      confirmMisses,
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		SignalSubtype: "early_maintrend_start", SignalSubtypeLabel: "主升启动早期确认",
	})
	signal.EarlyMainTrend = &metrics
	trigger := round4(metrics.BreakoutPrice)
	signal.CandidateTriggerPrice = &trigger
	return signal, true
}

// CandidateTrendPlatformReentry 对应 `_candidate_trend_platform_reentry_prepared`。
func CandidateTrendPlatformReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 60 {
		return Signal{}, false
	}
	metrics, ok := TrendPlatformMetricsOf(v)
	cont, contOK := TrendContinuationMetricsOf(v)
	if !ok || !contOK {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	behaviorWatchOK := BehaviorQualityOK(f, 38.0)
	behaviorConfirmOK := BehaviorQualityOK(f, 50.0)
	trendWatchOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 62.0
	trendConfirmOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 70.0
	stackWatchOK := cont.TrendStack
	volumeConfirmOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume" ||
		quality.VolumeBehavior == "strong_decay" || quality.VolumeBehavior == "neutral_decay"
	acceptanceConfirmOK := quality.AcceptanceQuality == "strong_acceptance"
	watchOK := trendWatchOK && stackWatchOK && cont.RecentStrength && metrics.SetupOK &&
		metrics.SupportHold && metrics.WatchReady && metrics.TriggerProbe &&
		!metrics.ChaseRisk && behaviorWatchOK && upperOK

	platformText := fmt.Sprintf("近期平台整理幅度%.2f%%且完成消化", metrics.PlatformHeight)
	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendWatchOK, "20-60日趋势结构仍保持偏强"},
		{stackWatchOK, "价格仍在10/20日趋势结构附近"},
		{cont.RecentStrength, "近期仍接近新高区"},
		{metrics.SetupOK, platformText},
		{metrics.SupportHold, "平台整理期间没有明显跌穿中期支撑"},
		{metrics.WatchReady, "价格已经靠近平台上沿"},
		{metrics.TriggerProbe, "已有再启动前兆"},
		{!metrics.ChaseRisk, "当前未进入明显追高区"},
		{behaviorWatchOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if !watchOK {
		return Signal{}, false
	}

	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendConfirmOK, "20-60日趋势持续抬高且强度达标"},
		{cont.TrendStack, "价格仍在10/20日趋势结构上方"},
		{cont.RecentStrength, "近期仍保持强趋势新高结构"},
		{metrics.SetupOK, platformText},
		{metrics.SupportHold, "平台整理期间没有明显跌穿中期支撑"},
		{metrics.BreakoutTurn, "今日放量突破近期整理平台上沿"},
		{!metrics.ChaseRisk, "当前突破未明显脱离合理追踪区"},
		{volumeConfirmOK, "平台再启动量能质量有效"},
		{acceptanceConfirmOK, "平台再启动承接达到强承接"},
		{behaviorConfirmOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})

	var hard, soft []string
	if !trendConfirmOK {
		soft = append(soft, "趋势强度未达到平台再启动确认阈值")
	}
	if !metrics.BreakoutTurn {
		soft = append(soft, "尚未真正突破近期整理平台上沿")
	}
	if metrics.ChaseRisk {
		hard = append(hard, "平台突破位置过于偏离，存在追高风险")
	}
	if !volumeConfirmOK {
		soft = append(soft, "平台再启动量能仍需进一步放大")
	}
	if !acceptanceConfirmOK {
		hard = append(hard, "平台再启动承接未达到强承接")
	}
	if !upperOK {
		hard = append(hard, "存在明显放量抛压上影")
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 50.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 78.0, MaxSoftMisses: 2, RequireStrongAcceptance: false,
	})

	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.0
	score := 49.0 + hitRatio(hits, misses)*25.0 +
		math.Min(12.0, f.TrendScore/9.0) + BehaviorScoreAdjust(f)
	signal := BuildCandidateSignal(ModelTrendPullback, marketState, v.Date[v.Len()-1], f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "强趋势正在近期整理平台内消化，已靠近平台上沿，明日需等待有效突破再启动。" +
			BehaviorConditionText(f) + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      confirmMisses,
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		SignalSubtype: "trend_platform_reentry", SignalSubtypeLabel: "趋势平台再启动",
	})
	trigger := round4(metrics.BreakoutPrice)
	signal.CandidateTriggerPrice = &trigger
	return signal, true
}

// CandidateTrendContinuationReentry 对应 `_candidate_trend_continuation_reentry_prepared`。
func CandidateTrendContinuationReentry(v *VectorBars, marketState string, f Feature) (Signal, bool) {
	if marketState != "bull" || v == nil || v.Len() < 60 {
		return Signal{}, false
	}
	metrics, ok := TrendContinuationMetricsOf(v)
	if !ok || !TrendContinuationCandidatePrefilter(v) {
		return Signal{}, false
	}
	closeP := metrics.Close
	stop := metrics.Stop
	if stop <= 0 || stop >= closeP {
		return Signal{}, false
	}
	quality := BehaviorQualityOf(f)
	upperOK := quality.UpperShadowBehavior != "selling_pressure"
	volumeConfirmOK := quality.VolumeBehavior == "breakout_volume" || quality.VolumeBehavior == "active_volume" ||
		quality.VolumeBehavior == "strong_decay" || quality.VolumeBehavior == "neutral_decay"
	acceptanceConfirmOK := quality.AcceptanceQuality == "strong_acceptance"
	behaviorWatchOK := BehaviorQualityOK(f, 38.0)
	behaviorConfirmOK := BehaviorQualityOK(f, 50.0)
	trendWatchOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 62.0
	trendConfirmOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 70.0
	n := v.Len()
	prevClose := v.Close[n-2]
	lastLow := v.Low[n-1]
	stackWatchOK := closeP >= metrics.Ma20*0.99 && metrics.Ma10 >= metrics.Ma20*0.98 &&
		metrics.Ma20 >= metrics.Ma60*0.96
	notFarWatchOK := closeP <= metrics.Ma20*1.22 && closeP >= metrics.High20*0.84
	recentWatchOK := closeP >= metrics.Prev20High*0.98 ||
		v.lastFloat(v.CloseMax5, closeP) >= metrics.Prev20High*0.98
	triggerProbe := metrics.Trigger ||
		(closeP >= metrics.Ma5 && closeP > prevClose && metrics.ClosePos >= 0.48 &&
			(lastLow <= metrics.Ma10*1.04 || closeP >= metrics.Prev20High*0.985))

	hits, misses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendWatchOK, "20-60日趋势仍在抬高"},
		{stackWatchOK, "价格维持在10/20日趋势结构附近"},
		{notFarWatchOK, "距离20日高点未明显脱离"},
		{recentWatchOK, "近期仍接近20日新高"},
		{triggerProbe, "已有续航触发前兆"},
		{behaviorWatchOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})
	if !(trendWatchOK && stackWatchOK && notFarWatchOK && recentWatchOK &&
		triggerProbe && behaviorWatchOK && upperOK) {
		return Signal{}, false
	}

	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketState == "bull", "市场状态匹配牛市/趋势扩张"},
		{trendConfirmOK, "20-60日趋势持续抬高且强度达标"},
		{metrics.TrendStack, "价格站上10/20日线且20日线保持中期趋势"},
		{metrics.NotFarFromHigh, "距离20日高点不过远，仍处于趋势续航区"},
		{metrics.RecentStrength, "近期创新高或接近20日新高"},
		{metrics.Trigger, "当日放量转强或回踩10日线后收复"},
		{metrics.DigestionOK, "趋势续航已有回踩/缩量消化"},
		{!metrics.OverheatRisk, "短线未明显过热，避免高位追强"},
		{metrics.ReentryQuality, "价格未明显追高，具备回踩后再上车质量"},
		{volumeConfirmOK, "趋势续航量能质量有效"},
		{acceptanceConfirmOK, "趋势续航承接达到强承接"},
		{behaviorConfirmOK, BehaviorConditionText(f)},
		{upperOK, "未出现明显放量抛压上影"},
	})

	var hard, soft []string
	if !trendConfirmOK {
		soft = append(soft, "趋势强度未达到确认阈值")
	}
	if !metrics.TrendStack {
		soft = append(soft, "10/20日趋势排列未完全确认")
	}
	if !metrics.NotFarFromHigh {
		soft = append(soft, "价格距离20日高点确认区偏远")
	}
	if !metrics.RecentStrength {
		hard = append(hard, "近期强度不足")
	}
	if !metrics.Trigger {
		soft = append(soft, "尚未放量突破或收复10日线")
	}
	if !metrics.DigestionOK {
		hard = append(hard, "趋势续航缺少回踩消化，暂不追强")
	}
	if metrics.OverheatRisk {
		hard = append(hard, "趋势续航短线过热，疑似高位追强")
	}
	if !metrics.ReentryQuality {
		hard = append(hard, "趋势续航缺少回踩再上车质量，存在追高风险")
	}
	if !volumeConfirmOK {
		hard = append(hard, "趋势续航量能质量不足，常态量仅观察")
	}
	if !acceptanceConfirmOK {
		hard = append(hard, "趋势续航承接不足，弱承接仅观察")
	}
	if !upperOK {
		hard = append(hard, "存在明显放量抛压上影")
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 50.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 75.0, MaxSoftMisses: 2, RequireStrongAcceptance: false,
	})

	risk := math.Max(closeP-stop, closeP*0.035)
	target := closeP + risk*2.0
	score := 48.0 + hitRatio(hits, misses)*26.0 +
		math.Min(12.0, f.TrendScore/9.0) + BehaviorScoreAdjust(f)
	return BuildCandidateSignal(ModelTrendPullback, marketState, v.Date[n-1], f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "强趋势仍在中短期结构上方，已进入趋势续航再上车观察区，明日需等待价格和量能继续确认。" +
			BehaviorConditionText(f) + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      confirmMisses,
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		SignalSubtype: "trend_continuation_reentry", SignalSubtypeLabel: "趋势续航再上车",
	}), true
}

// CandidateTrendPullback 对应 `_candidate_trend_pullback_prepared`。
func CandidateTrendPullback(v *VectorBars, marketState string) (Signal, bool) {
	if v == nil || v.Len() < 45 {
		return Signal{}, false
	}
	f := v.Snapshot()
	n := v.Len()
	last := v.rawLast()
	closeP := last.Close

	powerReentry, powerOK := CandidatePowerBreakoutReentry(v, marketState, f)
	platformReentry, platformOK := CandidateTrendPlatformReentry(v, marketState, f)
	earlyStart, earlyOK := CandidateEarlyMainTrendStart(v, marketState, f)
	var hits, misses []string

	marketOK := marketState == "bull"
	if marketOK {
		hits = append(hits, "市场状态匹配牛市/趋势扩张")
	} else {
		misses = append(misses, "市场状态匹配牛市/趋势扩张")
	}
	trendOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 60.0
	if trendOK {
		hits = append(hits, "20-60日趋势结构偏强")
	} else {
		misses = append(misses, "20-60日趋势结构偏强")
	}
	breakout, breakoutOK := RecentBreakoutOf(v)
	if breakoutOK {
		hits = append(hits, "60日内出现过放量突破平台")
	} else {
		misses = append(misses, "60日内出现过放量突破平台")
	}

	// 后续多处复用"取第一个非空"的链
	continuationOrPlatform := func() (Signal, bool) {
		if got, ok := CandidateTrendContinuationReentry(v, marketState, f); ok {
			return got, true
		}
		return platformReentry, platformOK
	}
	fallbackChain := func() (Signal, bool) {
		if got, ok := continuationOrPlatform(); ok {
			return got, true
		}
		if powerOK {
			return powerReentry, true
		}
		return earlyStart, earlyOK
	}

	if !breakoutOK {
		return fallbackChain()
	}

	sub := v.Slice(breakout.Idx, n)
	highSince := maxSeries(Series(sub.High))
	lowSince := minSeries(Series(sub.Low))
	support := breakout.PlatformHigh
	pullback := 0.0
	if highSince > 0 {
		pullback = (highSince - closeP) / highSince * 100.0
	}
	pullbackOK := pullback >= 2.0 && pullback <= 18.0

	avg3 := meanNonNaN(v.Volume[n-3:])
	volumeCoolOK := avg3 < breakout.Volume*0.75
	supportOK := lowSince >= support*0.94 && closeP >= support*0.97
	entryNearSupportWatch := closeP <= support*1.14

	prevClose := v.Close[n-2]
	prevHigh := v.High[n-2]
	prev2Vol := 0.0
	if n >= 3 {
		prev2Vol = meanNonNaN(v.Volume[n-3 : n-1])
	}
	restrengthProbe := closeP >= prevClose &&
		(closeP >= last.Open || last.Volume >= prev2Vol*0.8)

	behaviorOK := BehaviorQualityOK(f, 38.0)

	confirmTrendOK := f.HigherHigh && f.HigherLow && f.TrendScore >= 65.0
	confirmPullbackOK := pullback >= 3.0 && pullback <= 12.0
	confirmVolumeDecayOK := avg3 < breakout.Volume*0.5
	confirmSupportOK := lowSince >= support*0.98 && closeP >= support
	confirmEntryNearOK := closeP <= support*1.10
	confirmRestrengthOK := closeP > prevHigh && last.Volume > prev2Vol
	confirmBehaviorOK := BehaviorQualityOK(f, 45.0)
	quality := BehaviorQualityOf(f)
	strongAcceptance := quality.AcceptanceQuality == "strong_acceptance"

	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketOK, "市场状态匹配牛市/趋势扩张"},
		{confirmTrendOK, "20-60日高低点抬高且趋势强度达标"},
		{breakoutOK, "60日内出现过放量突破平台"},
		{confirmPullbackOK, fmt.Sprintf("回踩幅度%.2f%%位于3%%-12%%", pullback)},
		{confirmVolumeDecayOK, "最近3日均量低于启动日成交量50%"},
		{confirmSupportOK, "回踩未有效跌破突破平台"},
		{confirmEntryNearOK, "买点仍贴近突破平台支撑"},
		{confirmRestrengthOK, "当日收盘高于前高且成交量回升"},
		{confirmBehaviorOK, BehaviorConditionText(f)},
		{strongAcceptance, "回踩承接达到强承接"},
	})

	pullbackText := fmt.Sprintf("回踩幅度%.2f%%处于观察区间", pullback)
	if pullbackOK {
		hits = append(hits, pullbackText)
	} else {
		misses = append(misses, pullbackText)
	}
	if volumeCoolOK {
		hits = append(hits, "突破后成交量已有明显降温")
	} else {
		misses = append(misses, "突破后成交量已有明显降温")
	}
	if supportOK {
		hits = append(hits, "价格仍在突破平台附近")
	} else {
		misses = append(misses, "价格仍在突破平台附近")
	}
	if entryNearSupportWatch {
		hits = append(hits, "买点未明显远离突破平台")
	} else {
		misses = append(misses, "买点未明显远离突破平台")
	}
	if restrengthProbe {
		hits = append(hits, "已有止跌或温和转强迹象")
	} else {
		misses = append(misses, "已有止跌或温和转强迹象")
	}
	behaviorText := BehaviorConditionText(f)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}

	lateHits := countTrue(pullbackOK, volumeCoolOK, supportOK, restrengthProbe)
	if !(marketOK && trendOK && breakoutOK && entryNearSupportWatch && lateHits >= 2) {
		return fallbackChain()
	}
	if !supportOK && platformOK {
		return platformReentry, true
	}
	if !pullbackOK {
		if got, ok := continuationOrPlatform(); ok {
			return got, true
		}
		if powerOK {
			return powerReentry, true
		}
		if earlyOK {
			return earlyStart, true
		}
	}

	var hard, soft []string
	if !confirmTrendOK {
		hard = append(hard, "趋势结构未达到确认强度")
	}
	if !confirmPullbackOK {
		hard = append(hard, "回踩幅度不在3%-12%交易区")
	}
	if !supportOK {
		if strongAcceptance && closeP >= support*0.94 {
			soft = append(soft, "价格未站稳突破平台观察区")
		} else {
			hard = append(hard, "价格未站稳突破平台观察区")
		}
	} else if !confirmSupportOK {
		soft = append(soft, "突破平台支撑确认不足")
	}
	if !confirmEntryNearOK {
		hard = append(hard, "买点距离突破平台支撑过远，容易变成追买")
	}
	if !restrengthProbe {
		hard = append(hard, "缺少止跌或温和转强迹象")
	}
	if !confirmVolumeDecayOK {
		soft = append(soft, "缩量回踩不充分")
	}
	if !confirmRestrengthOK {
		switch {
		case !strongAcceptance:
			hard = append(hard, "尚未放量转强且承接未达到强承接")
		case !effectiveVolumeBehavior(quality.VolumeBehavior):
			soft = append(soft, "尚未放量转强且量能不是有效缩量")
		default:
			soft = append(soft, "尚未完全放量转强")
		}
	}
	if !strongAcceptance {
		hard = append(hard, "回踩承接未达到强承接，弱承接仅观察")
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 45.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 70.0, MaxSoftMisses: 2, RequireStrongAcceptance: true,
	})
	if powerOK && len(blockReasons) > 0 {
		return powerReentry, true
	}
	if platformOK && len(blockReasons) > 0 {
		return platformReentry, true
	}
	if earlyOK && len(blockReasons) > 0 {
		return earlyStart, true
	}

	stop := math.Min(lowSince, support*0.98)
	risk := math.Max(closeP-stop, closeP*0.03)
	target := closeP + risk*1.8
	score := 46.0 + hitRatio(hits, misses)*30.0 +
		math.Min(8.0, f.TrendScore/12.0) +
		math.Min(5.0, breakout.VolumeRatio*1.5) + BehaviorScoreAdjust(f)
	return BuildCandidateSignal(ModelTrendPullback, marketState, last.Date, f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "趋势突破后的回踩结构已接近可观察区，仍需等待缩量支撑或放量转强进一步确认。" +
			behaviorText + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      missingCoreFor(behaviorOK, behaviorText),
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
	}), true
}

// CandidateBoxLow 对应 `_candidate_box_low_prepared`。
func CandidateBoxLow(v *VectorBars, marketState string) (Signal, bool) {
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
	if support <= 0 || resistance <= support {
		return Signal{}, false
	}
	heightPct := boxMetrics.HeightPct
	midPrice := (last.High + last.Low) / 2.0
	boxQuality := BoxStructureQualityOf(v, f)
	behaviorText := BehaviorConditionText(f)
	lowerShadow := 0.0
	if f.LowerShadowRatio != nil {
		lowerShadow = *f.LowerShadowRatio
	}

	var hits, misses []string
	marketOK := marketState == "range"
	if marketOK {
		hits = append(hits, "市场状态匹配震荡/轮动")
	} else {
		misses = append(misses, "市场状态匹配震荡/轮动")
	}
	boxRangeOK := heightPct >= 6.0 && heightPct <= 32.0
	boxRangeText := fmt.Sprintf("30日箱体振幅%.2f%%处于观察区间", heightPct)
	if boxRangeOK {
		hits = append(hits, boxRangeText)
	} else {
		misses = append(misses, boxRangeText)
	}
	nearSupport := boxMetrics.NearSupportWatch
	if nearSupport {
		hits = append(hits, "价格处于箱体下沿附近")
	} else {
		misses = append(misses, "价格处于箱体下沿附近")
	}
	volumeSoft := boxMetrics.VolumeSoft
	if volumeSoft {
		hits = append(hits, "支撑附近成交量低于近期均量")
	} else {
		misses = append(misses, "支撑附近成交量低于近期均量")
	}
	prevClose := closeP
	if !isNaN(boxMetrics.PrevClose) {
		prevClose = boxMetrics.PrevClose
	}
	accept := lowerShadow >= 0.8 || closeP >= midPrice || closeP >= prevClose
	if accept {
		hits = append(hits, "下沿附近已有承接或收盘回升")
	} else {
		misses = append(misses, "下沿附近已有承接或收盘回升")
	}
	behaviorOK := BehaviorQualityOK(f, 38.0)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}
	boxWatchable := boxQuality.Watchable
	watchText := fmt.Sprintf("箱体观察质量评分%.0f", boxQuality.Score)
	if boxWatchable {
		hits = append(hits, watchText)
	} else {
		misses = append(misses, watchText)
	}

	confirmBoxOK := heightPct >= 8.0 && heightPct <= 25.0
	confirmNearSupport := boxMetrics.NearSupportConfirm
	confirmVolumeDecay := boxMetrics.VolumeDecay
	confirmAccept := lowerShadow >= 1.5 || closeP > midPrice
	confirmBehaviorOK := BehaviorQualityOK(f, 45.0)
	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketOK, "市场状态匹配震荡/轮动"},
		{confirmBoxOK, fmt.Sprintf("30日箱体振幅%.2f%%", heightPct)},
		{confirmNearSupport, "价格接近箱体下沿3%以内"},
		{confirmVolumeDecay, "下跌到支撑附近时成交量连续收缩"},
		{confirmAccept, "出现下影线或收盘回升承接"},
		{confirmBehaviorOK, behaviorText},
	})

	if !(marketOK && boxRangeOK && nearSupport && (volumeSoft || accept) && boxWatchable) {
		return Signal{}, false
	}

	var hard, soft []string
	if !confirmBoxOK {
		soft = append(soft, "箱体振幅不在确认交易区")
	}
	if !nearSupport {
		hard = append(hard, "价格未处于箱体下沿观察区")
	} else if !confirmNearSupport {
		soft = append(soft, "价格离箱体下沿确认区偏远")
	}
	if !confirmAccept {
		if quality.AcceptanceQuality != "strong_acceptance" {
			hard = append(hard, "下沿承接确认不足")
		} else {
			soft = append(soft, "下沿承接尚未完全确认")
		}
	}
	if !confirmVolumeDecay {
		if quality.AcceptanceQuality != "strong_acceptance" {
			hard = append(hard, "未连续缩量且承接未达到强承接")
		} else {
			soft = append(soft, "未连续缩量")
		}
	}
	for _, item := range boxQuality.BlockReasons {
		if text := strings.TrimSpace(item); text != "" {
			hard = append(hard, text)
		}
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 45.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 80.0, MaxSoftMisses: 1, RequireStrongAcceptance: true,
	})

	stop := support * 0.97
	target := resistance * 0.96
	if target <= closeP {
		return Signal{}, false
	}
	score := 45.0 + hitRatio(hits, misses)*32.0 +
		math.Min(8.0, math.Max(0.0, 32.0-heightPct)*0.25) +
		math.Min(4.0, lowerShadow*1.5) +
		math.Min(8.0, boxQuality.Score/12.0) + behaviorScoreAdjust(quality)
	candidate := BuildCandidateSignal(ModelBoxRange, marketState, last.Date, f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "价格已靠近箱体下沿并出现部分承接条件，属于震荡低吸观察位，确认仍看缩量和回升质量。" +
			behaviorText + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      missingCoreFor(behaviorOK, behaviorText),
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
	})
	candidate.BoxQuality = &boxQuality
	mergeExtras(&candidate.BehaviorQuality, boxQuality.AsFloatMap())
	candidate.QualityBlockReasons = append([]string(nil), boxQuality.BlockReasons...)
	return candidate, true
}

// CandidateOversoldRebound 对应 `_candidate_oversold_rebound_prepared`。
func CandidateOversoldRebound(v *VectorBars, marketState string) (Signal, bool) {
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
	prev20 := v.Volume[n-21 : n-1]
	avg20 := 0.0
	if len(prev20) >= 10 {
		avg20 = meanNonNaN(prev20)
	}
	panicSoft := avg20 > 0 && maxSeries(v.Volume[n-7:]) >= avg20*1.8
	stopDays := v.Volume[n-3 : n-1]
	volumeStop := len(stopDays) >= 2 && stopDays[len(stopDays)-1] <= stopDays[0]
	prevClose := v.Close[n-2]
	prevHigh := v.High[n-2]
	prevVolume := v.Volume[n-2]
	reboundProbe := closeP > prevClose && (closeP >= last.Open || last.Volume >= prevVolume*0.8)
	reboundQuality := OversoldReboundQualityOf(v, f, fall5, panicSoft, reboundProbe)
	lowerShadow := 0.0
	if f.LowerShadowRatio != nil {
		lowerShadow = *f.LowerShadowRatio
	}

	var hits, misses []string
	marketOK := marketState == "bear"
	if marketOK {
		hits = append(hits, "市场状态匹配熊市/风险释放")
	} else {
		misses = append(misses, "市场状态匹配熊市/风险释放")
	}
	fallOK := fall5 <= -12.0
	fallText := fmt.Sprintf("5日跌幅%.2f%%进入超跌观察区", fall5)
	if fallOK {
		hits = append(hits, fallText)
	} else {
		misses = append(misses, fallText)
	}
	if panicSoft {
		hits = append(hits, "近期出现放量风险释放")
	} else {
		misses = append(misses, "近期出现放量风险释放")
	}
	stopSoft := volumeStop || f.ReversalFlag || lowerShadow >= 1.2
	if stopSoft {
		hits = append(hits, "出现止跌、反包或下影承接")
	} else {
		misses = append(misses, "出现止跌、反包或下影承接")
	}
	if reboundProbe {
		hits = append(hits, "已有初步反抽迹象")
	} else {
		misses = append(misses, "已有初步反抽迹象")
	}
	behaviorOK := BehaviorQualityOK(f, 35.0)
	behaviorText := BehaviorConditionText(f)
	if behaviorOK {
		hits = append(hits, behaviorText)
	} else {
		misses = append(misses, behaviorText)
	}
	reboundWatchable := reboundQuality.Watchable
	watchText := fmt.Sprintf("超跌观察质量：恐慌%.0f/反抽%.0f",
		reboundQuality.PanicReleaseScore, reboundQuality.ReboundScore)
	if reboundWatchable {
		hits = append(hits, watchText)
	} else {
		misses = append(misses, watchText)
	}

	confirmFallOK := fall5 <= -20.0
	confirmPanic := avg20 > 0 && maxSeries(v.Volume[n-7:]) >= avg20*3.0
	confirmStop := volumeStop || f.ReversalFlag || lowerShadow >= 2.0
	confirmRebound := closeP > prevHigh && last.Volume > prevVolume
	confirmBehaviorOK := BehaviorQualityOK(f, 38.0)
	confirmHits, confirmMisses := ConditionLists([]ConditionPair{
		{marketOK, "市场状态匹配熊市/风险释放"},
		{confirmFallOK, fmt.Sprintf("5日跌幅%.2f%%达到超跌", fall5)},
		{confirmPanic, "近5日出现3倍均量恐慌释放"},
		{confirmStop, "出现缩量止跌或长下影"},
		{confirmRebound, "首次收复前一日高点且成交量回升"},
		{confirmBehaviorOK, behaviorText},
	})

	if !(marketOK && fallOK && countTrue(panicSoft, stopSoft, reboundProbe) >= 2 && reboundWatchable) {
		return Signal{}, false
	}

	quality := BehaviorQualityOf(f)
	var hard, soft []string
	if !confirmFallOK {
		soft = append(soft, "超跌幅度未达到确认交易区")
	}
	if !stopSoft {
		hard = append(hard, "缺少止跌承接观察信号")
	} else if !confirmStop {
		soft = append(soft, "止跌承接确认不足")
	}
	if !confirmRebound {
		if quality.AcceptanceQuality != "strong_acceptance" {
			hard = append(hard, "尚未首次收复前高并放量回升")
		} else {
			soft = append(soft, "尚未首次收复前高并放量回升")
		}
	}
	if !confirmPanic {
		if quality.AcceptanceQuality != "strong_acceptance" {
			hard = append(hard, "恐慌释放不足且承接未达到强承接")
		} else {
			soft = append(soft, "恐慌释放未达到确认强度")
		}
	}
	for _, item := range reboundQuality.BlockReasons {
		if text := strings.TrimSpace(item); text != "" {
			hard = append(hard, text)
		}
	}
	blockReasons := CandidateTradeBlockReasons(f, CandidateBlockOptions{
		MinScore: 38.0, ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
		HardReasons: hard, SoftReasons: soft,
		MinProgress: 80.0, MaxSoftMisses: 1, RequireStrongAcceptance: true,
	})

	stop := minSeries(v.Low[n-5:])
	risk := math.Max(closeP-stop, closeP*0.03)
	target := math.Min(closeP*1.12, closeP+risk*1.6)
	score := 45.0 + hitRatio(hits, misses)*32.0 +
		math.Min(8.0, math.Abs(fall5)*0.3) +
		math.Min(4.0, lowerShadow*1.2) +
		math.Min(8.0, reboundQuality.ReboundScore/14.0) + BehaviorScoreAdjust(f)
	candidate := BuildCandidateSignal(ModelOversoldRebound, marketState, last.Date, f, CandidateSignalOptions{
		EntryPrice: FloatPtr(closeP), StopPrice: FloatPtr(stop), TargetPrice: FloatPtr(target),
		Score: score, Hits: hits, Misses: misses,
		Explanation: "急跌后的风险释放和止跌特征已部分出现，属于反抽观察位，确认仍看首次放量修复。" +
			behaviorText + "。",
		LooseEntryAllowed: len(blockReasons) == 0, LooseEntryBlockReasons: blockReasons,
		MissingCore:      missingCoreFor(behaviorOK, behaviorText),
		ConfirmationHits: confirmHits, ConfirmationMisses: confirmMisses,
	})
	candidate.OversoldQuality = &reboundQuality
	mergeExtras(&candidate.BehaviorQuality, reboundQuality.AsFloatMap())
	candidate.QualityBlockReasons = append([]string(nil), reboundQuality.BlockReasons...)
	return candidate, true
}

// EvaluateCandidatePrepared 对应 `_evaluate_candidate_prepared`（分派）。
func EvaluateCandidatePrepared(modelID string, v *VectorBars, marketState string) (Signal, bool) {
	switch NormalizeModelID(modelID) {
	case ModelTrendPullback:
		return CandidateTrendPullback(v, marketState)
	case ModelBoxRange:
		return CandidateBoxLow(v, marketState)
	case ModelOversoldRebound:
		return CandidateOversoldRebound(v, marketState)
	}
	return Signal{}, false
}

// EvaluateModelCandidate 对应 `evaluate_model_candidate`。
func EvaluateModelCandidate(modelID string, bars []Bar, marketState, currentDate string) (Signal, bool) {
	state := NormalizeMarketState(marketState, "range")
	return EvaluateCandidatePrepared(modelID, PrepareBars(bars, currentDate), state)
}

// EvaluateModelLayers 对应 `evaluate_model_layers_prepared`：
// 先试确认，确认不出才试候选。
func EvaluateModelLayers(v *VectorBars, modelID, marketState string) (Signal, bool, Signal, bool) {
	confirmed, confirmedOK := EvaluateConfirmedPrepared(modelID, v, marketState)
	if confirmedOK {
		return confirmed, true, Signal{}, false
	}
	candidate, candidateOK := EvaluateCandidatePrepared(modelID, v, marketState)
	return Signal{}, false, candidate, candidateOK
}

// missingCoreFor 对应 `missing_core=None if behavior_ok else [text]`。
func missingCoreFor(behaviorOK bool, text string) []string {
	if behaviorOK {
		return nil
	}
	return []string{text}
}

// effectiveVolumeBehavior 对应
// `volume_behavior in {"strong_decay", "neutral_decay", "active_volume"}`。
func effectiveVolumeBehavior(value string) bool {
	return value == "strong_decay" || value == "neutral_decay" || value == "active_volume"
}
