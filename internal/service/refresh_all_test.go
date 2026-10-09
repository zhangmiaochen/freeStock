package service

import (
	"testing"

	"freestock/internal/portfolio"
)

// 「一次刷所有活跃策略」的策略集合选择 —— 对应原实现 `_active_strategy_ids`
// （portfolio_service.py:2675-2684）：取所有**非 archived** 组合用到的策略去重。
//
// 为什么重要：如果只刷当前选中组合的策略，**其它组合的评分会悄悄过期**，
// 之后点「生成建议」就会基于旧数据。
func TestActiveStrategyIDs(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// ① 一个组合都没有 → 回落到默认策略
	ids, err := svc.ActiveStrategyIDs()
	if err != nil {
		t.Fatalf("取活跃策略失败：%v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("没有组合时应回落到 1 个默认策略，实际 %v", ids)
	}
	t.Logf("无组合：%v", ids)

	// ② 建三个组合：两个不同策略 + 一个会被归档的
	mk := func(name string, strategy string, status string) {
		_, err := svc.CreatePortfolio(portfolio.PortfolioInput{
			Name: name, Type: portfolio.TypeSemiAuto,
			StrategyID: strategy, Status: status,
			TargetPositionCount: intPtr(3), MaxStockCount: intPtr(5), TotalLayers: 10,
		})
		if err != nil {
			t.Fatalf("建组合 %s 失败：%v", name, err)
		}
	}
	mk("成长一号", "trend_growth", "active")
	mk("低吸一号", "trend_pullback_low_buy", "active")
	mk("已归档的成长", "trend_growth", "archived")

	ids, err = svc.ActiveStrategyIDs()
	if err != nil {
		t.Fatalf("取活跃策略失败：%v", err)
	}
	// 去重后应恰好两个（trend_growth 去重；archived 被排除）
	if len(ids) != 2 {
		t.Fatalf("应有 2 个活跃策略（去重 + 排除归档），实际 %v", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["trend_growth"] || !seen["trend_pullback_low_buy"] {
		t.Errorf("应包含 trend_growth 与 trend_pullback_low_buy，实际 %v", ids)
	}
	t.Logf("两个活跃组合 + 一个归档：%v", ids)

	// ③ 把两个活跃的都归档 → 回落默认策略
	items, err := svc.ListPortfolios()
	if err != nil {
		t.Fatalf("列组合失败：%v", err)
	}
	for _, item := range items {
		if item.Status == portfolio.StatusArchived {
			continue
		}
		if _, err := svc.UpdatePortfolio(item.ID, portfolio.PortfolioInput{
			Name: item.Name, Type: item.Type, StrategyID: item.StrategyID,
			Status:              portfolio.StatusArchived,
			TargetPositionCount: intPtr(item.TargetPositionCount),
			MaxStockCount:       intPtr(item.MaxStockCount),
			TotalLayers:         item.TotalLayers,
		}); err != nil {
			t.Fatalf("归档 %s 失败：%v", item.Name, err)
		}
	}
	ids, err = svc.ActiveStrategyIDs()
	if err != nil {
		t.Fatalf("取活跃策略失败：%v", err)
	}
	if len(ids) != 1 {
		t.Errorf("全部归档后应回落到 1 个默认策略，实际 %v", ids)
	}
	t.Logf("全部归档：%v", ids)
}
