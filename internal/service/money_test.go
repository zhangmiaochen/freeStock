package service

import (
	"path/filepath"
	"testing"

	"freestock/internal/portfolio"
)

// 资金同步：按层数把权益换算成整手股数，现金 = 权益 - 持仓市值。
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
