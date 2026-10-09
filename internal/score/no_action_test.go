package score

import (
	"strings"
	"testing"
)

// U7 后半：解释"为什么没有建议"（原实现 `_build_no_action_reasons` :1906-1995）。
//
// 只在"候选池非空、且没有任何改仓动作"时才有值，最多 3 条。
func TestNoActionReasonsWhenBreadthBlocks(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "all")
	// 广度不足：全市场 200 只但都低于 ready 线
	scores := make([]float64, 200)
	for i := range scores {
		scores[i] = 10
	}
	holdings := []Row{hold("600519", nil)}

	plan := BuildRotationPlan(RotationInput{
		Holdings: holdings, Candidates: []Row{cand("A", 80)},
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
		UniverseScores: scores,
	})

	if len(plan.NoActionReasons) == 0 {
		t.Fatalf("广度不足时应给出解释，实际为空（动作 %d 条）", len(plan.Rows))
	}
	joined := strings.Join(plan.NoActionReasons, " | ")
	if !strings.Contains(joined, "市场广度不足") {
		t.Errorf("应说明广度不足，实际：%s", joined)
	}
	if len(plan.NoActionReasons) > 3 {
		t.Errorf("最多 3 条，实际 %d", len(plan.NoActionReasons))
	}
	t.Logf("解释：%v", plan.NoActionReasons)
}

// 已有改仓动作时**不该**给"没有建议"的解释。
func TestNoActionReasonsEmptyWhenActionsExist(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "all")
	// 一只结构破坏的持仓 → 会产生 clear 动作
	broken := hold("600519", func(r *Row) { r.TotalScore = 20 })

	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{broken}, Candidates: []Row{cand("A", 90)},
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
		UniverseScores: make([]float64, 200),
	})
	if countPositionChangeActions(plan.Rows) == 0 {
		t.Fatalf("这只持仓应当被判清仓，实际动作 %+v", plan.Rows)
	}
	if len(plan.NoActionReasons) != 0 {
		t.Errorf("有改仓动作时不该给'没有建议'的解释，实际 %v", plan.NoActionReasons)
	}
}

// 候选池为空时也不需要解释（那是"数据没准备好"，不是"策略判断"）。
func TestNoActionReasonsEmptyWithoutCandidates(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "all")
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("600519", nil)}, Candidates: nil,
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
		UniverseScores: make([]float64, 200),
	})
	if len(plan.NoActionReasons) != 0 {
		t.Errorf("候选池为空时不该给解释，实际 %v", plan.NoActionReasons)
	}
}

// `_dedupe_reasons` 去重且保持顺序。
func TestDedupeReasons(t *testing.T) {
	got := dedupeReasons([]string{"a", "b", "a", "", "  ", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("应为 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项应为 %q，实际 %q", i, want[i], got[i])
		}
	}
}

// 改仓动作计数：keep / observe 不计。
func TestCountPositionChangeActions(t *testing.T) {
	rows := []RotationRow{
		{Action: ActionKeep}, {Action: ActionObserve},
		{Action: ActionClear}, {Action: ActionAdd}, {Action: ActionAddLayer},
		{Action: ""},
	}
	if got := countPositionChangeActions(rows); got != 3 {
		t.Errorf("应计 3 条改仓动作，实际 %d", got)
	}
}
