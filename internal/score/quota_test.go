package score

import "testing"

// U1：熊市**减半**目标只数与层数（原实现 `policy_from_strategy_config` :248-262）。
func TestBearHalvesDesiredCounts(t *testing.T) {
	// 目标 5 只 / 10 层
	bear := BuildStrategyConfig(StrategyTrendGrowth, StateBear, 5, 10, 3, "all")
	if bear.DesiredPositionCount != 3 { // (5+1)/2
		t.Errorf("熊市目标只数应为 3，实际 %d", bear.DesiredPositionCount)
	}
	if bear.DesiredTotalLayers != 5 { // (10+1)/2
		t.Errorf("熊市目标层数应为 5，实际 %d", bear.DesiredTotalLayers)
	}

	// 震荡/牛市不减
	for _, state := range []string{StateRange, StateBull} {
		cfg := BuildStrategyConfig(StrategyTrendGrowth, state, 5, 10, 3, "all")
		if cfg.DesiredPositionCount != 5 || cfg.DesiredTotalLayers != 10 {
			t.Errorf("%s 不该减半：只数 %d 层数 %d", state, cfg.DesiredPositionCount, cfg.DesiredTotalLayers)
		}
	}

	// 边界：target=1 → (1+1)/2 = 1，不能变成 0
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateBear, 1, 1, 3, "all")
	if cfg.DesiredPositionCount != 1 || cfg.DesiredTotalLayers != 1 {
		t.Errorf("最小值应保持 1，实际 %d / %d", cfg.DesiredPositionCount, cfg.DesiredTotalLayers)
	}
	// 边界：target=2 → (2+1)/2 = 1
	cfg = BuildStrategyConfig(StrategyTrendGrowth, StateBear, 2, 2, 3, "all")
	if cfg.DesiredPositionCount != 1 || cfg.DesiredTotalLayers != 1 {
		t.Errorf("target=2 减半应为 1，实际 %d / %d", cfg.DesiredPositionCount, cfg.DesiredTotalLayers)
	}
}

// U2：建仓层数按策略分派（原实现 `initial_entry_layers` :1438-1544）。
func TestInitialEntryLayersByStrategy(t *testing.T) {
	row := Row{Code: "600519", TotalScore: 80, Ma20: 100, Ma30: 95}

	// 趋势成长 + 震荡市 → 2 层（此前恒为 1）
	rangeCfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 5, 10, 3, "all")
	if got := initialEntryLayers(row, rangeCfg, 10); got != 2 {
		t.Errorf("趋势成长+震荡市应建 2 层，实际 %d", got)
	}
	// 趋势成长 + 牛市 → 1 层（牛市那 2 层要靠信号，本项目不可达）
	bullCfg := BuildStrategyConfig(StrategyTrendGrowth, StateBull, 5, 10, 3, "all")
	if got := initialEntryLayers(row, bullCfg, 10); got != 1 {
		t.Errorf("趋势成长+牛市（无信号）应建 1 层，实际 %d", got)
	}

	// 超跌反弹：score >= 70 → 2 层；否则 1 层
	oversoldCfg := BuildStrategyConfig(StrategyOversold, StateRange, 5, 10, 3, "all")
	high := Row{Code: "A", TotalScore: 75}
	if got := initialEntryLayers(high, oversoldCfg, 10); got != 2 {
		t.Errorf("超跌评分>=70 应建 2 层，实际 %d", got)
	}
	low := Row{Code: "B", TotalScore: 55}
	if got := initialEntryLayers(low, oversoldCfg, 10); got != 1 {
		t.Errorf("超跌评分<70 应建 1 层，实际 %d", got)
	}

	// 高手模拟恒 1 层
	expertCfg := BuildStrategyConfig(StrategyExpert, StateRange, 5, 10, 3, "all")
	if got := initialEntryLayers(high, expertCfg, 10); got != 1 {
		t.Errorf("高手模拟应建 1 层，实际 %d", got)
	}

	// 低吸 → max(1, min(2, max_layers))，与 max_layers 有关
	lowBuyCfg := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 2, "main_gem")
	if got := initialEntryLayers(row, lowBuyCfg, 10); got != 2 {
		t.Errorf("低吸 max_layers=2 应建 2 层，实际 %d", got)
	}
	lowBuyCfg.MaxLayersPerStock = 1
	if got := initialEntryLayers(row, lowBuyCfg, 10); got != 1 {
		t.Errorf("低吸 max_layers=1 应建 1 层，实际 %d", got)
	}

	// max_layers_per_stock <= 1 → 恒 1
	tightCfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 5, 10, 1, "all")
	if got := initialEntryLayers(row, tightCfg, 10); got != 1 {
		t.Errorf("单股上限 1 层时恒 1，实际 %d", got)
	}
}

// 低吸每只的层数按**动态目标只数**分摊（原实现 `_low_buy_entry_layer_count` :492-496）。
func TestLowBuyEntryLayerCount(t *testing.T) {
	// 10 层 / 2 只 → 每只 5 层
	if got := LowBuyEntryLayerCount(10, 2); got != 5 {
		t.Errorf("10/2 应为 5，实际 %d", got)
	}
	// 10 层 / 5 只 → 每只 2 层
	if got := LowBuyEntryLayerCount(10, 5); got != 2 {
		t.Errorf("10/5 应为 2，实际 %d", got)
	}
	// 10 层 / 20 只 → 整数除得 0 → 抬到 1
	if got := LowBuyEntryLayerCount(10, 20); got != 1 {
		t.Errorf("10/20 应抬到 1，实际 %d", got)
	}
	// 目标数 <= 0 → 0（原实现直接返回 0，表示不开仓）
	if got := LowBuyEntryLayerCount(10, 0); got != 0 {
		t.Errorf("目标 0 应返回 0，实际 %d", got)
	}
}
