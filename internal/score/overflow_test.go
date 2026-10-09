package score

import "testing"

// U3：overflow 补位 —— 组合没填满时用更严的门槛补几只。
//
// 原实现 portfolio_rotation_logic.py:2575-2625。
// **此前整段缺失**，组合长期半仓。
func TestOverflowFillsUnfilledPortfolio(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "all")

	// 高分候选充足：正常建仓只能建到 target=3，剩下的靠 overflow 补
	var candidates []Row
	for i := 0; i < 30; i++ {
		row := cand("Y"+ftoa(float64(i), 0), 95)
		row.TrendScore = 25
		row.StructureScore = 15
		row.RSScore = 13
		row.RelativeStrength20D = 10
		row.Close = 100
		row.Ma20 = 100
		row.Ma30 = 100
		candidates = append(candidates, row)
	}

	plan := BuildRotationPlan(RotationInput{
		Holdings: nil, Candidates: candidates,
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
	})

	overflow := 0
	normal := 0
	for _, row := range plan.Rows {
		if row.Action != ActionAdd {
			continue
		}
		if row.Reason == "补位建仓" {
			overflow++
		} else {
			normal++
		}
	}
	if normal != 3 {
		t.Errorf("正常建仓应为 target=3，实际 %d", normal)
	}
	if overflow == 0 {
		t.Fatalf("候选充足时应触发补位，实际 0（上限 %d）", 5+7-3)
	}
	// 上限 = max_stock_limit + overflow_limit(7) - 已开只数
	if normal+overflow > 5+7 {
		t.Errorf("总开仓不该超过 max_stock_limit+7=%d，实际 %d", 5+7, normal+overflow)
	}
	t.Logf("正常建仓 %d 只 + 补位 %d 只 = %d 只", normal, overflow, normal+overflow)
}

// 熊市**不补位**（overflow_position_limit = 0）。
func TestOverflowDisabledInBear(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateBear, 3, 10, 3, "all")
	if cfg.AllowNewAdds {
		t.Skip("熊市该策略 allow_new_adds 为 true 时才需要测")
	}
	var candidates []Row
	for i := 0; i < 30; i++ {
		row := cand("Y"+ftoa(float64(i), 0), 95)
		row.TrendScore = 25
		row.StructureScore = 15
		row.RSScore = 13
		candidates = append(candidates, row)
	}
	plan := BuildRotationPlan(RotationInput{
		Holdings: nil, Candidates: candidates,
		Config: cfg, MarketState: StateBear, MaxStockLimit: 5,
	})
	for _, row := range plan.Rows {
		if row.Reason == "补位建仓" {
			t.Errorf("熊市不该补位: %+v", row)
		}
	}
}

// 补位门槛**更严**：只比 ready_score 高一点点的不该被补进来。
func TestOverflowRequiresHigherQuality(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "all")

	// 刚好过 normal 门槛（cfg.ReadyScore=74），但达不到 overflow 的 max(74+8, 84)
	weak := func(code string) Row {
		row := cand(code, cfg.ReadyScore+1)
		row.TrendScore = 20
		row.StructureScore = 10
		row.RSScore = 8
		row.RelativeStrength20D = 0
		row.Close = 100
		row.Ma20 = 100
		row.Ma30 = 100
		return row
	}
	// 先放 3 只占满正常额度，后面全是不达标的 → 不该有补位
	var candidates []Row
	for i := 0; i < 10; i++ {
		candidates = append(candidates, weak("W"+ftoa(float64(i), 0)))
	}
	plan := BuildRotationPlan(RotationInput{
		Holdings: nil, Candidates: candidates,
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
	})
	for _, row := range plan.Rows {
		if row.Reason == "补位建仓" {
			t.Errorf("不达补位门槛的候选不该被补进来: score=%.1f ready=%.1f", row.Score, cfg.ReadyScore)
		}
	}
}
