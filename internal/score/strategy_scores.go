package score

import "math"

// 四个策略各自的评分公式。
//
// 1:1 移植自 portfolio_strategy.apply_strategy_scores（portfolio_strategy.py:407-699）。
//
// **规格明确指出这四套公式"语义不同、无法统一抽象"** —— 它们连分项名称都不一样：
//
//	trend_growth          = (趋势, 结构, RS,   资金, 修复)
//	oversold_rebound      = (超跌, 反转, RS,   资金, 修复)
//	expert_simulation     = (主线, 位置, RS,   成交, 修复)
//	trend_pullback_low_buy= (趋势, 低吸, 节奏, 成交, 修复)
//
// 所以这里就是四个独立的函数，不做参数化 —— 强行抽象只会把差异藏起来。
//
// 各策略五项的上限都是 30/20/20/15/15（合计 100），
// 加上 `strategy_adjustment`（可正可负）后 clip 到 0..100。

// ScoreSummaryNames 返回策略的分项中文名（用于 score_summary）。
var ScoreSummaryNames = map[string][5]string{
	StrategyTrendGrowth: {"趋势", "结构", "RS", "资金", "修复"},
	StrategyOversold:    {"超跌", "反转", "RS", "资金", "修复"},
	StrategyExpert:      {"主线", "位置", "RS", "成交", "修复"},
	StrategyTrendLowBuy: {"趋势", "低吸", "节奏", "成交", "修复"},
}

// StrategyScoreInput 是评分需要的全部输入。
type StrategyScoreInput struct {
	Indicators Indicators
	Repair     RepairFeatures
	// NearRepair 是 near_term_repair_score（只有超跌/高手/低吸用得到）
	NearRepair float64
	// MainlineRaw 是 max(mainline_board_score, industry_board_score, board_research_score)。
	// 本项目没有板块研究分，调用方传 0；公式里它是 max 的一项，取 0 不影响其它两项。
	MainlineRaw float64
}

// ComputeStrategyScores 按策略算五项分与调整分。
//
// 返回的 BaseScores 里 Total 是按该策略口径合成后的总分。
func ComputeStrategyScores(strategyID string, in StrategyScoreInput) BaseScores {
	switch strategyID {
	case StrategyOversold:
		return oversoldReboundScores(in)
	case StrategyExpert:
		return expertSimulationScores(in)
	case StrategyTrendLowBuy:
		return trendPullbackLowBuyScores(in)
	default:
		return trendGrowthScores(in)
	}
}

// trendGrowthScores 趋势成长（详见 score.go 的五个基础分函数）。
func trendGrowthScores(in StrategyScoreInput) BaseScores {
	ind := in.Indicators
	trend := TrendBase(ind, 0, 0)
	structure := StructureBase(ind, 0, 0)
	rs := RSBase(ind)
	capital := CapitalBase(ind)
	// 第五项用**修复能力分**（不是近期修复分）
	repair := in.Repair.AbilityScore

	adjustment := TrendGrowthAdjustment(ind)
	return BaseScores{
		Trend: trend, Structure: structure, RS: rs, Capital: capital,
		RepairAbility:  repair,
		NearTermRepair: in.NearRepair,
		Total:          TotalScore(trend, structure, rs, capital, repair, adjustment),
	}
}

// trendGrowthAdjustmentValue 是趋势成长的"主线修正"分。
//
//	adj = clip(mainline/100,0,1)*6 + score_0_1(rs20, 5, 30)*3
//	    - 4*(mainline < 40 且 rs20 < 12)
//	    - 5*(dd120 > -2 且 rs20 > 30)
//
// mainline = max(mainline_raw, industry_strength_pct*100)。
// TrendGrowthAdjustment 是趋势成长的'主线修正'分（导出供 service 层复用）。
func TrendGrowthAdjustment(ind Indicators) float64 {
	mainline := math.Max(0, ind.IndustryStrengthPct*100)
	adj := clamp(mainline/100, 0, 1)*6 + scoreWindow(ind.RelativeStrength20D, 5, 30)*3
	if mainline < 40 && ind.RelativeStrength20D < 12 {
		adj -= 4
	}
	if ind.DrawdownFromHigh120 > -2 && ind.RelativeStrength20D > 30 {
		adj -= 5
	}
	return round(adj, 4)
}

