package portfolio

// 回测的三张表。
//
// 对照原实现 `portfolio_migrations.py:274-338`：
//   - `portfolio_backtest_run`   一次回测的元信息与绩效指标
//   - `portfolio_backtest_daily` 每个交易日的净值曲线
//   - `portfolio_backtest_trade` 每一笔成交（含手续费/印花税/已实现盈亏）
//
// 风格差异（与其它表一致）：
//   - 原表全可空无默认无外键；这里给关键列加 NOT NULL/默认值
//   - `portfolio_id` / `run_id` 加 ON DELETE CASCADE —— 原项目删组合要手动清 7+2 张表
//   - 时间戳用 RFC3339 TEXT（原表是 TIMESTAMP）
//   - 日期仍是 YYYYMMDD 字符串
//
// ⚠ `shares` / `amount` / `commission` / `tax` / `total_cost` / `profit_loss` /
// `profit_loss_pct` 在原版里是**后续 ALTER 补的列**（`portfolio_migrations.py:340-346`），
// 建表时并没有 —— 这里直接建全，因为本项目是新建库。
const backtestSchema = `
CREATE TABLE IF NOT EXISTS portfolio_backtest_run (
  run_id               TEXT PRIMARY KEY,
  portfolio_id         TEXT NOT NULL,
  portfolio_name       TEXT NOT NULL DEFAULT '',
  strategy_id          TEXT NOT NULL DEFAULT '',
  strategy_label       TEXT NOT NULL DEFAULT '',
  portfolio_type       TEXT NOT NULL DEFAULT '',
  portfolio_type_label TEXT NOT NULL DEFAULT '',
  benchmark_code       TEXT NOT NULL DEFAULT '',
  start_date           TEXT NOT NULL DEFAULT '',
  end_date             TEXT NOT NULL DEFAULT '',
  rebalance_frequency  TEXT NOT NULL DEFAULT 'signal',
  initial_capital      REAL NOT NULL DEFAULT 0,
  status               TEXT NOT NULL DEFAULT 'pending',
  total_return         REAL NOT NULL DEFAULT 0,
  annual_return        REAL NOT NULL DEFAULT 0,
  max_drawdown         REAL NOT NULL DEFAULT 0,
  sharpe_ratio         REAL NOT NULL DEFAULT 0,
  total_trades         INTEGER NOT NULL DEFAULT 0,
  avg_position_count   REAL NOT NULL DEFAULT 0,
  final_value          REAL NOT NULL DEFAULT 0,
  summary_json         TEXT NOT NULL DEFAULT '{}',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  finished_at          TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (portfolio_id) REFERENCES portfolio(portfolio_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS portfolio_backtest_daily (
  run_id           TEXT NOT NULL,
  trade_date       TEXT NOT NULL,
  total_value      REAL NOT NULL DEFAULT 0,
  cash             REAL NOT NULL DEFAULT 0,
  position_value   REAL NOT NULL DEFAULT 0,
  daily_return     REAL NOT NULL DEFAULT 0,
  cumulative_return REAL NOT NULL DEFAULT 0,
  benchmark_value  REAL NOT NULL DEFAULT 0,
  benchmark_return REAL NOT NULL DEFAULT 0,
  position_count   INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT NOT NULL,
  PRIMARY KEY (run_id, trade_date),
  FOREIGN KEY (run_id) REFERENCES portfolio_backtest_run(run_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS portfolio_backtest_trade (
  trade_id        TEXT PRIMARY KEY,
  run_id          TEXT NOT NULL,
  portfolio_id    TEXT NOT NULL,
  ts_code         TEXT NOT NULL,
  name            TEXT NOT NULL DEFAULT '',
  action          TEXT NOT NULL,
  trade_date      TEXT NOT NULL DEFAULT '',
  price           REAL NOT NULL DEFAULT 0,
  shares          INTEGER NOT NULL DEFAULT 0,
  amount          REAL NOT NULL DEFAULT 0,
  commission      REAL NOT NULL DEFAULT 0,
  tax             REAL NOT NULL DEFAULT 0,
  total_cost      REAL NOT NULL DEFAULT 0,
  profit_loss     REAL NOT NULL DEFAULT 0,
  profit_loss_pct REAL NOT NULL DEFAULT 0,
  position_weight REAL NOT NULL DEFAULT 0,
  score           REAL NOT NULL DEFAULT 0,
  reason          TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  FOREIGN KEY (run_id) REFERENCES portfolio_backtest_run(run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bt_run_portfolio ON portfolio_backtest_run(portfolio_id, created_at);
CREATE INDEX IF NOT EXISTS idx_bt_daily_run     ON portfolio_backtest_daily(run_id, trade_date);
CREATE INDEX IF NOT EXISTS idx_bt_trade_run     ON portfolio_backtest_trade(run_id, trade_date);
`

