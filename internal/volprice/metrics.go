package volprice

import (
	"math"
	"strconv"
)

// 对应 `volume_price_models.py` 里的四个"结构度量"函数。
//
// 这些是**纯计算**（只依赖已附加派生列的 K 线），是三种模型的确认/候选判定
// 与回测入场前置过滤的共同底座。原实现用 `_cache_bars_value` 做了按 frame 的缓存，
// 这里不做缓存 —— Go 侧调用方按需复用同一份 `*VectorBars` 即可。
//
// ⚠ 返回"空 dict"在原实现里表示**度量不可用**（数据不足或关键均线缺失），
// 而不是"全 0"。所以每个函数都返回 `(struct, ok bool)`：`ok == false`
// 对应原实现的 `return {}`，调用方必须据此短路，否则会把"不可用"当成"不满足"。

// lastFloat / lastInt / lastBool 取最后一行的派生列（等价 `last.get(...)`）。
func (v *VectorBars) lastFloat(s Series, def float64) float64 {
	if v == nil || len(s) == 0 {
		return def
	}
	return floatingOrDefault(s[len(s)-1], def)
}

func (v *VectorBars) lastInt(s Series, def int) int {
	if v == nil || len(s) == 0 {
		return def
	}
	value := s[len(s)-1]
	if isNaN(value) {
		return def
	}
	return int(value)
}

func (v *VectorBars) lastBool(s BoolSeries) bool {
	if v == nil || len(s) == 0 {
		return false
	}
	return s[len(s)-1]
}

// rawLast 取最后一根原始 K 线。
func (v *VectorBars) rawLast() Bar {
	i := v.Len() - 1
	return Bar{Date: v.Date[i], Open: v.Open[i], High: v.High[i], Low: v.Low[i], Close: v.Close[i], Volume: v.Volume[i]}
}

func (v *VectorBars) rawAt(i int) Bar {
	return Bar{Date: v.Date[i], Open: v.Open[i], High: v.High[i], Low: v.Low[i], Close: v.Close[i], Volume: v.Volume[i]}
}

// ---------------------------------------------------------------------------
// _trend_continuation_metrics
// ---------------------------------------------------------------------------

// TrendContinuationMetrics 对应 `_trend_continuation_metrics` 的返回字典。
type TrendContinuationMetrics struct {
	Close      float64
	Ma5        float64
	Ma10       float64
	Ma20       float64
	Ma60       float64
	High20     float64
	Low20      float64
	Prev20High float64
	Low10      float64
	Prev2Vol   float64

	TrendStack     bool
	NotFarFromHigh bool
	RecentStrength bool
	BreakoutTurn   bool
	Ma10Reclaim    bool
	Trigger        bool
	Bias20Pct      float64
	Pos20Pct       float64
	Ret5Pct        float64
	Ret10Pct       float64
	Ret20Pct       float64
	DigestionOK    bool
	DigestionDepth float64
	DigestionDownN int
	DigestionVolOK bool
	OverheatRisk   bool
	Volume5And20   float64
	NearMa10Today  bool
	NearMa20Today  bool
	ReentryQuality bool
	ClosePos       float64
	Stop           float64
}

// AsFloatMap 把度量导出成"名字 → 数值"，布尔按 0/1。
//
// 用途是与原版 pandas 的黄金值逐项比对：键名与原实现的字典键**完全一致**。
func (m TrendContinuationMetrics) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"close": m.Close, "ma5": m.Ma5, "ma10": m.Ma10, "ma20": m.Ma20, "ma60": m.Ma60,
		"high20": m.High20, "low20": m.Low20, "prev20_high": m.Prev20High,
		"low10": m.Low10, "prev2_vol": m.Prev2Vol,
		"trend_stack": b2f(m.TrendStack), "not_far_from_high": b2f(m.NotFarFromHigh),
		"recent_strength": b2f(m.RecentStrength), "breakout_turn": b2f(m.BreakoutTurn),
		"ma10_reclaim": b2f(m.Ma10Reclaim), "trigger": b2f(m.Trigger),
		"bias20_pct": m.Bias20Pct, "pos20_pct": m.Pos20Pct,
		"ret5_pct": m.Ret5Pct, "ret10_pct": m.Ret10Pct, "ret20_pct": m.Ret20Pct,
		"digestion_ok": b2f(m.DigestionOK), "digestion_depth_pct": m.DigestionDepth,
		"digestion_down_days": float64(m.DigestionDownN), "digestion_volume_ok": b2f(m.DigestionVolOK),
		"overheat_risk": b2f(m.OverheatRisk), "volume5_20_ratio": m.Volume5And20,
		"near_ma10_today": b2f(m.NearMa10Today), "near_ma20_today": b2f(m.NearMa20Today),
		"reentry_quality": b2f(m.ReentryQuality), "close_pos": m.ClosePos, "stop": m.Stop,
	}
}

