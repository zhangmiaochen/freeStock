package chanlunassist

import (
	"reflect"
	"strings"
	"testing"

	"freestock/internal/chanlun"
	"freestock/internal/score"
)

// fakeProvider 是按代码返回固定信号的假提供器：Apply 的规则测试不需要真行情。
type fakeProvider map[string]Snapshot

func (f fakeProvider) Signals(code string, asOf int) Snapshot { return f[code] }

func buySignal(t chanlun.SignalType, confirmDate int, score float64) *Signal {
	return &Signal{
		Type: t, TypeCN: chanlun.SignalTypeCN(t), State: chanlun.StateConfirmed,
		Score: score, ConfirmDate: confirmDate, ConfirmIndex: 10, Price: 10, AgeBars: 1,
	}
}

func sellSignal(t chanlun.SignalType, confirmDate int) *Signal {
	return &Signal{
		Type: t, TypeCN: chanlun.SignalTypeCN(t), State: chanlun.StateConfirmed,
		Score: 60, ConfirmDate: confirmDate, ConfirmIndex: 10, Price: 10, AgeBars: 1,
	}
}

func buyRow(code string, current, target int) score.RotationRow {
	return score.RotationRow{
		Code: code, Name: code, Action: score.ActionAdd,
		ActionLabel: score.ActionLabel(score.ActionAdd), SourceType: "candidate",
		CurrentLayers: current, TargetLayers: target, LayersDelta: target - current,
		RankNo: 1, Reason: "新增建仓",
	}
}

func holdingRow(code string, current, target int, action string) score.RotationRow {
	return score.RotationRow{
		Code: code, Name: code, Action: action, ActionLabel: score.ActionLabel(action),
		SourceType: "holding", CurrentLayers: current, TargetLayers: target,
		LayersDelta: target - current, Reason: "继续持有",
	}
}

func TestApplyBuyBoostBySignalType(t *testing.T) {
	cases := []struct {
		name      string
		signal    chanlun.SignalType
		wantLayer int
		wantStat  func(Stats) int
	}{
		{"二买加1层", chanlun.SignalBuy2, 3, func(s Stats) int { return s.Buy2Boosts }},
		{"三买加2层", chanlun.SignalBuy3, 4, func(s Stats) int { return s.Buy3Boosts }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			provider := fakeProvider{"600000": {Buy2: buyOrNil(tc.signal == chanlun.SignalBuy2, chanlun.SignalBuy2),
				Buy3: buyOrNil(tc.signal == chanlun.SignalBuy3, chanlun.SignalBuy3)}}
			plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}
			state := NewState()

			got := Apply(plan, nil, 20260101, provider, cfg, state)

			row := got.Rows[0]
			if row.TargetLayers != tc.wantLayer {
				t.Errorf("目标层数应为 %d，实际 %d", tc.wantLayer, row.TargetLayers)
			}
			if row.LayersDelta != tc.wantLayer {
				t.Errorf("建仓行 LayersDelta 应等于目标层数 %d，实际 %d", tc.wantLayer, row.LayersDelta)
			}
			if row.DecisionState != "chanlun_buy_boost" {
				t.Errorf("应标记为缠论加层，实际 %q", row.DecisionState)
			}
			if !strings.Contains(row.Reason, "缠论") {
				t.Errorf("原因应留痕，实际 %q", row.Reason)
			}
			if got := tc.wantStat(state.Stats); got != 1 {
				t.Errorf("对应信号计数应为 1，实际 %d", got)
			}
			if state.Stats.BuyLayersAdded != tc.wantLayer-2 {
				t.Errorf("加层数应为 %d，实际 %d", tc.wantLayer-2, state.Stats.BuyLayersAdded)
			}
		})
	}
}

func buyOrNil(ok bool, t chanlun.SignalType) *Signal {
	if !ok {
		return nil
	}
	return buySignal(t, 20251231, 70)
}

// 三买与二买同时出现时取三买，**不能叠加成 +3 层**。
func TestBuyBoostTakesStrongestSignalOnly(t *testing.T) {
	cfg := DefaultConfig()
	provider := fakeProvider{"600000": {Buy2: buySignal(chanlun.SignalBuy2, 20251230, 60),
		Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}
	plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}
	state := NewState()

	got := Apply(plan, nil, 20260101, provider, cfg, state)
	if got.Rows[0].TargetLayers != 4 {
		t.Errorf("三买 +2 层后应为 4 层，实际 %d", got.Rows[0].TargetLayers)
	}
	if state.Stats.Buy3Boosts != 1 || state.Stats.Buy2Boosts != 0 {
		t.Errorf("只应记一次三买：%+v", state.Stats)
	}
}