// backtestSchemaStandalone 是独立回测库用的 DDL：与 backtestSchema 逐列相同，
// 只去掉 `portfolio_id → portfolio(portfolio_id)` 外键。
//
// 为什么必须去掉：回测库与核心库是两个文件，SQLite 外键不能跨文件。更致命的是
// 迁移后**新建的组合只存在核心库**，回测库里没有对应 portfolio 行 —— 带外键就会
// 在写回测结果时直接撞 787（已复现）。组合与分析库之间本来就靠 portfolio_id
// 这个**应用层引用**，删组合时由 DeletePortfolio 显式清理。
//
// run_id 两个外键保留：它们在同文件内，删除单次回测时仍要靠级联清净值/成交。
//
// ⚠ 改列时必须同步 backtestSchema；`TestBacktestStandaloneSchemaMatchesFull`
// 会逐列比对两边的 table_info，漂移会直接失败。
const backtestSchemaStandalone = `
CREATE TABLE IF NOT EXISTS portfolio_backtest_run (
  run_id               TEXT PRIMARY KEY,
  portfolio_id         TEXT NOT NULL,
  portfolio_name       TEXT NOT NULL DEFAULT '',
  strategy_id          TEXT NOT NULL DEFAULT '',
  strategy_label       TEXT NOT NULL DEFAULT '',
  portfolio_type       TEXT NOT NULL DEFAULT '',
  portfolio_type_label TEXT NOT NULL DEFAULT '',
  benchmark_code       TEXT NOT NULL DEFAULT '',
  start_date           TEXT NOT NULL DEFAULT '',
  end_date             TEXT NOT NULL DEFAULT '',
  rebalance_frequency  TEXT NOT NULL DEFAULT 'signal',
  initial_capital      REAL NOT NULL DEFAULT 0,
  status               TEXT NOT NULL DEFAULT 'pending',
  total_return         REAL NOT NULL DEFAULT 0,
  annual_return        REAL NOT NULL DEFAULT 0,
  max_drawdown         REAL NOT NULL DEFAULT 0,
  sharpe_ratio         REAL NOT NULL DEFAULT 0,
  total_trades         INTEGER NOT NULL DEFAULT 0,
  avg_position_count   REAL NOT NULL DEFAULT 0,
  final_value          REAL NOT NULL DEFAULT 0,
  summary_json         TEXT NOT NULL DEFAULT '{}',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  finished_at          TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS portfolio_backtest_daily (
  run_id           TEXT NOT NULL,
  trade_date       TEXT NOT NULL,
  total_value      REAL NOT NULL DEFAULT 0,
  cash             REAL NOT NULL DEFAULT 0,
  position_value   REAL NOT NULL DEFAULT 0,
  daily_return     REAL NOT NULL DEFAULT 0,
  cumulative_return REAL NOT NULL DEFAULT 0,
  benchmark_value  REAL NOT NULL DEFAULT 0,
  benchmark_return REAL NOT NULL DEFAULT 0,
  position_count   INTEGER NOT NULL DEFAULT 0,
  created_at       TEXT NOT NULL,
  PRIMARY KEY (run_id, trade_date),
  FOREIGN KEY (run_id) REFERENCES portfolio_backtest_run(run_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS portfolio_backtest_trade (
  trade_id        TEXT PRIMARY KEY,
  run_id          TEXT NOT NULL,
  portfolio_id    TEXT NOT NULL,
  ts_code         TEXT NOT NULL,
  name            TEXT NOT NULL DEFAULT '',
  action          TEXT NOT NULL,
  trade_date      TEXT NOT NULL DEFAULT '',
  price           REAL NOT NULL DEFAULT 0,
  shares          INTEGER NOT NULL DEFAULT 0,
  amount          REAL NOT NULL DEFAULT 0,
  commission      REAL NOT NULL DEFAULT 0,
  tax             REAL NOT NULL DEFAULT 0,
  total_cost      REAL NOT NULL DEFAULT 0,
  profit_loss     REAL NOT NULL DEFAULT 0,
  profit_loss_pct REAL NOT NULL DEFAULT 0,
  position_weight REAL NOT NULL DEFAULT 0,
  score           REAL NOT NULL DEFAULT 0,
  reason          TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  FOREIGN KEY (run_id) REFERENCES portfolio_backtest_run(run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bt_run_portfolio ON portfolio_backtest_run(portfolio_id, created_at);
CREATE INDEX IF NOT EXISTS idx_bt_daily_run     ON portfolio_backtest_daily(run_id, trade_date);
CREATE INDEX IF NOT EXISTS idx_bt_trade_run     ON portfolio_backtest_trade(run_id, trade_date);
`

