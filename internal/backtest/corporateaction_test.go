package backtest

import "testing"

// 除权除息结算的语义测试。
//
// 这些语义**错了不会报错**，只会静默把账算错，所以必须用测试钉住：
//
//  1. 现金分红按**除权前**股数发放（顺序：先分红 → 后送转）；
//  2. 送转/拆股后**股数变、成本摊薄**（CostPrice /= ShareRatio）——
//     不摊薄就会出现「1 送 1 之后账面 −50%」这种假亏损（实测回测里出现过一次
//     最惨单笔 −49.80%，正是未复权 + 未处理公司行为造成的同类伪影）；
//  3. 只调整**持有**的股票，且未注入公司行为时账本一行都不动。
func TestApplyCorporateActionsSemantics(t *testing.T) {
	cases := []struct {
		name       string
		action     CorporateAction
		shares     int
		cash       float64
		cost       float64
		wantShares int
		wantCash   float64
		wantCost   float64
	}{
		{
			name:   "10送3且每股派0.5：先按1000股分红、再送转到1300股、成本摊薄",
			action: CorporateAction{Code: "600000", CashDividend: 0.5, ShareRatio: 1.3},
			shares: 1000, cash: 0, cost: 10,
			wantShares: 1300, wantCash: 500, // 1000 × 0.5（不是 1300 × 0.5）
			wantCost: 10.0 / 1.3,
		},
		{
			name:   "纯现金分红：股数与成本都不变",
			action: CorporateAction{Code: "600000", CashDividend: 0.733, ShareRatio: 1},
			shares: 1000, cash: 100, cost: 8.8,
			wantShares: 1000, wantCash: 833, wantCost: 8.8,
		},
		{
			name:   "1拆3：股数×3、成本÷3、无现金",
			action: CorporateAction{Code: "600000", ShareRatio: 3},
			shares: 900, cash: 50, cost: 30,
			wantShares: 2700, wantCash: 50, wantCost: 10,
		},
		{
			name:   "ShareRatio 为 0（数据缺失）：按 1 处理，不把股数清零",
			action: CorporateAction{Code: "600000", CashDividend: 0.1, ShareRatio: 0},
			shares: 500, cash: 0, cost: 12,
			wantShares: 500, wantCash: 50, wantCost: 12,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			e.SetCorporateActions(map[string][]CorporateAction{"20250729": {c.action}})
			positions := map[string]*Position{
				c.action.Code: {Code: c.action.Code, Shares: c.shares, CostPrice: c.cost},
			}
			cash := c.cash
			e.applyCorporateActions("20250729", positions, &cash)

			pos := positions[c.action.Code]
			if pos.Shares != c.wantShares {
				t.Errorf("股数 = %d，期望 %d", pos.Shares, c.wantShares)
			}
			if diff := cash - c.wantCash; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("现金 = %v，期望 %v", cash, c.wantCash)
			}
			if diff := pos.CostPrice - c.wantCost; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("成本价 = %v，期望 %v", pos.CostPrice, c.wantCost)
			}
		})
	}
}

// TestApplyCorporateActionsOnlyTouchesHeld 只调整持有的股票：
// 当天有除权事件但组合没持有 → 现金与持仓都必须原封不动。
func TestApplyCorporateActionsOnlyTouchesHeld(t *testing.T) {
	e := &Engine{}
	e.SetCorporateActions(map[string][]CorporateAction{
		"20250729": {
			{Code: "600000", CashDividend: 1.0, ShareRatio: 2}, // 未持有
			{Code: "000001", CashDividend: 0.2, ShareRatio: 1}, // 持有
		},
	})
	positions := map[string]*Position{
		"000001": {Code: "000001", Shares: 100, CostPrice: 10},
	}
	cash := 0.0
	e.applyCorporateActions("20250729", positions, &cash)

	if _, ok := positions["600000"]; ok {
		t.Error("不应为未持有的股票建仓")
	}
	if cash != 20 {
		t.Errorf("现金 = %v，期望 20（只算持有的 000001）", cash)
	}
	if positions["000001"].Shares != 100 {
		t.Errorf("000001 股数 = %d，期望 100（ShareRatio=1 不应改变）", positions["000001"].Shares)
	}
}

// TestApplyCorporateActionsNoOpWhenNotInjected 未注入（或数据湖没有
// corporate_actions）时，账本必须完全不变 —— 这是"不破坏既有行为"的保证。
func TestApplyCorporateActionsNoOpWhenNotInjected(t *testing.T) {
	e := &Engine{}
	positions := map[string]*Position{
		"600000": {Code: "600000", Shares: 1000, CostPrice: 20},
	}
	cash := 1234.5
	e.applyCorporateActions("20250729", positions, &cash)

	pos := positions["600000"]
	if pos.Shares != 1000 || pos.CostPrice != 20 || cash != 1234.5 {
		t.Errorf("未注入时账本被改动：shares=%d cost=%v cash=%v", pos.Shares, pos.CostPrice, cash)
	}
}
