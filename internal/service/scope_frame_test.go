package service

import (
	"testing"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

func scopeRow(code, market, industry string, total float64) portfolio.StrengthRow {
	return portfolio.StrengthRow{
		Code: code, Name: codeMarketName(code), Market: market, Industry: industry,
		TotalScore: total, TrendScore: 25, StructureScore: 15, RSScore: 13,
		CapitalScore: 10, RepairScore: 10,
		Close: 100, Ma20: 100, Ma30: 100, Ma60: 95, Ma120: 90, Ma250: 85,
		RelativeStrength20D: 10, DrawdownFromHigh120: -10,
	}
}

func codeMarketName(code string) string { return code + "名" }

// U6：广度闸门用的帧是**范围过滤后**的，不是全量。
//
// 原实现 portfolio_service.py:2469 —— `score_frame = _scope_filter(...)` 之后
// 才作为 `daily_strength` 传给 `build_rotation_plan`，
// `market_breadth_status` 在其上数 `universe_count`。
//
// 用全量的话，窄范围组合（比如只选主板 20 只）在原版里 `universe < 100` → 闸门**不启用**，
// 在 Go 里却按全市场数千只启用，行为正好相反。
func TestBreadthFrameIsScopeFiltered(t *testing.T) {
	svc := &PortfolioService{}
	item := portfolio.Portfolio{
		ID: "pf_test", StrategyID: score.StrategyTrendGrowth,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType:      portfolio.ScopeAll,
		ScopeValueJSON: `{"markets":["main"]}`,
	}
	cfg := score.BuildStrategyConfig(score.StrategyTrendGrowth, score.StateRange, 5, 10, 3, "all")

	all := []portfolio.StrengthRow{
		scopeRow("600519", "main", "白酒", 80),
		scopeRow("601318", "main", "保险", 70),
		scopeRow("300750", "gem", "电池", 90),
		scopeRow("688981", "star", "半导体", 85),
	}
	_, universeScores := svc.buildCandidatePoolFrom(all, item, cfg, nil)

	// **只有主板的两只**应进入广度帧
	if len(universeScores) != 2 {
		t.Fatalf("广度帧应只含主板 2 只，实际 %d 只：%v", len(universeScores), universeScores)
	}
	for _, got := range universeScores {
		if got != 80 && got != 70 {
			t.Errorf("广度帧混入了范围外的分数：%v", universeScores)
		}
	}
}

// 全市场范围时，广度帧 = 全部评分（不含 ST / 持仓 / 分数下限的过滤）。
func TestBreadthFrameIncludesAllWhenScopeAll(t *testing.T) {
	svc := &PortfolioService{}
	item := portfolio.Portfolio{
		ID: "pf_test", StrategyID: score.StrategyTrendGrowth,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType: portfolio.ScopeAll, ScopeValueJSON: `{}`,
	}
	cfg := score.BuildStrategyConfig(score.StrategyTrendGrowth, score.StateRange, 5, 10, 3, "all")

	all := []portfolio.StrengthRow{
		scopeRow("600519", "main", "白酒", 80),
		scopeRow("000001", "main", "银行", 10), // 低于 candidate floor，但**仍算进广度帧**
		scopeRow("300750", "gem", "电池", 90),
	}
	_, universeScores := svc.buildCandidatePoolFrom(all, item, cfg, nil)
	if len(universeScores) != 3 {
		t.Errorf("全市场范围的广度帧应含 3 只（不受分数下限影响），实际 %d", len(universeScores))
	}
}

// U4：低吸**先过质量门槛、再截断**（原实现 :2072-2079）。
//
// 其余策略是"先截断、后在使用处过滤"。低吸照抄后者的话，
// head-80 取的是"全池前 80 名再过滤"，与"前 80 个合格候选"是不同的集合。
func TestLowBuyFiltersBeforeTruncation(t *testing.T) {
	svc := &PortfolioService{}
	item := portfolio.Portfolio{
		ID: "pf_lowbuy", StrategyID: score.StrategyTrendLowBuy,
		TargetPositionCount: 5, TotalLayers: 10,
		ScopeType: portfolio.ScopeAll, ScopeValueJSON: `{}`,
	}
	cfg := score.BuildStrategyConfig(score.StrategyTrendLowBuy, score.StateRange, 5, 10, 10, "main_gem")

	// 前 100 只分数高但结构不达标（会撑满 head-80）；第 101 只起才合格。
	// 若先截断，合格的票**根本进不了 head-80** → 候选池为空。
	var all []portfolio.StrengthRow
	for i := 0; i < 100; i++ {
		row := scopeRow("A"+pad(i), "main", "白酒", 95)
		row.Close = 200 // 远高于均线 → 会被"过热"等质量门槛拒掉
		row.Ma20 = 100
		row.Ma30 = 100
		all = append(all, row)
	}
	good := scopeRow("Z00001", "main", "白酒", 80)
	// 低吸的结构条件要求 ma20 > ma30 > ma60、ma120 > ma250 > ma250_prev
	good.Close, good.Ma5, good.Ma20, good.Ma30 = 100, 99, 102, 100
	good.Ma60, good.Ma120, good.Ma250 = 95, 90, 85
	good.Ma5Prev, good.Ma250Prev = 98, 84
	good.Open = 99
	good.StockReturn20D = 5
	good.DrawdownFromHigh20 = -10
	all = append(all, good)

	out, _ := svc.buildCandidatePoolFrom(all, item, cfg, nil)

	// 关键断言：合格的那只**必须在**候选池里（先过滤再截断才能保住它）
	found := false
	for _, row := range out {
		if row.Code == "Z00001" {
			found = true
		}
	}
	if !found {
		t.Errorf("低吸应先过滤再截断；合格的 Z00001 被截断丢掉了（候选池 %d 只）", len(out))
	}
}

func pad(v int) string {
	s := ""
	switch {
	case v < 10:
		s = "0000"
	case v < 100:
		s = "000"
	default:
		s = "00"
	}
	digits := ""
	for n := v; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	if digits == "" {
		digits = "0"
	}
	return s + digits
}
