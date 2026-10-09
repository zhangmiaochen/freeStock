package score

import "testing"

// 这 5 个前置过滤是逐条从 Python 移植的（portfolio_rotation_logic.py:773-940），
// 条件多、容易抄错，所以每个都挑几个能区分的点验证。

func TestIsOverheatedCandidate(t *testing.T) {
	// 区间外：RS 低、回撤深、量比低
	cool := Row{RelativeStrength20D: 10, DrawdownFromHigh120: -20, VolumeRatio20: 1.0}
	if isOverheatedCandidate(cool, StateRange) {
		t.Error("普通候选不该判为过热")
	}
	// rs>=50 且 dd>=-4
	if !isOverheatedCandidate(Row{RelativeStrength20D: 50, DrawdownFromHigh120: -4}, StateRange) {
		t.Error("rs50/dd-4 应判为过热")
	}
	// rs>=42 且 dd>=-2
	if !isOverheatedCandidate(Row{RelativeStrength20D: 42, DrawdownFromHigh120: -2}, StateRange) {
		t.Error("rs42/dd-2 应判为过热")
	}
	// 量比>=2.4 且 dd>=-5 且 rs>=25
	if !isOverheatedCandidate(Row{RelativeStrength20D: 25, DrawdownFromHigh120: -5, VolumeRatio20: 2.4}, StateRange) {
		t.Error("高量比+高位应判为过热")
	}
	// 边界：rs=49.9 不该命中第一条
	if isOverheatedCandidate(Row{RelativeStrength20D: 49.9, DrawdownFromHigh120: -3}, StateRange) {
		t.Error("rs 未达 50 且未达 42 档时不该过热")
	}

	// 牛市里强势龙头可豁免（但要满足一整串条件）
	leader := Row{
		TotalScore: 85, TrendScore: 24, StructureScore: 14, RSScore: 12,
		RelativeStrength20D: 50, DrawdownFromHigh120: -4,
		Close: 100, Ma20: 95, Ma30: 90, VolumeRatio20: 1.5,
	}
	if isOverheatedCandidate(leader, StateBull) {
		t.Error("牛市里满足龙头条件的应豁免过热")
	}
	if !isOverheatedCandidate(leader, StateRange) {
		t.Error("非牛市不豁免（原实现只在 bull 里开口子）")
	}
	// 极端过热时连龙头也拦
	extreme := leader
	extreme.RelativeStrength20D = 70
	extreme.DrawdownFromHigh120 = -0.5
	if !isOverheatedCandidate(extreme, StateBull) {
		t.Error("极端过热时龙头也不豁免")
	}
}

func TestIsCrowdedTrendGrowthCandidate(t *testing.T) {
	base := Row{TotalScore: 80}
	// 只对趋势成长生效
	if isCrowdedTrendGrowthCandidate(base, StrategyExpert, StateRange) {
		t.Error("非趋势成长不该判拥挤")
	}
	// 牛市不判
	if isCrowdedTrendGrowthCandidate(Row{VolumeRatio20: 5, RelativeStrength20D: 20}, StrategyTrendGrowth, StateBull) {
		t.Error("牛市不判拥挤")
	}
	// 量比>=4 且 rs>=12
	if !isCrowdedTrendGrowthCandidate(Row{VolumeRatio20: 4, RelativeStrength20D: 12}, StrategyTrendGrowth, StateRange) {
		t.Error("量比4/rs12 应判拥挤")
	}
	// 当日涨>=7 且 rs>=20 且量比>=1.6
	if !isCrowdedTrendGrowthCandidate(Row{PctChg: 7, RelativeStrength20D: 20, VolumeRatio20: 1.6}, StrategyTrendGrowth, StateRange) {
		t.Error("大涨+高量比应判拥挤")
	}
	// 普通候选不判
	if isCrowdedTrendGrowthCandidate(Row{VolumeRatio20: 1.0, RelativeStrength20D: 5}, StrategyTrendGrowth, StateRange) {
		t.Error("普通候选不该判拥挤")
	}
}

