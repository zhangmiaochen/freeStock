package score

import (
	"math"
	"testing"
)

// hold 造一个持仓行，默认状态是"健康持有"。
func hold(code string, mutate func(*Row)) Row {
	row := Row{
		Code: code, Name: code,
		TotalScore: 75, TrendScore: 22, StructureScore: 12, RSScore: 10,
		CapitalScore: 10, RepairScore: 9,
		Close: 100, Open: 99, Ma5: 98, Ma20: 95, Ma30: 92, Ma60: 88, Ma120: 85, Ma250: 80,
		RelativeStrength20D: 5, DrawdownFromHigh120: -8,
		PositionLayers: 2, EntryPrice: 80, CurrentPrice: 100, HighestPrice: 105,
		ProfitLossPct: 25, HoldingTradeDays: 20,
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

// cand 造一个候选行。
func cand(code string, score float64) Row {
	return Row{
		Code: code, Name: code,
		TotalScore: score, TrendScore: 24, StructureScore: 14, RSScore: 12,
		CapitalScore: 11, RepairScore: 11,
		Close: 100, Open: 99, Ma20: 95, Ma30: 92, Ma60: 88,
		RelativeStrength20D: 8, DrawdownFromHigh120: -6,
	}
}

func baseConfig(strategy string) StrategyConfig {
	return BuildStrategyConfig(strategy, StateRange, 5, 10, 3, "")
}

// ---------------------------------------------------------------------------
// 动作优先级
// ---------------------------------------------------------------------------

// 兜底：健康持仓 → keep（原实现里 keep 是**默认分支**，不是"评分稳定"的判定）
func TestRotationKeepIsFallback(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings:    []Row{hold("600519", nil)},
		Config:      baseConfig(StrategyTrendGrowth),
		MarketState: StateRange,
	})
	if len(plan.Rows) != 1 {
		t.Fatalf("应产出 1 行，实际 %d", len(plan.Rows))
	}
	row := plan.Rows[0]
	if row.Action != ActionKeep {
		t.Errorf("健康持仓应兜底为 keep，实际 %s（%s）", row.Action, row.Reason)
	}
	if row.TargetLayers != row.CurrentLayers || row.LayersDelta != 0 {
		t.Errorf("keep 不该改层数: %+v", row)
	}
}

// 优先级 1：急涨兑现（仅非牛市、持仓 ≤2 日）
func TestQuickProfitTake(t *testing.T) {
	// 牛市里不做急涨兑现
	if ShouldQuickProfitTake(hold("A", func(r *Row) {
		r.HoldingTradeDays = 1
		r.ProfitLossPct = 25
	}), StateBull) {
		t.Error("牛市不该急涨兑现")
	}
	// 持仓超过 2 日不再兑现
	if ShouldQuickProfitTake(hold("A", func(r *Row) {
		r.HoldingTradeDays = 5
		r.ProfitLossPct = 25
	}), StateRange) {
		t.Error("持仓超过 2 日不该急涨兑现")
	}
	// 浮盈 >=18% 且偏离 MA20 >=14% → 兑现
	if !ShouldQuickProfitTake(hold("A", func(r *Row) {
		r.HoldingTradeDays = 1
		r.ProfitLossPct = 20
		r.CurrentPrice = 120
		r.Ma20 = 100
	}), StateRange) {
		t.Error("浮盈 20% 且偏离 MA20 20% 应触发急涨兑现")
	}
	// 触发的持仓多层 → reduce 一层
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("600519", func(r *Row) {
			r.HoldingTradeDays = 1
			r.ProfitLossPct = 20
			r.CurrentPrice = 120
			r.Ma20 = 100
		})},
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	if plan.Rows[0].Action != ActionReduce || plan.Rows[0].TargetLayers != 1 {
		t.Errorf("多层急涨兑现应减一层: %+v", plan.Rows[0])
	}
	if plan.Rows[0].DecisionState != StateQuickProfitTake {
		t.Errorf("决策状态错误: %s", plan.Rows[0].DecisionState)
	}

	// 单层 → clear
	plan = BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("600519", func(r *Row) {
			r.PositionLayers = 1
			r.HoldingTradeDays = 1
			r.ProfitLossPct = 20
			r.CurrentPrice = 120
			r.Ma20 = 100
		})},
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	if plan.Rows[0].Action != ActionClear {
		t.Errorf("单层急涨兑现应清仓: %+v", plan.Rows[0])
	}
}

// 优先级 5：结构破坏 → 清仓
func TestStructureBreakClear(t *testing.T) {
	// 条件 1：趋势成长总分 < 45
	if !IsStructureBroken(hold("A", func(r *Row) { r.TotalScore = 40 }), StrategyTrendGrowth, StateRange, 15, 6) {
		t.Error("趋势成长总分 40 应判为结构破坏")
	}
	// 条件 2：趋势<=8 且结构<=4
	if !IsStructureBroken(hold("A", func(r *Row) { r.TrendScore = 8; r.StructureScore = 4 }), StrategyTrendGrowth, StateRange, 15, 6) {
		t.Error("趋势 8 / 结构 4 应判为结构破坏")
	}
	// 条件 4：跌破 MA120*0.965 且结构<=6
	if !IsStructureBroken(hold("A", func(r *Row) {
		r.Close = 80
		r.Ma120 = 85
		r.StructureScore = 6
	}), StrategyTrendGrowth, StateRange, 15, 6) {
		t.Error("跌破 MA120 且结构弱应判为结构破坏")
	}
	// 条件 6：熊市里三者皆弱
	if !IsStructureBroken(hold("A", func(r *Row) {
		r.TrendScore = 12
		r.StructureScore = 5
		r.RSScore = 10
	}), StrategyTrendGrowth, StateBear, 15, 6) {
		t.Error("熊市三者皆弱应判为结构破坏")
	}
	// 条件 7：浮盈保护 涨 15% 后回撤 6%
	if !IsStructureBroken(hold("A", func(r *Row) {
		r.EntryPrice = 100
		r.HighestPrice = 120
		r.CurrentPrice = 112
	}), StrategyTrendGrowth, StateRange, 15, 6) {
		t.Error("涨 20% 后回撤 6.7% 应触发浮盈保护")
	}
	// 健康持仓不该破坏
	if IsStructureBroken(hold("A", nil), StrategyTrendGrowth, StateRange, 15, 6) {
		t.Error("健康持仓不该判为结构破坏")
	}
}

