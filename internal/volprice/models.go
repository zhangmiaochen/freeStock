package volprice

import (
	"math"
	"strings"
)

// 对应 `volume_price_models.py` 顶部的模型定义、别名、以及信号构造。
//
// 只有 `MODEL_DEFINITIONS` 里列出的三个模型会走到 `_evaluate_confirmed_prepared`
// / `_evaluate_candidate_prepared` 的分派；`_state_model_priority` 也只返回这三个。
// 文件里另外几族（power_breakout / early_maintrend / platform / continuation 的
// `_eval_*`/`_candidate_*`）**从未被任何分派调用**，所以不移植。

// 模型 ID。
const (
	ModelTrendPullback   = "trend_pullback"
	ModelBoxRange        = "box_range"
	ModelOversoldRebound = "oversold_rebound"
)

// ModelDefinition 对应 `MODEL_DEFINITIONS` 的一项。
type ModelDefinition struct {
	ID               string
	Name             string
	MarketState      string
	MarketStateLabel string
	Description      string
}

// ModelDefinitions 对应 `MODEL_DEFINITIONS`。
var ModelDefinitions = []ModelDefinition{
	{
		ID: ModelTrendPullback, Name: "趋势缩量回踩",
		MarketState: "bull", MarketStateLabel: MarketStateLabels["bull"],
		Description: "放量突破后缩量回踩关键位，再次放量转强。",
	},
	{
		ID: ModelBoxRange, Name: "箱体低吸高抛",
		MarketState: "range", MarketStateLabel: MarketStateLabels["range"],
		Description: "震荡箱体下沿缩量承接，接近压力位兑现。",
	},
	{
		ID: ModelOversoldRebound, Name: "超跌反抽",
		MarketState: "bear", MarketStateLabel: MarketStateLabels["bear"],
		Description: "连续急跌恐慌释放后，捕捉首次情绪修复。",
	},
}

var modelAliases = map[string]string{
	"trend": ModelTrendPullback, "trend_pullback": ModelTrendPullback,
	"趋势缩量回踩": ModelTrendPullback,
	"box":    ModelBoxRange, "box_low": ModelBoxRange, "box_range": ModelBoxRange,
	"箱体低吸高抛":   ModelBoxRange,
	"oversold": ModelOversoldRebound, "oversold_rebound": ModelOversoldRebound,
	"超跌反抽": ModelOversoldRebound,
}

// NormalizeModelID 对应 `normalize_model_id`。
//
// 未知输入一律回落到 trend_pullback（原实现如此）。
func NormalizeModelID(modelID string) string {
	s := strings.TrimSpace(modelID)
	if mapped, ok := modelAliases[s]; ok {
		return mapped
	}
	if mapped, ok := modelAliases[strings.ToLower(s)]; ok {
		return mapped
	}
	return ModelTrendPullback
}

// ModelDefOf 取模型定义（找不到返回零值）。
func ModelDefOf(modelID string) ModelDefinition {
	model := NormalizeModelID(modelID)
	for _, item := range ModelDefinitions {
		if item.ID == model {
			return item
		}
	}
	return ModelDefinition{}
}

// RequiredStateForModel 对应 `_required_state_for_model`。
func RequiredStateForModel(modelID string) string {
	switch NormalizeModelID(modelID) {
	case ModelTrendPullback:
		return "bull"
	case ModelBoxRange:
		return "range"
	case ModelOversoldRebound:
		return "bear"
	}
	return ""
}

// ModelEvalWindow 对应 `_model_eval_window`。
func ModelEvalWindow(modelID string) int {
	switch NormalizeModelID(modelID) {
	case ModelTrendPullback:
		return 120
	case ModelBoxRange:
		return 60
	case ModelOversoldRebound:
		return 45
	}
	return 120
}

// StateModelPriority 对应 `_state_model_priority`。
func StateModelPriority(state string) []string {
	switch NormalizeMarketState(state, "range") {
	case "bull":
		return []string{ModelTrendPullback, ModelBoxRange, ModelOversoldRebound}
	case "bear":
		return []string{ModelOversoldRebound, ModelBoxRange, ModelTrendPullback}
	}
	return []string{ModelBoxRange, ModelTrendPullback, ModelOversoldRebound}
}

