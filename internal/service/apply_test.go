package service

import (
	"strings"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// intPtr / floatPtr 由生产代码提供（见 auto_portfolio.go）——
// 它们不是测试专用：`PortfolioInput` / `PositionInput` 的指针字段
// 在业务代码里也要构造。

// 一键应用建议的端到端：刷新评分 → 生成建议 → 应用 → 持仓被正确改写。
//
// 这个用例专门覆盖 store 层测试抓不到的一类 bug：
// **service 层如果把 keep/observe 的行过滤掉，整表重写会把健康持仓静默删除。**
func TestApplyRotationEndToEnd(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "应用测试组合", Type: "auto", StrategyID: score.StrategyTrendGrowth,
		TargetPositionCount: intPtr(3), MaxStockCount: intPtr(5), TotalLayers: (10),
		ScopeType: portfolio.ScopeAll,
	})
	if err != nil {
		t.Fatalf("创建组合失败: %v", err)
	}
	// 三只持仓：一只有浮盈（应被保留）、两只深亏（可能被清仓）
	for _, item := range []struct {
		code  string
		price float64
	}{
		{"600519", 1500},
		{"000001", 11},
		{"601318", 60},
	} {
		if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: item.code, EntryPrice: item.price, Layers: intPtr(1), EntryDate: "20260801",
		}); err != nil {
			t.Fatalf("加持仓 %s 失败: %v", item.code, err)
		}
	}

	before, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("读详情失败: %v", err)
	}
	if len(before.Positions) != 3 {
		t.Fatalf("应用前应有 3 个持仓，实际 %d", len(before.Positions))
	}

	if _, err := svc.RefreshStrength(score.StrategyTrendGrowth, 300, true); err != nil {
		t.Fatalf("刷新评分失败: %v", err)
	}

	// 先生成建议看看会做什么
	plan, err := svc.BuildPlan(created.ID)
	if err != nil {
		t.Fatalf("生成建议失败: %v", err)
	}
	t.Logf("市场状态 %s，建议 %d 条", plan.MarketState.State, len(plan.Plan.Rows))
	for _, row := range plan.Plan.Rows {
		t.Logf("  %-6s %-6s 现 %d 层 → %d 层（可执行=%v）",
			row.ActionLabel, row.Code, row.CurrentLayers, row.TargetLayers, row.IsExecutable)
	}

	result, err := svc.ApplyRotation(created.ID)
	if err != nil {
		// 全是 keep/observe 时拒绝是正确行为
		if strings.Contains(err.Error(), "没有可执行的动作") {
			t.Skipf("本次建议里没有可执行动作，跳过应用验证: %v", err)
		}
		t.Fatalf("应用失败: %v", err)
	}
	t.Logf("应用结果：持仓 %d，新增 %d，减仓 %d，清仓 %d，不变 %d",
		result.Applied.PositionCount, result.Applied.Added,
		result.Applied.Reduced, result.Applied.Cleared, result.Applied.Unchanged)

	after, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("应用后读详情失败: %v", err)
	}

	// **关键断言**：keep/observe 的持仓必须保留下来。
	// 只传可执行动作会让它们被整表重写悄悄删掉。
	for _, row := range plan.Plan.Rows {
		if row.Action != score.ActionKeep && row.Action != score.ActionObserve {
			continue
		}
		found := false
		for _, position := range after.Positions {
			if position.Code == row.Code {
				found = true
				if position.PositionLayers != row.CurrentLayers {
					t.Errorf("keep/observe 的 %s 层数不该变: %d → %d",
						row.Code, row.CurrentLayers, position.PositionLayers)
				}
			}
		}
		if !found {
			t.Errorf("keep/observe 的持仓 %s 被静默删除了", row.Code)
		}
	}

	// 清仓的持仓确实消失
	for _, row := range plan.Plan.Rows {
		if row.Action != score.ActionClear {
			continue
		}
		for _, position := range after.Positions {
			if position.Code == row.Code {
				t.Errorf("应被清仓的 %s 还在", row.Code)
			}
		}
	}

	// 流水应记录本次应用
	//
	// U10：`source_signal_id` 是 `组合:日期:代码`（原实现 portfolio_service.py:923），
	// 不再是常量 "apply_rotation"。
	txns := after.Transactions
	applied := 0
	for _, txn := range txns {
		if strings.HasPrefix(txn.SourceSignalID, created.ID+":") {
			applied++
			if txn.Reason != "一键应用调仓建议" {
				t.Errorf("reason 应为「一键应用调仓建议」，实际 %q", txn.Reason)
			}
		}
	}
	if applied == 0 {
		t.Errorf("应写入以 `组合:日期:代码` 为来源的流水（前缀 %s:）", created.ID)
	}
	t.Logf("应用后持仓 %d 只，新增流水 %d 条", len(after.Positions), applied)
}

// 手动组合拒绝一键应用，且提示要说清原因。
func TestApplyRotationManualRejected(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "手动组合", Type: "manual", StrategyID: score.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	_, err = svc.ApplyRotation(created.ID)
	if err == nil {
		t.Fatal("手动组合应拒绝一键应用")
	}
	if !strings.Contains(err.Error(), "仅生成分析提醒") {
		t.Errorf("提示应说明原因: %s", err)
	}
	t.Logf("提示: %s", err)
}