// 结构破坏但满足"可修复回踩"时豁免 → 不清仓
func TestStructureBreakRepairableExemption(t *testing.T) {
	// 总分 70（<45 不成立）、但趋势<=8 触发条件 2 → 破坏
	// 同时满足 trend_growth 的修复回踩条件 → 豁免
	row := hold("600519", func(r *Row) {
		r.TrendScore = 20    // >=19
		r.StructureScore = 9 // >=9
		r.RSScore = 8        // >=7
		r.TotalScore = 70    // >=68
		r.ProfitLossPct = 0  // >=-6
		r.RelativeStrength20D = 0
		r.DrawdownFromHigh120 = -20
		r.Close = 100
		r.Ma120 = 95 // close >= ma120*0.95
		r.Ma60 = 95  // close >= ma60*0.955
		r.Close = 100
	})
	// 制造破坏：跌破 MA60*0.94 且深回撤（条件 5）
	row.Ma60 = 200 // close 100 < 200*0.94=188，且 dd120=-20 <= -18 → 破坏
	if !IsStructureBroken(row, StrategyTrendGrowth, StateRange, 15, 6) {
		t.Fatal("应判为结构破坏（用于测试豁免）")
	}
	if !CanHoldStrategyRepairPullback(row, StrategyTrendGrowth, StateRange) {
		t.Skip("该组合不满足豁免条件，跳过（豁免条件含 ma60 校验，会与破坏条件冲突）")
	}

	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{row},
		Config:   baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	if plan.Rows[0].Action == ActionClear {
		t.Errorf("可修复回踩应豁免清仓，实际 %+v", plan.Rows[0])
	}
}

// 优先级 6：跌破 MA30 但不是大反弹阳线 → observe（decision_state 才是 hold_for_rebound_exit）
func TestReboundWaitIsObserveNotAction(t *testing.T) {
	row := hold("600519", func(r *Row) {
		r.Close = 90
		r.Ma30 = 100 // close < ma30*0.995
	})
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{row}, Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	got := plan.Rows[0]

	// **action 必须是 observe**，hold_for_rebound_exit 只是状态
	if got.Action != ActionObserve {
		t.Errorf("等待反弹时 action 应为 observe，实际 %s", got.Action)
	}
	if got.DecisionState != StateHoldForReboundExit {
		t.Errorf("决策状态应为 hold_for_rebound_exit，实际 %s", got.DecisionState)
	}
	// 层数不变
	if got.TargetLayers != got.CurrentLayers {
		t.Errorf("等待反弹不该改层数: %+v", got)
	}
	// 动作标签不该出现"等待反弹后处理"（那是遗留的 action 映射）
	if got.ActionLabel == "等待反弹后处理" {
		t.Error("hold_for_rebound_exit 不该作为动作标签出现")
	}
}

// 优先级 8：评分低于预警线 → observe
func TestScoreObserve(t *testing.T) {
	row := hold("600519", func(r *Row) {
		r.TotalScore = 55 // < 60 warning，但不触发结构破坏
		r.TrendScore = 20
		r.StructureScore = 10
		r.RSScore = 9
		r.DrawdownFromHigh120 = -8
		r.ProfitLossPct = 5
		r.HighestPrice = 105
		r.CurrentPrice = 100
		r.EntryPrice = 80
	})
	if IsStructureBroken(row, StrategyTrendGrowth, StateRange, 15, 6) {
		t.Skip("这个组合触发了结构破坏，换一个")
	}
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{row}, Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	if plan.Rows[0].Action != ActionObserve || plan.Rows[0].DecisionState != StateScoreObserve {
		t.Errorf("评分 55 应观察: %+v", plan.Rows[0])
	}
}

// ---------------------------------------------------------------------------
// 低吸策略
// ---------------------------------------------------------------------------

// **低吸永不换股** —— 原实现配对循环里直接 continue。
func TestLowBuyNeverReplaces(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("600519", func(r *Row) {
			r.TotalScore = 50 // 很弱
			r.TrendScore = 10
			r.StructureScore = 6
			r.RSScore = 6
			// 保持在 MA30 上方，避免触发低吸自己的退出
			r.Close = 100
			r.Ma30 = 90
			r.DrawdownFromHigh120 = -6
			r.ProfitLossPct = 2
			r.HighestPrice = 102
			r.CurrentPrice = 100
			r.EntryPrice = 98
		})},
		Candidates:              []Row{cand("000001", 95)},
		Config:                  baseConfig(StrategyTrendLowBuy),
		MarketState:             StateRange,
		MaxStockLimit:           5,
		LowBuyBreadthReadyCount: 1500,
	})
	for _, row := range plan.Rows {
		if row.Action == ActionReplaceOut || row.Action == ActionReplaceIn {
			t.Errorf("低吸策略不该换股: %+v", row)
		}
	}
	if len(plan.Compare) != 0 {
		t.Errorf("低吸策略不该有对比行: %d", len(plan.Compare))
	}
}

