package volprice

import "math"

// 对应 `volume_price_backtest.py` 的"自动模型切换"一族：
//
//	_auto_switch_rank              :577-591
//	_auto_switch_candidate_score   :594-605
//	_auto_switch_choice_key        :608-622
//	_model_auto_choice             :625-664
//	_select_auto_switch_model      :667-683
//
// ⚠ 生产者（`research_service._collect_signal_rows`）**从不开启**这个功能，
// 但它属于 `volume_price_backtest.py` 的一部分，且 `volume_price_service.py`
// 会用，所以照 1:1 移植，而不是留一个"未实现"的分支。

// autoChoice 对应 `_model_auto_choice` 返回的字典。
type autoChoice struct {
	ModelID         string
	State           string
	GateReason      string
	Signal          Signal
	HasSignal       bool
	Confirmed       Signal
	ConfirmedOK     bool
	Candidate       Signal
	CandidateOK     bool
	Rank            int
	Score           float64
	PrefilterPassed bool
}

// autoSwitchRank 对应 `_auto_switch_rank`。
func autoSwitchRank(signal Signal, hasSignal bool) int {
	if !hasSignal {
		return 0
	}
	kind := lowerTrim(signal.SignalType)
	level := lowerTrim(signal.SignalLevel)
	if kind == "buy" || level == "confirmed" {
		return 4
	}
	if kind == "candidate_buy" && signal.LooseEntryAllowed {
		return 3
	}
	if kind == "candidate_buy" {
		return 2
	}
	if kind == "sell" {
		return 1
	}
	return 0
}

// autoSwitchCandidateScore 对应 `_auto_switch_candidate_score`。
func autoSwitchCandidateScore(signal Signal, hasSignal bool) float64 {
	if !hasSignal {
		return 0.0
	}
	if lowerTrim(signal.SignalType) == "candidate_buy" {
		// `float(signal.get("loose_entry_score") or signal.get("score") or 0.0)`
		if signal.LooseEntryScore != 0 {
			return signal.LooseEntryScore
		}
		return signal.Score
	}
	return signal.Score
}

// autoSwitchChoiceKey 对应 `_auto_switch_choice_key` 返回的元组。
//
// 原实现靠 Python 元组比较实现"字典序最大"，这里显式展开成比较函数，
// 顺序必须完全一致：rank → confirmed_bonus → candidate_bonus → score →
// **-priority_idx**（越靠前越好）→ default_bonus。
type autoChoiceKey struct {
	Rank           int
	ConfirmedBonus int
	CandidateBonus int
	Score          float64
	NegPriorityIdx int
	DefaultBonus   int
}

func (k autoChoiceKey) greaterThan(other autoChoiceKey) bool {
	if k.Rank != other.Rank {
		return k.Rank > other.Rank
	}
	if k.ConfirmedBonus != other.ConfirmedBonus {
		return k.ConfirmedBonus > other.ConfirmedBonus
	}
	if k.CandidateBonus != other.CandidateBonus {
		return k.CandidateBonus > other.CandidateBonus
	}
	if k.Score != other.Score {
		return k.Score > other.Score
	}
	if k.NegPriorityIdx != other.NegPriorityIdx {
		return k.NegPriorityIdx > other.NegPriorityIdx
	}
	return k.DefaultBonus > other.DefaultBonus
}

func autoSwitchChoiceKeyOf(choice autoChoice, priorityOrder []string, defaultModel string) autoChoiceKey {
	modelID := choice.ModelID
	if modelID == "" {
		modelID = NormalizeModelID(defaultModel)
	}
	level := lowerTrim(choice.Signal.SignalLevel)
	looseOK := choice.Signal.LooseEntryAllowed
	priorityIdx := len(priorityOrder)
	for index, item := range priorityOrder {
		if item == modelID {
			priorityIdx = index
			break
		}
	}
	key := autoChoiceKey{
		Rank: choice.Rank, Score: choice.Score, NegPriorityIdx: -priorityIdx,
	}
	if level == "confirmed" || choice.Signal.SignalType == "buy" {
		key.ConfirmedBonus = 1
	}
	if choice.Signal.SignalType == "candidate_buy" && looseOK {
		key.CandidateBonus = 1
	}
	if modelID == NormalizeModelID(defaultModel) {
		key.DefaultBonus = 1
	}
	return key
}