func TestBuyBoostRespectsMaxLayersPerStock(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxLayersPerStock = 3
	provider := fakeProvider{"600000": {Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}
	plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}
	state := NewState()

	got := Apply(plan, nil, 20260101, provider, cfg, state)
	if got.Rows[0].TargetLayers != 3 {
		t.Errorf("应被单股上限截到 3 层，实际 %d", got.Rows[0].TargetLayers)
	}
	if state.Stats.BuyLayersAdded != 1 {
		t.Errorf("实际只应加 1 层（2→3），实际 %d", state.Stats.BuyLayersAdded)
	}

	// 原计划已经在上限上：完全加不进去，要记「撞上限」。
	cappedPlan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600001", 0, 3)}}
	cappedState := NewState()
	capped := Apply(cappedPlan, nil, 20260101, fakeProvider{
		"600001": {Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}, cfg, cappedState)
	if capped.Rows[0].TargetLayers != 3 {
		t.Errorf("已在上限时不应再加，实际 %d", capped.Rows[0].TargetLayers)
	}
	if cappedState.Stats.SkippedCapped != 1 {
		t.Errorf("应记录一次「撞上限」，实际 %d", cappedState.Stats.SkippedCapped)
	}
}

// 同一信号（类型 + 确认日）跨交易日只能加一次。
func TestBuyBoostDeduplicatedAcrossDays(t *testing.T) {
	cfg := DefaultConfig()
	provider := fakeProvider{"600000": {Buy2: buySignal(chanlun.SignalBuy2, 20251231, 70)}}
	state := NewState()
	plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}

	first := Apply(plan, nil, 20260101, provider, cfg, state)
	second := Apply(plan, nil, 20260102, provider, cfg, state)

	if first.Rows[0].TargetLayers != 3 {
		t.Fatalf("首次应加 1 层，实际 %d", first.Rows[0].TargetLayers)
	}
	if second.Rows[0].TargetLayers != 2 {
		t.Errorf("同一信号次日不应再加层，实际 %d", second.Rows[0].TargetLayers)
	}
	if state.Stats.SkippedDuplicate != 1 {
		t.Errorf("应记录一次重复跳过，实际 %d", state.Stats.SkippedDuplicate)
	}
}

// 同股同日已有确认卖点：风险优先，不加层。
func TestBuyBoostSkippedWhenSellSignalPresent(t *testing.T) {
	cfg := DefaultConfig()
	provider := fakeProvider{"600000": {
		Buy2:  buySignal(chanlun.SignalBuy2, 20251231, 70),
		Sell3: sellSignal(chanlun.SignalSell3, 20251231),
	}}
	plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}
	state := NewState()

	got := Apply(plan, nil, 20260101, provider, cfg, state)
	if got.Rows[0].TargetLayers != 2 {
		t.Errorf("有反向卖点时应保持原计划 2 层，实际 %d", got.Rows[0].TargetLayers)
	}
	if state.Stats.SkippedConflict != 1 {
		t.Errorf("应记录一次冲突跳过，实际 %d", state.Stats.SkippedConflict)
	}
}

// 只有买入动作会被加层：Keep/Observe 不受影响。
func TestBuyBoostOnlyAppliesToBuyActions(t *testing.T) {
	cfg := DefaultConfig()
	provider := fakeProvider{"600000": {Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}
	plan := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600000", 2, 2, score.ActionKeep),
		holdingRow("600001", 2, 2, score.ActionObserve),
	}}
	state := NewState()

	got := Apply(plan, nil, 20260101, provider, cfg, state)
	for _, row := range got.Rows {
		if row.TargetLayers != 2 {
			t.Errorf("%s 不应被加层，实际 %d", row.Code, row.TargetLayers)
		}
	}
	if state.Stats.BuyRowsExamined != 0 {
		t.Errorf("没有买入动作时不应计入买入行，实际 %d", state.Stats.BuyRowsExamined)
	}
}