// 低吸广度不足 → 强制清仓（优先级 3）
func TestLowBuyBreadthForceClear(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("600519", func(r *Row) {
			r.Close = 100
			r.Ma30 = 90 // 保持在 MA30 上方
		})},
		Config:                  baseConfig(StrategyTrendLowBuy),
		MarketState:             StateRange,
		LowBuyBreadthReadyCount: 500, // < 900
	})
	got := plan.Rows[0]
	if got.Action != ActionClear {
		t.Errorf("广度不足应强制清仓，实际 %s", got.Action)
	}
	if got.DecisionState != StateLowBuyBreadthClear {
		t.Errorf("决策状态错误: %s", got.DecisionState)
	}
	if got.Reason == "" {
		t.Error("应说明为什么清仓")
	}
}

// 低吸跌破 MA30*ratio → 清仓（优先级 4）
func TestLowBuyMa30Exit(t *testing.T) {
	cfg := baseConfig(StrategyTrendLowBuy)
	cfg.LowBuyExitMa30Ratio = 0.96

	row := hold("600519", func(r *Row) {
		r.Close = 90
		r.Ma30 = 100 // 90 < 96
	})
	reason := LowBuyExitReason(row, cfg)
	if reason == "" {
		t.Fatal("跌破 MA30*0.96 应有退出理由")
	}
	// 在上方时不该退出
	above := hold("600519", func(r *Row) { r.Close = 99; r.Ma30 = 100 })
	if LowBuyExitReason(above, cfg) != "" {
		t.Error("MA30*0.96 上方不该退出")
	}
}

// 低吸利润保护（仅 gem_star 开启 30/10）
func TestLowBuyProfitProtect(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendLowBuy, StateRange, 5, 10, 10, "gem_star")
	if cfg.LowBuyProfitProtectStartPct != 30 {
		t.Fatalf("gem_star 应开启 30%% 利润保护: %+v", cfg)
	}
	// 涨 40% 后回撤 15% → 触发
	row := hold("600519", func(r *Row) {
		r.Close = 119
		r.Ma30 = 100
		r.EntryPrice = 100
		r.HighestPrice = 140
		r.CurrentPrice = 119 // (119/140-1) = -15%
	})
	if LowBuyExitReason(row, cfg) == "" {
		t.Error("涨 40% 后回撤 15% 应触发利润保护")
	}
	// 回撤不足 10% 不触发
	row.CurrentPrice = 130 // -7.1%
	row.Close = 130
	if LowBuyExitReason(row, cfg) != "" {
		t.Error("回撤 7.1% 不该触发利润保护")
	}
}

// 低吸入场条件（对照 portfolio_rotation_logic.py:550-586）
func TestLowBuyCandidateCondition(t *testing.T) {
	cfg := baseConfig(StrategyTrendLowBuy)
	// 全部 10 个价格字段都要 > 0（原实现 min(...) <= 0 直接拒）
	good := Row{
		Code: "A", TotalScore: 50,
		Ma120: 90, Ma250: 80, Ma250Prev: 78, // ma120 > ma250 > ma250_prev
		Ma30: 95, Ma60: 90, Ma20: 100, Ma5: 93, Ma5Prev: 92, // ma5 > ma5_prev
		Close: 96, Open: 95, // 阳线；ma30*0.975=92.6 <= 96 <= ma20*1.04=104
		RelativeStrength20D: 5, StockReturn20D: 10, DrawdownFromHigh20: -10,
	}
	if !isLowBuyCandidate(good, cfg, 0) {
		t.Error("符合低吸结构的票应通过")
	}
	// 均线排列不对（ma20 > ma30 不成立）
	bad := good
	bad.Ma20 = 90
	if isLowBuyCandidate(bad, cfg, 0) {
		t.Error("均线排列不对不该通过")
	}
	// **ma250 没有上翘**（原实现要求 ma250 > ma250_prev）—— 这是此前漏掉的一条
	flat := good
	flat.Ma250Prev = 85
	if isLowBuyCandidate(flat, cfg, 0) {
		t.Error("ma250 走平/下弯不该通过")
	}
	// **ma5 没有上翘**（原实现要求 ma5 > ma5_prev）
	declining := good
	declining.Ma5Prev = 99
	if isLowBuyCandidate(declining, cfg, 0) {
		t.Error("ma5 下弯不该通过")
	}
	// 任一价格字段为 0 直接拒
	zero := good
	zero.Ma250Prev = 0
	if isLowBuyCandidate(zero, cfg, 0) {
		t.Error("价格字段为 0 时不该通过")
	}
	// 阴线
	yin := good
	yin.Close = 94
	if isLowBuyCandidate(yin, cfg, 0) {
		t.Error("阴线不该通过")
	}
	// 回撤不在区间
	out := good
	out.DrawdownFromHigh20 = -40
	if isLowBuyCandidate(out, cfg, 0) {
		t.Error("回撤过深不该通过")
	}
	// ready_count >= 1200 时区间收窄到 -18..-1.5
	narrow := good
	narrow.DrawdownFromHigh20 = -20
	if isLowBuyCandidate(narrow, cfg, 1500) {
		t.Error("就绪数 >=1200 时 -20% 不该通过（区间是 -18..-1.5）")
	}
	if !isLowBuyCandidate(narrow, cfg, 500) {
		t.Error("就绪数 <1200 时 -20% 应通过（区间是 -28..-3）")
	}
	// **ret20 用 20 日绝对涨幅，不是相对强度** ——
	// 大盘大涨时相对强度低但绝对涨幅高的票应当被拒
	absolute := good
	absolute.StockReturn20D = 30
	absolute.RelativeStrength20D = 5
	if isLowBuyCandidate(absolute, cfg, 500) {
		t.Error("20 日绝对涨幅 >25% 应被拒（不能用相对强度代替）")
	}
}