// TrendContinuationMetricsOf 对应 `_trend_continuation_metrics`。
func TrendContinuationMetricsOf(v *VectorBars) (TrendContinuationMetrics, bool) {
	if v == nil || v.Len() < 60 {
		return TrendContinuationMetrics{}, false
	}
	last := v.rawLast()
	closeP := last.Close
	high := last.High
	low := last.Low
	volume := last.Volume
	if closeP <= 0 {
		return TrendContinuationMetrics{}, false
	}
	prevClose := v.lastFloat(v.PrevClose, 0.0)
	prevHigh := v.lastFloat(v.PrevHigh, 0.0)
	ma5 := v.lastFloat(v.CloseMa5, 0.0)
	ma10 := v.lastFloat(v.CloseMa10, 0.0)
	ma20 := v.lastFloat(v.CloseMa20, 0.0)
	ma60 := v.lastFloat(v.CloseMa60, 0.0)
	high20 := v.lastFloat(v.HighRoll20, 0.0)
	low20 := v.lastFloat(v.LowRoll20, 0.0)
	prev20High := v.lastFloat(v.Prev20High, 0.0)
	low10 := v.lastFloat(v.LowRoll10, 0.0)
	prev2Vol := v.lastFloat(v.Prev2VolumeMean, 0.0)
	priorLow := v.lastFloat(v.Prev8LowMin, low)
	priorHigh := v.lastFloat(v.Prev10HighMax, high)
	digestionDownDays := v.lastInt(v.Prev8DownCloseCnt, 0)
	prev5Vol := v.lastFloat(v.Prev5VolumeMean, volume)
	prev20Vol := v.lastFloat(v.Prev20VolumeMean, 0.0)
	prev5Close := v.lastFloat(v.Prev5Close, 0.0)
	prev10Close := v.lastFloat(v.Prev10Close, 0.0)
	prev20Close := v.lastFloat(v.Prev20Close, 0.0)
	closeMax5 := v.lastFloat(v.CloseMax5, closeP)
	closePos := v.lastFloat(v.ClosePos, 0.5)

	if minFloat(ma5, ma10, ma20, ma60, high20, low20, prev20High) <= 0 {
		return TrendContinuationMetrics{}, false
	}

	trendStack := closeP >= ma10 && ma10 >= ma20*0.99 && closeP >= ma20 && ma20 >= ma60*0.98
	notFar := closeP <= ma20*1.18 && closeP >= high20*0.88
	recentStrength := closeP >= prev20High*0.995 || closeMax5 >= prev20High*0.995
	breakoutTurn := closeP > prevHigh && prev2Vol > 0 && volume >= prev2Vol*0.95
	ma10Reclaim := low <= ma10*1.015 &&
		closeP >= ma5 &&
		closeP > prevClose &&
		prev2Vol > 0 &&
		volume >= prev2Vol*0.75 &&
		closePos >= 0.55

	bias20 := 999.0
	if ma20 > 0 {
		bias20 = (closeP/ma20 - 1.0) * 100.0
	}
	pos20 := 50.0
	if high20 > low20 {
		pos20 = (closeP - low20) / (high20 - low20) * 100.0
	}
	ret5 := 0.0
	if prev5Close > 0 {
		ret5 = (closeP/prev5Close - 1.0) * 100.0
	}
	ret10 := 0.0
	if prev10Close > 0 {
		ret10 = (closeP/prev10Close - 1.0) * 100.0
	}
	ret20 := 0.0
	if prev20Close > 0 {
		ret20 = (closeP/prev20Close - 1.0) * 100.0
	}
	digestionDepth := 0.0
	if priorLow > 0 {
		digestionDepth = (priorHigh/priorLow - 1.0) * 100.0
	}
	digestionTouch := priorLow <= ma10*1.02 || priorLow <= ma20*1.04
	digestionVolOK := prev20Vol > 0 && prev5Vol <= prev20Vol*1.10
	digestionOK := digestionTouch &&
		(digestionDepth >= 3.0 || digestionDownDays >= 2 || digestionVolOK)
	volume5And20 := 1.0
	if prev20Vol > 0 {
		volume5And20 = prev5Vol / prev20Vol
	}
	overheat := (ret5 >= 7.0 && ret10 >= 8.0 && pos20 >= 88.0) ||
		(ret10 >= 12.0 && pos20 >= 88.0) ||
		(ret5 >= 5.0 && ret10 >= 7.0 && volume5And20 >= 1.35 && pos20 >= 82.0) ||
		(bias20 >= 8.5 && pos20 >= 92.0 && ret10 >= 7.0)
	nearMa10 := low <= ma10*1.035 || closeP <= ma10*1.025
	nearMa20 := low <= ma20*1.04 || closeP <= ma20*1.025
	reentry := digestionOK && !overheat &&
		((bias20 <= 8.5 && nearMa10 && closePos >= 0.55) ||
			(ma10Reclaim && bias20 <= 10.5 && nearMa20))

	return TrendContinuationMetrics{
		Close: closeP, Ma5: ma5, Ma10: ma10, Ma20: ma20, Ma60: ma60,
		High20: high20, Low20: low20, Prev20High: prev20High, Low10: low10, Prev2Vol: prev2Vol,
		TrendStack: trendStack, NotFarFromHigh: notFar, RecentStrength: recentStrength,
		BreakoutTurn: breakoutTurn, Ma10Reclaim: ma10Reclaim,
		Trigger:   breakoutTurn || ma10Reclaim,
		Bias20Pct: bias20, Pos20Pct: pos20,
		Ret5Pct: ret5, Ret10Pct: ret10, Ret20Pct: ret20,
		DigestionOK: digestionOK, DigestionDepth: digestionDepth,
		DigestionDownN: digestionDownDays, DigestionVolOK: digestionVolOK,
		OverheatRisk: overheat, Volume5And20: volume5And20,
		NearMa10Today: nearMa10, NearMa20Today: nearMa20, ReentryQuality: reentry,
		ClosePos: closePos,
		Stop:     math.Max(math.Min(low10, ma20*0.985), closeP*0.90),
	}, true
}

