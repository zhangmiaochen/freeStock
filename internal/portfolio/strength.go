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
	High   float64 `json:"highPrice"`
	Low    float64 `json:"lowPrice"`
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
	StockReturn3D       float64 `json:"stockReturn3d"`
	StockReturn5D       float64 `json:"stockReturn5d"`
	StockReturn10D      float64 `json:"stockReturn10d"`
	StockReturn20D      float64 `json:"stockReturn20d"`
	VolumeRatio20       float64 `json:"volumeRatio20"`
	VolumeRatio520      float64 `json:"volumeRatio520"`
	Amount              float64 `json:"amountYuan"`
	Ma5Prev             float64 `json:"ma5PrevPrice"`
	Ma250Prev           float64 `json:"ma250PrevPrice"`
	IndustryReturn20D   float64 `json:"industryReturn20d"`
	BenchmarkReturn20D  float64 `json:"benchmarkReturn20d"`
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
  high_price            REAL NOT NULL DEFAULT 0,
  low_price             REAL NOT NULL DEFAULT 0,
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
  stock_return_3d       REAL NOT NULL DEFAULT 0,
  stock_return_5d       REAL NOT NULL DEFAULT 0,
  stock_return_10d      REAL NOT NULL DEFAULT 0,
  stock_return_20d      REAL NOT NULL DEFAULT 0,
  volume_ratio_20       REAL NOT NULL DEFAULT 0,
  volume_ratio_5_20     REAL NOT NULL DEFAULT 0,
  amount_yuan           REAL NOT NULL DEFAULT 0,
  ma5_prev_price        REAL NOT NULL DEFAULT 0,
  ma250_prev_price      REAL NOT NULL DEFAULT 0,
  industry_return_20d   REAL NOT NULL DEFAULT 0,
  benchmark_return_20d  REAL NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (trade_date, strategy_id, ts_code)
);

CREATE INDEX IF NOT EXISTS idx_strength_lookup ON stock_strength(trade_date, strategy_id, total_score DESC);
`

// StrengthCodes 返回某 (交易日, 策略) 下**已有评分**的代码集合。
//
// 用于**增量刷新**：已经算过的代码不必重算 —— 结果完全相同，
// 但省掉一次 320 个交易日的行情拉取（那是刷新评分的主要成本）。
func (s *Store) StrengthCodes(tradeDate int64, strategyID string) (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT ts_code FROM stock_strength WHERE trade_date=? AND strategy_id=?`,
		tradeDate, strategyID)
	if err != nil {
		return nil, fmt.Errorf("读取已有评分代码失败：%w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("扫描已有评分代码失败：%w", err)
		}
		out[code] = true
	}
	return out, rows.Err()
}

