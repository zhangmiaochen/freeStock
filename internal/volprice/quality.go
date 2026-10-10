package volprice

import (
	"math"
	"strconv"
	"strings"
)

// 对应 `volume_price_models.py` 的"行为质量 / 箱体结构 / 超跌修复"三组辅助判定。
//
// ⚠ 原实现里 `_box_structure_quality`（非 fast 版）**从未被调用** ——
// 只有 `_box_structure_quality_fast` 进了链路（确认箱体 :2170、候选箱体 :2457）。
// 所以这里只移植 fast 版；两版之间有一处 1 个单位的阈值差异
//（`overhead_supply_risk` 的 `>= len-10` vs `>= len-9`），
// 移植未被调用的那一版只会引入无法验证的分支。

// BehaviorQuality 对应 `_behavior_quality` 返回的字典。
type BehaviorQuality struct {
	VolumeBehavior         string
	VolumeBehaviorLabel    string
	AcceptanceQuality      string
	AcceptanceQualityLabel string
	UpperShadowBehavior    string
	UpperShadowBehaviorLbl string
	Score                  float64
	DeathVolumeRisk        bool
	// Extras 承载"被合并进 behavior_quality 的额外数值键"。
	//
	// 原实现里箱体/超跌两条路径会做
	// `signal["behavior_quality"].update({k: v for k, v in box_quality.items() if k != "block_reasons"})`
	// —— 于是 behavior_quality 里会多出 box_quality_score / support_touch_count 等键。
	// 那些键会落库，所以显式保留而不是丢掉。
	Extras map[string]float64
}

// BehaviorQualityOf 对应 `_behavior_quality`（传入 `VolumePriceFeature` 的那条路径）。
//
// 原实现还有一条"传 signal dict 时从 `behavior_quality` 键取"的路径；
// Go 侧所有调用点都持有 `Feature`，所以只保留结构体这条 —— 行为等价。
func BehaviorQualityOf(f Feature) BehaviorQuality {
	return BehaviorQuality{
		VolumeBehavior:         f.VolumeBehavior,
		VolumeBehaviorLabel:    f.VolumeBehaviorLabel,
		AcceptanceQuality:      f.AcceptanceQuality,
		AcceptanceQualityLabel: f.AcceptanceQualityLabel,
		UpperShadowBehavior:    f.UpperShadowBehavior,
		UpperShadowBehaviorLbl: f.UpperShadowBehaviorLbl,
		// 原实现这里 round(..., 2)
		Score:           round2(f.BehaviorQualityScore),
		DeathVolumeRisk: f.DeathVolumeRisk,
	}
}

// BehaviorConditionText 对应 `_behavior_condition_text`。
func BehaviorConditionText(f Feature) string {
	quality := BehaviorQualityOf(f)
	volumeLabel := quality.VolumeBehaviorLabel
	if volumeLabel == "" {
		volumeLabel = "未知量能"
	}
	acceptanceLabel := quality.AcceptanceQualityLabel
	if acceptanceLabel == "" {
		acceptanceLabel = "未知承接"
	}
	return "行为质量：" + volumeLabel + " / " + acceptanceLabel + "（" +
		pyFixed0(quality.Score) + "分）"
}

// BehaviorQualityOK 对应 `_behavior_quality_ok`。
func BehaviorQualityOK(f Feature, minScore float64) bool {
	quality := BehaviorQualityOf(f)
	return behaviorQualityOK(quality, minScore)
}

func behaviorQualityOK(quality BehaviorQuality, minScore float64) bool {
	if quality.DeathVolumeRisk {
		return false
	}
	if quality.AcceptanceQuality == "no_acceptance" {
		return false
	}
	return quality.Score >= minScore
}

// BehaviorTradeBlockReasons 对应 `_behavior_trade_block_reasons`。
func BehaviorTradeBlockReasons(f Feature, minScore float64) []string {
	return behaviorTradeBlockReasons(BehaviorQualityOf(f), minScore)
}

func behaviorTradeBlockReasons(quality BehaviorQuality, minScore float64) []string {
	var reasons []string
	if quality.DeathVolumeRisk {
		reasons = append(reasons, "存在衰竭缩量/死亡缩量风险")
	}
	if quality.AcceptanceQuality == "no_acceptance" {
		reasons = append(reasons, "承接质量为无承接")
	}
	if quality.Score < minScore {
		reasons = append(reasons, "行为质量分低于"+pyFixed0(minScore))
	}
	return reasons
}

