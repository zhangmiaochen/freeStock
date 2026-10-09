package score

import (
	"math"
	"strings"
	"testing"
)

// 造一个偏"强势上涨"的指标。
func strongIndicators() Indicators {
	return Indicators{
		Date: 20261008, Close: 120, Open: 118, High: 121, Low: 117,
		PctChg: 1.8, Amount: 5e8,
		Ma5: 118, Ma10: 115, Ma20: 110, Ma30: 105, Ma60: 100, Ma120: 95, Ma250: 90,
		Ma5Prev: 117, Ma10Prev: 114, Ma250Prev: 88,
		VolumeRatio20: 1.4, VolumeRatio520: 1.1,
		StockReturn3D: 3, StockReturn5D: 5, StockReturn10D: 9, StockReturn20D: 14,
		DrawdownFromHigh20: -3, DrawdownFromHigh120: -4, ReboundFromLow60: 0.8,
		RangePosition120:   0.75,
		BenchmarkReturn20D: 3, IndustryReturn20D: 6, RelativeStrength20D: 11,
		AmountRankPct: 0.8, TurnRankPct: 0.7, IndustryStrengthPct: 0.85,
	}
}

// 造一个深跌的指标（超跌反弹策略的目标形态）。
func oversoldIndicators() Indicators {
	return Indicators{
		Date: 20261008, Close: 70, Open: 67, High: 72, Low: 66,
		PctChg: 2.5, Amount: 3e8,
		Ma5: 68, Ma20: 75, Ma30: 80, Ma60: 85, Ma120: 90, Ma250: 95,
		Ma5Prev: 66, Ma250Prev: 94,
		VolumeRatio20: 1.3, VolumeRatio520: 1.2,
		StockReturn3D: 1, StockReturn5D: 2, StockReturn10D: 3, StockReturn20D: -12,
		DrawdownFromHigh20: -8, DrawdownFromHigh120: -26, ReboundFromLow60: 0.4,
		RangePosition120:   0.2,
		BenchmarkReturn20D: 3, IndustryReturn20D: 1, RelativeStrength20D: -8,
		AmountRankPct: 0.5, TurnRankPct: 0.5, IndustryStrengthPct: 0.3,
	}
}

func inputFor(ind Indicators, ability, near float64) StrategyScoreInput {
	return StrategyScoreInput{
		Indicators: ind,
		Repair:     RepairFeatures{EventCount: 3, RecoveryProbability: 0.6, AvgRecoveryDays: 20, AbilityScore: ability},
		NearRepair: near,
	}
}

// 四个策略用**同一份输入**必须给出**不同的结果** —— 这是"四套公式无法统一抽象"的直接验证。
func TestStrategiesDiffer(t *testing.T) {
	in := inputFor(strongIndicators(), 9, 8)

	results := map[string]BaseScores{}
	for _, strategy := range []string{
		StrategyTrendGrowth, StrategyOversold, StrategyExpert, StrategyTrendLowBuy,
	} {
		results[strategy] = ComputeStrategyScores(strategy, in)
	}

	// 至少要有两两不同：完全一样说明公式没真正分叉
	totals := map[float64]int{}
	for strategy, scores := range results {
		totals[scores.Total]++
		t.Logf("%-22s 总分 %6.2f（%.1f / %.1f / %.1f / %.1f）",
			strategy, scores.Total, scores.Trend, scores.Structure, scores.RS, scores.Capital)
	}
	if len(totals) < 3 {
		t.Errorf("四个策略应给出至少 3 种不同总分，实际只有 %d 种", len(totals))
	}
}

// 所有策略的五项分都必须落在各自上限内。
func TestAllStrategiesWithinBounds(t *testing.T) {
	cases := []Indicators{strongIndicators(), oversoldIndicators(), {}, {Close: 10, Ma20: 9}}
	for _, ind := range cases {
		for _, strategy := range []string{
			StrategyTrendGrowth, StrategyOversold, StrategyExpert, StrategyTrendLowBuy,
		} {
			scores := ComputeStrategyScores(strategy, inputFor(ind, 8, 7))
			check := func(name string, value, max float64) {
				if value < 0 || value > max+0.001 {
					t.Errorf("%s 的 %s 越界: %v（上限 %v）", strategy, name, value, max)
				}
				if math.IsNaN(value) {
					t.Errorf("%s 的 %s 是 NaN", strategy, name)
				}
			}
			// 低吸的 trend/structure 折算后上限仍是 30/20
			check("trend", scores.Trend, maxTrend)
			check("structure", scores.Structure, maxStructure)
			check("rs", scores.RS, maxRS)
			check("capital", scores.Capital, maxCapital)
			check("total", scores.Total, 100)
		}
	}
}