// oversoldReboundScores 超跌反弹。
func oversoldReboundScores(in StrategyScoreInput) BaseScores {
	ind := in.Indicators
	industryEdge := ind.IndustryReturn20D - ind.BenchmarkReturn20D

	sweetSpot := ind.DrawdownFromHigh120 <= -12 && ind.DrawdownFromHigh120 >= -34
	constructiveRebound := ind.ReboundFromLow60 >= 0.15 && ind.ReboundFromLow60 <= 0.72
	positiveBody := ind.Open <= 0 || ind.Close >= ind.Open*0.995

	// 超跌（上限 30）
	trend := scoreWindow(-ind.DrawdownFromHigh120, 8, 30)*14 +
		7*boolToFloat(sweetSpot) +
		4*boolToFloat(ind.Ma20 > 0 && ind.Close < ind.Ma20) +
		5*boolToFloat(constructiveRebound)
	trend = round(clamp(trend, 0, 30), 2)

	// 反转（上限 20）
	structure := scoreWindow(ind.ReboundFromLow60, 0.08, 0.62)*5 +
		3*boolToFloat(ind.PctChg > 0) +
		3*boolToFloat(ind.PctChg >= 0.8 && ind.PctChg <= 7.5) +
		2*boolToFloat(positiveBody) +
		scoreWindow(ind.VolumeRatio20, 0.85, 1.9)*4 +
		3*boolToFloat(ind.Ma20 > 0 && ind.Close >= ind.Ma20*0.965) +
		3*boolToFloat(ind.Ma30 > 0 && ind.Close >= ind.Ma30*0.925)
	structure = round(clamp(structure, 0, 20), 2)

	// RS（上限 20）
	rs := scoreWindow(ind.RelativeStrength20D, -10, 14)*11 +
		scoreWindow(industryEdge, -6, 12)*8 +
		1*boolToFloat(ind.RelativeStrength20D >= -4 && ind.RelativeStrength20D <= 28)
	rs = round(clamp(rs, 0, 20), 2)

	// 资金（上限 15）
	capital := clamp(ind.AmountRankPct, 0, 1)*5 +
		clamp(ind.TurnRankPct, 0, 1)*3 +
		scoreWindow(ind.VolumeRatio20, 0.75, 2.0)*7
	capital = round(clamp(capital, 0, 15), 2)

	// 修复 = clip(ability*0.9 + near*0.35, 0, 15)
	repair := round(clamp(in.Repair.AbilityScore*0.9+in.NearRepair*0.35, 0, 15), 2)

	// 调整分（全为负）
	adjustment := -(5*boolToFloat(ind.DrawdownFromHigh120 <= -42 && in.Repair.AbilityScore < 6) +
		4*boolToFloat(ind.StockReturn5D <= -8 || ind.StockReturn10D <= -15) +
		6*boolToFloat(ind.Ma30 > 0 && ind.Close < ind.Ma30*0.90))

	return BaseScores{
		Trend: trend, Structure: structure, RS: rs, Capital: capital,
		RepairAbility:  in.Repair.AbilityScore,
		NearTermRepair: in.NearRepair,
		Total:          TotalScore(trend, structure, rs, capital, repair, adjustment),
	}
}

