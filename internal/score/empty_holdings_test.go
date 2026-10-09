package score

import "testing"

// 回测是**从纯现金开始的**（原实现 `positions = {}`），所以"第一笔买入"完全
// 依赖 `BuildRotationPlan` 在**空持仓**下产出 `add` 行。
//
// 如果空持仓 + 有候选 却产出不了 add，回测就永远是零成交 ——
// 这正是用户报的现象。这个测试把这条链路单独拎出来验证。
func TestEmptyHoldingsProducesAddRows(t *testing.T) {
	candidates := []Row{
		cand("600519", 95), cand("000001", 92), cand("600036", 90),
		cand("000002", 88), cand("601318", 86),
	}
	// 广度给足，避免被广度闸门拦掉
	universe := make([]float64, 300)
	for i := range universe {
		universe[i] = 90
	}

	for _, state := range []string{StateBull, StateRange, StateBear} {
		cfg := BuildStrategyConfig(StrategyTrendGrowth, state, 5, 10, 3, "all")
		plan := BuildRotationPlan(RotationInput{
			Holdings: nil, Candidates: candidates, Config: cfg,
			MarketState: state, MaxStockLimit: 8, UniverseScores: universe,
		})
		adds := 0
		for _, row := range plan.Rows {
			if row.Action == ActionAdd || row.Action == ActionReplaceIn {
				adds++
			}
		}
		t.Logf("state=%-5s allowNewAdds=%-5v entryAllowed=%-5v 动作 %d 条，其中可买入 %d 条",
			state, cfg.AllowNewAdds, plan.EntryAllowed, len(plan.Rows), adds)
		if state == StateBear {
			// 熊市不开新仓是**策略设定**，这里只记录不报错
			continue
		}
		if adds == 0 {
			t.Errorf("state=%s 时空持仓应当产出可买入的行，实际 0 条（动作 %+v）",
				state, plan.Rows)
		}
	}
}

// 空持仓时 `DesiredPositionCount` / `desiredTotalLayers` 是否给出开仓空间。
func TestEmptyHoldingsTargetCount(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 5, 10, 3, "all")
	t.Logf("目标只数=%d 目标层数=%d allowNewAdds=%v",
		cfg.DesiredPositionCount, cfg.DesiredTotalLayers, cfg.AllowNewAdds)
	if cfg.DesiredPositionCount <= 0 {
		t.Errorf("range 状态下目标只数应 > 0，实际 %d", cfg.DesiredPositionCount)
	}
	if cfg.DesiredTotalLayers <= 0 {
		t.Errorf("range 状态下目标层数应 > 0，实际 %d", cfg.DesiredTotalLayers)
	}
}