// CandidateBlockOptions 对应 `_candidate_trade_block_reasons` 的关键字参数。
type CandidateBlockOptions struct {
	MinScore                float64
	ConfirmationHits        []string
	ConfirmationMisses      []string
	HardReasons             []string
	SoftReasons             []string
	MinProgress             float64
	MaxSoftMisses           int
	RequireStrongAcceptance bool
}

// DefaultCandidateBlockOptions 给出原实现的默认值。
func DefaultCandidateBlockOptions(minScore float64) CandidateBlockOptions {
	return CandidateBlockOptions{
		MinScore:                minScore,
		MinProgress:             85.0,
		MaxSoftMisses:           1,
		RequireStrongAcceptance: true,
	}
}

// CandidateTradeBlockReasons 对应 `_candidate_trade_block_reasons`。
func CandidateTradeBlockReasons(f Feature, opts CandidateBlockOptions) []string {
	quality := BehaviorQualityOf(f)
	hard := behaviorTradeBlockReasons(quality, opts.MinScore)
	for _, item := range opts.HardReasons {
		if text := strings.TrimSpace(item); text != "" {
			hard = append(hard, text)
		}
	}
	soft := make([]string, 0, len(opts.SoftReasons))
	for _, item := range opts.SoftReasons {
		if text := strings.TrimSpace(item); text != "" {
			soft = append(soft, text)
		}
	}
	if len(hard) > 0 {
		return append(hard, soft...)
	}
	if len(soft) == 0 {
		return nil
	}
	total := len(opts.ConfirmationHits) + len(opts.ConfirmationMisses)
	progress := 0.0
	if total > 0 {
		progress = float64(len(opts.ConfirmationHits)) / float64(total) * 100.0
	}
	if progress < opts.MinProgress {
		return append([]string{"距确认低于" + pyFixed0(opts.MinProgress) + "%"}, soft...)
	}
	if len(soft) > opts.MaxSoftMisses {
		return soft
	}
	if opts.RequireStrongAcceptance && quality.AcceptanceQuality != "strong_acceptance" {
		return append([]string{"候选未完全确认且承接未达到强承接"}, soft...)
	}
	return nil
}

// BehaviorScoreAdjust 对应 `_behavior_score_adjust`。
func BehaviorScoreAdjust(f Feature) float64 {
	return behaviorScoreAdjust(BehaviorQualityOf(f))
}

func behaviorScoreAdjust(quality BehaviorQuality) float64 {
	score := quality.Score
	if score == 0 {
		// 原实现 `float(quality.get("score") or 50.0)`：0 或缺失都算 50
		score = 50.0
	}
	adj := math.Max(-12.0, math.Min(10.0, (score-50.0)*0.18))
	if quality.DeathVolumeRisk {
		adj -= 8.0
	}
	return adj
}

// ---------------------------------------------------------------------------
// 箱体
// ---------------------------------------------------------------------------

// BoxWindowMetrics 对应 `_box_window_metrics` 的返回字典。
type BoxWindowMetrics struct {
	Support            float64
	Resistance         float64
	HeightPct          float64
	SupportTouchCount  int
	ResistanceTouchCnt int
	SupportBreakRisk   bool
	DowntrendChannel   bool
	OverheadSupplyRisk bool
	PrevClose          float64
	VolumeSoft         bool
	VolumeDecay        bool
	NearSupportWatch   bool
	NearSupportConfirm bool
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (m BoxWindowMetrics) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"support": m.Support, "resistance": m.Resistance, "height_pct": m.HeightPct,
		"support_touch_count":    float64(m.SupportTouchCount),
		"resistance_touch_count": float64(m.ResistanceTouchCnt),
		"support_break_risk":     b2f(m.SupportBreakRisk),
		"downtrend_channel_risk": b2f(m.DowntrendChannel),
		"overhead_supply_risk":   b2f(m.OverheadSupplyRisk),
		"prev_close":             m.PrevClose,
		"volume_soft":            b2f(m.VolumeSoft),
		"volume_decay":           b2f(m.VolumeDecay),
		"near_support_watch":     b2f(m.NearSupportWatch),
		"near_support_confirm":   b2f(m.NearSupportConfirm),
	}
}