// expertSimulationScores 高手模拟。
func expertSimulationScores(in StrategyScoreInput) BaseScores {
	ind := in.Indicators
	industryEdge := ind.IndustryReturn20D - ind.BenchmarkReturn20D
	ma20GapPct := 0.0
	if ind.Ma20 > 0 {
		ma20GapPct = (ind.Close/ind.Ma20 - 1) * 100
	}

	trendBase := TrendBase(ind, 0, 0)
	mainlineProxy := math.Max(
		in.MainlineRaw,
		math.Max(
			clamp(ind.IndustryStrengthPct, 0, 1)*100,
			clamp(trendBase/30, 0, 1)*78,
		),
	)
	reasonableRS := ind.RelativeStrength20D >= 2 && ind.RelativeStrength20D <= 44
	launchWindow := (ind.RangePosition120 >= 0.22 && ind.RangePosition120 <= 0.88) ||
		(ind.DrawdownFromHigh120 >= -24 && ind.DrawdownFromHigh120 <= -2)
	healthyPosition := ind.Close > 0 &&
		(ind.Ma20 <= 0 || ind.Close >= ind.Ma20*0.970) &&
		(ind.Ma30 <= 0 || ind.Close >= ind.Ma30*0.970) &&
		ma20GapPct <= 20
	trendRepairWindow := ind.DrawdownFromHigh20 >= -14 && ind.DrawdownFromHigh20 <= -1.5 &&
		ind.StockReturn5D <= 10 && ind.StockReturn10D <= 20 && ma20GapPct <= 18

	// 主线（上限 30）
	trend := clamp(mainlineProxy/100, 0, 1)*21 +
		scoreWindow(industryEdge, 0, 16)*4 +
		4*boolToFloat(reasonableRS) +
		2*boolToFloat(in.MainlineRaw >= 38)
	trend = round(clamp(trend, 0, 30), 2)

	// 位置（上限 20）
	structure := 2.0
	if launchWindow {
		structure = 6
	}
	structure += 4*boolToFloat(trendRepairWindow) +
		6*boolToFloat(healthyPosition) +
		3*boolToFloat(ind.StockReturn5D <= 10 && ind.StockReturn10D <= 20) +
		2*boolToFloat(ind.Open > 0 && ind.Close >= ind.Open*0.995)
	structure = round(clamp(structure, 0, 20), 2)

	// RS（上限 20）
	rs := scoreWindow(ind.RelativeStrength20D, -4, 32)*12 +
		scoreWindow(industryEdge, -4, 18)*6 +
		2*boolToFloat(reasonableRS)
	rs = round(clamp(rs, 0, 20), 2)

	// 成交（上限 15）
	capital := clamp(ind.AmountRankPct, 0, 1)*6 +
		clamp(ind.TurnRankPct, 0, 1)*2 +
		scoreWindow(ind.VolumeRatio20, 0.7, 2.2)*5 +
		clamp(1-math.Abs(ind.VolumeRatio520-1.05)/0.9, 0, 1)*2
	capital = round(clamp(capital, 0, 15), 2)

	// 修复 = clip(ability*0.45 + near*0.55 + 2*(trend_repair_window), 0, 15)
	repair := round(clamp(
		in.Repair.AbilityScore*0.45+in.NearRepair*0.55+2*boolToFloat(trendRepairWindow),
		0, 15), 4)

	// 调整分（全为负）
	adjustment := -(6*boolToFloat(ind.RelativeStrength20D >= 60 && ind.DrawdownFromHigh120 >= -3) +
		5*boolToFloat(ind.StockReturn5D >= 18 || ind.StockReturn10D >= 30) +
		4*boolToFloat(ind.Ma20 > 0 && ind.Close > ind.Ma20*1.26))

	return BaseScores{
		Trend: trend, Structure: structure, RS: rs, Capital: capital,
		RepairAbility:  in.Repair.AbilityScore,
		NearTermRepair: in.NearRepair,
		Total:          TotalScore(trend, structure, rs, capital, repair, adjustment),
	}
}

