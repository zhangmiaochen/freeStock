package score

import "testing"

// strongUniverse 造一份「广度充足」的全市场评分（全部 >=70）。
//
// 不能传全 0：广度闸门会把刚生成的加层行改写回 keep（原实现的行为）。
func strongUniverse(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 80
	}
	return out
}

// 回踩均线后企稳的判定（原实现 portfolio_rotation_logic.py:639-660）。
func TestIsConfirmedPullbackRebound(t *testing.T) {
	// ma=100：low 触及（<=103.5）、close 不低于 98.8、收在 99.7 上方
	// 阳线 → 通过
	if !isConfirmedPullbackRebound(99, 101, 99, 100, 1.0, 100) {
		t.Error("回踩均线后收阳应确认")
	}
	// 均线为 0 → 拒
	if isConfirmedPullbackRebound(99, 101, 99, 100, 1.0, 0) {
		t.Error("均线为 0 应拒绝")
	}
	// 没触及均线（low 远高于均线）→ 拒
	if isConfirmedPullbackRebound(110, 112, 110, 111, 1.0, 100) {
		t.Error("未触及均线应拒绝")
	}
	// 收盘跌破均线超过 1.2% → 拒
	if isConfirmedPullbackRebound(97, 98, 96, 97, -2.0, 100) {
		t.Error("收盘跌破均线过多应拒绝")
	}
}

// 加层判定：低吸策略永不加层；层数已达上限不加。
func TestCanAddSecondLayerBasics(t *testing.T) {
	lowBuyCfg := baseConfig(StrategyTrendLowBuy)
	lowBuyCfg.MaxLayersPerStock = 10
	if canAddSecondLayer(hold("600519", nil), lowBuyCfg) {
		t.Error("低吸策略不该加层")
	}

	cfg := baseConfig(StrategyTrendGrowth)
	cfg.MaxLayersPerStock = 2
	// 已经有 2 层 → 不加
	two := hold("600519", func(r *Row) { r.PositionLayers = 2 })
	if canAddSecondLayer(two, cfg) {
		t.Error("已到层数上限不该加层")
	}
	// 结构破坏 → 不加
	broken := hold("600519", func(r *Row) {
		r.PositionLayers = 1
		r.TotalScore = 30
	})
	if canAddSecondLayer(broken, cfg) {
		t.Error("结构破坏不该加层")
	}
	// 过热 → 不加
	hot := hold("600519", func(r *Row) {
		r.PositionLayers = 1
		r.RelativeStrength20D = 60
		r.DrawdownFromHigh120 = -1
	})
	if canAddSecondLayer(hot, cfg) {
		t.Error("过热不该加层")
	}
}

// **add_layer 必须真的会产出** —— 此前整段缺失，永远不会出现加层建议。
func TestAddLayerIsProduced(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateBull, 5, 10, 3, "")

	// 造一个"结构未破 + 强 + 回踩 MA20 后企稳"的持仓
	strong := Row{
		Code: "600519", Name: "贵州茅台",
		TotalScore: 88, TrendScore: 26, StructureScore: 17, RSScore: 14, CapitalScore: 12,
		Close: 102, High: 103, Low: 100.5, Open: 100.8,
		Ma5: 101, Ma20: 100, Ma30: 98, Ma60: 95, Ma120: 92, Ma250: 88,
		Ma5Prev: 100, Ma250Prev: 86,
		PctChg: 1.2, VolumeRatio20: 1.3, VolumeRatio520: 1.1,
		RelativeStrength20D: 12, DrawdownFromHigh120: -6, DrawdownFromHigh20: -4,
		ReboundFromLow60: 0.6, StockReturn3D: 2, StockReturn5D: 4, StockReturn10D: 8,
		PositionLayers: 1, EntryPrice: 95, CurrentPrice: 102, HighestPrice: 104,
		ProfitLossPct: 7.4, HoldingTradeDays: 10,
	}

	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{strong}, Config: cfg, MarketState: StateBull,
		MaxStockLimit: 5, UniverseScores: strongUniverse(200),
	})

	found := false
	for _, row := range plan.Rows {
		if row.Action == ActionAddLayer {
			found = true
			if row.DecisionState != StateRepairAddSecondLayer {
				t.Errorf("加层行的决策状态应为 repair_add_second_layer，实际 %s", row.DecisionState)
			}
			if row.TargetLayers != 2 || row.LayersDelta != 1 {
				t.Errorf("1 层加第二层应变为 2 层，实际 %+v", row)
			}
			if row.ActionLabel != "加1层" {
				t.Errorf("标签应为「加1层」，实际 %q", row.ActionLabel)
			}
			t.Logf("加层建议: %s %s 现 %d 层 → %d 层（%s）",
				row.ActionLabel, row.Code, row.CurrentLayers, row.TargetLayers, row.Reason)
		}
	}
	if !found {
		t.Errorf("强势持仓回踩均线后应产出加层建议，实际动作: %v", plan.Rows)
	}
}

// 低吸策略整段跳过加层阶段。
func TestAddLayerSkippedForLowBuy(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendLowBuy, StateBull, 5, 10, 10, "main_gem")
	row := hold("600519", func(r *Row) {
		r.Close = 100
		r.Ma30 = 90
		r.PositionLayers = 1
	})
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{row}, Config: cfg, MarketState: StateBull,
		MaxStockLimit: 5, UniverseScores: make([]float64, 200),
		LowBuyBreadthReadyCount: 2000,
	})
	for _, r := range plan.Rows {
		if r.Action == ActionAddLayer {
			t.Errorf("低吸策略不该加层: %+v", r)
		}
	}
}
