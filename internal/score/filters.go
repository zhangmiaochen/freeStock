package score

// 候选质量的 5 个前置过滤。
//
// 1:1 移植自 portfolio_rotation_logic.py:773-852、885-940。
// 这些是 `candidate_quality_ok` 在"分数下限"之后立刻执行的**公共前置**（:1131-1140），
// 换股配对、建仓、溢出补位共用。
//
// 少了它们，Go 会放进原实现明确拒绝的候选：过热票、拥挤票、下跌中继票。

// isOverheatedCandidate 判定候选是否过热（:773-811）。
func isOverheatedCandidate(row Row, marketState string) bool {
	relativeStrength := row.RelativeStrength20D
	drawdown := row.DrawdownFromHigh120
	volumeRatio := row.VolumeRatio20

	overheated := (relativeStrength >= 50.0 && drawdown >= -4.0) ||
		(relativeStrength >= 42.0 && drawdown >= -2.0) ||
		(volumeRatio >= 2.4 && drawdown >= -5.0 && relativeStrength >= 25.0)

	if !overheated || marketState != StateBull {
		return overheated
	}

	// 牛市里给"强势龙头"开口子：满足一串严格条件且不算极端过热时放行
	extremeOverheated := (relativeStrength >= 65.0 && drawdown >= -1.0) ||
		(volumeRatio >= 3.1 && relativeStrength >= 35.0 && drawdown >= -3.0)

	strongBullLeader := row.TotalScore >= 82.0 &&
		row.TrendScore >= 23.0 &&
		row.StructureScore >= 13.0 &&
		row.RSScore >= 11.0 &&
		relativeStrength >= 18.0 &&
		drawdown >= -8.0 && drawdown <= 4.0 &&
		row.Close > 0 &&
		(row.Ma20 <= 0 || row.Close >= row.Ma20*1.005) &&
		(row.Ma30 <= 0 || row.Close >= row.Ma30*1.015) &&
		volumeRatio <= 2.8

	if strongBullLeader && !extremeOverheated {
		return false
	}
	return true
}

// isCrowdedTrendGrowthCandidate 判定趋势成长候选是否过度拥挤（:814-830）。
func isCrowdedTrendGrowthCandidate(row Row, strategyID, marketState string) bool {
	if strategyID != StrategyTrendGrowth {
		return false
	}
	if marketState == StateBull {
		return false
	}
	relativeStrength := row.RelativeStrength20D
	drawdown := row.DrawdownFromHigh120
	rebound := row.ReboundFromLow60
	volumeRatio := row.VolumeRatio20
	pctChg := row.PctChg

	return (volumeRatio >= 4.0 && relativeStrength >= 12.0) ||
		(pctChg >= 7.0 && relativeStrength >= 20.0 && volumeRatio >= 1.6) ||
		(relativeStrength >= 52.0 && volumeRatio >= 2.0 && drawdown >= -16.0) ||
		(relativeStrength >= 45.0 && rebound >= 0.86 && drawdown >= -10.0 && pctChg >= 3.0)
}

// isTrendGrowthEntryTimingOK 判定趋势成长的入场时点是否合适（:833-852）。
//
// 非趋势成长策略**恒返回 true**（不过滤）。
func isTrendGrowthEntryTimingOK(row Row, strategyID string) bool {
	if strategyID != StrategyTrendGrowth {
		return true
	}
	ma20GapPct := 0.0
	if row.Close > 0 && row.Ma20 > 0 {
		ma20GapPct = (row.Close/row.Ma20 - 1) * 100
	}
	calmTrend := row.StockReturn5D <= 8.0 && row.StockReturn10D <= 18.0 && ma20GapPct <= 22.0
	pullbackAbsorption := row.DrawdownFromHigh20 <= -3.0 &&
		row.DrawdownFromHigh20 >= -12.0 &&
		ma20GapPct <= 18.0 &&
		row.VolumeRatio520 <= 1.6 &&
		row.PctChg >= -3.0
	return calmTrend || pullbackAbsorption
}

// isOversoldDowntrendContinuationCandidate 判定超跌候选是否仍在下跌中继（:885-918）。
func isOversoldDowntrendContinuationCandidate(row Row, strategyID string) bool {
	if strategyID != StrategyOversold {
		return false
	}
	closePrice := row.Close
	openPrice := row.Open
	if openPrice <= 0 {
		openPrice = closePrice
	}

	if closePrice > 0 && row.Ma30 > 0 && closePrice < row.Ma30*0.90 {
		return true
	}
	if closePrice > 0 && row.Ma60 > 0 && closePrice < row.Ma60*0.88 {
		return true
	}
	if row.DrawdownFromHigh120 <= -45.0 && row.RepairScore < 8.0 {
		return true
	}
	if row.ReboundFromLow60 < 0.12 && row.PctChg < 1.0 {
		return true
	}
	if row.StockReturn3D < -6.0 || row.StockReturn5D < -9.0 || row.StockReturn10D < -16.0 {
		return true
	}
	if row.VolumeRatio20 < 0.72 && row.PctChg <= 1.0 {
		return true
	}
	if closePrice > 0 && openPrice > 0 && closePrice < openPrice*0.985 {
		return true
	}
	if closePrice > 0 && row.Ma20 > 0 && closePrice < row.Ma20*0.92 && row.RelativeStrength20D < -4.0 {
		return true
	}
	return false
}

// isExpertSimulationCandidateTooCrowded 判定高手模拟候选是否过于拥挤（:921-940）。
func isExpertSimulationCandidateTooCrowded(row Row, strategyID, marketState string) bool {
	if strategyID != StrategyExpert {
		return false
	}
	ma20GapPct := 0.0
	if row.Close > 0 && row.Ma20 > 0 {
		ma20GapPct = (row.Close/row.Ma20 - 1) * 100
	}
	if row.RelativeStrength20D >= 70.0 && row.DrawdownFromHigh120 >= -6.0 {
		return true
	}
	if marketState != StateBull &&
		row.RelativeStrength20D >= 56.0 &&
		row.DrawdownFromHigh120 >= -4.0 &&
		row.VolumeRatio20 >= 2.1 {
		return true
	}
	if row.StockReturn5D >= 20.0 || row.StockReturn10D >= 34.0 {
		return true
	}
	if ma20GapPct >= 28.0 && row.RelativeStrength20D >= 38.0 {
		return true
	}
	return false
}

// passesCandidatePrefilters 是 5 个前置过滤的合并入口。
//
// 对应 Python `candidate_quality_ok` 里的（:1131-1140）：
//
//	if is_overheated_candidate(...) or is_crowded_trend_growth_candidate(...) \
//	   or is_oversold_downtrend_continuation_candidate(...) \
//	   or is_expert_simulation_candidate_too_crowded(...) \
//	   or not is_trend_growth_entry_timing_ok(...):
//	    return False
//
// 返回 (是否通过, 原因)。
func passesCandidatePrefilters(row Row, strategyID, marketState string) (bool, string) {
	if isOverheatedCandidate(row, marketState) {
		return false, "候选过热"
	}
	if isCrowdedTrendGrowthCandidate(row, strategyID, marketState) {
		return false, "候选过度拥挤"
	}
	if isOversoldDowntrendContinuationCandidate(row, strategyID) {
		return false, "仍处下跌中继"
	}
	if isExpertSimulationCandidateTooCrowded(row, strategyID, marketState) {
		return false, "候选过于拥挤"
	}
	if !isTrendGrowthEntryTimingOK(row, strategyID) {
		return false, "入场时点不合适"
	}
	return true, ""
}
