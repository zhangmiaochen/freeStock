package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// **加仓路径的真实数据端到端验证**：全市场评分 → 生成建议（应有 add 行）→ 一键应用 → 持仓被建起来。
//
// 为什么需要单独一个用例：
//   - `TestApplyRotationEndToEnd` 用 300 只小样本，广度闸门会删掉所有 add 行，
//     所以它只覆盖到**清仓**；
//   - 回测那个用例验证的是**引擎内部**的买入，不是服务层的 `BuildPlan → ApplyRotation`。
//
// 全市场刷新约 35 秒，用 FREESTOCK_TIMING=1 开启。
func TestApplyRotationAddPathOnFullMarket(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("要全市场评分（约 35 秒）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// ① 全市场评分 —— limit=0 才会让广度闸门拿到真实的全市场样本
	began := time.Now()
	refreshed, err := svc.RefreshStrength(score.StrategyTrendGrowth, 0, true)
	if err != nil {
		t.Fatalf("刷新评分失败：%v", err)
	}
	if refreshed.Saved == 0 {
		t.Skip("全市场评分没产出数据")
	}
	t.Logf("① 全市场评分：%d 只，耗时 %s", refreshed.Saved, time.Since(began).Round(time.Second))

	// ② 建一个**空仓**组合 —— 有开仓空间才会产生 add
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "加仓路径验证", Type: portfolio.TypeSemiAuto,
		StrategyID:          score.StrategyTrendGrowth,
		TargetPositionCount: intPtr(5),
		MaxStockCount:       intPtr(8),
		TotalLayers:         10,
	})
	if err != nil {
		t.Fatalf("建组合失败：%v", err)
	}

	// ③ 生成建议
	plan, err := svc.BuildPlan(created.ID)
	if err != nil {
		t.Fatalf("生成建议失败：%v", err)
	}
	adds := 0
	for _, row := range plan.Plan.Rows {
		if row.Action == score.ActionAdd || row.Action == score.ActionReplaceIn {
			adds++
		}
	}
	t.Logf("② 市场状态 %s，广度 %d/%d（全市场 %d，放行=%v），建议 %d 条，其中可买入 %d 条",
		plan.MarketState.State,
		plan.Plan.Breadth.ReadyCount, plan.Plan.Breadth.MinReadyCount,
		plan.Plan.Breadth.UniverseCount, plan.Plan.EntryAllowed,
		len(plan.Plan.Rows), adds)

	if adds == 0 {
		t.Fatalf("全市场样本下仍无 add 行 —— 广度=%d/%d 或候选池有问题；节点=%v",
			plan.Plan.Breadth.ReadyCount, plan.Plan.Breadth.MinReadyCount,
			plan.Plan.NoActionReasons)
	}

	// ④ 一键应用
	applied, err := svc.ApplyRotation(created.ID)
	if err != nil {
		t.Fatalf("应用建议失败：%v", err)
	}
	t.Logf("③ 应用结果：新增 %d，减仓 %d，清仓 %d",
		applied.Applied.Added, applied.Applied.Reduced, applied.Applied.Cleared)

	if applied.Applied.Added == 0 {
		t.Errorf("有 %d 条可买入建议，应用后却一只都没新增", adds)
	}

	// ⑤ 落库结果必须自洽
	detail, err := svc.GetPortfolioDetail(created.ID)
	if err != nil {
		t.Fatalf("读组合详情失败：%v", err)
	}
	if len(detail.Positions) == 0 {
		t.Fatal("应用后仍无持仓")
	}
	// 层数在合法区间、权重 = 层数/总层数
	for _, position := range detail.Positions {
		if position.PositionLayers < 1 {
			t.Errorf("%s 层数应 ≥1，实际 %d", position.Code, position.PositionLayers)
		}
		wantWeight := portfolio.WeightFromLayers(position.PositionLayers, created.TotalLayers)
		if diff := position.PositionWeight - wantWeight; diff > 0.01 || diff < -0.01 {
			t.Errorf("%s 权重应为 %.4f（层数 %d / 总层 %d），实际 %.4f",
				position.Code, wantWeight, position.PositionLayers, created.TotalLayers,
				position.PositionWeight)
		}
		if position.EntryPrice <= 0 {
			t.Errorf("%s 成本价应 > 0，实际 %.4f", position.Code, position.EntryPrice)
		}
	}
	// 每个新增持仓都应有一条 buy 流水，且来源是 `组合:日期:代码`
	// ⚠ 流水里的 action **不是** "buy" —— 原实现是
	// `action=action or ("add" if layer_delta > 0 else "clear")`，
	// 所以加仓写的 action 就是计划行的动作名（add / replace_in）。
	// 按**层数变化方向**数才稳。
	buyTxns := 0
	for _, txn := range detail.Transactions {
		if txn.LayersDelta > 0 {
			buyTxns++
		}
	}
	if buyTxns < applied.Applied.Added {
		t.Errorf("新增 %d 只却只有 %d 条加仓流水", applied.Applied.Added, buyTxns)
	}
	t.Logf("④ 落库 %d 只持仓、%d 条加仓流水；层数与权重自洽",
		len(detail.Positions), buyTxns)
}