// BoxWindowMetricsOf 对应 `_box_window_metrics`。
func BoxWindowMetricsOf(v *VectorBars) (BoxWindowMetrics, bool) {
	if v == nil || v.Len() < 35 {
		return BoxWindowMetrics{}, false
	}
	n := v.Len()
	lastIdx := n - 1

	support := v.lastFloat(v.Prev30Low, 0.0)
	resistance := v.lastFloat(v.Prev30High, 0.0)
	if support <= 0 || resistance <= support {
		return BoxWindowMetrics{}, false
	}

	// window = [n-31 : n-1]（30 根，不含最后一根）
	winStart, winEnd := n-31, n-1
	if winStart < 0 {
		winStart = 0
	}
	if winEnd-winStart < 20 {
		return BoxWindowMetrics{}, false
	}
	// established_low = [n-31 : n-4]
	estStart, estEnd := n-31, n-4
	if estStart < 0 {
		estStart = 0
	}
	// recent_close = [n-8 : n]
	recStart := n - 8
	if recStart < 0 {
		recStart = 0
	}

	lastClose := v.Close[lastIdx]
	lastLow := v.Low[lastIdx]
	prevClose := v.lastFloat(v.PrevClose, lastClose)
	prev10Close := v.lastFloat(v.Prev10Close, lastClose)
	prev20Close := v.lastFloat(v.Prev20Close, lastClose)
	prev3VolumeMean := v.lastFloat(v.Prev3VolumeMean, 0.0)
	currentVolume := v.Volume[lastIdx]

	establishedSupport := support
	if estEnd-estStart >= 12 {
		establishedSupport = math.Inf(1)
		for i := estStart; i < estEnd; i++ {
			if v.Low[i] < establishedSupport {
				establishedSupport = v.Low[i]
			}
		}
	}

	heightPct := (resistance/support - 1.0) * 100.0
	supportTouch, resistanceTouch := 0, 0
	for i := winStart; i < winEnd; i++ {
		if v.Low[i] <= support*1.035 && v.Close[i] >= support*0.985 {
			supportTouch++
		}
		if v.High[i] >= resistance*0.965 {
			resistanceTouch++
		}
	}
	recentBelow := 0
	for i := recStart; i < n; i++ {
		if v.Close[i] < establishedSupport*0.985 {
			recentBelow++
		}
	}
	supportBreakRisk := recentBelow >= 2 ||
		lastLow < establishedSupport*0.965 ||
		(support < establishedSupport*0.98 && lastClose < establishedSupport*0.995)
	downtrendChannel := lastClose < prev10Close*0.97 && prev10Close < prev20Close*0.97

	avg30Volume := 0.0
	if winEnd-winStart >= 10 {
		values := make([]float64, 0, winEnd-winStart)
		for i := winStart; i < winEnd; i++ {
			values = append(values, v.Volume[i])
		}
		avg30Volume = meanNonNaN(values)
	}
	overheadSupplyRisk := false
	if avg30Volume > 0 {
		lastHit := -1
		for i := winStart; i < winEnd; i++ {
			if v.High[i] >= resistance*0.93 && v.Volume[i] >= avg30Volume*1.8 {
				lastHit = i - winStart // numpy 的 nonzero 是窗口内位置
			}
		}
		overheadSupplyRisk = lastHit >= 0 && lastHit >= (winEnd-winStart)-10
	}

	return BoxWindowMetrics{
		Support: support, Resistance: resistance, HeightPct: heightPct,
		SupportTouchCount: supportTouch, ResistanceTouchCnt: resistanceTouch,
		SupportBreakRisk: supportBreakRisk, DowntrendChannel: downtrendChannel,
		OverheadSupplyRisk: overheadSupplyRisk, PrevClose: prevClose,
		VolumeSoft:         prev3VolumeMean > 0 && currentVolume <= prev3VolumeMean*0.9,
		VolumeDecay:        v.lastFloat(v.VolumeDecayScore, 0.0) >= 100.0,
		NearSupportWatch:   lastClose >= support*0.96 && lastClose <= support*1.06,
		NearSupportConfirm: lastClose <= support*1.03,
	}, true
}