// ---------------------------------------------------------------------------
// _power_breakout_reentry_metrics
// ---------------------------------------------------------------------------

// PowerBreakoutMetrics 对应 `_power_breakout_reentry_metrics`。
type PowerBreakoutMetrics struct {
	BreakoutDate      string
	BreakoutHigh      float64
	BreakoutLow       float64
	BreakoutMid       float64
	BreakoutVolume    float64
	BreakoutRetPct    float64
	DaysAfter         int
	ConsolidationHigh float64
	ConsolidationExt  float64
	HoldMidOK         bool
	PullbackPct       float64
	TurnStrong        bool
	Ret5Pct           float64
	Bias20Pct         float64
	AccelerationRisk  bool
	ClosePos          float64
	Close             float64
	Stop              float64
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (m PowerBreakoutMetrics) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"breakout_high": m.BreakoutHigh, "breakout_low": m.BreakoutLow, "breakout_mid": m.BreakoutMid,
		"breakout_volume_ratio": m.BreakoutVolume, "breakout_ret_pct": m.BreakoutRetPct,
		"days_after_breakout": float64(m.DaysAfter), "consolidation_high": m.ConsolidationHigh,
		"consolidation_extension_pct": m.ConsolidationExt,
		"hold_mid_ok":                 b2f(m.HoldMidOK), "pullback_pct": m.PullbackPct,
		"turn_strong": b2f(m.TurnStrong), "ret5_pct": m.Ret5Pct, "bias20_pct": m.Bias20Pct,
		"acceleration_risk": b2f(m.AccelerationRisk), "close_pos": m.ClosePos,
		"close": m.Close, "stop": m.Stop,
	}
}

