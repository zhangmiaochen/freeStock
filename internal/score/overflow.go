package score

import (
	"math"
	"strings"
)

// overflow 补位：组合**没填满**时，用更严的条件再补几只建仓。
//
// 1:1 移植自 portfolio_rotation_logic.py:47-70,153-158,1233-1290,2575-2625。
//
// 背景：正常建仓受 `desired_position_count` 与层数额度限制，
// 出现"额度还剩、但没有合格候选"时会留下空层。overflow 阶段用**更严**的门槛
// 再筛一轮补上，让组合不至于长期半仓。
//
// ⚠ 此前 Go 里**整段缺失** —— 组合未填满时永远不补位。

const (
	// overflowFreeLayersTrigger 触发补位所需的空闲层数。
	// 原实现：`PORTFOLIO_OVERFLOW_FREE_LAYERS_TRIGGER = 2`
	overflowFreeLayersTrigger = 2
	// minCapacityFillRatio：原实现是 1.0，即"填满 = 用满 desired_total_layers"
	minCapacityFillRatio = 1.0
)

// normalizeMarketState 对应 `portfolio_rotation_logic.normalize_market_state`（:135-145）。
//
//	"bull"/"range"/"bear"   → 原样
//	"牛市"/"strong"          → bull
//	"熊市"/"weak"            → bear
//	"震荡"/"range_market"    → range
//	**其它一切（含空串）**   → range
//
// ⚠ 最后那条"兜底为 range"很关键，不能省。Go 原来几个按状态取值的函数直接
// `switch state`，未知状态落进默认分支 —— 而默认分支的语义是"不补位"，
// 与原版"未知当 range（可以补位）"**正好相反**：
//
//	overflowPositionLimitForState("")  原版 7、Go 原来 0
//	overflowScoreBufferForState("")    原版 2.0、Go 原来 999
//
// 边界上就是"该不该补位"的反转，所以这里统一先规范化。
func normalizeMarketState(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	switch key {
	case StateBull, StateRange, StateBear:
		return key
	case "震荡", "range_market":
		return StateRange
	case "牛市", "strong":
		return StateBull
	case "熊市", "weak":
		return StateBear
	}
	return StateRange
}

// overflowPositionLimitForState 各市场状态下允许**超出**目标只数的补位上限。
//
// 原实现：bull/range = 7，bear = 0（熊市不补位）。
func overflowPositionLimitForState(marketState string) int {
	switch normalizeMarketState(marketState) {
	case StateBull, StateRange:
		return 7
	}
	return 0
}

// overflowScoreBufferForState 补位的评分缓冲（比 ready_score 高多少）。
//
//	bull / range → 2.0；bear → 999（等于禁用）
func overflowScoreBufferForState(marketState string) float64 {
	switch normalizeMarketState(marketState) {
	case StateBull, StateRange:
		return 2.0
	}
	return 999.0
}

// minimumFillLayers 视为"已填满"的层数。
//
// 原实现 `minimum_fill_layers`：`max(1, min(layers, ceil(layers * ratio)))`，
// ratio = 1.0 → 结果就是 layers 本身。
func minimumFillLayers(totalLayers int) int {
	layers := atLeastOne(totalLayers)
	fill := int(math.Ceil(float64(layers) * minCapacityFillRatio))
	return atLeastOne(permittedMax(layers, fill))
}

// plannedRotationState 统计计划里"已开的只数与层数"（原实现 :1826-1836）。
// 同一代码只计一次（取首次出现的目标层数）。
func plannedRotationState(rows []RotationRow) (int, int, map[string]bool) {
	openCodes := map[string]bool{}
	totalLayers := 0
	for _, row := range rows {
		if row.Code == "" || row.TargetLayers <= 0 || openCodes[row.Code] {
			continue
		}
		openCodes[row.Code] = true
		totalLayers += row.TargetLayers
	}
	return len(openCodes), totalLayers, openCodes
}

// overflowCandidateQualityOK 是 overflow 阶段**更严**的候选门槛。
//
// 原实现 :1233-1290。因为它**先调用 `candidate_quality_ok`**，
// 所以是"在合格候选里再挑更优的"。
//
// ⚠ `has_signal` 分支依赖 ML 信号表（本项目没有）→ 恒为 false，
// 走的是**无信号分支**：`score >= max(ready+8, 84/82)` 且各分项更高。
// 这条分支不需要信号数据，是真实可达的。
// overflowSignalMaxAgeForState 对应
// `PORTFOLIO_OVERFLOW_SIGNAL_MAX_AGE_BY_STATE` / `overflow_signal_max_age_for_state`。
//
// 溢出补位（"持仓不足时从高位股里补"）对**信号新鲜度**的要求比普通候选更严：
// bull 3 天、range 2 天、**bear 只认当天**（0）。
func overflowSignalMaxAgeForState(marketState string) int {
	switch normalizeMarketState(marketState) {
	case StateBull:
		return 3
	case StateRange:
		return 2
	case StateBear:
		return 0
	}
	return 0
}