// Signal 对应原实现里一路传递的信号字典。
//
// 价格用 `*float64` 表示 Python 的 `None`（对应 `_round` 对 None/NaN 返回 None）。
type Signal struct {
	ModelID          string
	ModelName        string
	MarketState      string
	MarketStateLabel string
	SignalType       string // buy / candidate_buy / sell
	SignalDate       string

	EntryPrice  *float64
	StopPrice   *float64
	TargetPrice *float64
	Score       float64
	RiskLevel   string

	ConditionHits   []string
	ConditionMisses []string
	Explanation     string
	Feature         Feature

	SignalLevel      string
	SignalLevelLabel string

	// 候选专属
	HitCount          int
	TotalConditions   int
	HitRatio          float64
	ConfirmationHits  []string
	ConfirmationMiss  []string
	ConfirmHitCount   int
	ConfirmTotal      int
	ConfirmProgress   float64
	LooseEntryScore   float64
	MissingCore       []string
	LooseEntryAllowed bool
	LooseBlockReasons []string

	BehaviorQuality BehaviorQuality

	SignalSubtype      string
	SignalSubtypeLabel string

	// 原实现会往 `signal["feature"][...]` 里塞子度量，并往
	// `signal["behavior_quality"]` 里合并箱体/超跌的数值字段。
	// 这些都是**落库内容的一部分**，所以显式建模而不是丢掉。
	QualityBlockReasons   []string
	CandidateTriggerPrice *float64
	EarlyMainTrend        *EarlyMainTrendMetrics
	TrendPlatform         *TrendPlatformMetrics
	BoxQuality            *BoxStructureQuality
	OversoldQuality       *OversoldReboundQuality

	// 回测循环写入的"执行态"字段（原实现直接塞进信号字典）。
	EntrySignalSource           string
	LooseEntryThreshold         float64
	CandidateExpireBars         int
	CandidateOrderStatus        string
	CandidateSignalIndex        int
	ExecutionMode               string
	CandidateTriggerDate        string
	CandidateTriggerClose       *float64
	CandidateTriggerVolumeRatio *float64
}

// BaseSignal 对应 `_base_signal`。
func BaseSignal(modelID, marketState, signalType, signalDate string) Signal {
	def := ModelDefOf(modelID)
	name := def.Name
	if name == "" {
		name = NormalizeModelID(modelID)
	}
	return Signal{
		ModelID: NormalizeModelID(modelID), ModelName: name,
		MarketState: marketState, MarketStateLabel: stateLabelOf(marketState),
		SignalType: signalType, SignalDate: signalDate,
		Score: 0.0, RiskLevel: "medium",
		ConditionHits: []string{}, ConditionMisses: []string{},
		SignalLevel: "confirmed", SignalLevelLabel: "确认",
	}
}

func stateLabelOf(state string) string {
	if label, ok := MarketStateLabels[state]; ok {
		return label
	}
	return state
}

// RiskLevel 对应 `_risk_level`。
func RiskLevel(entry, stop float64) string {
	if entry <= 0 || stop <= 0 {
		return "medium"
	}
	riskPct := (entry - stop) / entry * 100.0
	if riskPct <= 4.0 {
		return "low"
	}
	if riskPct <= 8.0 {
		return "medium"
	}
	return "high"
}

// ConditionPair 是 `(是否命中, 文案)`。
type ConditionPair struct {
	OK   bool
	Text string
}

// ConditionLists 对应 `_condition_lists`。
func ConditionLists(pairs []ConditionPair) ([]string, []string) {
	hits := make([]string, 0, len(pairs))
	misses := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair.OK {
			hits = append(hits, pair.Text)
		} else {
			misses = append(misses, pair.Text)
		}
	}
	return hits, misses
}

// CandidateSignalOptions 对应 `_candidate_signal` 的关键字参数。
type CandidateSignalOptions struct {
	EntryPrice             *float64
	StopPrice              *float64
	TargetPrice            *float64
	Score                  float64
	Hits                   []string
	Misses                 []string
	Explanation            string
	LooseEntryAllowed      bool
	LooseEntryBlockReasons []string
	MissingCore            []string
	ConfirmationHits       []string
	ConfirmationMisses     []string
	SignalSubtype          string
	SignalSubtypeLabel     string
}

// BuildCandidateSignal 对应 `_candidate_signal`。
func BuildCandidateSignal(
	modelID, marketState, signalDate string, feature Feature, opts CandidateSignalOptions,
) Signal {
	signal := BaseSignal(modelID, marketState, "candidate_buy", signalDate)
	hitCount := len(opts.Hits)
	total := hitCount + len(opts.Misses)
	confirmHits := append([]string(nil), opts.ConfirmationHits...)
	confirmMisses := append([]string(nil), opts.ConfirmationMisses...)
	confirmTotal := len(confirmHits) + len(confirmMisses)

	signal.EntryPrice = roundPtr(opts.EntryPrice)
	signal.StopPrice = roundPtr(opts.StopPrice)
	signal.TargetPrice = roundPtr(opts.TargetPrice)
	signal.Score = round2(math.Max(0.0, math.Min(89.0, opts.Score)))
	signal.RiskLevel = RiskLevel(derefFloat(opts.EntryPrice), derefFloat(opts.StopPrice))
	signal.ConditionHits = opts.Hits
	signal.ConditionMisses = opts.Misses
	signal.Explanation = opts.Explanation
	signal.Feature = feature
	signal.BehaviorQuality = BehaviorQualityOf(feature)
	signal.SignalLevel = "candidate"
	signal.SignalLevelLabel = "候选"
	signal.SignalSubtype = opts.SignalSubtype
	signal.SignalSubtypeLabel = opts.SignalSubtypeLabel
	signal.HitCount = hitCount
	signal.TotalConditions = total
	if total > 0 {
		signal.HitRatio = round2(float64(hitCount) / float64(total) * 100.0)
	}
	signal.ConfirmationHits = confirmHits
	signal.ConfirmationMiss = confirmMisses
	signal.ConfirmHitCount = len(confirmHits)
	signal.ConfirmTotal = confirmTotal
	if confirmTotal > 0 {
		signal.ConfirmProgress = round2(float64(len(confirmHits)) / float64(confirmTotal) * 100.0)
	}
	signal.LooseEntryScore = CandidateLooseEntryScore(
		signal.Score, signal.ConfirmProgress, signal.BehaviorQuality,
		derefFloat(opts.EntryPrice), derefFloat(opts.StopPrice))
	signal.MissingCore = append([]string(nil), opts.MissingCore...)
	if signal.MissingCore == nil {
		signal.MissingCore = append([]string(nil), confirmMisses...)
	}
	reasons := make([]string, 0, len(opts.LooseEntryBlockReasons))
	for _, item := range opts.LooseEntryBlockReasons {
		if text := strings.TrimSpace(item); text != "" {
			reasons = append(reasons, text)
		}
	}
	signal.LooseEntryAllowed = opts.LooseEntryAllowed && len(reasons) == 0
	signal.LooseBlockReasons = reasons
	return signal
}