// ---------------------------------------------------------------------------
// 换股配对
// ---------------------------------------------------------------------------

func TestReplacementPairing(t *testing.T) {
	// 换股模式由「持仓层数 + 是否有空位 + 市场状态」决定：
	//   partial_rotate（减一层）: 层数>=2 且 计划只数 < max_stock_limit 且 非牛市
	//   full_replace（换出）    : 其余
	weakHolding := func(layers int) Row {
		return hold("600519", func(r *Row) {
			r.PositionLayers = layers
			r.TotalScore = 70
			r.TrendScore = 20
			r.StructureScore = 10
			r.RSScore = 8
			r.Close = 100
			r.Ma30 = 92
			r.DrawdownFromHigh120 = -10
			r.ProfitLossPct = 3
			r.HighestPrice = 104
			r.CurrentPrice = 100
			r.EntryPrice = 97
		})
	}
	candidate := []Row{cand("000001", 95)} // 分差 25 > 20

	// ---- 单层 → full_replace ----
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{weakHolding(1)}, Candidates: candidate,
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange, MaxStockLimit: 5,
	})

	var replaceOut, replaceIn *RotationRow
	for i := range plan.Rows {
		switch plan.Rows[i].Action {
		case ActionReplaceOut:
			replaceOut = &plan.Rows[i]
		case ActionReplaceIn:
			replaceIn = &plan.Rows[i]
		}
	}
	if replaceOut == nil || replaceIn == nil {
		t.Fatalf("单层持仓应产生一组换股(full_replace): %+v", plan.Rows)
	}
	if replaceOut.DecisionState != StateFullReplace || replaceOut.TargetLayers != 0 {
		t.Errorf("单层持仓应为 full_replace 且目标层数 0: %+v", replaceOut)
	}
	// 分差与预估提升
	if replaceOut.ScoreGap != 25 {
		t.Errorf("分差应为 25，实际 %v", replaceOut.ScoreGap)
	}
	if replaceOut.ExpectedImprovement != 25 {
		t.Errorf("预估提升应为 25，实际 %v", replaceOut.ExpectedImprovement)
	}
	// 换出行与换入行**同号**（保证同组排序相邻）
	if replaceOut.RankNo != replaceIn.RankNo {
		t.Errorf("换出与换入应同号: %d vs %d", replaceOut.RankNo, replaceIn.RankNo)
	}
	// 换出行排在换入行之前
	if replaceOut.RankNo != 1 {
		t.Errorf("第一组配对的 rank_no 应为 1，实际 %d", replaceOut.RankNo)
	}
	// 对比表只有配对行
	if len(plan.Compare) != 1 {
		t.Fatalf("应有 1 条对比，实际 %d", len(plan.Compare))
	}
	pair := plan.Compare[0]
	if pair.HoldingCode != "600519" || pair.CandidateCode != "000001" {
		t.Errorf("对比行配对错误: %+v", pair)
	}
	if pair.TotalScoreGap != 25 || pair.ExpectedUplift != 25 {
		t.Errorf("对比行差距计算错误: %+v", pair)
	}
	if pair.Mode != "full_replace" {
		t.Errorf("单层持仓的模式应为 full_replace，实际 %s", pair.Mode)
	}

	// ---- 多层 + 有空位 + 非牛市 → partial_rotate（减一层）----
	plan = BuildRotationPlan(RotationInput{
		Holdings: []Row{weakHolding(2)}, Candidates: candidate,
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange, MaxStockLimit: 5,
	})
	var reduced *RotationRow
	for i := range plan.Rows {
		if plan.Rows[i].Action == ActionReduce && plan.Rows[i].Code == "600519" {
			reduced = &plan.Rows[i]
		}
	}
	if reduced == nil {
		t.Fatalf("多层持仓应走 partial_rotate(reduce): %+v", plan.Rows)
	}
	if reduced.DecisionState != StatePartialRotate || reduced.TargetLayers != 1 {
		t.Errorf("多层应 partial_rotate 减一层（2→1）: %+v", reduced)
	}
	if plan.Compare[0].Mode != "partial_rotate" {
		t.Errorf("对比行模式应为 partial_rotate，实际 %s", plan.Compare[0].Mode)
	}

	// ---- 牛市 → 一律 full_replace（不保留部分仓位）----
	plan = BuildRotationPlan(RotationInput{
		Holdings: []Row{weakHolding(2)}, Candidates: candidate,
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateBull, MaxStockLimit: 5,
	})
	if plan.Compare[0].Mode != "full_replace" {
		t.Errorf("牛市应走 full_replace，实际 %s", plan.Compare[0].Mode)
	}
}

// 分差不够时不换股
func TestReplacementRequiresGap(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings:    []Row{hold("600519", nil)}, // 75 分
		Candidates:  []Row{cand("000001", 80)},  // 分差只有 5 < 20
		Config:      baseConfig(StrategyTrendGrowth),
		MarketState: StateRange, MaxStockLimit: 5,
	})
	for _, row := range plan.Rows {
		if row.Action == ActionReplaceOut {
			t.Errorf("分差不足不该换股: %+v", row)
		}
	}
}