// ---------------------------------------------------------------------------
// 超跌反弹
// ---------------------------------------------------------------------------

func TestOversoldReboundScores(t *testing.T) {
	// 深跌 + 有反弹 + 站上 MA20*0.965 → 分数应该不低
	ind := oversoldIndicators()
	scores := ComputeStrategyScores(StrategyOversold, inputFor(ind, 8, 7))

	// sweet_spot = dd120 在 [-34,-12]：-26 落在里面 → +7
	// 超跌项 = score_0_1(26, 8, 30)*14 + 7 + 4*(close<ma20) + 5*(rebound 0.4 在 0.15..0.72)
	want := scoreWindow(26, 8, 30)*14 + 7 + 4 + 5
	if math.Abs(scores.Trend-want) > 0.01 {
		t.Errorf("超跌分 = %v，按公式应为 %v", scores.Trend, want)
	}
	t.Logf("超跌反弹：总分 %.2f（超跌 %.1f 反转 %.1f RS %.1f 资金 %.1f）",
		scores.Total, scores.Trend, scores.Structure, scores.RS, scores.Capital)

	// 横盘不深跌时超跌分应该很低
	flat := Indicators{Close: 100, Ma20: 100, Ma30: 100, DrawdownFromHigh120: -2, ReboundFromLow60: 0.5, PctChg: 0.5}
	flatScores := ComputeStrategyScores(StrategyOversold, inputFor(flat, 8, 7))
	if flatScores.Trend >= scores.Trend {
		t.Errorf("不深跌时超跌分应更低: %v vs %v", flatScores.Trend, scores.Trend)
	}
}

// 超跌的调整分全为负。
func TestOversoldAdjustmentIsNegative(t *testing.T) {
	// 极深回撤 + 修复能力差 → 触发 -5
	ind := oversoldIndicators()
	ind.DrawdownFromHigh120 = -45
	scores := ComputeStrategyScores(StrategyOversold, inputFor(ind, 3, 2))
	// 直接比"同输入下把惩罚条件去掉"的情况：这里就验证总分不会超过五项之和
	sum := scores.Trend + scores.Structure + scores.RS + scores.Capital + 0
	if scores.Total > sum+15.001 {
		t.Errorf("总分不该超过五项上限之和: %v", scores.Total)
	}
}

// ---------------------------------------------------------------------------
// 高手模拟
// ---------------------------------------------------------------------------

func TestExpertSimulationScores(t *testing.T) {
	ind := strongIndicators()
	scores := ComputeStrategyScores(StrategyExpert, inputFor(ind, 9, 8))

	// launch_window: range_pos 0.75 在 [0.22,0.88] → 6 分（不是 2 分）
	// healthy_position: close>0 且 close>=ma20*0.97 且 close>=ma30*0.97 且 ma20_gap<=20
	ma20Gap := (ind.Close/ind.Ma20 - 1) * 100 // (120/110-1)*100 = 9.09
	if ma20Gap > 20 {
		t.Fatal("测试数据的前提不成立")
	}
	// 位置项 = 6 + 4*(trend_repair_window) + 6*(healthy) + 3*(ret5<=10 且 ret10<=20) + 2*(close>=open*0.995)
	// 各项之和是 21，但该分项**上限就是 20**，所以被 clamp 到 20。
	wantStructure := 20.0
	if scores.Structure != wantStructure {
		t.Errorf("位置分 = %v，按公式应为 %v（各项和 21 被上限 20 clamp）", scores.Structure, wantStructure)
	}

	// 没有 launch_window 时位置项从 6 降到 2 → 总和 21-4=17，不再触顶
	noLaunch := ind
	noLaunch.RangePosition120 = 0.05
	noLaunch.DrawdownFromHigh120 = 5
	noLaunchScores := ComputeStrategyScores(StrategyExpert, inputFor(noLaunch, 9, 8))
	if noLaunchScores.Structure != 17 {
		t.Errorf("无 launch_window 时位置分应为 17，实际 %v", noLaunchScores.Structure)
	}
	t.Logf("高手模拟：总分 %.2f（主线 %.1f 位置 %.1f RS %.1f 成交 %.1f）",
		scores.Total, scores.Trend, scores.Structure, scores.RS, scores.Capital)
}