// BacktestRun 是一次回测的元信息与绩效。
type BacktestRun struct {
	RunID              string  `json:"runId"`
	PortfolioID        string  `json:"portfolioId"`
	PortfolioName      string  `json:"portfolioName"`
	StrategyID         string  `json:"strategyId"`
	StrategyLabel      string  `json:"strategyLabel"`
	PortfolioType      string  `json:"portfolioType"`
	PortfolioTypeLabel string  `json:"portfolioTypeLabel"`
	BenchmarkCode      string  `json:"benchmarkCode"`
	StartDate          string  `json:"startDate"`
	EndDate            string  `json:"endDate"`
	RebalanceFrequency string  `json:"rebalanceFrequency"`
	InitialCapital     float64 `json:"initialCapital"`
	Status             string  `json:"status"`
	TotalReturn        float64 `json:"totalReturn"`
	AnnualReturn       float64 `json:"annualReturn"`
	MaxDrawdown        float64 `json:"maxDrawdown"`
	SharpeRatio        float64 `json:"sharpeRatio"`
	TotalTrades        int     `json:"totalTrades"`
	AvgPositionCount   float64 `json:"avgPositionCount"`
	FinalValue         float64 `json:"finalValue"`
	SummaryJSON        string  `json:"summaryJson"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
	FinishedAt         string  `json:"finishedAt"`
}

// BacktestDaily 是回测净值曲线的一天。
type BacktestDaily struct {
	RunID            string  `json:"runId"`
	TradeDate        string  `json:"tradeDate"`
	TotalValue       float64 `json:"totalValue"`
	Cash             float64 `json:"cash"`
	PositionValue    float64 `json:"positionValue"`
	DailyReturn      float64 `json:"dailyReturn"`
	CumulativeReturn float64 `json:"cumulativeReturn"`
	BenchmarkValue   float64 `json:"benchmarkValue"`
	BenchmarkReturn  float64 `json:"benchmarkReturn"`
	PositionCount    int     `json:"positionCount"`
}

// BacktestTrade 是回测里的一笔成交。
type BacktestTrade struct {
	TradeID        string  `json:"tradeId"`
	RunID          string  `json:"runId"`
	PortfolioID    string  `json:"portfolioId"`
	Code           string  `json:"tsCode"`
	Name           string  `json:"name"`
	Action         string  `json:"action"`
	TradeDate      string  `json:"tradeDate"`
	Price          float64 `json:"price"`
	Shares         int     `json:"shares"`
	Amount         float64 `json:"amount"`
	Commission     float64 `json:"commission"`
	Tax            float64 `json:"tax"`
	TotalCost      float64 `json:"totalCost"`
	ProfitLoss     float64 `json:"profitLoss"`
	ProfitLossPct  float64 `json:"profitLossPct"`
	PositionWeight float64 `json:"positionWeight"`
	Score          float64 `json:"score"`
	Reason         string  `json:"reason"`
	CreatedAt      string  `json:"createdAt"`
}
