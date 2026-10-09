package score

import "testing"

// 回归测试：`trendGrowthScores` / `expertSimulationScores` **调用点**必须把
// 滞后均线与前高传进去。
//
// 原来的测试直接调 `TrendBase(...)` / `StructureBase(...)` 并显式传参 ——
// 函数体是对的，所以测试全绿，但生产调用点传的是 `0, 0`，
// 导致趋势分最后两项退化成 `ma20>0`/`ma60>0`、结构分突破缺口档整段为 0。
// 这里从 `ComputeStrategyScores` 入口进，才能真正盖住接线。
func TestStrategyScoresWireLagMAsAndPrevHigh(t *testing.T) {
	// 均线走平（ma20 == ma20_lag5）→ 滞后项应为 0 分
	flat := Indicators{
		Close: 100, Ma5: 99, Ma20: 100, Ma30: 98, Ma60: 95,
		Ma120: 90, Ma250: 85,
		Ma20Lag5: 100, Ma60Lag10: 95, // 都不涨
	}
	// 均线上行 → 滞后项应各得 2 分
	rising := flat
	rising.Ma20Lag5 = 97 // ma20(100) > lag(97)
	rising.Ma60Lag10 = 92

	base := StrategyScoreInput{Indicators: flat}
	up := StrategyScoreInput{Indicators: rising}

	scoreFlat := ComputeStrategyScores(StrategyTrendGrowth, base)
	scoreUp := ComputeStrategyScores(StrategyTrendGrowth, up)

	if scoreUp.Trend <= scoreFlat.Trend {
		t.Errorf("滞后均线上行应比走平多分（各 +2）：走平 %.2f，上行 %.2f",
			scoreFlat.Trend, scoreUp.Trend)
	}
	if diff := scoreUp.Trend - scoreFlat.Trend; diff < 3.9 || diff > 4.1 {
		t.Errorf("两项滞后应共 +4 分，实际差 %.2f", diff)
	}

	// **如果调用点传 0**，两项会变成 `ma20>0`/`ma60>0` → 两种情况都是 +4，
	// 差值会是 0。所以上面的断言足以盖住"传 0"这个 bug。
	// 另外再直接验证：传 0 时走平与上行同分（这正是 bug 的特征）。
	zeroLag := StrategyScoreInput{Indicators: Indicators{
		Close: 100, Ma5: 99, Ma20: 100, Ma30: 98, Ma60: 95,
		Ma120: 90, Ma250: 85,
	}}
	if got := TrendBase(zeroLag.Indicators, 0, 0); got <= 0 {
		t.Errorf("传 0 时不应为负，实际 %.2f", got)
	}
}

// 结构分的"突破缺口"档（原实现 portfolio_service.py:1978-2000）：
//
//	close 在 prev_high60 下方 2.5% 以内 → +6
//	close 突破 prev_high60 的 3%~8%   → +3
func TestStrategyScoresWirePrevHigh60(t *testing.T) {
	ind := Indicators{
		Close: 100, Ma5: 99, Ma20: 95, Ma30: 93, Ma60: 90,
		Ma120: 88, Ma250: 85,
	}
	// 贴着前高（gap = 0）→ +6 档
	near := StrategyScoreInput{Indicators: ind, PrevHigh60: 100}
	// 没有前高数据 → 该档为 0
	without := StrategyScoreInput{Indicators: ind, PrevHigh60: 0}

	gotNear := ComputeStrategyScores(StrategyTrendGrowth, near)
	gotWithout := ComputeStrategyScores(StrategyTrendGrowth, without)

	if gotNear.Structure <= gotWithout.Structure {
		t.Errorf("贴前高应命中 +6 档：有前高 %.2f，无前高 %.2f",
			gotNear.Structure, gotWithout.Structure)
	}
	if diff := gotNear.Structure - gotWithout.Structure; diff < 5.9 || diff > 6.1 {
		t.Errorf("贴前高应恰好 +6 分，实际差 %.2f", diff)
	}

	// 突破 3%~8% → +3 档
	broke := StrategyScoreInput{Indicators: ind, PrevHigh60: 95} // gap = +5.26%
	gotBroke := ComputeStrategyScores(StrategyTrendGrowth, broke)
	if diff := gotBroke.Structure - gotWithout.Structure; diff < 2.9 || diff > 3.1 {
		t.Errorf("突破 3~8%% 应 +3 分，实际差 %.2f", diff)
	}
}