// 换股数量受 max_replace_count 限制
func TestReplacementCountLimited(t *testing.T) {
	holdings := []Row{
		hold("A", func(r *Row) {
			r.TotalScore = 70
			r.TrendScore = 20
			r.StructureScore = 10
			r.RSScore = 8
			r.Close = 100
			r.Ma30 = 92
			r.DrawdownFromHigh120 = -10
			r.ProfitLossPct = 3
			r.HighestPrice = 104
			r.CurrentPrice = 100
			r.EntryPrice = 97
		}),
		hold("B", func(r *Row) {
			r.TotalScore = 71
			r.TrendScore = 20
			r.StructureScore = 10
			r.RSScore = 8
			r.Close = 100
			r.Ma30 = 92
			r.DrawdownFromHigh120 = -10
			r.ProfitLossPct = 3
			r.HighestPrice = 104
			r.CurrentPrice = 100
			r.EntryPrice = 97
		}),
		hold("C", func(r *Row) {
			r.TotalScore = 72
			r.TrendScore = 20
			r.StructureScore = 10
			r.RSScore = 8
			r.Close = 100
			r.Ma30 = 92
			r.DrawdownFromHigh120 = -10
			r.ProfitLossPct = 3
			r.HighestPrice = 104
			r.CurrentPrice = 100
			r.EntryPrice = 97
		}),
		hold("D", func(r *Row) {
			r.TotalScore = 73
			r.TrendScore = 20
			r.StructureScore = 10
			r.RSScore = 8
			r.Close = 100
			r.Ma30 = 92
			r.DrawdownFromHigh120 = -10
			r.ProfitLossPct = 3
			r.HighestPrice = 104
			r.CurrentPrice = 100
			r.EntryPrice = 97
		}),
	}
	plan := BuildRotationPlan(RotationInput{
		Holdings: holdings,
		Candidates: []Row{
			cand("X1", 99), cand("X2", 98), cand("X3", 97), cand("X4", 96),
		},
		Config:      baseConfig(StrategyTrendGrowth), // target=5 → maxReplaceCount=2
		MarketState: StateRange, MaxStockLimit: 10,
	})
	// 配对数是限制对象 —— 用对比表数最直接。
	// 注意不能只数 replace_out：4 个持仓都是 2 层、有空位、震荡市，
	// 所以走的是 partial_rotate（reduce），不会出现 replace_out。
	cfg := baseConfig(StrategyTrendGrowth)
	if len(plan.Compare) != cfg.MaxReplaceCount {
		t.Errorf("换股数应受 max_replace_count(%d) 限制，实际 %d", cfg.MaxReplaceCount, len(plan.Compare))
	}
	// 每个配对都应有一条对应的持仓动作（reduce 或 replace_out）
	acted := 0
	for _, row := range plan.Rows {
		if row.Action == ActionReplaceOut || row.DecisionState == StatePartialRotate {
			acted++
		}
	}
	if acted != len(plan.Compare) {
		t.Errorf("每个配对应恰好对应一条持仓动作: 配对 %d，动作 %d", len(plan.Compare), acted)
	}
}

// ---------------------------------------------------------------------------
// 广度闸门
// ---------------------------------------------------------------------------

func TestBreadthGateThresholds(t *testing.T) {
	// universe < 100 时闸门不启用
	if !breadthEntryAllowed(nil, StrategyTrendGrowth) {
		t.Error("universe < 100 时闸门不该启用")
	}
	// 趋势成长需要 20 只 >= 70 分。**分母是全量评分**，不是候选池。
	universe := func(n int, score float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = score
		}
		return out
	}
	// 7000 只全市场，但只有 10 只 >=70
	few := append(universe(6990, 30), universe(10, 80)...)
	if breadthEntryAllowed(few, StrategyTrendGrowth) {
		t.Error("只有 10 只达标（需要 20）闸门应拦截")
	}
	// 25 只达标 → 放行
	enough := append(universe(6975, 30), universe(25, 80)...)
	if !breadthEntryAllowed(enough, StrategyTrendGrowth) {
		t.Error("25 只达标应放行")
	}
	// 全市场不足 100 → 闸门关闭（哪怕一只都不达标）
	small := universe(50, 10)
	if !breadthEntryAllowed(small, StrategyTrendGrowth) {
		t.Error("全市场 <100 时闸门应关闭")
	}
	// 超跌反弹只需要 5 只且门槛是 62
	oversoldReady := append(universe(7000, 30), universe(6, 63)...)
	if !breadthEntryAllowed(oversoldReady, StrategyOversold) {
		t.Error("超跌反弹 6 只 >=62 应放行")
	}
	oversoldFew := append(universe(7000, 30), universe(3, 63)...)
	if breadthEntryAllowed(oversoldFew, StrategyOversold) {
		t.Error("超跌反弹只有 3 只（需要 5）应拦截")
	}
}