// modelAutoChoice 对应 `_model_auto_choice`。
func modelAutoChoice(modelID string, visible *VectorBars, state string) autoChoice {
	model := NormalizeModelID(modelID)
	out := autoChoice{
		ModelID: model, State: NormalizeMarketState(state, "range"),
	}
	if visible == nil || visible.Len() == 0 {
		out.GateReason = "可见K线不足"
		return out
	}
	evalWindow := ModelEvalWindow(model)
	signalVisible := visible
	if evalWindow > 0 && visible.Len() > evalWindow {
		signalVisible = visible.Slice(visible.Len()-evalWindow, visible.Len())
	}
	if !PassesEntryPrefilter(model, signalVisible) {
		out.GateReason = "入场预筛未通过"
		return out
	}
	out.PrefilterPassed = true
	out.Confirmed, out.ConfirmedOK, out.Candidate, out.CandidateOK =
		EvaluateModelLayers(signalVisible, model, out.State)

	if out.ConfirmedOK {
		kind := lowerTrim(out.Confirmed.SignalType)
		if kind == "buy" || kind == "sell" {
			out.Signal, out.HasSignal = out.Confirmed, true
			out.Rank = autoSwitchRank(out.Signal, true)
			out.Score = autoSwitchCandidateScore(out.Signal, true)
			return out
		}
	}
	if out.CandidateOK {
		out.Signal, out.HasSignal = out.Candidate, true
		out.Rank = autoSwitchRank(out.Signal, true)
		out.Score = autoSwitchCandidateScore(out.Signal, true)
		reasons := out.Candidate.QualityBlockReasons
		if len(reasons) == 0 {
			reasons = out.Candidate.LooseBlockReasons
		}
		cleaned := make([]string, 0, len(reasons))
		for _, item := range reasons {
			if text := trimWhitespace(item); text != "" {
				cleaned = append(cleaned, text)
			}
		}
		if len(cleaned) > 0 && out.Rank < 3 {
			out.GateReason = cleaned[0]
		}
		return out
	}
	out.GateReason = "模型适配质量不足"
	return out
}

// selectAutoSwitchModel 对应 `_select_auto_switch_model`。
//
// 候选模型顺序是**写死的三个**（与原实现一致），不是 `MODEL_DEFINITIONS` 顺序 ——
// 两者目前相同，但 `max` 遇到完全相同的键时要靠这个顺序决定谁胜出。
func selectAutoSwitchModel(visible *VectorBars, state, defaultModel string) autoChoice {
	priorityOrder := StateModelPriority(state)
	models := []string{ModelTrendPullback, ModelBoxRange, ModelOversoldRebound}
	choices := make([]autoChoice, 0, len(models))
	for _, modelID := range models {
		choices = append(choices, modelAutoChoice(modelID, visible, state))
	}

	ranked := make([]autoChoice, 0, len(choices))
	for _, choice := range choices {
		if choice.Rank > 0 {
			ranked = append(ranked, choice)
		}
	}
	if len(ranked) > 0 {
		best := ranked[0]
		bestKey := autoSwitchChoiceKeyOf(best, priorityOrder, defaultModel)
		for _, choice := range ranked[1:] {
			key := autoSwitchChoiceKeyOf(choice, priorityOrder, defaultModel)
			if key.greaterThan(bestKey) {
				best, bestKey = choice, key
			}
		}
		best.GateReason = ""
		return best
	}

	fallback := choices[0]
	fallbackKey := autoSwitchChoiceKeyOf(fallback, priorityOrder, defaultModel)
	for _, choice := range choices[1:] {
		key := autoSwitchChoiceKeyOf(choice, priorityOrder, defaultModel)
		if key.greaterThan(fallbackKey) {
			fallback, fallbackKey = choice, key
		}
	}
	fallbackReason := trimWhitespace(fallback.GateReason)
	gateReasons := make([]string, 0, len(choices))
	if fallbackReason != "" {
		gateReasons = append(gateReasons, fallbackReason)
	} else {
		for _, choice := range choices {
			if text := trimWhitespace(choice.GateReason); text != "" {
				gateReasons = append(gateReasons, text)
			}
		}
	}
	if len(gateReasons) > 0 {
		fallback.GateReason = gateReasons[0]
	} else {
		fallback.GateReason = "模型适配质量不足"
	}
	return fallback
}

// lowerTrim 对应 `str(x or "").strip().lower()`。
func lowerTrim(value string) string {
	return toLower(trimWhitespace(value))
}

// trimWhitespace 复刻 Python 的 `str.strip()`（含全角空格以外的常见空白）。
func trimWhitespace(value string) string {
	start, end := 0, len(value)
	for start < end && isSpaceByte(value[start]) {
		start++
	}
	for end > start && isSpaceByte(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

func toLower(value string) string {
	out := []byte(value)
	for i := 0; i < len(out); i++ {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// autoLayers 是复用自选结果时用到的那几项。
type autoLayers struct {
	Confirmed   Signal
	ConfirmedOK bool
	Candidate   Signal
	CandidateOK bool
}

// autoChoiceLayers 复刻主循环里的这段判断：
//
//	layers = auto_choice.get("layers") if isinstance(auto_choice, dict)
//	         and str(auto_choice.get("model_id") or "") == active_model else None
//
// 只有当自选结果的模型**就是当前生效模型**时才复用它的 layers。
// 注意自选在"前置过滤未通过"时把 layers 留成 `{signal: None, candidate: None}`，
// 所以这里返回的 ok 为 true 但两层的 OK 都是 false —— 与原实现一致。
func autoChoiceLayers(choice autoChoice, usable bool, activeModel string) (autoLayers, bool) {
	if !usable || choice.ModelID != activeModel {
		return autoLayers{}, false
	}
	return autoLayers{
		Confirmed: choice.Confirmed, ConfirmedOK: choice.ConfirmedOK,
		Candidate: choice.Candidate, CandidateOK: choice.CandidateOK,
	}, true
}

// autoChoiceFinite 便于测试断言（NaN 会让比较静默走偏）。
func autoChoiceFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
