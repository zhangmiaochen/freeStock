package service

import (
	"strings"
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 端到端：真实持仓 → 真实评分 → 真实调仓建议。
//
// 这是阶段二第一次把整条链路跑通：评分物化 → 候选池 → 轮动决策。
func TestBuildPlanEndToEnd(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	// 建一个组合 + 几个真实持仓
	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "端到端组合", Type: "semi_auto", StrategyID: score.StrategyTrendGrowth,
		TargetPositionCount: 3, MaxStockCount: 5, TotalLayers: (10),
		ScopeType: portfolio.ScopeAll,
	})
	if err != nil {
		t.Fatalf("创建组合失败: %v", err)
	}
	for _, item := range []struct {
		code  string
		price float64
	}{
		{"600519", 1500},
		{"000001", 12},
		{"601318", 60},
	} {
		if _, err := svc.AddPosition(created.ID, portfolio.PositionInput{
			Code: item.code, EntryPrice: item.price, Layers: intPtr(1), EntryDate: "20260901",
		}); err != nil {
			t.Fatalf("加持仓 %s 失败: %v", item.code, err)
		}
	}

	// ---- 刷新评分 ----
	refresh, err := svc.RefreshStrength(score.StrategyTrendGrowth, 300)
	if err != nil {
		t.Fatalf("刷新评分失败: %v", err)
	}
	if refresh.Saved == 0 {
		t.Fatal("应物化出评分")
	}
	// **持仓必须被评上**（哪怕不在股票列表前 300 只里）
	if refresh.Saved < 300 {
		t.Errorf("评分条数应 >= limit(300)，实际 %d", refresh.Saved)
	}
	t.Logf("评分：日 %d，物化 %d 条，市场状态 %s（%s）bull=%.1f bear=%.1f",
		refresh.TradeDate, refresh.Saved, refresh.MarketState.State,
		refresh.MarketState.Label, refresh.MarketState.BullScore, refresh.MarketState.BearScore)

	// ---- 出建议 ----
	plan, err := svc.BuildPlan(created.ID)
	if err != nil {
		t.Fatalf("生成建议失败: %v", err)
	}

	// 持仓都要有评分（不能是左连接落空的全 0）
	if len(plan.Holdings) != 3 {
		t.Fatalf("应有 3 个持仓行，实际 %d", len(plan.Holdings))
	}
	for _, row := range plan.Holdings {
		if row.Name == "" {
			t.Errorf("%s 应带上名称", row.Code)
		}
		if row.TotalScore <= 0 {
			t.Errorf("%s 的评分不该是 0（说明没评上）: %+v", row.Code, row)
		}
		if row.CurrentPrice <= 0 {
			t.Errorf("%s 应有现价: %v", row.Code, row.CurrentPrice)
		}
		// 盈亏比例要与成本/现价一致
		want := (row.CurrentPrice/row.EntryPrice - 1) * 100
		if diff := row.ProfitLossPct - want; diff > 0.01 || diff < -0.01 {
			t.Errorf("%s 盈亏比例不对: %v vs %v", row.Code, row.ProfitLossPct, want)
		}
		if row.HoldingTradeDays < 1 {
			t.Errorf("%s 持有交易日数应 >=1: %d", row.Code, row.HoldingTradeDays)
		}
		if row.HighestPrice < row.EntryPrice || row.HighestPrice < row.CurrentPrice {
			t.Errorf("%s 最高价应不低于成本与现价: %v", row.Code, row.HighestPrice)
		}
		t.Logf("持仓 %s %s 评分 %.2f（成本 %.2f 现价 %.2f 盈亏 %.2f%% 持有 %d 日 最高 %.2f）",
			row.Code, row.Name, row.TotalScore, row.EntryPrice, row.CurrentPrice,
			row.ProfitLossPct, row.HoldingTradeDays, row.HighestPrice)
	}

	// 建议必须有内容
	if len(plan.Plan.Rows) == 0 {
		t.Fatal("应产出调仓建议")
	}
	for _, row := range plan.Plan.Rows {
		if row.Action == "" {
			t.Errorf("动作不该为空: %+v", row)
		}
		if row.ActionLabel == "" {
			t.Errorf("动作应有中文标签: %+v", row)
		}
		// 层数变化要自洽
		if row.LayersDelta != row.TargetLayers-row.CurrentLayers {
			t.Errorf("%s 层数变化不自洽: %+v", row.Code, row)
		}
		if row.TargetLayers < 0 {
			t.Errorf("%s 目标层数不该为负: %d", row.Code, row.TargetLayers)
		}
		// 执行语义按组合类型（半自动）
		if row.ExecutionMode != "confirm_required" {
			t.Errorf("%s 执行模式应为 confirm_required: %s", row.Code, row.ExecutionMode)
		}
		if row.Action == score.ActionKeep || row.Action == score.ActionObserve {
			if row.IsExecutable {
				t.Errorf("%s 的 %s 不该是可执行动作", row.Code, row.Action)
			}
			if row.ActionPrefix != "" {
				t.Errorf("%s 的 %s 不该有动作前缀", row.Code, row.Action)
			}
		}
		t.Logf("建议 %-4s %-6s 评分 %.2f 现 %d 层 → 目标 %d 层（%s）",
			row.ActionLabel, row.Code, row.Score, row.CurrentLayers, row.TargetLayers, row.Reason)
	}

	// 候选池
	if len(plan.Candidates) > 0 {
		for _, candidate := range plan.Candidates {
			if candidate.TotalScore < plan.Config.CandidateScoreFloor {
				t.Errorf("候选 %s 低于分数下限 %v: %v",
					candidate.Code, plan.Config.CandidateScoreFloor, candidate.TotalScore)
			}
			if strings.Contains(strings.ToUpper(candidate.Name), "ST") {
				t.Errorf("候选不该包含 ST 股: %s %s", candidate.Code, candidate.Name)
			}
		}
		t.Logf("候选池 %d 只，最高分 %.2f（%s）",
			len(plan.Candidates), plan.Candidates[0].TotalScore, plan.Candidates[0].Name)
	} else {
		t.Logf("候选池为空（当前市场状态 %s 下没有达标的票）", plan.MarketState.State)
	}

	if plan.StrategyID != score.StrategyTrendGrowth {
		t.Errorf("策略应保留: %s", plan.StrategyID)
	}
	if plan.StrengthDate != refresh.TradeDate {
		t.Errorf("建议应基于最新评分日: %d vs %d", plan.StrengthDate, refresh.TradeDate)
	}
	for _, note := range plan.Notes {
		t.Logf("说明: %s", note)
	}
}