func TestSellReducesLayersByType(t *testing.T) {
	cases := []struct {
		name     string
		current  int
		signal   chanlun.SignalType
		want     int
		wantAct  string
		wantStat func(Stats) int
	}{
		{"一卖减1层", 3, chanlun.SignalSell1, 2, score.ActionReduce, func(s Stats) int { return s.Sell1Reduces }},
		{"二卖减1层", 3, chanlun.SignalSell2, 2, score.ActionReduce, func(s Stats) int { return s.Sell2Reduces }},
		{"三卖减2层", 3, chanlun.SignalSell3, 1, score.ActionReduce, func(s Stats) int { return s.Sell3Reduces }},
		{"三卖把2层清掉", 2, chanlun.SignalSell3, 0, score.ActionClear, func(s Stats) int { return s.Sell3Reduces }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			provider := fakeProvider{"600000": snapshotWithSell(tc.signal)}
			plan := score.RotationPlan{Rows: []score.RotationRow{
				holdingRow("600000", tc.current, tc.current, score.ActionKeep),
			}}
			state := NewState()

			got := Apply(plan, nil, 20260101, provider, cfg, state)
			row := got.Rows[0]
			if row.TargetLayers != tc.want {
				t.Errorf("目标层数应为 %d，实际 %d", tc.want, row.TargetLayers)
			}
			if row.Action != tc.wantAct {
				t.Errorf("动作应为 %s，实际 %s", tc.wantAct, row.Action)
			}
			if row.LayersDelta != tc.want-tc.current {
				t.Errorf("层数变化应为 %d，实际 %d", tc.want-tc.current, row.LayersDelta)
			}
			if !strings.Contains(row.Reason, "缠论") {
				t.Errorf("原因应留痕，实际 %q", row.Reason)
			}
			if got := tc.wantStat(state.Stats); got != 1 {
				t.Errorf("对应卖出信号计数应为 1，实际 %d", got)
			}
		})
	}
}

func snapshotWithSell(t chanlun.SignalType) Snapshot {
	switch t {
	case chanlun.SignalSell1:
		return Snapshot{Sell1: sellSignal(t, 20251231)}
	case chanlun.SignalSell2:
		return Snapshot{Sell2: sellSignal(t, 20251231)}
	default:
		return Snapshot{Sell3: sellSignal(t, 20251231)}
	}
}

// 基计划已经减得更多时，缠论不再加码；但同一信号记为已处理。
func TestSellComposesWithBaseReduce(t *testing.T) {
	cfg := DefaultConfig()

	// 基计划：3 层 → 2 层（减 1）；三卖要减 2 层 → 应取更激进的 1 层。
	plan := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600000", 3, 2, score.ActionReduce),
	}}
	state := NewState()
	got := Apply(plan, nil, 20260101,
		fakeProvider{"600000": snapshotWithSell(chanlun.SignalSell3)}, cfg, state)
	if got.Rows[0].TargetLayers != 1 {
		t.Errorf("应取更激进的 1 层，实际 %d", got.Rows[0].TargetLayers)
	}
	if state.Stats.SellLayersSold != 2 {
		t.Errorf("实际减层应为 2，实际 %d", state.Stats.SellLayersSold)
	}

	// 基计划：3 层 → 1 层（减 2）；一卖只减 1 层 → 保持基计划的 1 层。
	plan2 := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600001", 3, 1, score.ActionReduce),
	}}
	state2 := NewState()
	got2 := Apply(plan2, nil, 20260101,
		fakeProvider{"600001": snapshotWithSell(chanlun.SignalSell1)}, cfg, state2)
	if got2.Rows[0].TargetLayers != 1 {
		t.Errorf("基计划更激进时应保持 1 层，实际 %d", got2.Rows[0].TargetLayers)
	}
	if state2.Stats.SkippedAlreadyExit != 1 {
		t.Errorf("应记录一次「基计划已减更多」，实际 %d", state2.Stats.SkippedAlreadyExit)
	}
}

