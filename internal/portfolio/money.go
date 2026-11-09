package portfolio

import (
	"database/sql"
	"fmt"
)

// 资金账户：组合的"本金/现金"与每只持仓的"股数"。
//
// 为什么单独两张表而不是给 portfolio / portfolio_position 加列：
// 那两张表的所有 SQL 都列了显式列名，加列要改动所有读写路径，
// 风险高；资金数据是新增能力，单独存更安全，也不影响老库。
const moneySchema = `
CREATE TABLE IF NOT EXISTS portfolio_money (
  portfolio_id    TEXT PRIMARY KEY,
  initial_capital REAL NOT NULL DEFAULT 100000,
  cash            REAL NOT NULL DEFAULT 100000,
  updated_at      TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (portfolio_id) REFERENCES portfolio(portfolio_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS position_shares (
  portfolio_id TEXT NOT NULL,
  ts_code      TEXT NOT NULL,
  shares       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (portfolio_id, ts_code),
  FOREIGN KEY (portfolio_id) REFERENCES portfolio(portfolio_id) ON DELETE CASCADE
);
`

// Money 是一个组合的资金状态。
type Money struct {
	InitialCapital float64 `json:"initialCapital"`
	Cash           float64 `json:"cash"`
}

// GetMoney 读资金账户；没有记录时返回默认（本金 10 万、现金 10 万）。
func (s *Store) GetMoney(portfolioID string) (Money, error) {
	m := Money{InitialCapital: 100000, Cash: 100000}
	row := s.db.QueryRow(
		`SELECT initial_capital, cash FROM portfolio_money WHERE portfolio_id=?`, portfolioID)
	err := row.Scan(&m.InitialCapital, &m.Cash)
	if err == sql.ErrNoRows {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, nil
}

// SetMoney 写资金账户（upsert）。
func (s *Store) SetMoney(portfolioID string, m Money) error {
	if m.InitialCapital < 0 {
		m.InitialCapital = 0
	}
	if m.Cash < 0 {
		m.Cash = 0
	}
	_, err := s.db.Exec(
		`INSERT INTO portfolio_money (portfolio_id, initial_capital, cash, updated_at)
		 VALUES (?,?,?,?)
		 ON CONFLICT(portfolio_id) DO UPDATE SET
		   initial_capital=excluded.initial_capital,
		   cash=excluded.cash,
		   updated_at=excluded.updated_at`,
		portfolioID, m.InitialCapital, m.Cash, nowText())
	if err != nil {
		return fmt.Errorf("写资金账户失败：%w", err)
	}
	return nil
}

// SetInitialCapital 设置本金：现金按"本金 - 已持仓成本"重置。
//
// 用于用户在界面上改本金；已持仓的成本从持仓表读取（成本 × 股数）。
func (s *Store) SetInitialCapital(portfolioID string, capital float64) (Money, error) {
	if capital < 0 {
		capital = 0
	}
	positions, err := s.ListPositions(portfolioID)
	if err != nil {
		return Money{}, err
	}
	shares, err := s.SharesMap(portfolioID)
	if err != nil {
		return Money{}, err
	}
	cost := 0.0
	for _, p := range positions {
		cost += p.EntryPrice * float64(shares[p.Code])
	}
	cash := capital - cost
	if cash < 0 {
		cash = 0
	}
	m := Money{InitialCapital: capital, Cash: cash}
	if err := s.SetMoney(portfolioID, m); err != nil {
		return Money{}, err
	}
	return m, nil
}

// SharesMap 返回 代码 → 股数。
func (s *Store) SharesMap(portfolioID string) (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT ts_code, shares FROM position_shares WHERE portfolio_id=?`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out[code] = n
	}
	return out, rows.Err()
}

// SetShares 全量写持仓股数（先清空再写），与持仓改动放在同一事务之外调用。
func (s *Store) SetShares(portfolioID string, shares map[string]int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM position_shares WHERE portfolio_id=?`, portfolioID); err != nil {
		return err
	}
	for code, n := range shares {
		if n <= 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO position_shares (portfolio_id, ts_code, shares) VALUES (?,?,?)`,
			portfolioID, code, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}
