package portfolio

import (
	"testing"
)

// intPtr 造一个 *int —— PositionInput.Layers 用指针区分"未提供"与"显式 0"。
func intPtr(v int) *int { return &v }

// floatPtr 造一个 *float64（Weight 同理）。
func floatPtr(v float64) *float64 { return &v }

func applyAction(code, action string, layers int, price float64) ApplyAction {
	return ApplyAction{Code: code, Name: code, Action: action, TargetLayers: layers, Price: price, Reason: "测试"}
}

// 手动组合不支持一键应用（参考实现直接抛错）。
func TestApplyRotationRejectsManual(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "手动组合", Type: "manual"})
	store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 100, Layers: intPtr(1)})

	_, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{applyAction("600519", "clear", 0, 100)})
	if err == nil {
		t.Fatal("手动组合应拒绝一键应用")
	}
	// 提示要说清为什么
	if !contains(err.Error(), "仅生成分析提醒") {
		t.Errorf("错误信息应说明原因: %s", err)
	}
	// 持仓不该被动过
	if positions, _ := store.ListPositions(item.ID); len(positions) != 1 {
		t.Errorf("被拒绝时持仓不该变化: %d", len(positions))
	}
}

func contains(text, sub string) bool {
	return len(text) >= len(sub) && (text == sub || indexOf(text, sub) >= 0)
}

func indexOf(text, sub string) int {
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 半自动组合可以应用。
func TestApplyRotationClear(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "半自动", Type: "semi_auto", TotalLayers: (10)})
	store.AddPosition(item.ID, PositionInput{Code: "600519", EntryPrice: 100, Layers: intPtr(2)})
	store.AddPosition(item.ID, PositionInput{Code: "000001", EntryPrice: 10, Layers: intPtr(1)})

	// ⚠ store 层契约：**必须传入完整持仓集合**（整表 DELETE 再 INSERT）。
	// 只传一个动作会把没提到的持仓一起删掉 —— 这是参考实现的行为，
	// 调用方（service 层）负责保证传全。
	result, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "clear", 0, 90),
		applyAction("000001", "keep", 1, 10),
	})
	if err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	if result.Cleared != 1 {
		t.Errorf("应清仓 1 只，实际 %d", result.Cleared)
	}
	if result.PositionCount != 1 {
		t.Errorf("清仓后应剩 1 只，实际 %d", result.PositionCount)
	}

	positions, _ := store.ListPositions(item.ID)
	if len(positions) != 1 || positions[0].Code != "000001" {
		t.Fatalf("应只剩 000001: %+v", positions)
	}

	// 流水：应补一条 clear
	txns, _ := store.ListTransactions(item.ID)
	var clearTxn *Transaction
	for i := range txns {
		if txns[i].Action == "clear" {
			clearTxn = &txns[i]
		}
	}
	if clearTxn == nil {
		t.Fatal("应写入 clear 流水")
	}
	if clearTxn.LayersDelta != -2 {
		t.Errorf("清仓的层数变化应为 -2，实际 %d", clearTxn.LayersDelta)
	}
	if clearTxn.SourceSignalID != "apply_rotation" {
		t.Errorf("来源应为 apply_rotation: %s", clearTxn.SourceSignalID)
	}
}

// **硬编码 clamp 到 3** —— 即使请求 10 层也只能到 3（参考实现如此）。
func TestApplyRotationClampsLayers(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})

	if _, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "add", 10, 100),
	}); err != nil {
		t.Fatalf("应用失败: %v", err)
	}

	positions, _ := store.ListPositions(item.ID)
	if len(positions) != 1 {
		t.Fatalf("应有 1 个持仓: %+v", positions)
	}
	if positions[0].PositionLayers != maxApplyLayers {
		t.Errorf("层数应被 clamp 到 %d，实际 %d", maxApplyLayers, positions[0].PositionLayers)
	}
	// 权重按 clamp 后的层数算：3/10 = 30%
	if positions[0].PositionWeight != 30 {
		t.Errorf("权重应为 30%%，实际 %v", positions[0].PositionWeight)
	}
}

// 加层用加权平均摊薄成本，**分母是 target_layers**。
func TestApplyRotationWeightedAverageCost(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})

	// 先建 1 层 @100
	if _, err := store.ApplyRotation(item.ID, "20261001", []ApplyAction{
		applyAction("600519", "add", 1, 100),
	}); err != nil {
		t.Fatalf("建仓失败: %v", err)
	}
	// 再加到 3 层 @130
	// 期望成本 = (100*1 + 130*2) / 3 = 360/3 = 120
	if _, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "add_layer", 3, 130),
	}); err != nil {
		t.Fatalf("加仓失败: %v", err)
	}

	positions, _ := store.ListPositions(item.ID)
	if len(positions) != 1 {
		t.Fatalf("应有 1 个持仓: %+v", positions)
	}
	if positions[0].PositionLayers != 3 {
		t.Errorf("层数应为 3: %d", positions[0].PositionLayers)
	}
	if positions[0].EntryPrice != 120 {
		t.Errorf("加权平均成本应为 120，实际 %v", positions[0].EntryPrice)
	}
	// 建仓日期保持最早
	if positions[0].EntryDate != "20261001" {
		t.Errorf("加层不该改建仓日期，实际 %s", positions[0].EntryDate)
	}
}