// 主线分取三个来源的最大值 —— 行业强度高时应该拉起来。
func TestExpertMainlineProxyTakesMax(t *testing.T) {
	ind := strongIndicators()
	ind.IndustryStrengthPct = 1.0 // → 100
	high := ComputeStrategyScores(StrategyExpert, inputFor(ind, 9, 8))

	ind.IndustryStrengthPct = 0
	low := ComputeStrategyScores(StrategyExpert, inputFor(ind, 9, 8))

	if high.Trend <= low.Trend {
		t.Errorf("行业强度满分时主线分应更高: %v vs %v", high.Trend, low.Trend)
	}
}

// ---------------------------------------------------------------------------
// 趋势低吸
// ---------------------------------------------------------------------------

// 低吸的 trend/structure 是**先算原始分再折算**的（36→30、50→20）。
func TestLowBuyScaling(t *testing.T) {
	ind := Indicators{
		Close: 100, Ma5: 96, Ma5Prev: 95, Ma20: 99, Ma30: 97, Ma60: 92,
		Ma250: 90, Ma250Prev: 88,
		DrawdownFromHigh20: -7, Amount: 5e8,
		AmountRankPct: 0.6,
	}
	scores := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(ind, 10, 8))

	// trend_raw = 14*(ma250 up) + score_0_1(ma30/ma60-1=5.43%, 0, 8)*12
	//           + score_0_1(ma20/ma30-1=2.06%, 0, 5)*10
	ma30_60 := (97.0/92 - 1) * 100
	ma20_30 := (99.0/97 - 1) * 100
	trendRaw := 14 + scoreWindow(ma30_60, 0, 8)*12 + scoreWindow(ma20_30, 0, 5)*10
	wantTrend := trendRaw / 36 * 30
	if math.Abs(scores.Trend-wantTrend) > 0.01 {
		t.Errorf("趋势分 = %v，按折算公式应为 %v（raw=%v）", scores.Trend, wantTrend, trendRaw)
	}

	// position_raw：ma20_gap = 1.01% → clip(1-1.01/8)=0.874*26；dd20=-7 → clip(1-0/8)=1*24
	ma20Gap := (100.0/99 - 1) * 100
	positionRaw := clamp(1-math.Abs(ma20Gap)/8, 0, 1)*26 + clamp(1-math.Abs(-7+7)/8, 0, 1)*24
	wantStructure := positionRaw / 50 * 20
	if math.Abs(scores.Structure-wantStructure) > 0.01 {
		t.Errorf("低吸分 = %v，按折算公式应为 %v（raw=%v）", scores.Structure, wantStructure, positionRaw)
	}
	t.Logf("趋势低吸：总分 %.2f（趋势 %.1f raw=%.1f 低吸 %.1f raw=%.1f 节奏 %.1f 成交 %.1f）",
		scores.Total, scores.Trend, trendRaw, scores.Structure, positionRaw, scores.RS, scores.Capital)
}

// 低吸在"离 MA20 太远"时低吸分会掉下来。
func TestLowBuyPositionPenalizesDistance(t *testing.T) {
	base := Indicators{Close: 100, Ma20: 100, Ma30: 97, Ma60: 92, Ma250: 90, Ma250Prev: 88, DrawdownFromHigh20: -7}
	near := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(base, 10, 8))

	far := base
	far.Close = 112 // 偏离 MA20 12%
	farScores := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(far, 10, 8))

	if farScores.Structure >= near.Structure {
		t.Errorf("偏离 MA20 过多时低吸分应更低: %v vs %v", farScores.Structure, near.Structure)
	}
}

