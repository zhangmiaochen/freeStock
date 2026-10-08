package portfolio

import (
	"database/sql"
	"fmt"
	"strings"
)

// 评分物化表。
//
// 为什么要物化：对全市场逐股拉 320 天日线算指标需要数分钟（本机实测 179 秒），
// 交互式场景根本用不了。参考实现同样是物化的 —— 它把评分写进
// stock_strength_history，轮动决策再从那张表读。
//
// 与参考实现的差异：
//   - 参考表有 80+ 列（含 research_score / best_model_* / low_buy_* 等）。
//     本项目没有研究库与模型信号数据源，因此只存**能算出来的那些列**，
//     其余留空并在接口层如实声明（见 ScoringResult.Unavailable）。
//   - 主键是 (trade_date, strategy_id, ts_code)，与参考实现一致 ——
//     同一只股票在不同策略下分数不同，所以策略必须在键里。
type StrengthRow struct {
	TradeDate  int64  `json:"tradeDate"`
	StrategyID string `json:"strategyId"`
	Code       string `json:"tsCode"`
	Name       string `json:"name"`
	Industry   string `json:"industry"`
	Market     string `json:"market"`

	TotalScore     float64 `json:"totalScore"`
	TrendScore     float64 `json:"trendScore"`
	StructureScore float64 `json:"structureScore"`
	RSScore        float64 `json:"rsScore"`
	CapitalScore   float64 `json:"capitalScore"`
	RepairScore    float64 `json:"repairScore"`

	Close  float64 `json:"closePrice"`
	Open   float64 `json:"openPrice"`
	PctChg float64 `json:"pctChg"`

	Ma5   float64 `json:"ma5Price"`
	Ma20  float64 `json:"ma20Price"`
	Ma30  float64 `json:"ma30Price"`
	Ma60  float64 `json:"ma60Price"`
	Ma120 float64 `json:"ma120Price"`
	Ma250 float64 `json:"ma250Price"`

	RelativeStrength20D float64 `json:"relativeStrength20d"`
	DrawdownFromHigh20  float64 `json:"drawdownFromHigh20"`
	DrawdownFromHigh120 float64 `json:"drawdownFromHigh120"`
	ReboundFromLow60    float64 `json:"reboundFromLow60"`
	StockReturn5D       float64 `json:"stockReturn5d"`
	StockReturn10D      float64 `json:"stockReturn10d"`
	VolumeRatio20       float64 `json:"volumeRatio20"`
}

const strengthSchema = `
CREATE TABLE IF NOT EXISTS stock_strength (
  trade_date            INTEGER NOT NULL,
  strategy_id           TEXT NOT NULL,
  ts_code               TEXT NOT NULL,
  name                  TEXT NOT NULL DEFAULT '',
  industry              TEXT NOT NULL DEFAULT '',
  market                TEXT NOT NULL DEFAULT '',
  total_score           REAL NOT NULL DEFAULT 0,
  trend_score           REAL NOT NULL DEFAULT 0,
  structure_score       REAL NOT NULL DEFAULT 0,
  rs_score              REAL NOT NULL DEFAULT 0,
  capital_score         REAL NOT NULL DEFAULT 0,
  repair_score          REAL NOT NULL DEFAULT 0,
  close_price           REAL NOT NULL DEFAULT 0,
  open_price            REAL NOT NULL DEFAULT 0,
  pct_chg               REAL NOT NULL DEFAULT 0,
  ma5_price             REAL NOT NULL DEFAULT 0,
  ma20_price            REAL NOT NULL DEFAULT 0,
  ma30_price            REAL NOT NULL DEFAULT 0,
  ma60_price            REAL NOT NULL DEFAULT 0,
  ma120_price           REAL NOT NULL DEFAULT 0,
  ma250_price           REAL NOT NULL DEFAULT 0,
  relative_strength_20d REAL NOT NULL DEFAULT 0,
  drawdown_from_high_20 REAL NOT NULL DEFAULT 0,
  drawdown_from_high_120 REAL NOT NULL DEFAULT 0,
  rebound_from_low_60   REAL NOT NULL DEFAULT 0,
  stock_return_5d       REAL NOT NULL DEFAULT 0,
  stock_return_10d      REAL NOT NULL DEFAULT 0,
  volume_ratio_20       REAL NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (trade_date, strategy_id, ts_code)
);

CREATE INDEX IF NOT EXISTS idx_strength_lookup ON stock_strength(trade_date, strategy_id, total_score DESC);
`

