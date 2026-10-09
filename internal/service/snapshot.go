package service

import (
	"fmt"
	"strings"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 把一次调仓建议落成四份留档（见 portfolio/snapshot.go 的说明）。
//
// 对应原实现 `_rebuild_portfolio_view`（portfolio_service.py:2437+）。
//
// ⚠ 原表里的 `active_signal_*` / `best_model_*` / `research_score` /
// `board_research_score` / `character_label` 在本项目**没有数据源**，
// store 层会写 0 / 空串 —— **不伪造**。

// savePlanSnapshots 写四张留档表。
func (s *PortfolioService) savePlanSnapshots(
	store *portfolio.Store, item portfolio.Portfolio, result *PlanResult,
) error {
	tradeDate := fmt.Sprintf("%d", result.StrengthDate)
	if result.StrengthDate <= 0 {
		return fmt.Errorf("评分日无效，跳过留档")
	}

	// ① 组合层面
	counts := map[string]int{}
	for _, row := range result.Plan.Rows {
		counts[row.Action]++
	}
	summary := fmt.Sprintf(
		`{"keep":%d,"observe":%d,"add":%d,"reduce":%d,"clear":%d,"replaceOut":%d,"replaceIn":%d,"addLayer":%d}`,
		counts[score.ActionKeep], counts[score.ActionObserve], counts[score.ActionAdd],
		counts[score.ActionReduce], counts[score.ActionClear], counts[score.ActionReplaceOut],
		counts[score.ActionReplaceIn], counts[score.ActionAddLayer],
	)
	if err := store.SaveScoreSnapshot(portfolio.ScoreSnapshot{
		PortfolioID: item.ID, TradeDate: tradeDate,
		StrategyID: result.StrategyID, StrategyLabel: result.StrategyLabel,
		PortfolioType: item.Type, PortfolioTypeLabel: item.TypeLabel,
		ExecutionMode: item.ExecutionMode, ExecutionModeLabel: item.ExecutionModeLabel,
		AllowApplyRotation:   item.AllowApplyRotation,
		RequireManualConfirm: item.RequireManualConfirmation,
		PortfolioScore:       result.PortfolioScore, HealthLabel: result.HealthLabel,
		PositionCount: len(result.Holdings), CandidateCount: len(result.Candidates),
		KeepCount:         counts[score.ActionKeep] + counts[score.ActionObserve],
		WarningCount:      counts[score.ActionReduce] + counts[score.ActionClear],
		ReplaceCount:      counts[score.ActionReplaceOut],
		AddCount:          counts[score.ActionAdd],
		ClearCount:        counts[score.ActionClear],
		ActionSummaryJSON: summary,
		MaxDrawdownPct:    result.MaxDrawdownPct,
	}); err != nil {
		return err
	}

	// ② 候选池（带排名）
	candidateRows := make([]portfolio.CandidateSnapshot, 0, len(result.Candidates))
	for i, row := range result.Candidates {
		candidateRows = append(candidateRows, portfolio.CandidateSnapshot{
			PortfolioID: item.ID, TradeDate: tradeDate,
			StrategyID: result.StrategyID, StrategyLabel: result.StrategyLabel,
			PortfolioType: item.Type, PortfolioTypeLabel: item.TypeLabel,
			CandidateRank: i + 1, Code: row.Code, Name: row.Name,
			// ⚠ `score.Row` 没有 Industry（行业只挂在 `portfolio.StrengthRow` 上）；
			// 候选池里本来也没带过来，这里留空而不是去查一次库。
			Market:      row.Market,
			MarketState: result.MarketState.State, MarketStateLabel: result.MarketState.Label,
			TotalScore: row.TotalScore, TrendScore: row.TrendScore,
			StructureScore: row.StructureScore, RSScore: row.RSScore,
			CapitalScore: row.CapitalScore, RepairScore: row.RepairScore,
			ClosePrice: row.Close, RelativeStrength20D: row.RelativeStrength20D,
			DrawdownFromHigh120: row.DrawdownFromHigh120,
			ScoreSummary:        scoreSummaryOf(result.StrategyID, row),
		})
	}
	if err := store.ReplaceCandidateSnapshots(item.ID, tradeDate, candidateRows); err != nil {
		return err
	}

	// ③ 调仓信号
	signalRows := make([]portfolio.RotationSignalRow, 0, len(result.Plan.Rows))
	for _, row := range result.Plan.Rows {
		signalRows = append(signalRows, portfolio.RotationSignalRow{
			PortfolioID: item.ID, TradeDate: tradeDate,
			StrategyID: result.StrategyID, StrategyLabel: result.StrategyLabel,
			PortfolioType: item.Type, PortfolioTypeLabel: item.TypeLabel,
			ExecutionMode: row.ExecutionMode, ExecutionModeLabel: row.ExecutionModeLabel,
			RequiresConfirm: row.RequiresConfirmation, IsExecutable: row.IsExecutable,
			Code: row.Code, Name: row.Name, Action: row.Action,
			Score: row.Score, ScoreGap: row.ScoreGap, RankNo: row.RankNo,
			CandidateCode: row.CandidateCode, CandidateName: row.CandidateName,
			CandidateScore: row.CandidateScore, ExpectedImprovement: row.ExpectedImprovement,
			Reason: row.Reason, SourceType: row.SourceType,
			CurrentLayers: row.CurrentLayers, TargetLayers: row.TargetLayers,
			LayersDelta: row.LayersDelta,
		})
	}
	if err := store.ReplaceRotationSignals(item.ID, tradeDate, signalRows); err != nil {
		return err
	}

	// ④ 持仓 vs 候选对比（只看有配对的持仓）
	compareRows := make([]portfolio.HoldCompareRow, 0)
	for _, row := range result.Plan.Rows {
		if row.CandidateCode == "" || row.SourceType != "holding" {
			continue
		}
		compareRows = append(compareRows, portfolio.HoldCompareRow{
			PortfolioID: item.ID, TradeDate: tradeDate,
			StrategyID: result.StrategyID, StrategyLabel: result.StrategyLabel,
			PortfolioType: item.Type, PortfolioTypeLabel: item.TypeLabel,
			HoldingCode: row.Code, HoldingName: row.Name, HoldingScore: row.Score,
			CandidateCode: row.CandidateCode, CandidateName: row.CandidateName,
			CandidateScore: row.CandidateScore,
			TotalScoreGap:  round4(row.CandidateScore - row.Score),
			MainReason:     row.Reason,
			ExpectedUplift: row.ExpectedImprovement,
		})
	}
	return store.ReplaceHoldCompares(item.ID, tradeDate, compareRows)
}

// scoreSummaryOf 拼一句分项摘要（原表是 `score_summary` 列）。
func scoreSummaryOf(strategyID string, row score.Row) string {
	names, ok := score.ScoreSummaryNames[strategyID]
	if !ok {
		return ""
	}
	values := [5]float64{row.TrendScore, row.StructureScore, row.RSScore, row.CapitalScore, row.RepairScore}
	parts := make([]string, 0, 5)
	for i, name := range names {
		parts = append(parts, fmt.Sprintf("%s%.1f", name, values[i]))
	}
	return strings.Join(parts, " ")
}

// GetScoreSnapshots 读某组合的评分留档（供界面看历史建议）。
func (s *PortfolioService) GetScoreSnapshots(portfolioID string, limit int) ([]portfolio.ScoreSnapshot, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	return store.LoadScoreSnapshots(portfolioID, limit)
}