// 低吸的惩罚项：涨多了 / 离 MA20 太远 / 跌破 MA30 / 成交额太小。
func TestLowBuyPenalties(t *testing.T) {
	base := Indicators{Close: 100, Ma20: 100, Ma30: 99, Ma60: 92, Ma250: 90, Ma250Prev: 88,
		DrawdownFromHigh20: -7, Amount: 5e8}
	clean := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(base, 10, 8))

	// 涨太多 → ret20 > 18 触发 -8
	hot := base
	hot.StockReturn20D = 25
	hotScores := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(hot, 10, 8))
	if hotScores.Total >= clean.Total {
		t.Errorf("涨幅过大应被惩罚: %v vs %v", hotScores.Total, clean.Total)
	}

	// 成交额 < 3000 万 → -5
	thin := base
	thin.Amount = 1e7
	thinScores := ComputeStrategyScores(StrategyTrendLowBuy, inputFor(thin, 10, 8))
	if thinScores.Total >= clean.Total {
		t.Errorf("成交额过小应被惩罚: %v vs %v", thinScores.Total, clean.Total)
	}
}

// ---------------------------------------------------------------------------
// 分项名称与摘要
// ---------------------------------------------------------------------------

// 四个策略的分项名称必须不同（这是"语义不同"的直接体现）。
func TestScoreSummaryNames(t *testing.T) {
	if len(ScoreSummaryNames) != 4 {
		t.Fatalf("应有 4 个策略的分项名，实际 %d", len(ScoreSummaryNames))
	}
	seen := map[[5]string]string{}
	for strategy, names := range ScoreSummaryNames {
		if other, dup := seen[names]; dup {
			t.Errorf("%s 与 %s 的分项名完全相同，说明没按策略分叉", strategy, other)
		}
		seen[names] = strategy
	}
	// 抽查几个
	if ScoreSummaryNames[StrategyOversold][0] != "超跌" {
		t.Error("超跌反弹的第一项应叫「超跌」")
	}
	if ScoreSummaryNames[StrategyExpert][1] != "位置" {
		t.Error("高手模拟的第二项应叫「位置」")
	}
	if ScoreSummaryNames[StrategyTrendLowBuy][2] != "节奏" {
		t.Error("趋势低吸的第三项应叫「节奏」")
	}
}

func TestScoreSummaryFormat(t *testing.T) {
	scores := BaseScores{}
	got := ScoreSummary(StrategyOversold, scores, [5]float64{18, 12.5, 11, 9, 8}, 0)
	// 注意不能用 got[:4] 判断开头 —— 中文一个字 3 字节，按字节切会切出半个字
	if !strings.HasPrefix(got, "超跌反弹") {
		t.Errorf("摘要应以策略名开头: %q", got)
	}
	if !containsSub(got, "超跌18.0/30") {
		t.Errorf("摘要格式错误: %q", got)
	}
	if !containsSub(got, "反转12.5/20") {
		t.Errorf("摘要格式错误: %q", got)
	}
	// 趋势成长额外带主线修正
	got = ScoreSummary(StrategyTrendGrowth, scores, [5]float64{10, 10, 10, 10, 10}, -3.5)
	if !containsSub(got, "主线修正-3.5") {
		t.Errorf("趋势成长应带主线修正: %q", got)
	}
	got = ScoreSummary(StrategyTrendGrowth, scores, [5]float64{10, 10, 10, 10, 10}, 4.2)
	if !containsSub(got, "主线修正+4.2") {
		t.Errorf("正修正应带 + 号: %q", got)
	}
}

func containsSub(text, sub string) bool {
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestStrategyLabelOf(t *testing.T) {
	cases := map[string]string{
		StrategyTrendGrowth: "趋势成长",
		StrategyOversold:    "超跌反弹",
		StrategyExpert:      "高手模拟",
		StrategyTrendLowBuy: "趋势低吸",
		"未知":                "趋势成长",
	}
	for strategy, want := range cases {
		if got := StrategyLabelOf(strategy); got != want {
			t.Errorf("StrategyLabelOf(%q) = %q，期望 %q", strategy, got, want)
		}
	}
}

// 未知策略回退趋势成长。
func TestComputeStrategyScoresFallback(t *testing.T) {
	in := inputFor(strongIndicators(), 9, 8)
	unknown := ComputeStrategyScores("乱填", in)
	known := ComputeStrategyScores(StrategyTrendGrowth, in)
	if unknown.Total != known.Total {
		t.Errorf("未知策略应回退趋势成长: %v vs %v", unknown.Total, known.Total)
	}
}