func TestSellSkipsClearAndReplaceOut(t *testing.T) {
	cfg := DefaultConfig()
	for _, action := range []string{score.ActionClear, score.ActionReplaceOut} {
		t.Run(action, func(t *testing.T) {
			plan := score.RotationPlan{Rows: []score.RotationRow{
				holdingRow("600000", 3, 0, action),
			}}
			state := NewState()
			got := Apply(plan, nil, 20260101,
				fakeProvider{"600000": snapshotWithSell(chanlun.SignalSell3)}, cfg, state)
			if got.Rows[0].Action != action || got.Rows[0].TargetLayers != 0 {
				t.Errorf("已清仓/换出的行不应再改：%+v", got.Rows[0])
			}
			if state.Stats.SellRowsExamined != 0 {
				t.Errorf("已退出行不应计入卖出行，实际 %d", state.Stats.SellRowsExamined)
			}
		})
	}
}

func TestSellDeduplicatedAcrossDays(t *testing.T) {
	cfg := DefaultConfig()
	provider := fakeProvider{"600000": snapshotWithSell(chanlun.SignalSell2)}
	state := NewState()
	plan := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600000", 3, 3, score.ActionKeep),
	}}

	first := Apply(plan, nil, 20260101, provider, cfg, state)
	second := Apply(plan, nil, 20260102, provider, cfg, state)
	if first.Rows[0].TargetLayers != 2 {
		t.Fatalf("首次应减到 2 层，实际 %d", first.Rows[0].TargetLayers)
	}
	if second.Rows[0].TargetLayers != 3 {
		t.Errorf("同一卖点次日不应再减，实际 %d", second.Rows[0].TargetLayers)
	}
	if state.Stats.SkippedDuplicate != 1 {
		t.Errorf("应记录一次重复跳过，实际 %d", state.Stats.SkippedDuplicate)
	}
}

// 没有任何信号时，计划必须逐字段不变（辅助层是纯增量）。
func TestNoSignalsKeepsPlanUnchanged(t *testing.T) {
	cfg := DefaultConfig()
	plan := score.RotationPlan{
		Rows: []score.RotationRow{
			buyRow("600000", 0, 2),
			holdingRow("600001", 3, 3, score.ActionKeep),
		},
		Notes: []string{"原计划"},
	}
	state := NewState()

	got := Apply(plan, nil, 20260101, fakeProvider{}, cfg, state)
	if !reflect.DeepEqual(got, plan) {
		t.Errorf("无信号时计划不应变化：\n got=%+v\nwant=%+v", got, plan)
	}
	if state.Stats.Buy2Boosts+state.Stats.Buy3Boosts+state.Stats.SellLayersSold != 0 {
		t.Errorf("无信号时不应有动作：%+v", state.Stats)
	}
}

// 未启用 / provider 为 nil / state 为 nil 时都必须原样返回。
func TestApplyDisabledReturnsPlan(t *testing.T) {
	plan := score.RotationPlan{Rows: []score.RotationRow{buyRow("600000", 0, 2)}}
	provider := fakeProvider{"600000": {Buy2: buySignal(chanlun.SignalBuy2, 20260101, 70)}}

	disabled := DefaultConfig()
	disabled.Enable = false
	if got := Apply(plan, nil, 20260102, provider, disabled, NewState()); !reflect.DeepEqual(got, plan) {
		t.Error("未启用时计划不应变化")
	}
	if got := Apply(plan, nil, 20260102, nil, DefaultConfig(), NewState()); !reflect.DeepEqual(got, plan) {
		t.Error("provider 为 nil 时计划不应变化")
	}
	if got := Apply(plan, nil, 20260102, provider, DefaultConfig(), nil); !reflect.DeepEqual(got, plan) {
		t.Error("state 为 nil 时计划不应变化")
	}
}

// 卖出用 held 回填层数：计划行没带 CurrentLayers 时也不能漏减。
func TestSellFallsBackToHeldLayers(t *testing.T) {
	cfg := DefaultConfig()
	plan := score.RotationPlan{Rows: []score.RotationRow{{
		Code: "600000", Name: "600000", Action: score.ActionKeep,
		SourceType: "holding", CurrentLayers: 0, TargetLayers: 0,
	}}}
	held := []score.Row{{Code: "600000", PositionLayers: 3}}
	state := NewState()

	got := Apply(plan, held, 20260101,
		fakeProvider{"600000": snapshotWithSell(chanlun.SignalSell1)}, cfg, state)
	if got.Rows[0].TargetLayers != 2 {
		t.Errorf("应从 held 回填 3 层并减到 2，实际 %d", got.Rows[0].TargetLayers)
	}
}