func TestIsTrendGrowthEntryTimingOK(t *testing.T) {
	// 非趋势成长恒 true
	if !isTrendGrowthEntryTimingOK(Row{StockReturn5D: 100}, StrategyExpert) {
		t.Error("非趋势成长不校验入场时点")
	}
	// 平静上涨 → ok
	calm := Row{StockReturn5D: 5, StockReturn10D: 12, Close: 100, Ma20: 95}
	if !isTrendGrowthEntryTimingOK(calm, StrategyTrendGrowth) {
		t.Error("平静上涨应通过")
	}
	// 短期涨幅过大且不符合回踩吸收 → 拒
	hot := Row{StockReturn5D: 15, StockReturn10D: 30, Close: 100, Ma20: 90}
	if isTrendGrowthEntryTimingOK(hot, StrategyTrendGrowth) {
		t.Error("短期涨幅过大应拒绝")
	}
	// 回踩吸收路径：dd20 在 -3..-12、乖离<=18、量比<=1.6、当日跌幅不超过 3%
	absorb := Row{
		StockReturn5D: 15, StockReturn10D: 30,
		DrawdownFromHigh20: -6, Close: 100, Ma20: 98,
		VolumeRatio520: 1.2, PctChg: -1,
	}
	if !isTrendGrowthEntryTimingOK(absorb, StrategyTrendGrowth) {
		t.Error("符合回踩吸收的应通过")
	}
	// 偏离 MA20 超过 22% → 拒
	far := Row{StockReturn5D: 5, StockReturn10D: 12, Close: 130, Ma20: 100}
	if isTrendGrowthEntryTimingOK(far, StrategyTrendGrowth) {
		t.Error("偏离 MA20 超过 22% 应拒绝")
	}
}

func TestIsOversoldDowntrendContinuationCandidate(t *testing.T) {
	// 只对超跌反弹生效
	row := Row{Close: 50, Ma30: 100}
	if isOversoldDowntrendContinuationCandidate(row, StrategyTrendGrowth) {
		t.Error("非超跌策略不该判下跌中继")
	}
	// close < ma30*0.90
	if !isOversoldDowntrendContinuationCandidate(row, StrategyOversold) {
		t.Error("跌破 MA30*0.90 应判下跌中继")
	}
	// close < ma60*0.88
	if !isOversoldDowntrendContinuationCandidate(Row{Close: 50, Ma60: 100}, StrategyOversold) {
		t.Error("跌破 MA60*0.88 应判下跌中继")
	}
	// dd<=-45 且 repair<8
	if !isOversoldDowntrendContinuationCandidate(Row{DrawdownFromHigh120: -50, RepairScore: 5}, StrategyOversold) {
		t.Error("深回撤+修复差应判下跌中继")
	}
	// 反弹弱且当日不涨
	if !isOversoldDowntrendContinuationCandidate(Row{ReboundFromLow60: 0.05, PctChg: 0.5}, StrategyOversold) {
		t.Error("反弹弱+不涨应判下跌中继")
	}
	// 短期跌幅过大
	if !isOversoldDowntrendContinuationCandidate(Row{StockReturn3D: -7}, StrategyOversold) {
		t.Error("3 日跌超 6% 应判下跌中继")
	}
	// 缩量且不涨
	if !isOversoldDowntrendContinuationCandidate(Row{VolumeRatio20: 0.5, PctChg: 0}, StrategyOversold) {
		t.Error("缩量+不涨应判下跌中继")
	}
	// 阴线
	if !isOversoldDowntrendContinuationCandidate(Row{Close: 100, Open: 110}, StrategyOversold) {
		t.Error("实体下跌超 1.5% 应判下跌中继")
	}
	// 站在均线上方、有反弹、放量阳线 → 不判
	healthy := Row{
		Close: 100, Open: 98, Ma20: 95, Ma30: 92, Ma60: 90,
		PctChg: 3, ReboundFromLow60: 0.4, DrawdownFromHigh120: -20,
		RepairScore: 9, VolumeRatio20: 1.3, StockReturn3D: 1,
	}
	if isOversoldDowntrendContinuationCandidate(healthy, StrategyOversold) {
		t.Error("健康的超跌反弹候选不该判下跌中继")
	}
}

