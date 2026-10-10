package portfolio

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// 对应原项目 `research_service.py` 里的 `signal_history` 表（量价模型信号）。
//
// 生产链：`run_volume_price_backtest` → `daily_signals` → `_collect_signal_rows`
// → `_build_signal_history_row` → `append_rows("signal_history", ...)`。
//
// 消费链：`portfolio_service._load_signal_candidate_frame`（读这张表、按
// ts_code 前缀正则过滤、按 `(trade_date, ts_code)` 去重）→ `merge_asof`
// 回填到评分物化行的 `active_signal_*`。
//
// ⚠ 表名刻意与原项目保持一致：这份数据是要与 Python 侧**对齐/对照**的，
// 改名会让"原版跑出来的信号表"和"Go 跑出来的信号表"无法直接比对。

// SignalHistoryRow 是 `signal_history` 的一行。
type SignalHistoryRow struct {
	TradeDate          int64  `json:"tradeDate"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	ModelID            string `json:"modelId"`
	ModelName          string `json:"modelName"`
	MarketState        string `json:"marketState"`
	MarketStateLabel   string `json:"marketStateLabel"`
	SignalType         string `json:"signalType"`
	SignalLevel        string `json:"signalLevel"`
	SignalLevelLabel   string `json:"signalLevelLabel"`
	SignalSubtype      string `json:"signalSubtype"`
	SignalSubtypeLabel string `json:"signalSubtypeLabel"`
	// 三个价格可空：原实现用 `_round_or_none`，拿不到时写 NULL 而不是 0 ——
	// 0 会被下游当成"有效价格 0"，NULL 才是"没有这个价"。
	EntryPrice  *float64 `json:"entryPrice"`
	StopPrice   *float64 `json:"stopPrice"`
	TargetPrice *float64 `json:"targetPrice"`
	Score       float64  `json:"score"`
	RiskLevel   string   `json:"riskLevel"`
	// BehaviorQualityJSON / ReasonJSON 对应原表的两个 JSON 文本列
	//（`_json_text(behavior_quality)` / `_json_text(reason_payload)`）。
	BehaviorQualityJSON string `json:"behaviorQualityJson"`
	ReasonJSON          string `json:"reasonJson"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
}

const signalSchema = `
CREATE TABLE IF NOT EXISTS signal_history (
    trade_date INTEGER NOT NULL,
    ts_code TEXT NOT NULL,
    name TEXT DEFAULT '',
    model_id TEXT NOT NULL,
    model_name TEXT DEFAULT '',
    market_state TEXT DEFAULT '',
    market_state_label TEXT DEFAULT '',
    signal_type TEXT NOT NULL,
    signal_level TEXT DEFAULT '',
    signal_level_label TEXT DEFAULT '',
    signal_subtype TEXT DEFAULT '',
    signal_subtype_label TEXT DEFAULT '',
    entry_price REAL,
    stop_price REAL,
    target_price REAL,
    score REAL DEFAULT 0,
    risk_level TEXT DEFAULT '',
    behavior_quality_json TEXT DEFAULT '',
    reason_json TEXT DEFAULT '',
    created_at TEXT DEFAULT '',
    updated_at TEXT DEFAULT '',
    PRIMARY KEY (trade_date, ts_code, model_id, signal_type)
);
CREATE INDEX IF NOT EXISTS idx_signal_history_date ON signal_history(trade_date);
CREATE INDEX IF NOT EXISTS idx_signal_history_code ON signal_history(ts_code, trade_date);
`

// signalColumnCount / signalColumnNames 用于拼接批量 INSERT。
const signalColumnCount = 21

const signalColumnNames = `trade_date, ts_code, name, model_id, model_name,
	market_state, market_state_label, signal_type, signal_level, signal_level_label,
	signal_subtype, signal_subtype_label, entry_price, stop_price, target_price,
	score, risk_level, behavior_quality_json, reason_json, created_at, updated_at`

func (s *Store) migrateSignal() error {
	if _, err := s.db.Exec(signalSchema); err != nil {
		return fmt.Errorf("建信号表失败：%w", err)
	}
	return nil
}