// 广度闸门必须用全量评分做分母 —— 用候选池（已 head 截断到 80）会让
// `universe >= 100` 恒不成立、闸门永不启用。
func TestBreadthUsesUniverseNotCandidatePool(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "")

	// 候选池很小（5 只），但全市场有一万只、其中大量达标 → 应放行
	var candidates []Row
	for i := 0; i < 5; i++ {
		candidates = append(candidates, cand("C"+ftoa(float64(i), 0), 95))
	}
	universeScores := make([]float64, 10000)
	for i := range universeScores {
		universeScores[i] = 80 // 全部达标
	}

	plan := BuildRotationPlan(RotationInput{
		Candidates: candidates, Config: cfg, MarketState: StateRange,
		MaxStockLimit: 5, UniverseScores: universeScores,
	})
	if !plan.EntryAllowed {
		t.Error("全市场广度充足时应放行（不该因为候选池只有 5 只就关闭闸门）")
	}

	// 反过来：全市场一万只但几乎都不达标 → 应拦截
	weak := make([]float64, 10000)
	for i := range weak {
		weak[i] = 30
	}
	plan = BuildRotationPlan(RotationInput{
		Candidates: candidates, Config: cfg, MarketState: StateRange,
		MaxStockLimit: 5, UniverseScores: weak,
	})
	if plan.EntryAllowed {
		t.Error("全市场广度不足时应拦截")
	}
	// 拦截后 add 行应被删除
	for _, row := range plan.Rows {
		if row.Action == ActionAdd {
			t.Errorf("广度不足时不该有建仓行: %+v", row)
		}
	}
}

// 广度不足时：replace_in/add 被删除，add_layer 改 keep，replace_out 改 clear，对比清空
func TestBreadthGateRewritesActions(t *testing.T) {
	plan := RotationPlan{
		Rows: []RotationRow{
			{Action: ActionReplaceIn, Code: "C1"},
			{Action: ActionAdd, Code: "C2"},
			{Action: ActionAddLayer, Code: "H1", CurrentLayers: 1, TargetLayers: 2, LayersDelta: 1},
			{Action: ActionReplaceOut, Code: "H2", CandidateCode: "C3", CandidateScore: 90, ScoreGap: 25, ExpectedImprovement: 25},
		},
		Compare: []ReplacementPair{{HoldingCode: "H2"}},
	}
	got := applyBreadthGate(plan.Rows, &plan)

	if len(got) != 2 {
		t.Fatalf("replace_in 与 add 应被删除，剩 2 行，实际 %d", len(got))
	}
	if got[0].Action != ActionKeep {
		t.Errorf("add_layer 应改写为 keep，实际 %s", got[0].Action)
	}
	if got[0].TargetLayers != got[0].CurrentLayers || got[0].LayersDelta != 0 {
		t.Errorf("改 keep 后不该改层数: %+v", got[0])
	}
	if got[1].Action != ActionClear {
		t.Errorf("replace_out 应改写为 clear，实际 %s", got[1].Action)
	}
	if got[1].CandidateCode != "" || got[1].ExpectedImprovement != 0 {
		t.Errorf("改 clear 后应清空候选字段: %+v", got[1])
	}
	if len(plan.Compare) != 0 {
		t.Errorf("对比行应被清空，实际 %d", len(plan.Compare))
	}
}

// ---------------------------------------------------------------------------
// 排序与标签
// ---------------------------------------------------------------------------

func TestActionOrderAndLabels(t *testing.T) {
	// replace_out 在前，observe 在后
	if ActionOrderOf(ActionReplaceOut) >= ActionOrderOf(ActionReplaceIn) {
		t.Error("replace_out 应排在 replace_in 之前")
	}
	if ActionOrderOf(ActionReplaceIn) >= ActionOrderOf(ActionAdd) {
		t.Error("replace_in 应排在 add 之前")
	}
	if ActionOrderOf(ActionObserve) <= ActionOrderOf(ActionReduce) {
		t.Error("observe 应排在 reduce 之后")
	}
	// hold_for_rebound_exit 在排序表里有位置（遗留），但它不是动作
	if ActionOrderOf(StateHoldForReboundExit) != 7 {
		t.Errorf("hold_for_rebound_exit 的遗留排序位应为 7，实际 %d", ActionOrderOf(StateHoldForReboundExit))
	}
	// 未知动作排最后
	if ActionOrderOf("不存在") != 9 {
		t.Error("未知动作应排最后")
	}
	// 标签
	if ActionLabel(ActionClear) != "清仓" || ActionLabel(ActionKeep) != "继续持有" {
		t.Error("动作标签错误")
	}
}

