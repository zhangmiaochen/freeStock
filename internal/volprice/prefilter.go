package volprice

import "math"

// 对应 `volume_price_models.py` 的三个入场前置过滤，以及
// `volume_price_backtest.py` 里按模型分派的 `_passes_entry_prefilter` /
// `_passes_candidate_prefilter`。
//
// ⚠ 这些函数在原实现里都先 `prepare_bars(bars)`。Go 侧统一接收**已准备好的**
// `*VectorBars`：`prepare_bars` 对"已带派生列的帧"是**直接返回、不再排序去重**的
//（`_has_vector_bars_columns` 分支），所以传入 prepared 帧与在函数内再准备一次
// **完全等价**，只是省掉重复计算。
//
// 原实现还用 `_cache_bars_value` 按帧做了缓存；那是纯性能手段，
// 语义上无差别，这里不做。

// RecentBreakout 对应 `_recent_breakout` 的返回字典。
type RecentBreakout struct {
	Idx          int
	TradeDate    string
	Close        float64
	High         float64
	Low          float64
	Volume       float64
	PlatformHigh float64
	PlatformLow  float64
	VolumeRatio  float64
}

// RecentBreakoutOf 对应 `_recent_breakout`。
//
// 在 `[max(20, n-60), n-2]` 这个窗口里找"收盘创 20 日新高 且 量 ≥ 20 日均量×2"
// 的**最后一根**，并要求它的 20 日平台上下沿与均量都为正。
func RecentBreakoutOf(v *VectorBars) (RecentBreakout, bool) {
	if v == nil || v.Len() < 35 {
		return RecentBreakout{}, false
	}
	n := v.Len()
	start := n - 60
	if start < 20 {
		start = 20
	}
	end := n - 2
	if end < start {
		end = start
	}
	last := -1
	for i := start; i <= end && i < n; i++ {
		aboveHigh := v.Close[i] > v.Prev20High[i]
		volumeOK := v.Volume[i] >= v.Prev20VolumeMean[i]*2.0
		if aboveHigh && volumeOK {
			last = i
		}
	}
	if last < 0 {
		return RecentBreakout{}, false
	}
	platformHigh := floatingOrDefault(v.Prev20High[last], 0.0)
	platformLow := floatingOrDefault(v.Prev20Low[last], 0.0)
	avgVol := floatingOrDefault(v.Prev20VolumeMean[last], 0.0)
	if platformHigh <= 0 || avgVol <= 0 {
		return RecentBreakout{}, false
	}
	return RecentBreakout{
		Idx: last, TradeDate: v.Date[last],
		Close: v.Close[last], High: v.High[last], Low: v.Low[last], Volume: v.Volume[last],
		PlatformHigh: platformHigh, PlatformLow: platformLow,
		VolumeRatio: v.Volume[last] / avgVol,
	}, true
}

// TrendContinuationPrefilter 对应 `trend_continuation_prefilter`。
func TrendContinuationPrefilter(v *VectorBars) bool {
	if v == nil || v.Len() < 45 {
		return false
	}
	feature := v.Snapshot()
	metrics, hasMetrics := TrendContinuationMetricsOf(v)
	standardOK := false
	platformOK := false
	if hasMetrics {
		standardOK = feature.HigherHigh && feature.HigherLow && feature.TrendScore >= 70.0 &&
			metrics.TrendStack && metrics.NotFarFromHigh && metrics.RecentStrength &&
			metrics.Trigger && metrics.ReentryQuality
		if platform, ok := TrendPlatformMetricsOf(v); ok {
			platformOK = feature.HigherHigh && feature.HigherLow && feature.TrendScore >= 70.0 &&
				metrics.TrendStack && metrics.RecentStrength &&
				platform.SetupOK && platform.BreakoutTurn && !platform.ChaseRisk
		}
	}
	earlyOK := false
	if early, ok := EarlyMainTrendMetricsOf(v); ok {
		earlyOK = early.LowBaseOK && early.PlatformOK && early.TrendTurnOK &&
			early.ConfirmBreak && !early.ChaseRisk
	}
	return standardOK || platformOK || earlyOK
}