// PowerBreakoutMetricsOf 对应 `_power_breakout_reentry_metrics`。
func PowerBreakoutMetricsOf(v *VectorBars) (PowerBreakoutMetrics, bool) {
	if v == nil || v.Len() < 45 {
		return PowerBreakoutMetrics{}, false
	}
	type candidate struct {
		idx     int
		date    string
		high    float64
		low     float64
		mid     float64
		close   float64
		volRate float64
		retPct  float64
	}
	var candidates []candidate
	recentStart := v.Len() - 8
	if recentStart < 20 {
		recentStart = 20
	}
	for idx := recentStart; idx < v.Len()-1; idx++ {
		bar := v.rawAt(idx)
		prevClose := v.lastFloatAt(v.PrevClose, idx, 0.0)
		avgVol := v.lastFloatAt(v.Prev20VolumeMean, idx, 0.0)
		prevHigh := v.lastFloatAt(v.Prev20High, idx, 0.0)
		if prevClose <= 0 || avgVol <= 0 || prevHigh <= 0 {
			continue
		}
		dayRet := (bar.Close/prevClose - 1.0) * 100.0
		closePos := v.lastFloatAt(v.ClosePos, idx, 0.5)
		bodyPct := 0.0
		if bar.Open > 0 {
			bodyPct = math.Abs(bar.Close/bar.Open-1.0) * 100.0
		}
		volRate := bar.Volume / avgVol
		if !(bar.Close > prevHigh*1.01 && dayRet >= 7.0 && volRate >= 2.0 &&
			closePos >= 0.78 && bodyPct >= 4.0) {
			continue
		}
		candidates = append(candidates, candidate{
			idx: idx, date: bar.Date, high: bar.High, low: bar.Low,
			mid: (bar.High + bar.Low) / 2.0, close: bar.Close,
			volRate: volRate, retPct: dayRet,
		})
	}
	if len(candidates) == 0 {
		return PowerBreakoutMetrics{}, false
	}
	breakout := candidates[len(candidates)-1]
	afterLen := v.Len() - (breakout.idx + 1)
	if afterLen < 3 || afterLen > 8 {
		return PowerBreakoutMetrics{}, false
	}
	// consolidation = bars[breakout+1:-1]
	consStart, consEnd := breakout.idx+1, v.Len()-1
	if consEnd-consStart < 2 {
		return PowerBreakoutMetrics{}, false
	}
	lowAfter := math.Inf(1)
	closeAfterMin := math.Inf(1)
	closeAfterMax := math.Inf(-1)
	consHigh := math.Inf(-1)
	for i := consStart; i < consEnd; i++ {
		if v.Low[i] < lowAfter {
			lowAfter = v.Low[i]
		}
		if v.Close[i] < closeAfterMin {
			closeAfterMin = v.Close[i]
		}
		if v.Close[i] > closeAfterMax {
			closeAfterMax = v.Close[i]
		}
		if v.High[i] > consHigh {
			consHigh = v.High[i]
		}
	}

	last := v.rawLast()
	closeP := last.Close
	lowP := last.Low
	volume := last.Volume
	ma20 := v.lastFloat(v.CloseMa20, closeP)
	close5 := v.lastFloat(v.Prev5Close, closeP)
	prev2Vol := v.lastFloat(v.Prev2VolumeMean, 0.0)
	prevHigh := v.lastFloat(v.PrevHigh, 0.0)
	closePos := v.lastFloat(v.ClosePos, 0.5)

	holdMidOK := lowAfter >= breakout.mid*0.985 && closeAfterMin >= breakout.mid
	pullback := 0.0
	if breakout.high > 0 {
		pullback = (breakout.high - lowAfter) / breakout.high * 100.0
	}
	ret5 := 0.0
	if close5 > 0 {
		ret5 = (closeP/close5 - 1.0) * 100.0
	}
	bias20 := 0.0
	if ma20 > 0 {
		bias20 = (closeP/ma20 - 1.0) * 100.0
	}
	consExt := 0.0
	if breakout.high > 0 {
		consExt = (consHigh/breakout.high - 1.0) * 100.0
	}
	turnStrong := closeP > math.Max(prevHigh, consHigh*1.003) &&
		prev2Vol > 0 && volume >= prev2Vol*1.05 && closePos >= 0.62
	accelRisk := ret5 >= 35.0 || bias20 >= 25.0 || consExt >= 18.0 ||
		(ret5 >= 25.0 && bias20 >= 18.0 && closeAfterMax > breakout.close*1.18)

	return PowerBreakoutMetrics{
		BreakoutDate: breakout.date, BreakoutHigh: breakout.high, BreakoutLow: breakout.low,
		BreakoutMid: breakout.mid, BreakoutVolume: breakout.volRate, BreakoutRetPct: breakout.retPct,
		DaysAfter: afterLen, ConsolidationHigh: consHigh, ConsolidationExt: consExt,
		HoldMidOK: holdMidOK, PullbackPct: pullback, TurnStrong: turnStrong,
		Ret5Pct: ret5, Bias20Pct: bias20, AccelerationRisk: accelRisk,
		ClosePos: closePos, Close: closeP,
		Stop: math.Max(breakout.high*0.99, lowP*0.985),
	}, true
}