// 打开 BoostHoldings 后，持仓中的确认三买可以把 Keep 改成 add_layer。
func TestBoostHoldingsAddsLayersToHeldPosition(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BoostHoldings = true
	provider := fakeProvider{"600000": {Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}
	plan := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600000", 5, 5, score.ActionKeep),
	}}
	state := NewState()

	got := Apply(plan, nil, 20260101, provider, cfg, state)
	row := got.Rows[0]
	if row.Action != score.ActionAddLayer {
		t.Errorf("应改成 add_layer，实际 %s", row.Action)
	}
	if row.TargetLayers != 7 || row.LayersDelta != 2 {
		t.Errorf("5 层 + 三买 2 层应到 7 层，实际 target=%d delta=%d", row.TargetLayers, row.LayersDelta)
	}
	if state.Stats.HoldBoosts != 1 || state.Stats.Buy3Boosts != 1 {
		t.Errorf("持仓加层计数不对：%+v", state.Stats)
	}

	// 默认（不打开）时同一行不能被改。
	cfgDefault := DefaultConfig()
	state2 := NewState()
	got2 := Apply(plan, nil, 20260101, provider, cfgDefault, state2)
	if got2.Rows[0].Action != score.ActionKeep || got2.Rows[0].TargetLayers != 5 {
		t.Errorf("默认不应改持仓行：%+v", got2.Rows[0])
	}
}

// 持仓加层同样受单股上限约束，并且同日有卖点时风险优先。
func TestBoostHoldingsCapAndConflict(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BoostHoldings = true
	cfg.MaxLayersPerStock = 7

	// 已经 6 层：只能加 1 层。
	plan := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600000", 6, 6, score.ActionKeep),
	}}
	state := NewState()
	got := Apply(plan, nil, 20260101, fakeProvider{
		"600000": {Buy3: buySignal(chanlun.SignalBuy3, 20251231, 80)}}, cfg, state)
	if got.Rows[0].TargetLayers != 7 || state.Stats.BuyLayersAdded != 1 {
		t.Errorf("应只加 1 层，实际 target=%d added=%d", got.Rows[0].TargetLayers, state.Stats.BuyLayersAdded)
	}

	// 同日有卖点：卖出优先，不加层（5 层先被三卖减到 3 层）。
	plan2 := score.RotationPlan{Rows: []score.RotationRow{
		holdingRow("600001", 5, 5, score.ActionKeep),
	}}
	state2 := NewState()
	got2 := Apply(plan2, nil, 20260101, fakeProvider{
		"600001": {Buy2: buySignal(chanlun.SignalBuy2, 20251231, 70),
			Sell3: sellSignal(chanlun.SignalSell3, 20251231)}}, cfg, state2)
	if got2.Rows[0].TargetLayers != 3 || got2.Rows[0].Action != score.ActionReduce {
		t.Errorf("有卖点时应优先卖出（5→3 层），实际 %+v", got2.Rows[0])
	}
	if state2.Stats.HoldBoosts != 0 {
		t.Errorf("有卖点时不应加层，实际 holdBoosts=%d", state2.Stats.HoldBoosts)
	}
}

// 层数字段全零时按默认方案补齐；显式零仍然表示「该侧关闭」。
func TestConfigSanitizeDefaults(t *testing.T) {
	cfg := Config{Enable: true}.Sanitized()
	if cfg.Buy2ExtraLayers != 1 || cfg.Buy3ExtraLayers != 2 ||
		cfg.Sell1Layers != 1 || cfg.Sell2Layers != 1 || cfg.Sell3Layers != 2 {
		t.Errorf("全零配置应补默认权重：%+v", cfg)
	}

	sellsOnly := Config{Enable: true, Sell1Layers: 1, Sell2Layers: 1, Sell3Layers: 2}.Sanitized()
	if sellsOnly.Buy2ExtraLayers != 0 || sellsOnly.Buy3ExtraLayers != 0 {
		t.Errorf("显式关闭买入侧时不应被默认值覆盖：%+v", sellsOnly)
	}
	if !sellsOnly.HasSellRule() || sellsOnly.HasBuyRule() {
		t.Errorf("sells_only 的启用判定不对：%+v", sellsOnly)
	}
}