func TestIsExpertSimulationCandidateTooCrowded(t *testing.T) {
	if isExpertSimulationCandidateTooCrowded(Row{RelativeStrength20D: 90}, StrategyTrendGrowth, StateRange) {
		t.Error("非高手模拟不该判拥挤")
	}
	// rs>=70 且 dd>=-6
	if !isExpertSimulationCandidateTooCrowded(Row{RelativeStrength20D: 70, DrawdownFromHigh120: -6}, StrategyExpert, StateRange) {
		t.Error("rs70/dd-6 应判拥挤")
	}
	// 非牛市 rs>=56 且 dd>=-4 且量比>=2.1
	if !isExpertSimulationCandidateTooCrowded(
		Row{RelativeStrength20D: 56, DrawdownFromHigh120: -4, VolumeRatio20: 2.1},
		StrategyExpert, StateRange) {
		t.Error("非牛市高量比高位应判拥挤")
	}
	// 牛市同一条件不判（第二条要求非 bull）
	if isExpertSimulationCandidateTooCrowded(
		Row{RelativeStrength20D: 56, DrawdownFromHigh120: -4, VolumeRatio20: 2.1},
		StrategyExpert, StateBull) {
		t.Error("牛市不适用第二条")
	}
	// 短期涨幅过大
	if !isExpertSimulationCandidateTooCrowded(Row{StockReturn5D: 20}, StrategyExpert, StateBull) {
		t.Error("5 日涨 20% 应判拥挤")
	}
	if !isExpertSimulationCandidateTooCrowded(Row{StockReturn10D: 34}, StrategyExpert, StateBull) {
		t.Error("10 日涨 34% 应判拥挤")
	}
	// 偏离 MA20 >=28% 且 rs>=38
	if !isExpertSimulationCandidateTooCrowded(
		Row{Close: 128, Ma20: 100, RelativeStrength20D: 38}, StrategyExpert, StateBull) {
		t.Error("偏离 MA20 28% 应判拥挤")
	}
}

func TestPassesCandidatePrefilters(t *testing.T) {
	healthy := Row{
		TotalScore: 80, TrendScore: 22, StructureScore: 12, RSScore: 10,
		Close: 100, Open: 98, Ma20: 95, Ma30: 92, Ma60: 90,
		RelativeStrength20D: 8, DrawdownFromHigh120: -12, DrawdownFromHigh20: -5,
		ReboundFromLow60: 0.4, PctChg: 1, VolumeRatio20: 1.2, VolumeRatio520: 1.0,
		StockReturn3D: 1, StockReturn5D: 3, StockReturn10D: 6,
	}
	if ok, reason := passesCandidatePrefilters(healthy, StrategyTrendGrowth, StateRange); !ok {
		t.Errorf("健康候选应通过，实际被拒: %s", reason)
	}
	// 过热 → 拒
	hot := healthy
	hot.RelativeStrength20D = 60
	hot.DrawdownFromHigh120 = -1
	if ok, _ := passesCandidatePrefilters(hot, StrategyTrendGrowth, StateRange); ok {
		t.Error("过热候选应被拒")
	}
	// 超跌下跌中继 → 拒
	declining := Row{Close: 50, Ma30: 100, ReboundFromLow60: 0.03, PctChg: 0}
	if ok, _ := passesCandidatePrefilters(declining, StrategyOversold, StateRange); ok {
		t.Error("下跌中继应被拒")
	}
}