// BoxStructureQuality 对应 `_box_structure_quality_fast` 的返回字典。
type BoxStructureQuality struct {
	Score              float64
	SupportTouchCount  int
	ResistanceTouchCnt int
	SupportBreakRisk   bool
	DowntrendChannel   bool
	OverheadSupplyRisk bool
	Tradeable          bool
	Watchable          bool
	BlockReasons       []string
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (q BoxStructureQuality) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"box_quality_score":      q.Score,
		"support_touch_count":    float64(q.SupportTouchCount),
		"resistance_touch_count": float64(q.ResistanceTouchCnt),
		"support_break_risk":     b2f(q.SupportBreakRisk),
		"downtrend_channel_risk": b2f(q.DowntrendChannel),
		"overhead_supply_risk":   b2f(q.OverheadSupplyRisk),
		"tradeable":              b2f(q.Tradeable),
		"watchable":              b2f(q.Watchable),
	}
}

// BlockReasonsText 把原因列表拼成一段文本，供黄金值比对。
func (q BoxStructureQuality) BlockReasonsText() string {
	return strings.Join(q.BlockReasons, "|")
}

// BoxStructureQualityOf 对应 `_box_structure_quality_fast`。
func BoxStructureQualityOf(v *VectorBars, f Feature) BoxStructureQuality {
	metrics, ok := BoxWindowMetricsOf(v)
	if !ok {
		return BoxStructureQuality{
			SupportBreakRisk: true, DowntrendChannel: true, OverheadSupplyRisk: true,
			BlockReasons: []string{"箱体结构样本不足"},
		}
	}
	quality := BehaviorQualityOf(f)
	acceptance := quality.AcceptanceQuality
	volumeBehavior := quality.VolumeBehavior

	score := 35.0
	score += math.Min(20.0, float64(metrics.SupportTouchCount)*5.0)
	score += math.Min(16.0, float64(metrics.ResistanceTouchCnt)*4.0)
	if metrics.HeightPct >= 8.0 && metrics.HeightPct <= 25.0 {
		score += 12.0
	} else {
		score -= 10.0
	}
	switch {
	case acceptance == "strong_acceptance":
		score += 10.0
	case acceptance == "weak_acceptance" || acceptance == "no_acceptance":
		score -= 8.0
	}
	if volumeBehavior == "strong_decay" || volumeBehavior == "neutral_decay" {
		score += 8.0
	} else if quality.DeathVolumeRisk {
		score -= 6.0
	}
	if metrics.SupportBreakRisk {
		score -= 18.0
	}
	if metrics.DowntrendChannel {
		score -= 16.0
	}
	if metrics.OverheadSupplyRisk {
		score -= 10.0
	}

	var reasons []string
	if metrics.SupportTouchCount < 2 {
		reasons = append(reasons, "箱体下沿验证次数不足")
	}
	if metrics.ResistanceTouchCnt < 1 {
		reasons = append(reasons, "箱体上沿压力验证不足")
	}
	if metrics.SupportBreakRisk {
		reasons = append(reasons, "近期存在箱体下沿破位风险")
	}
	if metrics.DowntrendChannel {
		reasons = append(reasons, "价格仍处在下降通道中")
	}
	if metrics.OverheadSupplyRisk {
		reasons = append(reasons, "上方近期放量套牢抛压较重")
	}
	if acceptance != "strong_acceptance" {
		reasons = append(reasons, "下沿承接未达到强承接")
	}
	if quality.DeathVolumeRisk {
		reasons = append(reasons, "存在死亡缩量风险")
	}
	finalScore := round2(math.Max(0.0, math.Min(100.0, score)))
	return BoxStructureQuality{
		Score: finalScore, SupportTouchCount: metrics.SupportTouchCount,
		ResistanceTouchCnt: metrics.ResistanceTouchCnt,
		SupportBreakRisk:   metrics.SupportBreakRisk, DowntrendChannel: metrics.DowntrendChannel,
		OverheadSupplyRisk: metrics.OverheadSupplyRisk,
		Tradeable:          finalScore >= 72.0 && len(reasons) == 0,
		Watchable:          finalScore >= 50.0 && metrics.SupportTouchCount >= 1,
		BlockReasons:       reasons,
	}
}

// ---------------------------------------------------------------------------
// 超跌反抽
// ---------------------------------------------------------------------------

// OversoldReboundQuality 对应 `_oversold_rebound_quality` 的返回字典。
type OversoldReboundQuality struct {
	PanicReleaseScore float64
	ReboundScore      float64
	DowntrendContinue bool
	GapChaseRisk      bool
	FailedReversal    bool
	Tradeable         bool
	Watchable         bool
	BlockReasons      []string
}

