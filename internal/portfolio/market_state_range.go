package portfolio

import "fmt"

// LoadMarketStateRange 读取区间内的市场状态（按交易日升序）。
//
// 量价信号生成要用它当 `market_state_cache` —— 对应原项目的
// `build_market_state_cache(market_width, trade_dates)`：
// 那是"每个交易日 → 当天市场状态"的映射，回测逐日从这里取状态，
// **不再走个股上下文状态**。少了它，信号会被大幅压掉。
func (s *Store) LoadMarketStateRange(startDate, endDate int64) ([]MarketStateRow, error) {
	rows, err := s.db.Query(`
		SELECT trade_date, state, label, bull_score, bear_score, proxy_ret_20, days, created_at
		FROM market_state
		WHERE trade_date >= ? AND trade_date <= ?
		ORDER BY trade_date ASC`, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("读取市场状态区间失败：%w", err)
	}
	defer rows.Close()

	out := make([]MarketStateRow, 0, 64)
	for rows.Next() {
		var row MarketStateRow
		if err := rows.Scan(
			&row.TradeDate, &row.State, &row.Label, &row.BullScore,
			&row.BearScore, &row.ProxyRet20, &row.Days, &row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("扫描市场状态失败：%w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历市场状态失败：%w", err)
	}
	return out, nil
}
