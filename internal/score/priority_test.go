package score

import "testing"

func prioRow(code string, mutate func(*Row)) Row {
	row := Row{
		Code: code, Name: code,
		TotalScore: 80, StructureScore: 14, RepairScore: 9,
		Close: 100, Ma30: 95, VolumeRatio20: 1.2, RelativeStrength20D: 5,
		DrawdownFromHigh120: -10,
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

// 趋势成长的入场优先级是六项加权复合分，**不是**简单按总分。
//
// 其中 `ma30乖离` / `量比` / `rs20` 是 **low_first（低者优先）** ——
// 偏好"离 MA30 不远、量能温和、相对强度不极端"的入场点。
func TestTrendGrowthEntryPriorityPrefersCalmEntry(t *testing.T) {
	// 两只总分/结构完全相同的票，只有"入场热度"不同
	calm := prioRow("A", func(r *Row) {
		r.Close = 96 // ma30 乖离 +1.05%
		r.VolumeRatio20 = 0.9
		r.RelativeStrength20D = 1
	})
	hot := prioRow("B", func(r *Row) {
		r.Close = 115 // ma30 乖离 +21%
		r.VolumeRatio20 = 3.5
		r.RelativeStrength20D = 40
	})

	out := PrioritizeEntryPoints([]Row{hot, calm}, StrategyTrendGrowth, "")
	if len(out) != 2 {
		t.Fatalf("应返回 2 行，实际 %d", len(out))
	}
	if out[0].Code != "A" {
		t.Errorf("温和入场的应排在前面（三项 low_first 生效），实际首位 %s", out[0].Code)
	}
}

// 总分更高仍然有优势（0.25+0.25 的权重）。
func TestTrendGrowthEntryPriorityRespectsScore(t *testing.T) {
	weak := prioRow("A", func(r *Row) {
		r.TotalScore = 60
		r.StructureScore = 8
	})
	strong := prioRow("B", func(r *Row) {
		r.TotalScore = 92
		r.StructureScore = 18
	})
	out := PrioritizeEntryPoints([]Row{weak, strong}, StrategyTrendGrowth, "")
	if out[0].Code != "B" {
		t.Errorf("总分与结构分都高的应排前面，实际首位 %s", out[0].Code)
	}
}

// 非趋势成长策略不重排（原实现只对 trend_growth 走那条分支）。
func TestPrioritizeOnlyForTrendGrowth(t *testing.T) {
	rows := []Row{prioRow("B", nil), prioRow("A", nil)}
	out := PrioritizeEntryPoints(rows, StrategyExpert, "")
	if out[0].Code != "B" {
		t.Errorf("非趋势成长不该重排，实际首位 %s", out[0].Code)
	}
	// 空输入安全
	if got := PrioritizeEntryPoints(nil, StrategyTrendGrowth, ""); got != nil {
		t.Error("空输入应原样返回")
	}
}

func TestSeriesRankScore(t *testing.T) {
	values := []float64{10, 20, 30, 40}
	// high_first：最大的 rank 最高
	if got := seriesRankScore(values, 40, true); got != 1.0 {
		t.Errorf("high_first 最大值应为 1.0，实际 %v", got)
	}
	if got := seriesRankScore(values, 10, true); got != 0.25 {
		t.Errorf("high_first 最小值应为 0.25，实际 %v", got)
	}
	// low_first：最大的反而最低 = 1 - rank
	if got := seriesRankScore(values, 40, false); got != 0.0 {
		t.Errorf("low_first 最大值应为 0.0，实际 %v", got)
	}
	if got := seriesRankScore(values, 10, false); got != 0.75 {
		t.Errorf("low_first 最小值应为 0.75，实际 %v", got)
	}
}
