package portfolio

import (
	"database/sql"
	"errors"
	"fmt"
)

// SaveBacktestRun 写入/覆盖一次回测的元信息与绩效。
func (s *Store) SaveBacktestRun(run BacktestRun) error {
	if run.RunID == "" {
		return errors.New("回测 ID 不能为空")
	}
	if run.SummaryJSON == "" {
		run.SummaryJSON = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO portfolio_backtest_run (
			run_id, portfolio_id, portfolio_name, strategy_id, strategy_label,
			portfolio_type, portfolio_type_label, benchmark_code, start_date, end_date,
			rebalance_frequency, initial_capital, status,
			total_return, annual_return, max_drawdown, sharpe_ratio,
			total_trades, avg_position_count, final_value, summary_json,
			created_at, updated_at, finished_at
		) VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?)
		ON CONFLICT(run_id) DO UPDATE SET
			portfolio_name=excluded.portfolio_name,
			strategy_id=excluded.strategy_id, strategy_label=excluded.strategy_label,
			portfolio_type=excluded.portfolio_type, portfolio_type_label=excluded.portfolio_type_label,
			benchmark_code=excluded.benchmark_code,
			start_date=excluded.start_date, end_date=excluded.end_date,
			rebalance_frequency=excluded.rebalance_frequency,
			initial_capital=excluded.initial_capital, status=excluded.status,
			total_return=excluded.total_return, annual_return=excluded.annual_return,
			max_drawdown=excluded.max_drawdown, sharpe_ratio=excluded.sharpe_ratio,
			total_trades=excluded.total_trades, avg_position_count=excluded.avg_position_count,
			final_value=excluded.final_value, summary_json=excluded.summary_json,
			updated_at=excluded.updated_at, finished_at=excluded.finished_at`,
		run.RunID, run.PortfolioID, run.PortfolioName, run.StrategyID, run.StrategyLabel,
		run.PortfolioType, run.PortfolioTypeLabel, run.BenchmarkCode, run.StartDate, run.EndDate,
		firstNonEmpty(run.RebalanceFrequency, "signal"), run.InitialCapital, firstNonEmpty(run.Status, "pending"),
		run.TotalReturn, run.AnnualReturn, run.MaxDrawdown, run.SharpeRatio,
		run.TotalTrades, run.AvgPositionCount, run.FinalValue, run.SummaryJSON,
		firstNonEmpty(run.CreatedAt, nowText()), firstNonEmpty(run.UpdatedAt, nowText()), run.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("写入回测记录失败：%w", err)
	}
	return nil
}

// SaveBacktestDaily 批量写入净值曲线（同一 run 会先清空，便于重跑覆盖）。
func (s *Store) SaveBacktestDaily(rows []BacktestDaily) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	runID := rows[0].RunID
	if _, err := tx.Exec(`DELETE FROM portfolio_backtest_daily WHERE run_id=?`, runID); err != nil {
		return fmt.Errorf("清理旧净值失败：%w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO portfolio_backtest_daily (
			run_id, trade_date, total_value, cash, position_value,
			daily_return, cumulative_return, benchmark_value, benchmark_return,
			position_count, created_at
		) VALUES (?,?,?,?,?, ?,?,?,?, ?,?)`)
	if err != nil {
		return fmt.Errorf("准备净值写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for _, row := range rows {
		if _, err := stmt.Exec(
			row.RunID, row.TradeDate, row.TotalValue, row.Cash, row.PositionValue,
			row.DailyReturn, row.CumulativeReturn, row.BenchmarkValue, row.BenchmarkReturn,
			row.PositionCount, now,
		); err != nil {
			return fmt.Errorf("写入净值失败（%s）：%w", row.TradeDate, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交净值失败：%w", err)
	}
	return nil
}

// SaveBacktestTrades 批量写入成交（同一 run 会先清空）。
func (s *Store) SaveBacktestTrades(rows []BacktestTrade) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	runID := rows[0].RunID
	if _, err := tx.Exec(`DELETE FROM portfolio_backtest_trade WHERE run_id=?`, runID); err != nil {
		return fmt.Errorf("清理旧成交失败：%w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO portfolio_backtest_trade (
			trade_id, run_id, portfolio_id, ts_code, name, action, trade_date,
			price, shares, amount, commission, tax, total_cost,
			profit_loss, profit_loss_pct, position_weight, score, reason, created_at
		) VALUES (?,?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("准备成交写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for i := range rows {
		row := &rows[i]
		if row.TradeID == "" {
			row.TradeID = newBacktestTradeID()
		}
		if _, err := stmt.Exec(
			row.TradeID, row.RunID, row.PortfolioID, row.Code, row.Name, row.Action, row.TradeDate,
			row.Price, row.Shares, row.Amount, row.Commission, row.Tax, row.TotalCost,
			row.ProfitLoss, row.ProfitLossPct, row.PositionWeight, row.Score, row.Reason,
			firstNonEmpty(row.CreatedAt, now),
		); err != nil {
			return fmt.Errorf("写入成交失败（%s %s）：%w", row.TradeDate, row.Code, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交成交失败：%w", err)
	}
	return nil
}

const backtestRunSelect = `
	SELECT run_id, portfolio_id, portfolio_name, strategy_id, strategy_label,
	       portfolio_type, portfolio_type_label, benchmark_code, start_date, end_date,
	       rebalance_frequency, initial_capital, status,
	       total_return, annual_return, max_drawdown, sharpe_ratio,
	       total_trades, avg_position_count, final_value, summary_json,
	       created_at, updated_at, finished_at
	FROM portfolio_backtest_run`

func scanBacktestRun(row interface{ Scan(...any) error }) (BacktestRun, error) {
	var out BacktestRun
	err := row.Scan(
		&out.RunID, &out.PortfolioID, &out.PortfolioName, &out.StrategyID, &out.StrategyLabel,
		&out.PortfolioType, &out.PortfolioTypeLabel, &out.BenchmarkCode, &out.StartDate, &out.EndDate,
		&out.RebalanceFrequency, &out.InitialCapital, &out.Status,
		&out.TotalReturn, &out.AnnualReturn, &out.MaxDrawdown, &out.SharpeRatio,
		&out.TotalTrades, &out.AvgPositionCount, &out.FinalValue, &out.SummaryJSON,
		&out.CreatedAt, &out.UpdatedAt, &out.FinishedAt,
	)
	return out, err
}

// GetBacktestRun 读一次回测。
func (s *Store) GetBacktestRun(runID string) (BacktestRun, error) {
	out, err := scanBacktestRun(s.db.QueryRow(backtestRunSelect+` WHERE run_id=?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return BacktestRun{}, fmt.Errorf("回测记录不存在：%s", runID)
	}
	if err != nil {
		return BacktestRun{}, fmt.Errorf("读取回测失败：%w", err)
	}
	return out, nil
}

// ListBacktestRuns 列出回测记录（新的在前）。portfolioID 为空时列全部。
func (s *Store) ListBacktestRuns(portfolioID string) ([]BacktestRun, error) {
	query := backtestRunSelect
	args := []any{}
	if portfolioID != "" {
		query += ` WHERE portfolio_id=?`
		args = append(args, portfolioID)
	}
	query += ` ORDER BY created_at DESC, run_id ASC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取回测列表失败：%w", err)
	}
	defer rows.Close()

	var out []BacktestRun
	for rows.Next() {
		item, err := scanBacktestRun(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描回测失败：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListBacktestDaily 读净值曲线（按日期升序）。
func (s *Store) ListBacktestDaily(runID string) ([]BacktestDaily, error) {
	rows, err := s.db.Query(`
		SELECT run_id, trade_date, total_value, cash, position_value,
		       daily_return, cumulative_return, benchmark_value, benchmark_return,
		       position_count
		FROM portfolio_backtest_daily WHERE run_id=? ORDER BY trade_date ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("读取净值曲线失败：%w", err)
	}
	defer rows.Close()

	var out []BacktestDaily
	for rows.Next() {
		var item BacktestDaily
		if err := rows.Scan(
			&item.RunID, &item.TradeDate, &item.TotalValue, &item.Cash, &item.PositionValue,
			&item.DailyReturn, &item.CumulativeReturn, &item.BenchmarkValue, &item.BenchmarkReturn,
			&item.PositionCount,
		); err != nil {
			return nil, fmt.Errorf("扫描净值失败：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListBacktestTrades 读成交明细（按日期、代码升序）。
func (s *Store) ListBacktestTrades(runID string) ([]BacktestTrade, error) {
	rows, err := s.db.Query(`
		SELECT trade_id, run_id, portfolio_id, ts_code, name, action, trade_date,
		       price, shares, amount, commission, tax, total_cost,
		       profit_loss, profit_loss_pct, position_weight, score, reason, created_at
		FROM portfolio_backtest_trade
		WHERE run_id=? ORDER BY trade_date ASC, ts_code ASC, action ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("读取成交明细失败：%w", err)
	}
	defer rows.Close()

	var out []BacktestTrade
	for rows.Next() {
		var item BacktestTrade
		if err := rows.Scan(
			&item.TradeID, &item.RunID, &item.PortfolioID, &item.Code, &item.Name, &item.Action,
			&item.TradeDate, &item.Price, &item.Shares, &item.Amount, &item.Commission, &item.Tax,
			&item.TotalCost, &item.ProfitLoss, &item.ProfitLossPct, &item.PositionWeight,
			&item.Score, &item.Reason, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("扫描成交失败：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DeleteBacktestRun 删除一次回测（净值与成交由外键级联清理）。
func (s *Store) DeleteBacktestRun(runID string) error {
	if _, err := s.db.Exec(`DELETE FROM portfolio_backtest_run WHERE run_id=?`, runID); err != nil {
		return fmt.Errorf(`删除回测失败：%w`, err)
	}
	return nil
}