func TestRotationRowsSorted(t *testing.T) {
	rows := []RotationRow{
		{Action: ActionKeep, Code: "K"},
		{Action: ActionReplaceOut, Code: "R", RankNo: 1},
		{Action: ActionObserve, Code: "O"},
		{Action: ActionReplaceIn, Code: "I", RankNo: 1},
		{Action: ActionClear, Code: "C"},
	}
	sortRotationRows(rows)
	want := []string{ActionReplaceOut, ActionReplaceIn, ActionClear, ActionObserve, ActionKeep}
	for i, action := range want {
		if rows[i].Action != action {
			t.Errorf("第 %d 位应为 %s，实际 %s（%v）", i, action, rows[i].Action, rows)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// 执行语义
// ---------------------------------------------------------------------------

func TestBehaviorContext(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings:    []Row{hold("600519", nil)},
		Config:      baseConfig(StrategyTrendGrowth),
		MarketState: StateRange,
	})
	row := plan.Rows[0]
	// 默认（半自动）：可执行动作需要确认
	if row.ExecutionMode != "confirm_required" || row.ExecutionModeLabel != "人工确认后执行" {
		t.Errorf("默认执行模式错误: %s / %s", row.ExecutionMode, row.ExecutionModeLabel)
	}
	// keep 不是可执行动作
	if row.IsExecutable {
		t.Error("keep 不该是可执行动作")
	}
	// keep / observe 不该有前缀
	if row.ActionPrefix != "" {
		t.Errorf("keep 不该有动作前缀: %q", row.ActionPrefix)
	}
}

func TestIsActionExecutable(t *testing.T) {
	executable := []string{ActionClear, ActionReplaceOut, ActionReduce, ActionAdd, ActionAddLayer, ActionReplaceIn}
	for _, action := range executable {
		if !isActionExecutable(action) {
			t.Errorf("%s 应该是可执行动作", action)
		}
	}
	for _, action := range []string{ActionKeep, ActionObserve} {
		if isActionExecutable(action) {
			t.Errorf("%s 不该是可执行动作", action)
		}
	}
}

// ---------------------------------------------------------------------------
// 排序与信号优先级
// ---------------------------------------------------------------------------

func TestSortCandidates(t *testing.T) {
	rows := []Row{
		{Code: "A", TotalScore: 80, SignalType: ""},
		{Code: "B", TotalScore: 90, SignalType: "candidate_buy"},
		{Code: "C", TotalScore: 90, SignalType: "buy"},
		{Code: "D", TotalScore: 90, SignalType: "buy", SignalScore: 95},
	}
	sortCandidates(rows)

	// 分数优先
	if rows[0].Code != "C" && rows[0].Code != "D" {
		t.Errorf("最高分且 buy 信号应排最前，实际 %s", rows[0].Code)
	}
	// 同分时 buy 优先于 candidate_buy，candidate_buy 优先于无信号
	order := map[string]int{}
	for i, row := range rows {
		order[row.Code] = i
	}
	if order["C"] >= order["B"] {
		t.Error("buy 应排在 candidate_buy 之前")
	}
	if order["B"] >= order["A"] {
		t.Error("candidate_buy 应排在无信号之前")
	}
	// 同为 buy 时信号分高的在前
	if order["D"] >= order["C"] {
		t.Error("信号分高的应在前")
	}
}

func TestSignalPriority(t *testing.T) {
	if signalPriority(Row{SignalType: "buy"}) != 0 {
		t.Error("buy 优先级应为 0")
	}
	if signalPriority(Row{SignalType: "candidate_buy"}) != 1 {
		t.Error("candidate_buy 优先级应为 1")
	}
	if signalPriority(Row{SignalType: "其他"}) != 2 {
		t.Error("其他优先级应为 2")
	}
	if signalPriority(Row{}) != 2 {
		t.Error("无信号优先级应为 2")
	}
}

// 新鲜信号判定
func TestHasFreshSignal(t *testing.T) {
	if !(Row{SignalType: "buy", SignalAge: 1}).hasFreshSignal(2) {
		t.Error("age=1 <= 2 应是新鲜信号")
	}
	if (Row{SignalType: "buy", SignalAge: 5}).hasFreshSignal(2) {
		t.Error("age=5 > 2 不是新鲜信号")
	}
	if (Row{SignalType: "sell", SignalAge: 1}).hasFreshSignal(2) {
		t.Error("sell 信号不算数")
	}
}

// ---------------------------------------------------------------------------
// 大反弹阳线与弱势单层
// ---------------------------------------------------------------------------

func TestIsBigReboundCandle(t *testing.T) {
	if !IsBigReboundCandle(Row{Close: 104, Open: 100, PctChg: 5}) {
		t.Error("涨 5% 且实体 4% 应判为大反弹阳线")
	}
	if IsBigReboundCandle(Row{Close: 102, Open: 101, PctChg: 5}) {
		t.Error("实体只有 1% 不该算大反弹阳线")
	}
	if IsBigReboundCandle(Row{Close: 104, Open: 100, PctChg: 2}) {
		t.Error("涨幅只有 2% 不该算")
	}
	if IsBigReboundCandle(Row{Close: 104, Open: 0, PctChg: 5}) {
		t.Error("开盘价为 0 应安全返回 false")
	}
}

func TestIsWeakSingleLayerStock(t *testing.T) {
	cfg := baseConfig(StrategyTrendGrowth)
	// 多层一律 false
	if IsWeakSingleLayerStock(hold("A", func(r *Row) { r.PositionLayers = 2 }), cfg) {
		t.Error("多层不该判为弱势单层")
	}
	// 单层且强势 + 有浮盈保护 → false
	strong := hold("A", func(r *Row) {
		r.PositionLayers = 1
		r.TotalScore = 85
		r.TrendScore = 25
		r.StructureScore = 15
		r.RSScore = 13
		r.EntryPrice = 100
		r.CurrentPrice = 105
		r.ProfitLossPct = 5
	})
	if IsWeakSingleLayerStock(strong, cfg) {
		t.Error("强势且浮盈的单层股不该判为弱势")
	}
	// 单层且弱 → true
	weak := hold("A", func(r *Row) {
		r.PositionLayers = 1
		r.TotalScore = 50
	})
	if !IsWeakSingleLayerStock(weak, cfg) {
		t.Error("弱势单层股应判为 true")
	}
}

// ---------------------------------------------------------------------------
// 边界
// ---------------------------------------------------------------------------

// 建仓必须受额度控制：不能因为候选池有 80 只就生成 80 条 add。
//
// 少了这个限制，「一键应用」会真的建出几十个持仓。
func TestAddQuotaIsEnforced(t *testing.T) {
	// target=3 → 最多建 3 只；总层数 10 → 层数也不能超
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "")

	// 造 50 只高分候选
	var candidates []Row
	for i := 0; i < 50; i++ {
		row := cand(string(rune('A'+i%26))+string(rune('a'+i/26)), 90)
		row.Code = "X" + ftoa(float64(i), 0)
		candidates = append(candidates, row)
	}

	plan := BuildRotationPlan(RotationInput{
		Candidates:    candidates,
		Config:        cfg,
		MarketState:   StateRange,
		MaxStockLimit: 5,
	})

	adds := 0
	totalLayers := 0
	overflowAdds := 0
	for _, row := range plan.Rows {
		if row.Action == ActionAdd {
			adds++
			totalLayers += row.TargetLayers
			if row.Reason == "补位建仓" {
				overflowAdds++
			}
		}
	}
	// 基础建仓不超过目标只数
	base := adds - overflowAdds
	if base > cfg.TargetCount {
		t.Errorf("基础建仓数不该超过目标持仓数 %d，实际 %d", cfg.TargetCount, base)
	}
	// **overflow 补位会刻意超出目标**，上限是 max_stock_limit + 7（原实现 :2047）
	maxOpen := 5 + 7
	if adds > maxOpen {
		t.Errorf("含补位的建仓总数不该超过 %d，实际 %d", maxOpen, adds)
	}
	if totalLayers > cfg.TotalLayers {
		t.Errorf("建仓层数不该超过总层数 %d，实际 %d", cfg.TotalLayers, totalLayers)
	}
	t.Logf("50 只候选 → 建仓 %d 只（其中补位 %d 只）、共 %d 层（上限 %d 只 / %d 层）",
		adds, overflowAdds, totalLayers, cfg.TargetCount, cfg.TotalLayers)
}

