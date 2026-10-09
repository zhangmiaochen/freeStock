package service

import (
	"fmt"
	"log/slog"
	"sort"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 自动建组合：按当前策略与范围，从最新评分里直接组出一个可用的初始组合。
//
// 1:1 移植自 portfolio_service.py:387-462（create/preview）+ :2280-2435
// （`_build_auto_compose_context` / `_auto_compose_payload`）。
//
// 核心思路：**用"空持仓"跑一次正常的轮动计划** —— 计划里的 add 行就是
// 该策略在当前市场状态下会建的首批仓，直接拿它们当初始持仓。
// 这样自动组合与后续的调仓建议天然同源，不会出现"自动建的组合
// 按策略标准根本不该持有"的情况。

// AutoPortfolioRequest 是自动建组合的入参。
type AutoPortfolioRequest struct {
	StrategyID          string               `json:"strategyId"`
	Name                string               `json:"name"`
	Type                string               `json:"type"`
	Status              string               `json:"status"`
	BenchmarkCode       string               `json:"benchmarkCode"`
	TargetPositionCount *int                 `json:"targetPositionCount"`
	MaxStockCount       *int                 `json:"maxStockCount"`
	TotalLayers         int                  `json:"totalLayers"`
	ScopeType           string               `json:"scopeType"`
	ScopeValue          portfolio.ScopeValue `json:"scopeValue"`
	Notes               string               `json:"notes"`
}

// AutoPortfolioResult 是预演/创建的结果。
type AutoPortfolioResult struct {
	OK        bool   `json:"ok"`
	Available bool   `json:"available"`
	Mode      string `json:"mode"`
	Message   string `json:"message"`
	TradeDate int    `json:"tradeDate"`
	// Portfolio 是**将要创建**（或已创建）的组合
	Portfolio portfolio.Portfolio `json:"portfolio"`
	Snapshot  AutoSnapshot        `json:"snapshot"`
	// SelectedPositions 是初始持仓
	SelectedPositions []score.Row `json:"selectedPositions"`
	// TopCandidates 是候选池前 20
	TopCandidates  []score.Row         `json:"topCandidates"`
	Rotation       []score.RotationRow `json:"rotation"`
	SelectedCount  int                 `json:"selectedCount"`
	CandidateCount int                 `json:"candidateCount"`
	PortfolioID    string              `json:"portfolioId,omitempty"`
	PositionCount  int                 `json:"positionCount,omitempty"`
}

// AutoSnapshot 是自动组合的概览快照。
type AutoSnapshot struct {
	PortfolioScore     float64 `json:"portfolioScore"`
	HealthLabel        string  `json:"healthLabel"`
	PositionCount      int     `json:"positionCount"`
	CandidateCount     int     `json:"candidateCount"`
	AddCount           int     `json:"addCount"`
	ActionBrief        string  `json:"actionBrief"`
	PortfolioType      string  `json:"portfolioType"`
	PortfolioTypeLabel string  `json:"portfolioTypeLabel"`
	ExecutionMode      string  `json:"executionMode"`
	ExecutionModeLabel string  `json:"executionModeLabel"`
}

// autoComposeContext 是预演与创建共用的中间结果。
type autoComposeContext struct {
	tradeDate  int
	item       portfolio.Portfolio
	snapshot   AutoSnapshot
	selected   []score.Row
	candidates []score.Row
	rotation   []score.RotationRow
}

// PreviewAutoPortfolio 预演自动组合（不落库）。
func (s *PortfolioService) PreviewAutoPortfolio(req AutoPortfolioRequest) (*AutoPortfolioResult, error) {
	ctx, err := s.buildAutoCompose(req)
	if err != nil {
		return nil, err
	}
	return s.autoComposePayload(ctx, "preview"), nil
}

// CreateAutoPortfolio 创建自动组合并落库（组合 + 持仓 + 买入流水）。
func (s *PortfolioService) CreateAutoPortfolio(req AutoPortfolioRequest) (*AutoPortfolioResult, error) {
	ctx, err := s.buildAutoCompose(req)
	if err != nil {
		return nil, err
	}
	if len(ctx.selected) == 0 {
		return nil, fmt.Errorf("自动组合没有可落库的候选持仓")
	}
	store, err := s.db()
	if err != nil {
		return nil, err
	}

	// 落库：组合（沿用归一化后的字段，只补 ID 与时间戳）
	created, err := store.Create(portfolio.PortfolioInput{
		Name: ctx.item.Name, Type: ctx.item.Type, StrategyID: ctx.item.StrategyID,
		BenchmarkCode: ctx.item.BenchmarkCode, Status: ctx.item.Status,
		TargetPositionCount: intPtr(ctx.item.TargetPositionCount),
		MaxStockCount:       intPtr(ctx.item.MaxStockCount),
		TotalLayers:         ctx.item.TotalLayers,
		ScopeType:           ctx.item.ScopeType,
		ScopeValue:          scopePtr(portfolio.ParseScopeValue(ctx.item.ScopeValueJSON)),
		Notes:               ctx.item.Notes,
	})
	if err != nil {
		return nil, err
	}
	ctx.item = created

	// 初始持仓：source_type / last_action 都是 auto_compose、strategy_tag 是策略 ID
	entryDate := fmt.Sprintf("%d", ctx.tradeDate)
	written := 0
	// 直接走 store 层：service 层的 AddPosition 有额度限制（防止误建几十个仓），
	// 而自动组合本来就是**一次性建一批** —— 那是设计目标，不是误操作。
	for _, row := range ctx.selected {
		layers := atLeastOne(row.PositionLayers)
		_, err := store.AddPosition(created.ID, portfolio.PositionInput{
			Code: row.Code, Name: row.Name,
			EntryPrice: row.Close, EntryDate: entryDate,
			Layers:      intPtr(layers),
			Weight:      floatPtr(portfolio.WeightFromLayers(layers, created.TotalLayers)),
			StrategyTag: created.StrategyID,
			SourceType:  "auto_compose",
			LastAction:  "auto_compose",
		})
		if err != nil {
			slog.Warn("自动组合写入持仓失败", "code", row.Code, "err", err)
			continue
		}
		written++
	}
	if written == 0 {
		return nil, fmt.Errorf("自动组合没有可落库的候选持仓")
	}

	result := s.autoComposePayload(ctx, "apply")
	result.PortfolioID = created.ID
	result.PositionCount = written
	result.Message = fmt.Sprintf("自动组合已创建，初始持仓 %d 只", written)
	slog.Info("自动组合已创建",
		"portfolio", created.ID, "strategy", created.StrategyID,
		"positions", written, "tradeDate", ctx.tradeDate)
	return result, nil
}

// buildAutoCompose 组装预演/创建共用的中间结果。
func (s *PortfolioService) buildAutoCompose(req AutoPortfolioRequest) (*autoComposeContext, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	strategyID := portfolio.NormalizeStrategy(req.StrategyID, "")

	strengthDate, err := store.LatestStrengthDate(strategyID)
	if err != nil || strengthDate <= 0 {
		return nil, fmt.Errorf("暂无最新评分数据，请先执行组合刷新")
	}

	// 组合入参归一化（与手动创建同一套默认值/别名解析）
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("%s自动组合%d", strategyLabelOf(strategyID), strengthDate)
	}
	item, err := portfolio.NormalizePortfolioInput(portfolio.PortfolioInput{
		Name: name,
		// 自动组合固定是 auto 类型（原实现 `source["portfolio_type"] = "auto"`）
		Type:                portfolio.TypeAuto,
		StrategyID:          strategyID,
		BenchmarkCode:       req.BenchmarkCode,
		Status:              req.Status,
		TargetPositionCount: req.TargetPositionCount,
		MaxStockCount:       req.MaxStockCount,
		TotalLayers:         req.TotalLayers,
		ScopeType:           req.ScopeType,
		ScopeValue:          scopePtr(req.ScopeValue),
		Notes:               req.Notes,
	})
	if err != nil {
		return nil, err
	}

	all, err := store.LoadStrength(strengthDate, strategyID, 0)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%s策略没有最新评分，请先刷新组合评分", strategyLabelOf(strategyID))
	}

	marketState, _ := store.LoadMarketState(strengthDate)
	state := marketState.State
	if state == "" {
		state = score.StateRange
	}
	cfg := score.BuildStrategyConfig(
		strategyID, state, item.TargetPositionCount, item.TotalLayers,
		maxLayersForBacktest(strategyID, item.TotalLayers), marketScopeKey(item),
	)

	// 用**空持仓**跑一次正常计划 —— 计划里的 add 行就是首批建仓
	candidates, universeScores := s.buildCandidatePoolFrom(all, item, cfg, nil)
	plan := score.BuildRotationPlan(score.RotationInput{
		Holdings: nil, Candidates: candidates, Config: cfg,
		MarketState: state, MaxStockLimit: item.MaxStockCount,
		UniverseScores: universeScores,
	})

	// 取 add / replace_in 且目标层数 > 0 的行
	rows := make([]score.RotationRow, 0, len(plan.Rows))
	for _, row := range plan.Rows {
		if row.TargetLayers <= 0 {
			continue
		}
		if row.Action != score.ActionAdd && row.Action != score.ActionReplaceIn {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("当前策略和范围没有通过质量过滤的自动组合候选")
	}
	// 排序：(rank_no, -score, code)，取前 target_count
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].RankNo != rows[j].RankNo {
			return rows[i].RankNo < rows[j].RankNo
		}
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].Code < rows[j].Code
	})
	if len(rows) > item.TargetPositionCount {
		rows = rows[:item.TargetPositionCount]
	}

	// 候选池按代码索引，用来补现价
	byCode := map[string]score.Row{}
	for _, row := range candidates {
		byCode[row.Code] = row
	}

	selected := make([]score.Row, 0, len(rows))
	for _, row := range rows {
		pool, ok := byCode[row.Code]
		if !ok || pool.Close <= 0 {
			continue
		}
		layers := atLeastOne(row.TargetLayers)
		pool.PositionLayers = layers
		pool.EntryPrice = pool.Close
		pool.CurrentPrice = pool.Close
		selected = append(selected, pool)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("当前策略和范围没有可写入持仓的自动组合候选")
	}

	// 组合评分 = 入选持仓的总分均值（原实现 :2381-2382）
	sum := 0.0
	for _, row := range selected {
		sum += row.TotalScore
	}
	portfolioScore := sum / float64(len(selected))

	snapshot := AutoSnapshot{
		PortfolioScore: round4(portfolioScore),
		HealthLabel:    HealthLabel(portfolioScore),
		PositionCount:  len(selected),
		CandidateCount: len(candidates),
		AddCount:       len(selected),
		ActionBrief:    fmt.Sprintf("自动生成 %d 只初始持仓", len(selected)),
		PortfolioType:  item.Type, PortfolioTypeLabel: item.TypeLabel,
		ExecutionMode: item.ExecutionMode, ExecutionModeLabel: item.ExecutionModeLabel,
	}
	return &autoComposeContext{
		tradeDate: int(strengthDate), item: item, snapshot: snapshot,
		selected: selected, candidates: candidates, rotation: plan.Rows,
	}, nil
}

