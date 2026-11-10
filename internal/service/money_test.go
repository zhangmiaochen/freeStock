package service

import (
	"path/filepath"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 其它策略（10 层制）也按实际本金折算股数。
func TestApplyMoneyToPlanLowBuy(t *testing.T) {
	svc := NewPortfolioServiceAt(nil, filepath.Join(t.TempDir(), "portfolio.db"))
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(portfolio.PortfolioInput{
		Name: "低吸资金", Type: portfolio.TypeAuto,
		StrategyID: portfolio.StrategyTrendLowBuy, TotalLayers: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetInitialCapital(item.ID, 100000); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRotation(item.ID, "20261103", []portfolio.ApplyAction{
		{Code: "600000", Action: "add", TargetLayers: 3, Price: 50},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncPortfolioMoney(item.ID); err != nil {
		t.Fatal(err)
	}

	plan := &PlanResult{
		Config: score.StrategyConfig{TotalLayers: 10},
		Plan: score.RotationPlan{Rows: []score.RotationRow{
			{Code: "600000", TargetLayers: 3},
			{Code: "600519", TargetLayers: 2},
		}},
		Candidates: []score.Row{
			{Code: "600000", Close: 50},
			{Code: "600519", Close: 1000},
		},
	}
	if err := svc.ApplyMoneyToPlan(item.ID, plan); err != nil {
		t.Fatal(err)
	}
	// 权益 10 万：3/10 = 3 万 → 50 元 → 600 股；2/10 = 2 万 → 1000 元/股 → 一手 10 万 > 2 万 → 买不起
	if plan.Plan.Rows[0].TargetShares != 600 {
		t.Errorf("600000 应为 600 股，实际 %d", plan.Plan.Rows[0].TargetShares)
	}
	if !plan.Plan.Rows[0].Affordable {
		t.Error("600000 应标记为买得起")
	}
	if plan.Plan.Rows[1].TargetShares != 0 || plan.Plan.Rows[1].Affordable {
		t.Errorf("600519 一手 10 万 > 2 万预算，应买不起，实际 %d 股", plan.Plan.Rows[1].TargetShares)
	}
}

func TestSyncPortfolioMoney(t *testing.T) {
	svc := NewPortfolioServiceAt(nil, filepath.Join(t.TempDir(), "portfolio.db"))
	t.Cleanup(svc.Close)
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Create(portfolio.PortfolioInput{
		Name: "资金测试", Type: portfolio.TypeAuto,
		StrategyID: portfolio.StrategyQualityGrowth, TotalLayers: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetInitialCapital(item.ID, 100000); err != nil {
		t.Fatal(err)
	}
	// 用"一键应用"建仓（不受手动 [1,2] 层 clamp，可写 50 层）
	if _, err := store.ApplyRotation(item.ID, "20261103", []portfolio.ApplyAction{
		{Code: "600519", Action: "add", TargetLayers: 50, Price: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRotation(item.ID, "20261103", []portfolio.ApplyAction{
		{Code: "000001", Action: "add", TargetLayers: 50, Price: 10},
	}); err != nil {
		t.Fatal(err)
	}

	m, err := svc.SyncPortfolioMoney(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 权益 10 万：600519 分 5 万 → 500 股；000001 分 5 万 → 5000 股；现金 0
	if m.Cash > 1 {
		t.Errorf("现金应接近 0，实际 %.2f", m.Cash)
	}
	shares, err := store.SharesMap(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if shares["600519"] != 500 {
		t.Errorf("600519 应为 500 股，实际 %d", shares["600519"])
	}
	if shares["000001"] != 5000 {
		t.Errorf("000001 应为 5000 股，实际 %d", shares["000001"])
	}

	// 改本金为 20 万并重新同步：股数翻倍、现金仍接近 0
	if _, err := store.SetInitialCapital(item.ID, 200000); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncPortfolioMoney(item.ID); err != nil {
		t.Fatal(err)
	}
	shares, _ = store.SharesMap(item.ID)
	if shares["600519"] != 1000 || shares["000001"] != 10000 {
		t.Errorf("本金翻倍后股数应翻倍，实际 600519=%d 000001=%d", shares["600519"], shares["000001"])
	}

	view, err := svc.GetPortfolioMoney(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.InitialCapital != 200000 {
		t.Errorf("初始本金应为 200000，实际 %.0f", view.InitialCapital)
	}
	if view.Equity < 199000 || view.Equity > 201000 {
		t.Errorf("当前权益应接近 20 万，实际 %.0f", view.Equity)
	}
}