// PowerBreakoutAccelerationText 对应 `_power_breakout_acceleration_text`。
func PowerBreakoutAccelerationText(m PowerBreakoutMetrics) string {
	if !m.AccelerationRisk {
		return "强突破后不是连续加速追高"
	}
	return "强突破后连续加速风险：5日涨幅" + formatFixed(m.Ret5Pct, 2) +
		"%、20日线乖离" + formatFixed(m.Bias20Pct, 2) +
		"%、突破后整理区扩张" + formatFixed(m.ConsolidationExt, 2) + "%"
}

// ---------------------------------------------------------------------------
// _early_maintrend_start_metrics
// ---------------------------------------------------------------------------

// EarlyMainTrendMetrics 对应 `_early_maintrend_start_metrics`。
type EarlyMainTrendMetrics struct {
	PlatformHigh20 float64
	PlatformLow20  float64
	PlatformHigh30 float64
	PlatformLow30  float64
	PlatformHeight float64
	Position60     float64
	VolumeRatio    float64
	DayRetPct      float64
	ClosePos       float64
	Ma20           float64
	Ma60           float64
	LowBaseOK      bool
	PlatformOK     bool
	TrendTurnOK    bool
	ConfirmBreak   bool
	WatchReady     bool
	ChaseRisk      bool
	Support        float64
	BreakoutPrice  float64
	Stop           float64
	Close          float64
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (m EarlyMainTrendMetrics) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"platform_high20": m.PlatformHigh20, "platform_low20": m.PlatformLow20,
		"platform_high30": m.PlatformHigh30, "platform_low30": m.PlatformLow30,
		"platform_height_pct": m.PlatformHeight, "position60_pct": m.Position60,
		"volume_ratio": m.VolumeRatio, "day_ret_pct": m.DayRetPct, "close_pos": m.ClosePos,
		"ma20": m.Ma20, "ma60": m.Ma60,
		"low_base_ok": b2f(m.LowBaseOK), "platform_ok": b2f(m.PlatformOK),
		"trend_turn_ok": b2f(m.TrendTurnOK), "confirm_breakout": b2f(m.ConfirmBreak),
		"watch_ready": b2f(m.WatchReady), "chase_risk": b2f(m.ChaseRisk),
		"support": m.Support, "breakout_price": m.BreakoutPrice, "stop": m.Stop, "close": m.Close,
	}
}