// autoComposePayload 把中间结果拼成对外的预演/创建结果。
func (s *PortfolioService) autoComposePayload(ctx *autoComposeContext, mode string) *AutoPortfolioResult {
	top := ctx.candidates
	if len(top) > 20 {
		top = top[:20]
	}
	message := "自动组合预演已生成"
	if mode == "apply" {
		message = "自动组合已生成"
	}
	return &AutoPortfolioResult{
		OK: true, Available: len(ctx.selected) > 0, Mode: mode, Message: message,
		TradeDate: ctx.tradeDate, Portfolio: ctx.item, Snapshot: ctx.snapshot,
		SelectedPositions: ctx.selected, TopCandidates: top, Rotation: ctx.rotation,
		SelectedCount: len(ctx.selected), CandidateCount: len(ctx.candidates),
	}
}

// strategyLabelOf 取策略的中文标签。
func strategyLabelOf(strategyID string) string {
	return score.StrategyLabelOf(strategyID)
}

// atLeastOne 把小于 1 的整数抬到 1（本包内多处要用）。
func atLeastOne(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// intPtr / floatPtr 造指针（`PortfolioInput` / `PositionInput` 用指针
// 区分"未提供"与"显式传 0"）。
func intPtr(v int) *int { return &v }

// scopePtr 让"显式提供了范围"在指针类型下可表达（区分"没传"与"传空"）。
func scopePtr(v portfolio.ScopeValue) *portfolio.ScopeValue { return &v }

func floatPtr(v float64) *float64 { return &v }