// overflowSignalScoreMinForState 对应
// `PORTFOLIO_OVERFLOW_SIGNAL_SCORE_MIN_BY_STATE`（缺省 999 = 实际上不可能通过）。
func overflowSignalScoreMinForState(marketState string) float64 {
	switch normalizeMarketState(marketState) {
	case StateBull:
		return 68.0
	case StateRange:
		return 66.0
	case StateBear:
		return 999.0
	}
	return 999.0
}

// overflowCandidateQualityOK 对应 `overflow_candidate_quality_ok`（:1233-1290）。
//
// ⚠ **这里原本把 `has_signal` 写死为 false**，注释是"无信号数据源"。
// 那条注释在本项目接上 `signal_history` + merge_asof 回填之后就**过期了**，
// 而漏掉的是整个"有新鲜信号"分支 —— 它不是"少一个优化"，而是**判定不同**：
//
//	有新鲜信号时（原版走这条）              Go 原来走的是"无信号"那条
//	  trend      >= 21(range)/22             >= 23/22
//	  structure  >= 11/12                    >= 13/12
//	  rs         >=  9/10                    >= 11/10
//	  signal_score >= 66(range)/68           （完全不看）
//	  且不看 relative_strength_20d           要求 >= 2/1
//	  且 bear 下允许（信号当天）             bear 一律 False
//
// 也就是：**有信号的股票在原版更容易被补位选中，Go 却按无信号的高门槛拒掉**；
// bear 下更是完全相反。
func overflowCandidateQualityOK(row Row, cfg StrategyConfig) bool {
	state := cfg.MarketState
	if overflowPositionLimitForState(state) <= 0 {
		return false
	}
	if ok, _ := CandidateQualityOK(row, cfg, state, false); !ok {
		return false
	}

	signalType := strings.ToLower(strings.TrimSpace(row.SignalType))
	signalAge := row.SignalAge
	signalScore := row.SignalScore
	hasSignal := (signalType == "buy" || signalType == "candidate_buy") &&
		signalAge <= overflowSignalMaxAgeForState(state)
	candidateSignal := signalType == "candidate_buy" && hasSignal

	if row.TotalScore < cfg.ReadyScore+overflowScoreBufferForState(state) {
		return false
	}
	isRange := state == StateRange
	if hasSignal {
		minTrend, minStructure, minRS := 22.0, 12.0, 10.0
		if isRange {
			minTrend, minStructure, minRS = 21.0, 11.0, 9.0
		}
		if row.TrendScore < minTrend || row.StructureScore < minStructure || row.RSScore < minRS {
			return false
		}
		signalScoreMin := overflowSignalScoreMinForState(state)
		if signalScore < signalScoreMin {
			return false
		}
		if candidateSignal {
			ageLimit := 2
			if isRange {
				ageLimit = 1
			}
			if signalAge > ageLimit {
				return false
			}
			if row.TotalScore < cfg.ReadyScore+4.0 {
				return false
			}
			if signalScore < signalScoreMin+4.0 {
				return false
			}
		}
	} else {
		if state == StateBear {
			return false
		}
		minScore := cfg.ReadyScore + 8.0
		base := 82.0
		if isRange {
			base = 84.0
		}
		if minScore < base {
			minScore = base
		}
		minTrend, minStructure, minRS := 22.0, 12.0, 10.0
		minRS20 := 1.0
		if isRange {
			minTrend, minStructure, minRS = 23.0, 13.0, 11.0
			minRS20 = 2.0
		}
		if row.TotalScore < minScore {
			return false
		}
		if row.TrendScore < minTrend || row.StructureScore < minStructure || row.RSScore < minRS {
			return false
		}
		if row.RelativeStrength20D < minRS20 {
			return false
		}
	}
	ma20Floor, ma30Floor := 0.992, 0.995
	if isRange {
		ma20Floor, ma30Floor = 0.988, 0.993
	}
	if row.Close > 0 && row.Ma20 > 0 && row.Close < row.Ma20*ma20Floor {
		return false
	}
	if row.Close > 0 && row.Ma30 > 0 && row.Close < row.Ma30*ma30Floor {
		return false
	}
	return true
}
