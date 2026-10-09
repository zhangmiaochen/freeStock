package portfolio

import "fmt"

// 留档表的写入与读取（见 snapshot.go 的说明）。
//
// 全部按 `(portfolio_id, trade_date)` 覆盖写 —— 同一天重算建议时应当**替换**
// 而不是累积（原实现也是先删后写）。

// SaveScoreSnapshot 写组合层面的评分与动作统计。
func (s *Store) SaveScoreSnapshot(row ScoreSnapshot) error {
	now := nowText()
	_, err := s.db.Exec(`
		INSERT INTO portfolio_score_snapshot (
			portfolio_id, trade_date, strategy_id, strategy_label,
			portfolio_type, portfolio_type_label, execution_mode, execution_mode_label,
			allow_apply_rotation, require_manual_confirmation,
			portfolio_score, health_label, position_count, candidate_count,
			keep_count, warning_count, replace_count, add_count, clear_count,
			action_summary_json, total_return_pct, max_drawdown_pct,
			created_at, updated_at
		) VALUES (?,?,?,?, ?,?,?,?, ?,?, ?,?,?,?, ?,?,?,?,?, ?,?,?, ?,?)
		ON CONFLICT(portfolio_id, trade_date) DO UPDATE SET
			strategy_id=excluded.strategy_id, strategy_label=excluded.strategy_label,
			portfolio_type=excluded.portfolio_type, portfolio_type_label=excluded.portfolio_type_label,
			execution_mode=excluded.execution_mode, execution_mode_label=excluded.execution_mode_label,
			allow_apply_rotation=excluded.allow_apply_rotation,
			require_manual_confirmation=excluded.require_manual_confirmation,
			portfolio_score=excluded.portfolio_score, health_label=excluded.health_label,
			position_count=excluded.position_count, candidate_count=excluded.candidate_count,
			keep_count=excluded.keep_count, warning_count=excluded.warning_count,
			replace_count=excluded.replace_count, add_count=excluded.add_count,
			clear_count=excluded.clear_count, action_summary_json=excluded.action_summary_json,
			total_return_pct=excluded.total_return_pct, max_drawdown_pct=excluded.max_drawdown_pct,
			updated_at=excluded.updated_at`,
		row.PortfolioID, row.TradeDate, row.StrategyID, row.StrategyLabel,
		row.PortfolioType, row.PortfolioTypeLabel, row.ExecutionMode, row.ExecutionModeLabel,
		boolToInt(row.AllowApplyRotation), boolToInt(row.RequireManualConfirm),
		row.PortfolioScore, row.HealthLabel, row.PositionCount, row.CandidateCount,
		row.KeepCount, row.WarningCount, row.ReplaceCount, row.AddCount, row.ClearCount,
		firstNonEmpty(row.ActionSummaryJSON, "{}"), row.TotalReturnPct, row.MaxDrawdownPct,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("写入评分快照失败：%w", err)
	}
	return nil
}

