package portfolio

import (
	"testing"
)

// **自动组合落库的层数不能被"手动新增"的 [1,2] clamp 砍掉。**
//
// 原实现里这是**两条不同的写入路径**：
//
//	手动新增 → `add_position`（portfolio_service.py:534）
//	           `position_layers = min(2, max(1, ...))`
//	自动组合 → `create_auto_portfolio`（:405-442）
//	           **直接写 `portfolio_position`**，层数只有 `max(1, ...)`，
//	           权重取 `_weight_from_layers(layers, total_layers)`
//
// 之前 Go 的自动组合复用了 `store.AddPosition`，于是：
// 低吸的 3/5 层被砍成 2 层，而权重仍按原层数算 →
// 磁盘上 `position_layers=2 / position_weight=50%`，层数与权重自相矛盾。
func TestAddAutoComposePositionsKeepsLayersAboveManualClamp(t *testing.T) {
	store := newStore(t)

	created, err := store.Create(PortfolioInput{
		Name: "自动组合", Type: TypeAuto, StrategyID: StrategyTrendLowBuy,
		TargetPositionCount: intPtr(2), MaxStockCount: intPtr(5), TotalLayers: 10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}

	const signalID = "pf_test:20261009:auto_compose"
	// 层数 3 与 5 都**超过** maxManualLayers(2) —— 正是低吸的典型层数
	written, err := store.AddAutoComposePositions(created.ID, []AutoComposePosition{
		{
			Code: "600519", Name: "贵州茅台", EntryPrice: 1500, EntryDate: "20261009",
			Layers: 3, Weight: WeightFromLayers(3, 10),
			StrategyTag: created.StrategyID, SourceSignalID: signalID,
		},
		{
			Code: "300750", Name: "宁德时代", EntryPrice: 200, EntryDate: "20261009",
			Layers: 5, Weight: WeightFromLayers(5, 10),
			StrategyTag: created.StrategyID, SourceSignalID: signalID,
		},
		// 无效行：必须被跳过（原实现是 continue），且不影响其它行落库
		{Code: "", Name: "空代码", EntryPrice: 10, Layers: 1, Weight: 10},
		{Code: "000001", Name: "零价格", EntryPrice: 0, Layers: 1, Weight: 10},
	})
	if err != nil {
		t.Fatalf("写入自动组合持仓失败：%v", err)
	}
	if written != 2 {
		t.Fatalf("应写入 2 只（跳过 2 条无效行），实际 %d", written)
	}

	positions, err := store.ListPositions(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 2 {
		t.Fatalf("应落库 2 只持仓，实际 %d", len(positions))
	}
	byCode := map[string]Position{}
	for _, position := range positions {
		byCode[position.Code] = position
	}

	for code, wantLayers := range map[string]int{"600519": 3, "300750": 5} {
		position, ok := byCode[code]
		if !ok {
			t.Fatalf("%s 没落库", code)
		}
		if position.PositionLayers != wantLayers {
			t.Errorf("%s 的层数应为 %d（自动组合没有上限），实际 %d —— 被手动新增的 clamp 砍了",
				code, wantLayers, position.PositionLayers)
		}
		// 权重必须与**落库的层数**自洽（同一条公式、同一份层数）
		wantWeight := WeightFromLayers(wantLayers, 10)
		if diff := position.PositionWeight - wantWeight; diff > 1e-6 || diff < -1e-6 {
			t.Errorf("%s 的权重应 %.2f%%（层数 %d / 总层数 10），实际 %.2f%% —— 层数与权重不一致",
				code, wantWeight, wantLayers, position.PositionWeight)
		}
		if position.SourceType != "auto_compose" || position.LastAction != "auto_compose" {
			t.Errorf("%s 的来源应 auto_compose/auto_compose，实际 %q/%q",
				code, position.SourceType, position.LastAction)
		}
		if position.StrategyTag != created.StrategyID {
			t.Errorf("%s 的 strategy_tag 应为 %s，实际 %q", code, created.StrategyID, position.StrategyTag)
		}
	}

	// 流水：reason / source_signal_id / 层数都要对齐原实现
	detail, err := store.Detail(created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	buys := 0
	for _, txn := range detail.Transactions {
		if txn.Action != "buy" {
			continue
		}
		buys++
		if txn.Reason != "自动组合生成" {
			t.Errorf("%s 的流水原因应为「自动组合生成」，实际 %q（原来写成了「手动新增持仓」）",
				txn.Code, txn.Reason)
		}
		if txn.SourceSignalID != signalID {
			t.Errorf("%s 的 source_signal_id 应为 %q，实际 %q", txn.Code, signalID, txn.SourceSignalID)
		}
		if txn.LayersDelta != byCode[txn.Code].PositionLayers {
			t.Errorf("%s 的流水层数 %d 与持仓层数 %d 不一致",
				txn.Code, txn.LayersDelta, byCode[txn.Code].PositionLayers)
		}
	}
	if buys != 2 {
		t.Errorf("应有 2 条买入流水，实际 %d", buys)
	}
}

// **"最新评分日"要取全表 MAX，不是该策略自己的 MAX。**
//
// 原实现 `_get_latest_strength_trade_date`（portfolio_service.py:2611-2612）是
// `SELECT MAX(trade_date) FROM stock_strength_history` —— 不带 strategy_id。
// 建/删持仓的日期回落、自动建组合、调仓建议都用它，然后**再按策略取那一帧**。
//
// 差别在两套评分不同步时显现：策略 A 更到 D2、策略 B 停在 D1 时，
// 原版会拿 D2 去查 B 的帧 → 空帧（报"该策略没有最新评分"/持仓全判 0 分），
// 而"取 B 自己的最新日"会**静默地用 D1 的过期评分**出建议。
func TestLatestStrengthDateAnyIgnoresStrategy(t *testing.T) {
	store := newStore(t)

	if err := store.SaveStrength(20261009, StrategyTrendGrowth, benchStrengthRows(3)); err != nil {
		t.Fatal(err)
	}
	bOlder := benchStrengthRows(3)
	for i := range bOlder {
		bOlder[i].Code = "3007" + bOlder[i].Code[2:]
	}
	if err := store.SaveStrength(20260601, StrategyTrendLowBuy, bOlder); err != nil {
		t.Fatal(err)
	}

	any, err := store.LatestStrengthDateAny()
	if err != nil {
		t.Fatal(err)
	}
	if any != 20261009 {
		t.Errorf("全表最新评分日应为 20261009，实际 %d", any)
	}
	// 按策略的那个仍然只看自己（两者不是一回事，别混用）
	if got, _ := store.LatestStrengthDate(StrategyTrendLowBuy); got != 20260601 {
		t.Errorf("按策略查应为 20260601，实际 %d", got)
	}
	if got, _ := store.LatestStrengthDate(StrategyTrendGrowth); got != 20261009 {
		t.Errorf("按策略查应为 20261009，实际 %d", got)
	}

	// 空表 → 0（两个函数都一样）
	empty := newStore(t)
	if got, err := empty.LatestStrengthDateAny(); err != nil || got != 0 {
		t.Errorf("空表应返回 0，实际 %d（err=%v）", got, err)
	}
}

// 手动新增那条路**必须保持**原实现的 [1,2] clamp —— 别把两处一起改了。
func TestAddPositionStillClampsManualLayers(t *testing.T) {
	store := newStore(t)
	created, err := store.Create(PortfolioInput{
		Name: "手动组合", Type: TypeSemiAuto, StrategyID: StrategyTrendGrowth,
		TargetPositionCount: intPtr(5), MaxStockCount: intPtr(5), TotalLayers: 10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}
	position, err := store.AddPosition(created.ID, PositionInput{
		Code: "600519", Name: "贵州茅台", EntryPrice: 1500, Layers: intPtr(9),
	})
	if err != nil {
		t.Fatalf("手动加持仓失败：%v", err)
	}
	if position.PositionLayers != maxManualLayers {
		t.Errorf("手动新增应被 clamp 到 %d（原实现 :534），实际 %d",
			maxManualLayers, position.PositionLayers)
	}
	if diff := position.PositionWeight - WeightFromLayers(maxManualLayers, 10); diff > 1e-6 || diff < -1e-6 {
		t.Errorf("手动新增的权重应随**截断后**的层数，实际 %.2f%%", position.PositionWeight)
	}
}