// SaveStrength 覆盖写入某日某策略的评分。
//
// 先删后插：一次刷新的结果应当**完整替换**该 (日期, 策略) 的旧数据，
// 否则上一轮评上、这一轮数据不足被跳过的股票会残留成幽灵行。
func (s *Store) SaveStrength(tradeDate int64, strategyID string, rows []StrengthRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启评分写入事务失败：%w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM stock_strength WHERE trade_date=? AND strategy_id=?`,
		tradeDate, strategyID,
	); err != nil {
		return fmt.Errorf("清理旧评分失败：%w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO stock_strength (
			trade_date, strategy_id, ts_code, name, industry, market,
			total_score, trend_score, structure_score, rs_score, capital_score, repair_score,
			close_price, open_price, pct_chg,
			ma5_price, ma20_price, ma30_price, ma60_price, ma120_price, ma250_price,
			relative_strength_20d, drawdown_from_high_20, drawdown_from_high_120,
			rebound_from_low_60, stock_return_5d, stock_return_10d, volume_ratio_20, created_at
		) VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?, ?,?,?,?,?,?, ?,?,?, ?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("准备评分写入失败：%w", err)
	}
	defer stmt.Close()

	now := nowText()
	for _, row := range rows {
		// 策略与日期以入参为准，避免调用方漏填
		row.TradeDate, row.StrategyID = tradeDate, strategyID
		if _, err := stmt.Exec(
			row.TradeDate, row.StrategyID, row.Code, row.Name, row.Industry, row.Market,
			row.TotalScore, row.TrendScore, row.StructureScore, row.RSScore,
			row.CapitalScore, row.RepairScore,
			row.Close, row.Open, row.PctChg,
			row.Ma5, row.Ma20, row.Ma30, row.Ma60, row.Ma120, row.Ma250,
			row.RelativeStrength20D, row.DrawdownFromHigh20, row.DrawdownFromHigh120,
			row.ReboundFromLow60, row.StockReturn5D, row.StockReturn10D, row.VolumeRatio20, now,
		); err != nil {
			return fmt.Errorf("写入评分失败（%s）：%w", row.Code, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交评分失败：%w", err)
	}
	return nil
}

// LatestStrengthDate 返回已物化的最新评分日（无数据返回 0）。
func (s *Store) LatestStrengthDate(strategyID string) (int64, error) {
	var date sql.NullInt64
	err := s.db.QueryRow(
		`SELECT MAX(trade_date) FROM stock_strength WHERE strategy_id=?`,
		strategyID,
	).Scan(&date)
	if err != nil {
		return 0, fmt.Errorf("查询最新评分日失败：%w", err)
	}
	if !date.Valid {
		return 0, nil
	}
	return date.Int64, nil
}

// StrengthDates 返回已物化的评分日（升序）。
func (s *Store) StrengthDates(strategyID string) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT trade_date FROM stock_strength WHERE strategy_id=? ORDER BY trade_date`,
		strategyID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询评分日失败：%w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var date int64
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		out = append(out, date)
	}
	return out, rows.Err()
}

// StrengthCount 返回某日某策略的评分条数。
func (s *Store) StrengthCount(tradeDate int64, strategyID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM stock_strength WHERE trade_date=? AND strategy_id=?`,
		tradeDate, strategyID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计评分失败：%w", err)
	}
	return count, nil
}

// strengthSelect 是评分的读取列（顺序与 scanStrength 对应）。
const strengthSelect = `
	SELECT trade_date, strategy_id, ts_code, name, industry, market,
	       total_score, trend_score, structure_score, rs_score, capital_score, repair_score,
	       close_price, open_price, pct_chg,
	       ma5_price, ma20_price, ma30_price, ma60_price, ma120_price, ma250_price,
	       relative_strength_20d, drawdown_from_high_20, drawdown_from_high_120,
	       rebound_from_low_60, stock_return_5d, stock_return_10d, volume_ratio_20
	FROM stock_strength`

func scanStrength(row scanner) (StrengthRow, error) {
	var out StrengthRow
	err := row.Scan(
		&out.TradeDate, &out.StrategyID, &out.Code, &out.Name, &out.Industry, &out.Market,
		&out.TotalScore, &out.TrendScore, &out.StructureScore, &out.RSScore,
		&out.CapitalScore, &out.RepairScore,
		&out.Close, &out.Open, &out.PctChg,
		&out.Ma5, &out.Ma20, &out.Ma30, &out.Ma60, &out.Ma120, &out.Ma250,
		&out.RelativeStrength20D, &out.DrawdownFromHigh20, &out.DrawdownFromHigh120,
		&out.ReboundFromLow60, &out.StockReturn5D, &out.StockReturn10D, &out.VolumeRatio20,
	)
	return out, err
}

// LoadStrength 读取某日某策略的评分，按总分降序。
//
// limit > 0 时只取前 limit 条（候选池只需要头部）。
func (s *Store) LoadStrength(tradeDate int64, strategyID string, limit int) ([]StrengthRow, error) {
	query := strengthSelect + ` WHERE trade_date=? AND strategy_id=? ORDER BY total_score DESC, ts_code`
	args := []any{tradeDate, strategyID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取评分失败：%w", err)
	}
	defer rows.Close()

	out := make([]StrengthRow, 0, 256)
	for rows.Next() {
		item, err := scanStrength(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// LoadStrengthForCodes 读取指定股票的评分（持仓详情用）。
func (s *Store) LoadStrengthForCodes(tradeDate int64, strategyID string, codes []string) (map[string]StrengthRow, error) {
	out := map[string]StrengthRow{}
	if len(codes) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(codes)), ",")
	args := []any{tradeDate, strategyID}
	for _, code := range codes {
		args = append(args, BareCode(code))
	}
	rows, err := s.db.Query(
		strengthSelect+` WHERE trade_date=? AND strategy_id=? AND ts_code IN (`+placeholders+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("读取持仓评分失败：%w", err)
	}
	defer rows.Close()

	for rows.Next() {
		item, err := scanStrength(rows)
		if err != nil {
			return nil, err
		}
		out[item.Code] = item
	}
	return out, rows.Err()
}

// migrateStrength 建评分表（在 Store.migrate 里调用）。
func (s *Store) migrateStrength() error {
	if _, err := s.db.Exec(strengthSchema); err != nil {
		return fmt.Errorf("建评分表失败：%w", err)
	}
	return nil
}