// 减层不改成本价、不改建仓日期。
func TestApplyRotationReduceKeepsCost(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})

	store.ApplyRotation(item.ID, "20261001", []ApplyAction{applyAction("600519", "add", 3, 100)})
	// 减到 1 层
	if _, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "reduce", 1, 150),
	}); err != nil {
		t.Fatalf("减仓失败: %v", err)
	}

	positions, _ := store.ListPositions(item.ID)
	if positions[0].PositionLayers != 1 {
		t.Errorf("层数应为 1: %d", positions[0].PositionLayers)
	}
	if positions[0].EntryPrice != 100 {
		t.Errorf("减层不该改成本价，实际 %v", positions[0].EntryPrice)
	}
	if positions[0].EntryDate != "20261001" {
		t.Errorf("减层不该改建仓日期，实际 %s", positions[0].EntryDate)
	}
}

// 整表重写但 **created_at 必须沿用**（参考实现显式保留）。
func TestApplyRotationPreservesCreatedAt(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})

	store.ApplyRotation(item.ID, "20261001", []ApplyAction{applyAction("600519", "add", 2, 100)})
	before, _ := store.ListPositions(item.ID)
	if len(before) != 1 {
		t.Fatalf("前置条件失败: %+v", before)
	}
	createdAt := before[0].CreatedAt

	// 层数不变的应用（unchanged 分支）
	if _, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "keep", 2, 120),
	}); err != nil {
		t.Fatalf("应用失败: %v", err)
	}

	after, _ := store.ListPositions(item.ID)
	if after[0].CreatedAt != createdAt {
		t.Errorf("created_at 应沿用: %s → %s", createdAt, after[0].CreatedAt)
	}
}

// 不可执行的动作被忽略，只应用可执行的。
func TestApplyRotationSkipsNonExecutable(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})
	store.ApplyRotation(item.ID, "20261001", []ApplyAction{applyAction("600519", "add", 2, 100)})

	result, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "keep", 2, 100),   // 不可执行
		applyAction("000001", "observe", 1, 10), // 不可执行
		applyAction("601318", "add", 1, 50),     // 可执行
	})
	if err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	// keep/observe 不参与，但它们的持仓会被"整表重写"清掉？
	// **不会** —— 参考实现的 apply_rotation 只处理传入的动作，
	// 但它是整表 DELETE 再 INSERT，所以没被传入的持仓会丢。
	// 这里如实反映：传进来的动作决定最终持仓。
	if result.Added != 1 {
		t.Errorf("应只新增 1 只: %+v", result)
	}
	t.Logf("结果: 持仓 %d，新增 %d，减仓 %d，清仓 %d，不变 %d",
		result.PositionCount, result.Added, result.Reduced, result.Cleared, result.Unchanged)
}

// 未知组合
func TestApplyRotationUnknownPortfolio(t *testing.T) {
	store := newStore(t)
	if _, err := store.ApplyRotation("pf_不存在", "20261008", []ApplyAction{
		applyAction("600519", "add", 1, 100),
	}); err == nil {
		t.Error("不存在的组合应报错")
	}
}

func TestIsApplyExecutable(t *testing.T) {
	for _, action := range []string{"clear", "replace_out", "reduce", "add", "add_layer", "replace_in"} {
		if !IsApplyExecutable(action) {
			t.Errorf("%s 应可执行", action)
		}
	}
	for _, action := range []string{"keep", "observe", "hold_for_rebound_exit", ""} {
		if IsApplyExecutable(action) {
			t.Errorf("%s 不该可执行", action)
		}
	}
}

// 换入是新建仓：用给定价格，日期为交易日。
func TestApplyRotationReplaceIn(t *testing.T) {
	store := newStore(t)
	item, _ := store.Create(PortfolioInput{Name: "组合", Type: "auto", TotalLayers: (10)})
	store.ApplyRotation(item.ID, "20261001", []ApplyAction{applyAction("600519", "add", 1, 100)})

	// 换出旧的，换入新的
	if _, err := store.ApplyRotation(item.ID, "20261008", []ApplyAction{
		applyAction("600519", "replace_out", 0, 100),
		applyAction("000001", "replace_in", 1, 12.5),
	}); err != nil {
		t.Fatalf("换股失败: %v", err)
	}

	positions, _ := store.ListPositions(item.ID)
	if len(positions) != 1 || positions[0].Code != "000001" {
		t.Fatalf("应只剩换入的 000001: %+v", positions)
	}
	if positions[0].EntryPrice != 12.5 {
		t.Errorf("换入成本应为 12.5，实际 %v", positions[0].EntryPrice)
	}
	if positions[0].EntryDate != "20261008" {
		t.Errorf("换入的建仓日期应为交易日，实际 %s", positions[0].EntryDate)
	}
	if positions[0].PositionWeight != 10 {
		t.Errorf("1/10 层 = 10%%，实际 %v", positions[0].PositionWeight)
	}
}