// CandidateLooseEntryScore 对应 `_candidate_loose_entry_score`。
func CandidateLooseEntryScore(
	score, confirmationProgress float64, quality BehaviorQuality,
	entryPrice, stopPrice float64,
) float64 {
	progress := round4OrZero(confirmationProgress)
	modelScore := round4OrZero(score)
	behaviorScore := quality.Score
	if behaviorScore == 0 {
		// 原实现 `float(quality.get("score") or 50.0)`
		behaviorScore = 50.0
	}
	strength := progress*0.72 + behaviorScore*0.18 + modelScore*0.10
	riskPct := 0.0
	if entryPrice > 0 && stopPrice > 0 {
		riskPct = (entryPrice - stopPrice) / entryPrice * 100.0
	}
	if riskPct > 8.0 {
		strength -= (riskPct - 8.0) * 1.5
	}
	if riskPct > 12.0 {
		strength -= riskPct - 12.0
	}
	return round2(math.Max(0.0, math.Min(100.0, strength)))
}

// AttachBehaviorQuality 对应 `_attach_behavior_quality`。
//
// 原实现把 quality 合并进 `signal["behavior_quality"]`，**signal 里已有的键优先**。
// Go 侧 Signal 只有一个 BehaviorQuality 字段，所以"已有键优先"体现为：
// 调用方若已经设置过非空字段，这里不覆盖它们。
func AttachBehaviorQuality(signal *Signal, feature Feature) {
	if signal == nil {
		return
	}
	quality := BehaviorQualityOf(feature)
	existing := signal.BehaviorQuality
	if existing.VolumeBehavior != "" {
		quality.VolumeBehavior = existing.VolumeBehavior
	}
	if existing.VolumeBehaviorLabel != "" {
		quality.VolumeBehaviorLabel = existing.VolumeBehaviorLabel
	}
	if existing.AcceptanceQuality != "" {
		quality.AcceptanceQuality = existing.AcceptanceQuality
	}
	if existing.AcceptanceQualityLabel != "" {
		quality.AcceptanceQualityLabel = existing.AcceptanceQualityLabel
	}
	if existing.UpperShadowBehavior != "" {
		quality.UpperShadowBehavior = existing.UpperShadowBehavior
	}
	if existing.UpperShadowBehaviorLbl != "" {
		quality.UpperShadowBehaviorLbl = existing.UpperShadowBehaviorLbl
	}
	if existing.Score != 0 {
		quality.Score = existing.Score
	}
	if existing.DeathVolumeRisk {
		quality.DeathVolumeRisk = true
	}
	// ⚠ Extras 也必须"已有键优先"地合并回来。
	//
	// 原实现是 `merged = dict(quality); merged.update(existing)` —— 于是箱体/超跌
	// 路径先塞进 behavior_quality 的那些键（box_quality_score 等）会被保留。
	// 这里如果只挑 base 字段、把 Extras 丢掉，那些键就**静默消失**了
	//（落库内容会跟着缺字段），是对比黄金值时抓出来的真实缺陷。
	if len(existing.Extras) > 0 {
		if quality.Extras == nil {
			quality.Extras = make(map[string]float64, len(existing.Extras))
		}
		for key, value := range existing.Extras {
			quality.Extras[key] = value
		}
	}
	signal.BehaviorQuality = quality
}

// roundPtr 对应 `_round`（None 原样返回）。
func roundPtr(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) {
		return nil
	}
	value := math.Round(*v*100) / 100
	return &value
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0.0
	}
	return *v
}

// FloatPtr 便于调用方构造可选价格。
func FloatPtr(v float64) *float64 { return &v }

// round4OrZero 对应 `float(_round(v, 4) or 0.0)`。
func round4OrZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0.0
	}
	return math.Round(v*10000) / 10000
}