// EarlyMainTrendMetricsOf 对应 `_early_maintrend_start_metrics`。
func EarlyMainTrendMetricsOf(v *VectorBars) (EarlyMainTrendMetrics, bool) {
	if v == nil || v.Len() < 45 {
		return EarlyMainTrendMetrics{}, false
	}
	last := v.rawLast()
	closeP := last.Close
	high := last.High
	low := last.Low
	openP := last.Open
	volume := last.Volume
	if closeP <= 0 || high <= low {
		return EarlyMainTrendMetrics{}, false
	}
	platformHigh20 := v.lastFloat(v.Prev20High, 0.0)
	platformLow20 := v.lastFloat(v.Prev20Low, 0.0)
	platformHigh30 := v.lastFloat(v.Prev30High, 0.0)
	platformLow30 := v.lastFloat(v.Prev30Low, 0.0)
	avg20Vol := v.lastFloat(v.Prev20VolumeMean, 0.0)
	if platformHigh20 <= 0 || platformLow30 <= 0 || avg20Vol <= 0 {
		return EarlyMainTrendMetrics{}, false
	}
	ma20 := v.lastFloat(v.CloseMa20, 0.0)
	ma60 := v.lastFloat(v.CloseMa60, 0.0)
	prevClose := v.lastFloat(v.PrevClose, 0.0)
	closePos := v.lastFloat(v.ClosePos, 0.5)
	volumeRatio := volume / avg20Vol
	dayRet := 0.0
	if prevClose > 0 {
		dayRet = (closeP/prevClose - 1.0) * 100.0
	}
	platformHeight := (platformHigh30/platformLow30 - 1.0) * 100.0
	prior60High := v.lastFloat(v.Prev60High, 0.0)
	prior60Low := v.lastFloat(v.Prev60Low, 0.0)
	prevHigh := v.lastFloat(v.PrevHigh, 0.0)
	if minFloat(ma20, ma60, prior60High, prior60Low, prevHigh) <= 0 {
		return EarlyMainTrendMetrics{}, false
	}
	position60 := 50.0
	if prior60High > prior60Low {
		position60 = (platformHigh30 - prior60Low) / (prior60High - prior60Low) * 100.0
	}
	baseReturn := (platformHigh30/platformLow30 - 1.0) * 100.0
	preBreakHigh := math.Max(platformHigh20, platformHigh30)
	breakoutPrice := preBreakHigh * 1.003

	lowBaseOK := (position60 <= 88.0 || closeP <= ma60*1.28) && closeP <= ma60*1.35
	platformOK := platformHeight >= 6.0 && platformHeight <= 28.0 && baseReturn <= 32.0
	trendTurnOK := ma20 >= ma60*0.94 && closeP >= ma20*1.01
	confirmBreak := closeP >= breakoutPrice && closeP > prevHigh &&
		volumeRatio >= 1.8 && dayRet >= 3.0 && closePos >= 0.68
	watchReady := closeP >= preBreakHigh*0.985 && closePos >= 0.55 &&
		volumeRatio >= 0.95 && closeP >= ma20*0.985
	upperShadow := 0.0
	if math.Max(closeP, openP) > 0 {
		upperShadow = (high/math.Max(closeP, openP) - 1.0) * 100.0
	}
	chaseRisk := closeP > preBreakHigh*1.12 || closeP > ma20*1.22 ||
		(dayRet >= 8.5 && closePos < 0.72) || (upperShadow >= 5.0 && closePos < 0.58)
	support := math.Max(platformHigh20*0.985, ma20*0.965)
	stop := math.Max(math.Max(platformLow20, ma20*0.94), closeP*0.90)
	if stop >= closeP {
		stop = math.Min(low*0.985, closeP*0.94)
	}
	return EarlyMainTrendMetrics{
		PlatformHigh20: platformHigh20, PlatformLow20: platformLow20,
		PlatformHigh30: platformHigh30, PlatformLow30: platformLow30,
		PlatformHeight: platformHeight, Position60: position60,
		VolumeRatio: volumeRatio, DayRetPct: dayRet, ClosePos: closePos,
		Ma20: ma20, Ma60: ma60,
		LowBaseOK: lowBaseOK, PlatformOK: platformOK, TrendTurnOK: trendTurnOK,
		ConfirmBreak: confirmBreak, WatchReady: watchReady, ChaseRisk: chaseRisk,
		Support: support, BreakoutPrice: breakoutPrice, Stop: stop, Close: closeP,
	}, true
}

// ---------------------------------------------------------------------------
// _trend_platform_reentry_metrics
// ---------------------------------------------------------------------------

// TrendPlatformMetrics 对应 `_trend_platform_reentry_metrics`。
type TrendPlatformMetrics struct {
	PlatformHigh    float64
	PlatformLow     float64
	PlatformHeight  float64
	PlatformCloseLo float64
	DownDays        int
	VolumeDry       bool
	SupportHold     bool
	SetupOK         bool
	WatchReady      bool
	TriggerProbe    bool
	BreakoutTurn    bool
	BreakoutPrice   float64
	ChaseRisk       bool
	Close           float64
	Stop            float64
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (m TrendPlatformMetrics) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"platform_high": m.PlatformHigh, "platform_low": m.PlatformLow,
		"platform_height_pct": m.PlatformHeight, "platform_close_low": m.PlatformCloseLo,
		"down_days":  float64(m.DownDays),
		"volume_dry": b2f(m.VolumeDry), "support_hold": b2f(m.SupportHold),
		"setup_ok": b2f(m.SetupOK), "watch_ready": b2f(m.WatchReady),
		"trigger_probe": b2f(m.TriggerProbe), "breakout_turn": b2f(m.BreakoutTurn),
		"breakout_price": m.BreakoutPrice, "chase_risk": b2f(m.ChaseRisk),
		"close": m.Close, "stop": m.Stop,
	}
}