// ReplaceCandidateSnapshots 覆盖写某组合某日的候选池快照。
func (s *Store) ReplaceCandidateSnapshots(portfolioID, tradeDate string, rows []CandidateSnapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM portfolio_candidate_snapshot WHERE portfolio_id=? AND trade_date=?`,
		portfolioID, tradeDate); err != nil {
		return fmt.Errorf("清理旧候选快照失败：%w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO portfolio_candidate_snapshot (
			portfolio_id, trade_date, strategy_id, strategy_label,
			portfolio_type, portfolio_type_label, candidate_rank, ts_code, name,
			industry, market, market_state, market_state_label,
			active_signal_type, active_signal_score, active_signal_model_id,
			active_signal_model_name, signal_trade_date, signal_age, character_label,
			best_model_name, best_model_win_rate,
			total_score, trend_score, structure_score, rs_score, capital_score, repair_score,
			research_score, board_research_score, close_price,
			relative_strength_20d, drawdown_from_high_120, score_summary,
			created_at, updated_at
		) VALUES (?,?,?,?, ?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?,?, ?,?, ?,?,?,?,?,?, ?,?,?, ?,?,?, ?,?)`)
	if err != nil {
		return fmt.Errorf("准备候选快照写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for _, row := range rows {
		if _, err := stmt.Exec(
			row.PortfolioID, row.TradeDate, row.StrategyID, row.StrategyLabel,
			row.PortfolioType, row.PortfolioTypeLabel, row.CandidateRank, row.Code, row.Name,
			row.Industry, row.Market, row.MarketState, row.MarketStateLabel,
			// ⚠ 以下信号/研究列本项目无数据源，写 0/空串，不伪造
			"", 0, "", "", "", 0, "", "", 0,
			row.TotalScore, row.TrendScore, row.StructureScore, row.RSScore,
			row.CapitalScore, row.RepairScore, 0, 0, row.ClosePrice,
			row.RelativeStrength20D, row.DrawdownFromHigh120, row.ScoreSummary,
			now, now,
		); err != nil {
			return fmt.Errorf("写入候选快照失败（%s）：%w", row.Code, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交候选快照失败：%w", err)
	}
	return nil
}

// ReplaceRotationSignals 覆盖写某组合某日的调仓信号。
func (s *Store) ReplaceRotationSignals(portfolioID, tradeDate string, rows []RotationSignalRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM rotation_signal WHERE portfolio_id=? AND trade_date=?`,
		portfolioID, tradeDate); err != nil {
		return fmt.Errorf("清理旧调仓信号失败：%w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO rotation_signal (
			portfolio_id, trade_date, strategy_id, strategy_label,
			portfolio_type, portfolio_type_label, execution_mode, execution_mode_label,
			requires_confirmation, is_executable, ts_code, name, action,
			score, score_gap, rank_no, candidate_ts_code, candidate_name, candidate_score,
			expected_improvement, reason, source_type, current_layers, target_layers,
			layers_delta, created_at, updated_at
		) VALUES (?,?,?,?, ?,?,?,?, ?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?, ?,?,?)`)
	if err != nil {
		return fmt.Errorf("准备调仓信号写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for _, row := range rows {
		if _, err := stmt.Exec(
			row.PortfolioID, row.TradeDate, row.StrategyID, row.StrategyLabel,
			row.PortfolioType, row.PortfolioTypeLabel, row.ExecutionMode, row.ExecutionModeLabel,
			boolToInt(row.RequiresConfirm), boolToInt(row.IsExecutable),
			row.Code, row.Name, row.Action,
			row.Score, row.ScoreGap, row.RankNo, row.CandidateCode, row.CandidateName,
			row.CandidateScore, row.ExpectedImprovement, row.Reason, row.SourceType,
			row.CurrentLayers, row.TargetLayers, row.LayersDelta, now, now,
		); err != nil {
			return fmt.Errorf("写入调仓信号失败（%s）：%w", row.Code, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交调仓信号失败：%w", err)
	}
	return nil
}

// ReplaceHoldCompares 覆盖写某组合某日的持仓对比。
func (s *Store) ReplaceHoldCompares(portfolioID, tradeDate string, rows []HoldCompareRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM hold_compare WHERE portfolio_id=? AND trade_date=?`,
		portfolioID, tradeDate); err != nil {
		return fmt.Errorf("清理旧持仓对比失败：%w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO hold_compare (
			portfolio_id, trade_date, strategy_id, strategy_label,
			portfolio_type, portfolio_type_label, holding_ts_code, holding_name, holding_score,
			candidate_ts_code, candidate_name, candidate_score,
			total_score_gap, rs_score_gap, repair_score_gap, main_reason,
			expected_portfolio_uplift, created_at, updated_at
		) VALUES (?,?,?,?, ?,?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?)`)
	if err != nil {
		return fmt.Errorf("准备持仓对比写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for _, row := range rows {
		if _, err := stmt.Exec(
			row.PortfolioID, row.TradeDate, row.StrategyID, row.StrategyLabel,
			row.PortfolioType, row.PortfolioTypeLabel,
			row.HoldingCode, row.HoldingName, row.HoldingScore,
			row.CandidateCode, row.CandidateName, row.CandidateScore,
			row.TotalScoreGap, row.RSScoreGap, row.RepairScoreGap, row.MainReason,
			row.ExpectedUplift, now, now,
		); err != nil {
			return fmt.Errorf("写入持仓对比失败（%s）：%w", row.HoldingCode, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交持仓对比失败：%w", err)
	}
	return nil
}

// LoadScoreSnapshots 读某组合的评分快照（日期降序）。
func (s *Store) LoadScoreSnapshots(portfolioID string, limit int) ([]ScoreSnapshot, error) {
	query := `
		SELECT portfolio_id, trade_date, strategy_id, strategy_label,
		       portfolio_type, portfolio_type_label, execution_mode, execution_mode_label,
		       allow_apply_rotation, require_manual_confirmation,
		       portfolio_score, health_label, position_count, candidate_count,
		       keep_count, warning_count, replace_count, add_count, clear_count,
		       action_summary_json, total_return_pct, max_drawdown_pct
		FROM portfolio_score_snapshot WHERE portfolio_id=? ORDER BY trade_date DESC`
	args := []any{portfolioID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取评分快照失败：%w", err)
	}
	defer rows.Close()

	var out []ScoreSnapshot
	for rows.Next() {
		var item ScoreSnapshot
		var allowApply, requireConfirm int
		if err := rows.Scan(
			&item.PortfolioID, &item.TradeDate, &item.StrategyID, &item.StrategyLabel,
			&item.PortfolioType, &item.PortfolioTypeLabel, &item.ExecutionMode, &item.ExecutionModeLabel,
			&allowApply, &requireConfirm,
			&item.PortfolioScore, &item.HealthLabel, &item.PositionCount, &item.CandidateCount,
			&item.KeepCount, &item.WarningCount, &item.ReplaceCount, &item.AddCount, &item.ClearCount,
			&item.ActionSummaryJSON, &item.TotalReturnPct, &item.MaxDrawdownPct,
		); err != nil {
			return nil, fmt.Errorf("扫描评分快照失败：%w", err)
		}
		item.AllowApplyRotation = allowApply != 0
		item.RequireManualConfirm = requireConfirm != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