// trendPullbackLowBuyScores 趋势低吸。
//
// 注意这个策略的 trend/structure 是**先算原始分再按各自满分折算**的
// （trend_raw 满分 36 → 折成 30；position_raw 满分 50 → 折成 20），
// 与其它三个策略直接给分不同。
func trendPullbackLowBuyScores(in StrategyScoreInput) BaseScores {
	ind := in.Indicators
	ma20GapPct := 0.0
	if ind.Ma20 > 0 {
		ma20GapPct = (ind.Close/ind.Ma20 - 1) * 100
	}
	ma30GapPct := 0.0
	if ind.Ma60 > 0 {
		ma30GapPct = (ind.Ma30/ind.Ma60 - 1) * 100
	}
	ma20GapToMa30Pct := 0.0
	if ind.Ma30 > 0 {
		ma20GapToMa30Pct = (ind.Ma20/ind.Ma30 - 1) * 100
	}
	ma250Up := ind.Ma250 > 0 && ind.Ma250Prev > 0 && ind.Ma250 > ind.Ma250Prev

	// 趋势：满分 36 → 折成 30
	trendRaw := 14*boolToFloat(ma250Up) +
		scoreWindow(ma30GapPct, 0, 8)*12 +
		scoreWindow(ma20GapToMa30Pct, 0, 5)*10
	trend := round(clamp(trendRaw/36*30, 0, 30), 2)

	// 低吸：满分 50 → 折成 20
	positionRaw := clamp(1-math.Abs(ma20GapPct)/8, 0, 1)*26 +
		clamp(1-math.Abs(ind.DrawdownFromHigh20+7)/8, 0, 1)*24
	structure := round(clamp(positionRaw/50*20, 0, 20), 2)

	// 节奏：满分 20
	rs := 8*boolToFloat(ind.Ma5 > 0 && ind.Ma20 > 0 && ind.Ma5 < ind.Ma20) +
		6*boolToFloat((ind.Ma5Prev > 0 && ind.Ma5 >= ind.Ma5Prev) ||
			(ind.Ma5 > 0 && ind.Close >= ind.Ma5)) +
		3*boolToFloat(ind.StockReturn3D <= 2) +
		3*boolToFloat(ind.StockReturn5D <= 3)
	rs = round(clamp(rs, 0, 20), 2)

	// 成交：stable_volume*10 + amount_rank*5
	stableVolume := clamp(1-math.Abs(ind.VolumeRatio520-1)/0.8, 0, 1)
	capital := stableVolume*10 + clamp(ind.AmountRankPct, 0, 1)*5
	capital = round(clamp(capital, 0, 15), 2)

	// 修复
	repair := 5*boolToFloat(ind.DrawdownFromHigh20 >= -15 && ind.DrawdownFromHigh20 <= -2.5) +
		5*boolToFloat(ind.Ma30 > 0 && ind.Close >= ind.Ma30*0.985) +
		clamp(in.Repair.AbilityScore/15, 0, 1)*5
	repair = round(clamp(repair, 0, 15), 2)

	// 惩罚（全为负）
	penalty := 8*boolToFloat(ind.StockReturn20D > 18 || ind.StockReturn5D > 6) +
		8*boolToFloat(ind.Ma20 > 0 && ind.Close > ind.Ma20*1.02) +
		8*boolToFloat(ind.Ma30 > 0 && ind.Close < ind.Ma30*0.985) +
		5*boolToFloat(ind.Amount < 30_000_000)
	adjustment := -penalty

	return BaseScores{
		Trend: trend, Structure: structure, RS: rs, Capital: capital,
		RepairAbility:  in.Repair.AbilityScore,
		NearTermRepair: in.NearRepair,
		Total:          TotalScore(trend, structure, rs, capital, repair, adjustment),
	}
}

// ScoreSummary 生成分项摘要（对应参考实现的 score_summary）。
//
// 格式：`{策略名} | {名1}{值}/30 | {名2}{值}/20 | ...`
// trend_growth 额外追加 ` | 主线修正{adj:+.1f}`。
func ScoreSummary(strategyID string, scores BaseScores, scores5 [5]float64, adjustment float64) string {
	names, ok := ScoreSummaryNames[strategyID]
	if !ok {
		names = ScoreSummaryNames[StrategyTrendGrowth]
	}
	out := StrategyLabelOf(strategyID) + " | " +
		names[0] + ftoa(scores5[0], 1) + "/30 | " +
		names[1] + ftoa(scores5[1], 1) + "/20 | " +
		names[2] + ftoa(scores5[2], 1) + "/20 | " +
		names[3] + ftoa(scores5[3], 1) + "/15 | " +
		names[4] + ftoa(scores5[4], 1) + "/15"
	if strategyID == StrategyTrendGrowth {
		sign := "+"
		if adjustment < 0 {
			sign = ""
		}
		out += " | 主线修正" + sign + ftoa(adjustment, 1)
	}
	_ = scores
	return out
}

// StrategyLabelOf 返回策略中文名。
func StrategyLabelOf(strategyID string) string {
	switch strategyID {
	case StrategyOversold:
		return "超跌反弹"
	case StrategyExpert:
		return "高手模拟"
	case StrategyTrendLowBuy:
		return "趋势低吸"
	default:
		return "趋势成长"
	}
}