// TrendContinuationCandidatePrefilter 对应 `trend_continuation_candidate_prefilter`。
func TrendContinuationCandidatePrefilter(v *VectorBars) bool {
	if v == nil || v.Len() < 45 {
		return false
	}
	feature := v.Snapshot()
	metrics, hasMetrics := TrendContinuationMetricsOf(v)
	standardOK := false
	platformOK := false
	if hasMetrics {
		n := v.Len()
		closeP := metrics.Close
		prevClose := v.Close[n-2]
		low := v.Low[n-1]
		trendWatch := feature.HigherHigh && feature.HigherLow && feature.TrendScore >= 62.0
		stackWatch := closeP >= metrics.Ma20*0.99 && metrics.Ma10 >= metrics.Ma20*0.98 &&
			metrics.Ma20 >= metrics.Ma60*0.96
		notFarWatch := closeP <= metrics.Ma20*1.22 && closeP >= metrics.High20*0.84
		recentWatch := closeP >= metrics.Prev20High*0.98 ||
			v.lastFloat(v.CloseMax5, closeP) >= metrics.Prev20High*0.98
		triggerProbe := metrics.Trigger ||
			(closeP >= metrics.Ma5 &&
				closeP > prevClose &&
				metrics.ClosePos >= 0.48 &&
				(low <= metrics.Ma10*1.04 || closeP >= metrics.Prev20High*0.985))
		standardOK = trendWatch && stackWatch && notFarWatch && recentWatch && triggerProbe
		if platform, ok := TrendPlatformMetricsOf(v); ok {
			platformOK = trendWatch && stackWatch && recentWatch && platform.SetupOK &&
				platform.SupportHold && platform.WatchReady && !platform.ChaseRisk
		}
	}
	earlyOK := false
	if early, ok := EarlyMainTrendMetricsOf(v); ok {
		earlyOK = early.WatchReady && early.PlatformOK && early.LowBaseOK && !early.ChaseRisk
	}
	return standardOK || platformOK || earlyOK
}

// TrendPullbackCandidatePrefilter 对应 `trend_pullback_candidate_prefilter`。
func TrendPullbackCandidatePrefilter(v *VectorBars) bool {
	if v == nil || v.Len() < 45 {
		return false
	}
	if _, ok := RecentBreakoutOf(v); ok {
		return true
	}
	if TrendContinuationCandidatePrefilter(v) {
		return true
	}
	_, ok := PowerBreakoutMetricsOf(v)
	return ok
}

// ---------------------------------------------------------------------------
// volume_price_backtest.py 的两个分派入口
// ---------------------------------------------------------------------------

// PassesEntryPrefilter 对应 `_passes_entry_prefilter`。
func PassesEntryPrefilter(modelID string, v *VectorBars) bool {
	model := NormalizeModelID(modelID)
	if v == nil || v.Len() == 0 {
		return false
	}
	switch model {
	case ModelTrendPullback:
		if v.Len() < 45 {
			return false
		}
		// bars = data.iloc[-80:]
		bars := v
		if v.Len() > 80 {
			bars = v.Slice(v.Len()-80, v.Len())
		}
		// breakout = (close > prev20_high) & (volume >= prev20_volume_mean*2)
		// if len(breakout) >= 2: 丢掉最后一个元素
		limit := bars.Len()
		if limit >= 2 {
			limit--
		}
		for i := 0; i < limit; i++ {
			if bars.Close[i] > bars.Prev20High[i] && bars.Volume[i] >= bars.Prev20VolumeMean[i]*2.0 {
				return true
			}
		}
		return TrendContinuationPrefilter(v) || TrendContinuationCandidatePrefilter(v)
	case ModelBoxRange:
		if v.Len() < 35 {
			return false
		}
		// 原实现写的是 `float(last.get("vp_prev30_low") or 0.0)`；
		// NaN 在 Python 里是**真值**，所以 `or 0.0` 不会把 NaN 变成 0 ——
		// 这里保持原样（NaN 会让下面的比较全为假，最终返回 false）。
		support := v.lastFloat(v.Prev30Low, nan())
		resistance := v.lastFloat(v.Prev30High, nan())
		closeP := v.Close[v.Len()-1]
		if support <= 0 || resistance <= support {
			return false
		}
		heightPct := (resistance/support - 1.0) * 100.0
		nearSupport := support*0.96 <= closeP && closeP <= support*1.06
		nearResistance := closeP >= resistance*0.97
		return heightPct >= 6.0 && heightPct <= 32.0 && (nearSupport || nearResistance)
	case ModelOversoldRebound:
		if v.Len() < 25 {
			return false
		}
		closeP := v.Close[v.Len()-1]
		close5 := closeP
		if v.Len() >= 6 {
			close5 = v.Close[v.Len()-6]
		}
		fall5 := 0.0
		if close5 > 0 {
			fall5 = (closeP/close5 - 1.0) * 100.0
		}
		return fall5 <= -12.0
	}
	return true
}

// PassesCandidatePrefilter 对应 `_passes_candidate_prefilter`。
func PassesCandidatePrefilter(modelID string, v *VectorBars) bool {
	model := NormalizeModelID(modelID)
	if v == nil || v.Len() == 0 {
		return false
	}
	if model == ModelTrendPullback {
		return TrendPullbackCandidatePrefilter(v)
	}
	return true
}