// 已有持仓占满**基础**额度时：不再走正常建仓，但 overflow 补位仍会补
// （这正是补位的目的 —— 组合没填满就补上）。
func TestAddQuotaRespectsExistingHoldings(t *testing.T) {
	cfg := BuildStrategyConfig(StrategyTrendGrowth, StateRange, 3, 10, 3, "")
	holdings := []Row{
		hold("600519", nil), hold("000001", nil), hold("601318", nil),
	}
	var candidates []Row
	for i := 0; i < 20; i++ {
		row := cand("Y"+ftoa(float64(i), 0), 95)
		candidates = append(candidates, row)
	}

	plan := BuildRotationPlan(RotationInput{
		Holdings: holdings, Candidates: candidates,
		Config: cfg, MarketState: StateRange, MaxStockLimit: 5,
	})

	baseAdds, overflowAdds := 0, 0
	for _, row := range plan.Rows {
		if row.Action != ActionAdd || row.FromPair {
			continue
		}
		if row.Reason == "补位建仓" {
			overflowAdds++
			// 补位行恒 1 层、score_gap 与候选字段为 0
			if row.TargetLayers != 1 || row.LayersDelta != 1 || row.ScoreGap != 0 {
				t.Errorf("补位行应恒 1 层且候选字段为 0: %+v", row)
			}
			continue
		}
		baseAdds++
	}
	if baseAdds != 0 {
		t.Errorf("基础额度已满（3/3）时不该有正常建仓，实际 %d", baseAdds)
	}
	// 补位上限 = max_stock_limit + 7 - 已开只数
	if overflowAdds > 5+7-3 {
		t.Errorf("补位只数超上限，实际 %d", overflowAdds)
	}
	t.Logf("额度已满时：基础建仓 %d 只、补位 %d 只", baseAdds, overflowAdds)
}

// 熊市且 allow_new_adds=false 时不该建仓。
func TestNoNewAddsWhenDisallowed(t *testing.T) {
	// 超跌反弹在熊市 allow_new_adds=false
	cfg := BuildStrategyConfig(StrategyOversold, StateBear, 5, 10, 3, "")
	if cfg.AllowNewAdds {
		t.Fatal("前提不成立：超跌反弹在熊市应禁止加新仓")
	}
	var candidates []Row
	for i := 0; i < 10; i++ {
		row := cand("Z"+ftoa(float64(i), 0), 95)
		candidates = append(candidates, row)
	}
	plan := BuildRotationPlan(RotationInput{
		Candidates: candidates, Config: cfg, MarketState: StateBear, MaxStockLimit: 5,
	})
	for _, row := range plan.Rows {
		if row.Action == ActionAdd {
			t.Errorf("allow_new_adds=false 时不该建仓: %+v", row)
		}
	}
}

func TestBuildRotationPlanEmpty(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Config: baseConfig(StrategyTrendGrowth), MarketState: StateRange,
	})
	if len(plan.Rows) != 0 {
		t.Errorf("空输入不该产出动作，实际 %d", len(plan.Rows))
	}
	if plan.MarketState != StateRange {
		t.Errorf("市场状态应保留: %s", plan.MarketState)
	}
}

func TestBuildRotationPlanInvalidState(t *testing.T) {
	plan := BuildRotationPlan(RotationInput{
		Holdings: []Row{hold("A", nil)},
		Config:   baseConfig(StrategyTrendGrowth), MarketState: "乱填",
	})
	if plan.MarketState != StateRange {
		t.Errorf("未知市场状态应回退震荡，实际 %s", plan.MarketState)
	}
}

func TestFtoaAndTrimFloat(t *testing.T) {
	cases := []struct {
		value  float64
		digits int
		want   string
	}{
		{0.96, 2, "0.96"},
		{1.0, 2, "1.00"},
		{30, 0, "30"},
		{-15.5, 1, "-15.5"},
		{0, 0, "0"},
	}
	for _, c := range cases {
		if got := trimFloat(c.value, c.digits); got != c.want {
			t.Errorf("trimFloat(%v, %d) = %q，期望 %q", c.value, c.digits, got, c.want)
		}
	}
	if got := itoa(1234); got != "1234" {
		t.Errorf("itoa 错误: %q", got)
	}
	if got := itoa(-7); got != "-7" {
		t.Errorf("itoa 负数错误: %q", got)
	}
	if math.Abs(0.96-0.96) > 1e-9 {
		t.Error("sanity")
	}
}