// TrendPlatformMetricsOf 对应 `_trend_platform_reentry_metrics`。
//
// 依赖 `TrendContinuationMetricsOf`（原实现第一行 `base = _trend_continuation_metrics(bars)`）。
func TrendPlatformMetricsOf(v *VectorBars) (TrendPlatformMetrics, bool) {
	base, ok := TrendContinuationMetricsOf(v)
	if !ok || v.Len() < 60 {
		return TrendPlatformMetrics{}, false
	}
	last := v.rawLast()
	closeP := last.Close
	volume := last.Volume
	platformHigh := v.lastFloat(v.Prev8HighMax, 0.0)
	platformLow := v.lastFloat(v.Prev8LowMin, 0.0)
	if platformHigh <= 0 || platformLow <= 0 || platformHigh <= platformLow {
		return TrendPlatformMetrics{}, false
	}
	prev3Vol := v.lastFloat(v.Prev3VolumeMean, volume)
	prev20Vol := v.lastFloat(v.Prev20VolumeMean, prev3Vol)
	platformVol := v.lastFloat(v.Prev8VolumeMean, prev3Vol)
	platformCloseLow := v.lastFloat(v.Prev8CloseMin, closeP)
	platformHeight := (platformHigh/platformLow - 1.0) * 100.0
	downDays := v.lastInt(v.Prev8DownCloseCnt, 0)
	supportHold := platformLow >= base.Ma20*0.94 && platformCloseLow >= base.Ma20*0.955
	volumeDry := prev20Vol > 0 && platformVol <= prev20Vol*1.08
	setupOK := platformHeight >= 4.0 && platformHeight <= 18.0 && supportHold &&
		(downDays >= 2 || volumeDry)
	watchReady := closeP >= platformHigh*0.97 && base.ClosePos >= 0.55
	prevHigh := v.lastFloat(v.PrevHigh, 0.0)
	prevClose := v.lastFloat(v.PrevClose, 0.0)
	breakoutTurn := setupOK && closeP > platformHigh*1.003 && closeP > prevHigh &&
		prev3Vol > 0 && volume >= prev3Vol*1.05 && base.ClosePos >= 0.62
	triggerProbe := watchReady && closeP >= prevClose && prev3Vol > 0 &&
		volume >= prev3Vol*0.85 && base.ClosePos >= 0.58
	chaseRisk := closeP > platformHigh*1.08 ||
		(closeP > platformHigh*1.045 && base.Bias20Pct >= 18.0 &&
			base.Ret5Pct >= 9.0 && base.Pos20Pct >= 92.0)
	return TrendPlatformMetrics{
		PlatformHigh: platformHigh, PlatformLow: platformLow,
		PlatformHeight: platformHeight, PlatformCloseLo: platformCloseLow,
		DownDays: downDays, VolumeDry: volumeDry, SupportHold: supportHold,
		SetupOK: setupOK, WatchReady: watchReady, TriggerProbe: triggerProbe,
		BreakoutTurn: breakoutTurn, BreakoutPrice: platformHigh * 1.003,
		ChaseRisk: chaseRisk, Close: closeP,
		Stop: math.Max(math.Min(base.Low10, base.Ma10*0.98), closeP*0.90),
	}, true
}

// lastFloatAt 取第 i 行的派生列（带默认值），供多行扫描使用。
func (v *VectorBars) lastFloatAt(s Series, i int, def float64) float64 {
	if v == nil || i < 0 || i >= len(s) {
		return def
	}
	return floatingOrDefault(s[i], def)
}

func b2f(v bool) float64 {
	if v {
		return 1.0
	}
	return 0.0
}

func minFloat(values ...float64) float64 {
	out := values[0]
	for _, value := range values[1:] {
		if value < out {
			out = value
		}
	}
	return out
}

// formatFixed 复刻 Python 的 `f"{v:.2f}"`。
func formatFixed(v float64, digits int) string {
	return strconv.FormatFloat(v, 'f', digits, 64)
}