// Slice 返回 [from, to) 的子帧（派生列一并带上）。
//
// 对应 pandas 的 `data.iloc[-80:]` 这类按行切片：切出来的帧**保留派生列**，
// 所以后续的 `close > vp_prev20_high` 之类仍然成立。
func (v *VectorBars) Slice(from, to int) *VectorBars {
	if v == nil {
		return nil
	}
	if from < 0 {
		from = 0
	}
	if to > v.Len() {
		to = v.Len()
	}
	if to < from {
		to = from
	}
	sub := func(s Series) Series { return append(Series(nil), s[from:to]...) }
	subBool := func(s BoolSeries) BoolSeries { return append(BoolSeries(nil), s[from:to]...) }
	subStr := func(s StrSeries) StrSeries { return append(StrSeries(nil), s[from:to]...) }

	out := &VectorBars{
		Date: subStr(v.Date), Open: sub(v.Open), High: sub(v.High),
		Low: sub(v.Low), Close: sub(v.Close), Volume: sub(v.Volume),
		PrevOpen: sub(v.PrevOpen), PrevHigh: sub(v.PrevHigh),
		PrevLow: sub(v.PrevLow), PrevClose: sub(v.PrevClose),
		CloseMa5: sub(v.CloseMa5), CloseMa10: sub(v.CloseMa10),
		CloseMa20: sub(v.CloseMa20), CloseMa60: sub(v.CloseMa60),
		CloseMax5: sub(v.CloseMax5), CloseMedian60: sub(v.CloseMedian60),
		HighRoll20: sub(v.HighRoll20), LowRoll10: sub(v.LowRoll10), LowRoll20: sub(v.LowRoll20),
		Prev20High: sub(v.Prev20High), Prev20Low: sub(v.Prev20Low),
		Prev20VolumeMean: sub(v.Prev20VolumeMean),
		Prev30High:       sub(v.Prev30High), Prev30Low: sub(v.Prev30Low),
		Prev40High: sub(v.Prev40High), Prev40Low: sub(v.Prev40Low),
		Prev60High: sub(v.Prev60High), Prev60Low: sub(v.Prev60Low),
		Prev2VolumeMean: sub(v.Prev2VolumeMean), Prev3VolumeMean: sub(v.Prev3VolumeMean),
		Prev5VolumeMean: sub(v.Prev5VolumeMean), Prev8VolumeMean: sub(v.Prev8VolumeMean),
		Prev8LowMin: sub(v.Prev8LowMin), Prev8HighMax: sub(v.Prev8HighMax),
		Prev8CloseMin: sub(v.Prev8CloseMin), Prev8DownCloseCnt: sub(v.Prev8DownCloseCnt),
		Prev10HighMax: sub(v.Prev10HighMax),
		Prev5Close:    sub(v.Prev5Close), Prev10Close: sub(v.Prev10Close),
		Prev20Close: sub(v.Prev20Close), Prev60Close: sub(v.Prev60Close),
		ClosePos: sub(v.ClosePos), LowerShadowRatio: sub(v.LowerShadowRatio),
		UpperShadowRatio: sub(v.UpperShadowRatio), VolumeDecayScore: sub(v.VolumeDecayScore),
		ThreeDayDrift:    subBool(v.ThreeDayDrift),
		PlatformRangePct: sub(v.PlatformRangePct),
		VolumeRatio20:    sub(v.VolumeRatio20), PullbackRatio: sub(v.PullbackRatio),
		HigherHighFlag: subBool(v.HigherHighFlag), HigherLowFlag: subBool(v.HigherLowFlag),
		SupportHoldFlag: subBool(v.SupportHoldFlag), BreakoutFlag: subBool(v.BreakoutFlag),
		PanicVolumeFlag: subBool(v.PanicVolumeFlag),
		TrendScore:      sub(v.TrendScore), ReversalFlag: subBool(v.ReversalFlag),
		VolumeBehavior: subStr(v.VolumeBehavior), VolumeBehaviorLabel: subStr(v.VolumeBehaviorLabel),
		AcceptanceQuality:      subStr(v.AcceptanceQuality),
		AcceptanceQualityLabel: subStr(v.AcceptanceQualityLabel),
		UpperShadowBehavior:    subStr(v.UpperShadowBehavior),
		UpperShadowBehaviorLbl: subStr(v.UpperShadowBehaviorLbl),
		BehaviorQualityScore:   sub(v.BehaviorQualityScore),
		DeathVolumeRisk:        subBool(v.DeathVolumeRisk),
	}
	return out
}

// IsFinite 便于测试与调试：NaN/Inf 一律视为"非有限"。
func IsFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