// 没有评分时给出可操作的提示，而不是空白或 panic。
func TestBuildPlanWithoutStrength(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	created, err := svc.CreatePortfolio(portfolio.PortfolioInput{
		Name: "没评分的组合", StrategyID: score.StrategyTrendGrowth,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	_, err = svc.BuildPlan(created.ID)
	if err == nil {
		t.Fatal("没有评分时应报错")
	}
	// 错误信息要告诉用户下一步做什么
	message := err.Error()
	if !strings.Contains(message, "刷新评分") {
		t.Errorf("错误信息应告诉用户要刷新评分: %s", message)
	}
	if !strings.Contains(message, "没评分的组合") {
		t.Errorf("错误信息应带上组合名: %s", message)
	}
	t.Logf("提示信息: %s", message)
}

// 不存在的组合
func TestBuildPlanUnknownPortfolio(t *testing.T) {
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	if _, err := svc.BuildPlan("pf_不存在"); err == nil {
		t.Error("不存在的组合应报错")
	}
}

// 评分物化状态
func TestStrengthSummary(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)
	t.Cleanup(svc.Close)

	// 没刷新过时是空的
	summary, err := svc.GetStrengthSummary(score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("取摘要失败: %v", err)
	}
	if summary.LatestDate != 0 || summary.Count != 0 {
		t.Errorf("没刷新过时应为空: %+v", summary)
	}

	if _, err := svc.RefreshStrength(score.StrategyTrendGrowth, 150); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	summary, err = svc.GetStrengthSummary(score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("取摘要失败: %v", err)
	}
	if summary.LatestDate == 0 || summary.Count == 0 {
		t.Errorf("刷新后应能读到: %+v", summary)
	}
	if len(summary.Dates) == 0 {
		t.Error("应列出评分日")
	}
	// 另一个策略还没刷新
	other, err := svc.GetStrengthSummary(score.StrategyTrendLowBuy)
	if err != nil {
		t.Fatalf("取摘要失败: %v", err)
	}
	if other.LatestDate != 0 {
		t.Errorf("低吸策略还没刷新过，应为空: %+v", other)
	}
	t.Logf("趋势成长：日 %d，%d 条，评分日 %v", summary.LatestDate, summary.Count, summary.Dates)
}

// 评分必须跨重启保留（物化的意义就在于此）。
func TestStrengthPersistsAcrossReopen(t *testing.T) {
	svc, _ := newPortfolioService(t)
	requireLocalServer(t, svc.stock)

	if _, err := svc.RefreshStrength(score.StrategyTrendGrowth, 120); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	before, err := svc.GetStrengthSummary(score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("取摘要失败: %v", err)
	}
	dbPath := svc.GetDatabasePath()

	// 关掉再打开
	svc.Close()
	reopened := NewPortfolioServiceAt(svc.stock, dbPath)
	t.Cleanup(reopened.Close)

	after, err := reopened.GetStrengthSummary(score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("重开后取摘要失败: %v", err)
	}
	if after.LatestDate != before.LatestDate || after.Count != before.Count {
		t.Errorf("评分应跨重启保留: 前 %+v，后 %+v", before, after)
	}
	t.Logf("重开后仍读到 %d 条评分（日 %d）", after.Count, after.LatestDate)
}

// 低吸策略的 market_scope 要从组合的市场范围推出来。
func TestScopeKeyOf(t *testing.T) {
	cases := []struct {
		markets []string
		want    string
	}{
		{nil, "all"},
		{[]string{"main", "gem", "star"}, "all"},
		{[]string{"main"}, "main"},
		{[]string{"gem"}, "gem"},
		{[]string{"main", "gem"}, "main_gem"},
		{[]string{"gem", "star"}, "gem_star"},
		{[]string{"main", "star"}, "main_star"},
	}
	for _, c := range cases {
		raw := portfolio.NormalizeScopeValue(portfolio.ScopeAll,
			portfolio.ScopeValue{Markets: c.markets})
		item := portfolio.Portfolio{ScopeValueJSON: raw}
		if got := scopeKeyOf(item, nil); got != c.want {
			t.Errorf("markets=%v → %q，期望 %q", c.markets, got, c.want)
		}
	}
}