// AsFloatMap 见 TrendContinuationMetrics.AsFloatMap 的说明。
func (q OversoldReboundQuality) AsFloatMap() map[string]float64 {
	return map[string]float64{
		"panic_release_score":         q.PanicReleaseScore,
		"rebound_quality_score":       q.ReboundScore,
		"downtrend_continuation_risk": b2f(q.DowntrendContinue),
		"gap_chase_risk":              b2f(q.GapChaseRisk),
		"failed_reversal_risk":        b2f(q.FailedReversal),
		"tradeable":                   b2f(q.Tradeable),
		"watchable":                   b2f(q.Watchable),
	}
}

// BlockReasonsText 把原因列表拼成一段文本，供黄金值比对。
func (q OversoldReboundQuality) BlockReasonsText() string {
	return strings.Join(q.BlockReasons, "|")
}

// OversoldReboundQualityOf 对应 `_oversold_rebound_quality`。
func OversoldReboundQualityOf(
	v *VectorBars, f Feature, fall5 float64, panic, rebound bool,
) OversoldReboundQuality {
	if v == nil || v.Len() < 25 {
		return OversoldReboundQuality{
			DowntrendContinue: true, GapChaseRisk: true, FailedReversal: true,
			BlockReasons: []string{"超跌结构样本不足"},
		}
	}
	n := v.Len()
	lastClose := v.Close[n-1]
	lastOpen := v.Open[n-1]
	high := v.High[n-1]
	low := v.Low[n-1]
	close10 := lastClose
	if n >= 11 {
		close10 = v.Close[n-11]
	}
	close20 := lastClose
	if n >= 21 {
		close20 = v.Close[n-21]
	}
	prevClose := v.Close[n-2]

	quality := BehaviorQualityOf(f)
	acceptance := quality.AcceptanceQuality

	panicScore := 35.0 + math.Min(35.0, math.Abs(math.Min(fall5, 0.0))*1.2)
	if panic {
		panicScore += 20.0
	} else {
		panicScore -= 12.0
	}
	lowerShadow := 0.0
	if f.LowerShadowRatio != nil {
		lowerShadow = *f.LowerShadowRatio
	}
	if f.ReversalFlag || lowerShadow >= 2.0 {
		panicScore += 10.0
	}

	reboundScore := 35.0
	if rebound {
		reboundScore += 25.0
	} else {
		reboundScore -= 15.0
	}
	mid := (high + low) / 2.0
	if lastClose >= mid {
		reboundScore += 20.0
	} else {
		reboundScore -= 8.0
	}
	if acceptance == "strong_acceptance" {
		reboundScore += 15.0
	} else {
		reboundScore -= 10.0
	}

	downtrendContinue := lastClose < close10*0.96 && close10 < close20*0.96
	gapChase := lastOpen > prevClose*1.06 && lastClose < lastOpen*1.02
	failedReversal := lastClose < mid || lastClose <= lastOpen
	if downtrendContinue {
		reboundScore -= 15.0
	}
	if gapChase {
		reboundScore -= 18.0
	}
	if failedReversal {
		reboundScore -= 14.0
	}

	var reasons []string
	if panicScore < 75.0 {
		reasons = append(reasons, "恐慌释放完成度不足")
	}
	if reboundScore < 70.0 {
		reasons = append(reasons, "首次修复质量不足")
	}
	if downtrendContinue {
		reasons = append(reasons, "仍有下跌中继风险")
	}
	if gapChase {
		reasons = append(reasons, "高开追买风险过高")
	}
	if failedReversal {
		reasons = append(reasons, "反包修复失败")
	}
	if acceptance != "strong_acceptance" {
		reasons = append(reasons, "止跌承接未达到强承接")
	}
	panicScore = round2(math.Max(0.0, math.Min(100.0, panicScore)))
	reboundScore = round2(math.Max(0.0, math.Min(100.0, reboundScore)))
	return OversoldReboundQuality{
		PanicReleaseScore: panicScore, ReboundScore: reboundScore,
		DowntrendContinue: downtrendContinue, GapChaseRisk: gapChase,
		FailedReversal: failedReversal,
		Tradeable:      panicScore >= 75.0 && reboundScore >= 70.0 && len(reasons) == 0,
		Watchable:      panicScore >= 60.0 && reboundScore >= 55.0,
		BlockReasons:   reasons,
	}
}

// pyFixed0 复刻 Python 的 `f"{v:.0f}"`（strconv 与 Python 同为 round-half-even）。
func pyFixed0(v float64) string {
	if isNaN(v) {
		return "nan"
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}
