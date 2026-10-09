package score

import "math"

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

// overflowPositionLimitForState 各市场状态下允许**超出**目标只数的补位上限。
//
// 原实现：bull/range = 7，bear = 0（熊市不补位）。
func overflowPositionLimitForState(marketState string) int {
	switch marketState {
	case StateBull, StateRange:
		return 7
	}
	return 0
}

// overflowScoreBufferForState 补位的评分缓冲（比 ready_score 高多少）。
//
//	bull / range → 2.0；bear → 999（等于禁用）
func overflowScoreBufferForState(marketState string) float64 {
	switch marketState {
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
func overflowCandidateQualityOK(row Row, cfg StrategyConfig) bool {
	state := cfg.MarketState
	if overflowPositionLimitForState(state) <= 0 {
		return false
	}
	if ok, _ := CandidateQualityOK(row, cfg, state, false); !ok {
		return false
	}
	if row.TotalScore < cfg.ReadyScore+overflowScoreBufferForState(state) {
		return false
	}
	// has_signal 恒为 false（无信号数据源）
	if state == StateBear {
		return false
	}
	isRange := state == StateRange
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