// SaveSignalHistory 批量写入信号（`INSERT OR REPLACE`，主键与生产者去重键一致）。
//
// 与 `AddAutoComposePositions` 一样走单事务：几千行逐条提交会慢到不可用，
// 而且中途失败会留下半张表。
func (s *Store) SaveSignalHistory(rows []SignalHistoryRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启信号写入事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const batch = 200
	for start := 0; start < len(rows); start += batch {
		end := start + batch
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		args := make([]any, 0, len(chunk)*signalColumnCount)
		for _, row := range chunk {
			args = append(args,
				row.TradeDate, row.Code, row.Name, row.ModelID, row.ModelName,
				row.MarketState, row.MarketStateLabel, row.SignalType,
				row.SignalLevel, row.SignalLevelLabel,
				row.SignalSubtype, row.SignalSubtypeLabel,
				row.EntryPrice, row.StopPrice, row.TargetPrice,
				row.Score, row.RiskLevel, row.BehaviorQualityJSON, row.ReasonJSON,
				row.CreatedAt, row.UpdatedAt,
			)
		}
		statement := insertManySQL("signal_history", signalColumnNames, signalColumnCount, len(chunk), true)
		if _, err := tx.Exec(statement, args...); err != nil {
			return fmt.Errorf("写入信号表失败：%w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交信号写入失败：%w", err)
	}
	return nil
}

// DeleteSignalHistoryFrom 删除某交易日之后的信号（全量重算时先清尾巴）。
func (s *Store) DeleteSignalHistoryFrom(tradeDate int64) (int64, error) {
	result, err := s.db.Exec("DELETE FROM signal_history WHERE trade_date >= ?", tradeDate)
	if err != nil {
		return 0, fmt.Errorf("清理信号表失败：%w", err)
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

// DeleteSignalHistoryForCode 删除某只票的信号（单票重算）。
func (s *Store) DeleteSignalHistoryForCode(code string, fromDate int64) (int64, error) {
	result, err := s.db.Exec(
		"DELETE FROM signal_history WHERE ts_code = ? AND trade_date >= ?", code, fromDate)
	if err != nil {
		return 0, fmt.Errorf("清理单票信号失败：%w", err)
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

// SignalHistoryCount 返回信号总条数（对应原实现的状态栏统计）。
func (s *Store) SignalHistoryCount() (int, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM signal_history").Scan(&count); err != nil {
		return 0, fmt.Errorf("统计信号条数失败：%w", err)
	}
	return count, nil
}

// LatestSignalDate 返回信号表里的最大交易日（对应
// `SELECT MAX(trade_date) FROM signal_history`）。
func (s *Store) LatestSignalDate() (int64, error) {
	var value sql.NullInt64
	if err := s.db.QueryRow("SELECT MAX(trade_date) FROM signal_history").Scan(&value); err != nil {
		return 0, fmt.Errorf("查询最新信号日期失败：%w", err)
	}
	if !value.Valid {
		return 0, nil
	}
	return value.Int64, nil
}

// SignalHistoryDates 返回有信号的交易日期（降序，最多 limit 个；limit<=0 表示全部）。
func (s *Store) SignalHistoryDates(limit int) ([]int64, error) {
	query := "SELECT DISTINCT trade_date FROM signal_history ORDER BY trade_date DESC"
	args := []any{}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询信号日期失败：%w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, 32)
	for rows.Next() {
		var date int64
		if err := rows.Scan(&date); err != nil {
			return nil, fmt.Errorf("扫描信号日期失败：%w", err)
		}
		out = append(out, date)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历信号日期失败：%w", err)
	}
	return out, nil
}

// SignalCandidate 是 attach 用的候选信号（对应 `_load_signal_candidate_frame`
// 选出的 10 列）。
//
// `Code` 已被归一化为**裸 6 位代码**（表里存的是 `600519.SH` 这样的 tushare 代码，
// 见 `LoadSignalCandidates` 里的说明）。
type SignalCandidate struct {
	TradeDate        int64   `json:"tradeDate"`
	Code             string  `json:"code"`
	ModelID          string  `json:"modelId"`
	ModelName        string  `json:"modelName"`
	MarketState      string  `json:"marketState"`
	MarketStateLabel string  `json:"marketStateLabel"`
	SignalType       string  `json:"signalType"`
	SignalLevel      string  `json:"signalLevel"`
	SignalLevelLabel string  `json:"signalLevelLabel"`
	Score            float64 `json:"score"`
	// SignalCount 是**去重前**同 `(trade_date, ts_code)` 的信号条数
	//（原实现 `groupby(...).transform("size")`）。
	SignalCount int `json:"signalCount"`
}

// SignalCodePattern 对应原实现的 ts_code 过滤正则
// `^(?:600|601|603|605|688|689|000|001|002|003|300|301)\d{3}\.(?:SH|SZ)$`。
//
// 原实现用这条正则把"非 A 股主板/创业板/科创板"的信号剔掉。Go 侧用等价的前缀
// 判断实现（避免每行编译正则），并把判断单独暴露出来便于测试。
func SignalCodeAllowed(code string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if len(normalized) != 9 {
		return false
	}
	if normalized[6] != '.' {
		return false
	}
	suffix := normalized[7:]
	if suffix != "SH" && suffix != "SZ" {
		return false
	}
	digits := normalized[3:6]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	switch normalized[:3] {
	case "600", "601", "603", "605", "688", "689", "000", "001", "002", "003", "300", "301":
		return true
	}
	return false
}

// LoadSignalCandidates 对应 `_load_signal_candidate_frame`：
// 取区间内的 buy/candidate_buy 信号，按 ts_code 前缀过滤，
// 按 `(trade_date, ts_code)` 去重（优先级 buy=0 < candidate_buy=1，再按分数降序、
// model_id 升序），并保留**去重前**的条数。
func (s *Store) LoadSignalCandidates(startDate, endDate int64) ([]SignalCandidate, error) {
	rows, err := s.db.Query(`
		SELECT trade_date, ts_code, model_id, model_name, market_state,
		       market_state_label, signal_type, signal_level, signal_level_label, score
		FROM signal_history
		WHERE trade_date >= ? AND trade_date <= ?
		  AND signal_type IN ('buy', 'candidate_buy')`,
		startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("查询信号候选失败：%w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	var all []SignalCandidate
	for rows.Next() {
		var item SignalCandidate
		if err := rows.Scan(
			&item.TradeDate, &item.Code, &item.ModelID, &item.ModelName,
			&item.MarketState, &item.MarketStateLabel, &item.SignalType,
			&item.SignalLevel, &item.SignalLevelLabel, &item.Score,
		); err != nil {
			return nil, fmt.Errorf("读取信号候选失败：%w", err)
		}
		item.Code = strings.ToUpper(strings.TrimSpace(item.Code))
		item.SignalType = strings.ToLower(strings.TrimSpace(item.SignalType))
		if !SignalCodeAllowed(item.Code) {
			continue
		}
		// ⚠ 表里存的是**原项目的 tushare 代码格式**（`600519.SH`，与 Python 侧
		// 可直接对照），而评分侧的 `Code` 是**裸 6 位**（`600519`，
		// 见 `market.KeyOf` / `BareCode`）。join 的键必须统一到裸码，
		// 否则 attach 永远匹配不上 —— 信号表再满也不会生效。
		item.Code = BareCode(item.Code)
		// 原实现是 `groupby(["trade_date","ts_code"]).transform("size")`，
		// 统计发生在**正则过滤之后、去重之前**。
		key := signalKey(item.TradeDate, item.Code)
		counts[key]++
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历信号候选失败：%w", err)
	}

	// 排序键与去重规则：priority 升序 → score 降序 → model_id 升序，保留第一条。
	sortSignalCandidates(all)
	seen := map[string]bool{}
	out := make([]SignalCandidate, 0, len(all))
	for _, item := range all {
		key := signalKey(item.TradeDate, item.Code)
		if seen[key] {
			continue
		}
		seen[key] = true
		item.SignalCount = counts[key]
		out = append(out, item)
	}
	return out, nil
}

func signalKey(tradeDate int64, code string) string {
	return fmt.Sprintf("%d|%s", tradeDate, code)
}

// signalPriorityOf 对应 `signal_type.map({"buy": 0, "candidate_buy": 1}).fillna(9)`。
func signalPriorityOf(signalType string) int {
	switch signalType {
	case "buy":
		return 0
	case "candidate_buy":
		return 1
	}
	return 9
}

func sortSignalCandidates(items []SignalCandidate) {
	// 原实现是 `sort_values` + `drop_duplicates(keep="first")`，
	// 排序必须**稳定**，否则同分同优先级的行谁留下会随实现漂移。
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.TradeDate != b.TradeDate {
			return a.TradeDate < b.TradeDate
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		pa, pb := signalPriorityOf(a.SignalType), signalPriorityOf(b.SignalType)
		if pa != pb {
			return pa < pb
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.ModelID < b.ModelID
	})
}

// insertManySQL 拼出写入 n 行的 `INSERT` 语句。
//
// 与 `strengthInsertSQL` 同构，但表名/列名可传入 —— 信号表列数不同，
// 而那两处逻辑完全一样，重复一份只会让以后改起来漏掉一处。
func insertManySQL(table, columns string, columnCount, n int, orReplace bool) string {
	if n < 1 {
		n = 1
	}
	var b strings.Builder
	if orReplace {
		b.WriteString("INSERT OR REPLACE INTO ")
	} else {
		b.WriteString("INSERT INTO ")
	}
	b.WriteString(table)
	b.WriteString(" (")
	b.WriteString(columns)
	b.WriteString(") VALUES ")
	group := "(?" + strings.Repeat(",?", columnCount-1) + ")"
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(group)
	}
	return b.String()
}