// StrengthCount 返回某 (交易日, 策略) 已落库的评分数（增量刷新后用来报告总量）。
func (s *Store) StrengthCount(tradeDate int64, strategyID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM stock_strength WHERE trade_date=? AND strategy_id=?`,
		tradeDate, strategyID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计评分条数失败：%w", err)
	}
	return count, nil
}

// SaveStrength 覆盖写入某日某策略的评分。
//
// 先删后插：一次刷新的结果应当**完整替换**该 (日期, 策略) 的旧数据，
// 否则上一轮评上、这一轮数据不足被跳过的股票会残留成幽灵行。
//
// **写入按批做**（`strengthInsertBatch` 行一条 INSERT）。语义完全不变：
// 同一个事务、同一个表、**同一行顺序**，只是把"一行一次 Exec"换成
// "一批一次 Exec"。
//
// 为什么必须批：纯 Go 驱动（modernc/sqlite）下单个 `stmt.Exec` 要绑 39 个参数
// 再走一次 VDBE，实测 **7600 行 = 108ms**（约 14µs/行、每行 28 次分配）——
// 指标算完只要 69ms，落库反而成了回填单日最大的一块。
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

	now := nowText()
	// 满批的语句只 Prepare 一次、反复执行；余数单独 Prepare 一次。
	fullBatches := len(rows) / strengthInsertBatch
	if fullBatches > 0 {
		stmt, err := tx.Prepare(strengthInsertSQL(strengthInsertBatch))
		if err != nil {
			return fmt.Errorf("准备评分写入失败：%w", err)
		}
		defer stmt.Close()

		args := make([]any, 0, strengthInsertBatch*strengthColumnCount)
		for i := 0; i < fullBatches; i++ {
			batch := rows[i*strengthInsertBatch : (i+1)*strengthInsertBatch]
			args = appendStrengthArgs(args[:0], batch, tradeDate, strategyID, now)
			if _, err := stmt.Exec(args...); err != nil {
				return fmt.Errorf("写入评分失败（%s 起 %d 行）：%w",
					batch[0].Code, len(batch), err)
			}
		}
	}
	if rest := len(rows) % strengthInsertBatch; rest > 0 {
		batch := rows[fullBatches*strengthInsertBatch:]
		args := appendStrengthArgs(make([]any, 0, rest*strengthColumnCount), batch, tradeDate, strategyID, now)
		if _, err := tx.Exec(strengthInsertSQL(rest), args...); err != nil {
			return fmt.Errorf("写入评分失败（%s 起 %d 行）：%w",
				batch[0].Code, len(batch), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交评分失败：%w", err)
	}
	return nil
}

// strengthInsertBatch 是每条 INSERT 写多少行。
//
// 取值只受 SQLite 的"单条语句绑定参数上限"约束：200 × 39 = 7800 个占位符，
// 远低于 SQLite 的默认上限（32766）。再大收益有限，而语句编译出的程序会变长。
const strengthInsertBatch = 200

// strengthColumnCount 是 stock_strength 的插入列数（39）。
const strengthColumnCount = 39

// strengthInsertColumns 是插入列清单（列顺序必须与 appendStrengthArgs 一致）。
const strengthInsertColumns = `
	trade_date, strategy_id, ts_code, name, industry, market,
	total_score, trend_score, structure_score, rs_score, capital_score, repair_score,
	close_price, high_price, low_price, open_price, pct_chg, amount_yuan,
	ma5_price, ma20_price, ma30_price, ma60_price, ma120_price, ma250_price,
	relative_strength_20d, drawdown_from_high_20, drawdown_from_high_120,
	rebound_from_low_60, stock_return_3d, stock_return_5d, stock_return_10d,
	stock_return_20d, volume_ratio_20, volume_ratio_5_20,
	ma5_prev_price, ma250_prev_price,
	industry_return_20d, benchmark_return_20d, created_at`

// strengthInsertSQL 拼出写入 n 行的 INSERT 语句。
func strengthInsertSQL(n int) string {
	if n < 1 {
		n = 1
	}
	var b strings.Builder
	b.WriteString("INSERT INTO stock_strength (")
	b.WriteString(strengthInsertColumns)
	b.WriteString(") VALUES ")
	group := "(?" + strings.Repeat(",?", strengthColumnCount-1) + ")"
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(group)
	}
	return b.String()
}

// appendStrengthArgs 按列顺序摊平一批行。
//
// ⚠ 策略与日期以入参为准（避免调用方漏填）—— 与原来逐行写入时的处理一致。
func appendStrengthArgs(
	args []any, rows []StrengthRow, tradeDate int64, strategyID, now string,
) []any {
	for _, row := range rows {
		args = append(args,
			tradeDate, strategyID, row.Code, row.Name, row.Industry, row.Market,
			row.TotalScore, row.TrendScore, row.StructureScore, row.RSScore,
			row.CapitalScore, row.RepairScore,
			row.Close, row.High, row.Low, row.Open, row.PctChg, row.Amount,
			row.Ma5, row.Ma20, row.Ma30, row.Ma60, row.Ma120, row.Ma250,
			row.RelativeStrength20D, row.DrawdownFromHigh20, row.DrawdownFromHigh120,
			row.ReboundFromLow60, row.StockReturn3D, row.StockReturn5D, row.StockReturn10D,
			row.StockReturn20D, row.VolumeRatio20, row.VolumeRatio520,
			row.Ma5Prev, row.Ma250Prev, row.IndustryReturn20D, row.BenchmarkReturn20D, now,
		)
	}
	return args
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

// strengthSelect 是评分的读取列（顺序与 scanStrength 对应）。
const strengthSelect = `
	SELECT trade_date, strategy_id, ts_code, name, industry, market,
	       total_score, trend_score, structure_score, rs_score, capital_score, repair_score,
	       close_price, high_price, low_price, open_price, pct_chg, amount_yuan,
	       ma5_price, ma20_price, ma30_price, ma60_price, ma120_price, ma250_price,
	       relative_strength_20d, drawdown_from_high_20, drawdown_from_high_120,
	       rebound_from_low_60, stock_return_3d, stock_return_5d, stock_return_10d,
	       stock_return_20d, volume_ratio_20, volume_ratio_5_20,
	       ma5_prev_price, ma250_prev_price,
	       industry_return_20d, benchmark_return_20d
	FROM stock_strength`

func scanStrength(row scanner) (StrengthRow, error) {
	var out StrengthRow
	err := row.Scan(
		&out.TradeDate, &out.StrategyID, &out.Code, &out.Name, &out.Industry, &out.Market,
		&out.TotalScore, &out.TrendScore, &out.StructureScore, &out.RSScore,
		&out.CapitalScore, &out.RepairScore,
		&out.Close, &out.High, &out.Low, &out.Open, &out.PctChg, &out.Amount,
		&out.Ma5, &out.Ma20, &out.Ma30, &out.Ma60, &out.Ma120, &out.Ma250,
		&out.RelativeStrength20D, &out.DrawdownFromHigh20, &out.DrawdownFromHigh120,
		&out.ReboundFromLow60, &out.StockReturn3D, &out.StockReturn5D, &out.StockReturn10D,
		&out.StockReturn20D, &out.VolumeRatio20, &out.VolumeRatio520,
		&out.Ma5Prev, &out.Ma250Prev, &out.IndustryReturn20D, &out.BenchmarkReturn20D,
	)
	return out, err
}

// LoadStrengthRange 读取某个日期区间内、某策略的全部评分，按日期升序。
//
// 回测要把整个区间的评分一次性读出来（逐日查库会变成 N 次查询）。
func (s *Store) LoadStrengthRange(strategyID string, startDate, endDate int64) ([]StrengthRow, error) {
	rows, err := s.db.Query(
		strengthSelect+` WHERE strategy_id=? AND trade_date>=? AND trade_date<=?
		ORDER BY trade_date ASC, total_score DESC, ts_code ASC`,
		strategyID, startDate, endDate,
	)
	if err != nil {
		return nil, fmt.Errorf("读取区间评分失败：%w", err)
	}
	defer rows.Close()

	var out []StrengthRow
	for rows.Next() {
		item, err := scanStrength(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// StrengthDatesInRange 返回区间内**有评分**的交易日（升序）。
func (s *Store) StrengthDatesInRange(strategyID string, startDate, endDate int64) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT trade_date FROM stock_strength
		 WHERE strategy_id=? AND trade_date>=? AND trade_date<=?
		 ORDER BY trade_date ASC`,
		strategyID, startDate, endDate,
	)
	if err != nil {
		return nil, fmt.Errorf("读取评分日期失败：%w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var date int64
		if err := rows.Scan(&date); err != nil {
			return nil, fmt.Errorf("扫描评分日期失败：%w", err)
		}
		out = append(out, date)
	}
	return out, rows.Err()
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

// LowBuyBreadthReadyCount 统计"低吸广度就绪"的股票数。
//
// 原实现的口径（portfolio_service.py）：
//
//	low_buy_breadth_ready_count = 当日 (close > ma60 且 ma20 > ma60 且 close > 0 且 ma60 > 0) 的股票数
//
// 这个数**驱动两件事**：低吸持仓的强制清仓阈值（< 900）与低吸入场档位（>=1200）。
// 传 0 会让低吸组合的每只持仓都被判"广度不足"而清仓 —— 所以必须真的算出来。
//
// ⚠ 口径差异：这里是在**已物化的评分表**上数（受 RefreshStrength 的 limit 截断），
// 原实现是在全市场评分帧上数。limit 越小这个数越偏小，低吸越容易被误判为广度不足。
func (s *Store) LowBuyBreadthReadyCount(tradeDate int64, strategyID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM stock_strength
		WHERE trade_date=? AND strategy_id=?
		  AND close_price > 0 AND ma60_price > 0
		  AND close_price > ma60_price AND ma20_price > ma60_price`,
		tradeDate, strategyID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计低吸广度失败：%w", err)
	}
	return count, nil
}

// migrateStrength 建评分表（在 Store.migrate 里调用）。
func (s *Store) migrateStrength() error {
	if _, err := s.db.Exec(strengthSchema); err != nil {
		return fmt.Errorf("建评分表失败：%w", err)
	}
	if _, err := s.db.Exec(marketStateSchema); err != nil {
		return fmt.Errorf("建市场状态表失败：%w", err)
	}
	// 给已存在的老库补列（SQLite 没有 ADD COLUMN IF NOT EXISTS，只能忽略重复列错误）
	for _, col := range []string{
		"high_price REAL NOT NULL DEFAULT 0",
		"low_price REAL NOT NULL DEFAULT 0",
		"stock_return_3d REAL NOT NULL DEFAULT 0",
		"stock_return_20d REAL NOT NULL DEFAULT 0",
		"volume_ratio_5_20 REAL NOT NULL DEFAULT 0",
		"amount_yuan REAL NOT NULL DEFAULT 0",
		"ma5_prev_price REAL NOT NULL DEFAULT 0",
		"ma250_prev_price REAL NOT NULL DEFAULT 0",
		"industry_return_20d REAL NOT NULL DEFAULT 0",
		"benchmark_return_20d REAL NOT NULL DEFAULT 0",
	} {
		if _, err := s.db.Exec("ALTER TABLE stock_strength ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("补评分列失败（%s）：%w", col, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 市场状态
// ---------------------------------------------------------------------------

// MarketStateRow 是物化的市场状态。
//
// 与评分一起物化：市场状态由**全市场宽度**算出（80 天 × 全市场），
// 每次出建议都重算要十几秒，所以跟着评分一起刷新。
type MarketStateRow struct {
	TradeDate  int64   `json:"tradeDate"`
	State      string  `json:"state"`
	Label      string  `json:"label"`
	BullScore  float64 `json:"bullScore"`
	BearScore  float64 `json:"bearScore"`
	ProxyRet20 float64 `json:"proxyRet20"`
	Days       int     `json:"days"`
	CreatedAt  string  `json:"createdAt"`
}

const marketStateSchema = `
CREATE TABLE IF NOT EXISTS market_state (
  trade_date   INTEGER PRIMARY KEY,
  state        TEXT NOT NULL DEFAULT 'range',
  label        TEXT NOT NULL DEFAULT '',
  bull_score   REAL NOT NULL DEFAULT 0,
  bear_score   REAL NOT NULL DEFAULT 0,
  proxy_ret_20 REAL NOT NULL DEFAULT 0,
  days         INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL DEFAULT ''
);`

// SaveMarketState 写入市场状态（同一天覆盖）。
func (s *Store) SaveMarketState(row MarketStateRow) error {
	_, err := s.db.Exec(`
		INSERT INTO market_state (trade_date, state, label, bull_score, bear_score, proxy_ret_20, days, created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(trade_date) DO UPDATE SET
			state=excluded.state, label=excluded.label,
			bull_score=excluded.bull_score, bear_score=excluded.bear_score,
			proxy_ret_20=excluded.proxy_ret_20, days=excluded.days,
			created_at=excluded.created_at`,
		row.TradeDate, row.State, row.Label, row.BullScore, row.BearScore,
		row.ProxyRet20, row.Days, nowText(),
	)
	if err != nil {
		return fmt.Errorf("写入市场状态失败：%w", err)
	}
	return nil
}

// LoadMarketState 读取市场状态；没物化过时返回 range 兜底而不是报错 ——
// 缺市场状态不该让整个组合页打不开。
func (s *Store) LoadMarketState(tradeDate int64) (MarketStateRow, error) {
	var out MarketStateRow
	err := s.db.QueryRow(`
		SELECT trade_date, state, label, bull_score, bear_score, proxy_ret_20, days, created_at
		FROM market_state WHERE trade_date=?`, tradeDate,
	).Scan(&out.TradeDate, &out.State, &out.Label, &out.BullScore,
		&out.BearScore, &out.ProxyRet20, &out.Days, &out.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return MarketStateRow{TradeDate: tradeDate, State: "range", Label: "震荡/轮动"}, nil
		}
		return out, fmt.Errorf("读取市场状态失败：%w", err)
	}
	return out, nil
}
