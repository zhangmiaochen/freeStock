package portfolio

// 留档表：每次生成调仓建议时，把"当时看到的东西"落一份快照。
//
// 对照原实现 `portfolio_migrations.py:148-273`：
//   - `portfolio_score_snapshot`     组合层面的评分与动作统计（每组合每天一行）
//   - `portfolio_candidate_snapshot` 候选池前 N 只（每组合每天每票一行）
//   - `rotation_signal`              调仓信号（每组合每天每票一行）
//   - `hold_compare`                 持仓 vs 候选的对比（每组合每天每持仓一行）
//
// **为什么重要**：Go 侧原本是"按需重算、不留档" —— 于是看不到
// "系统在某天建议过什么"，也没法事后对账（我按建议操作了，结果如何）。
//
// ⚠ 原表里这些列本项目**没有数据源**，一律写 0 / 空串，**不伪造**：
//
//	active_signal_type / active_signal_score / active_signal_model_id /
//	active_signal_model_name / signal_trade_date / signal_age
//	  → 依赖 ML 模型信号表
//	best_model_name / best_model_win_rate  → 同上
//	research_score / board_research_score  → 依赖 research_service.py 与研究库
//	character_label                        → 研究库的个股画像
const snapshotSchema = `
CREATE TABLE IF NOT EXISTS portfolio_score_snapshot (
  portfolio_id              TEXT NOT NULL,
  trade_date                TEXT NOT NULL,
  strategy_id               TEXT NOT NULL DEFAULT '',
  strategy_label            TEXT NOT NULL DEFAULT '',
  portfolio_type            TEXT NOT NULL DEFAULT '',
  portfolio_type_label      TEXT NOT NULL DEFAULT '',
  execution_mode            TEXT NOT NULL DEFAULT '',
  execution_mode_label      TEXT NOT NULL DEFAULT '',
  allow_apply_rotation      INTEGER NOT NULL DEFAULT 0,
  require_manual_confirmation INTEGER NOT NULL DEFAULT 0,
  portfolio_score           REAL NOT NULL DEFAULT 0,
  health_label              TEXT NOT NULL DEFAULT '',
  position_count            INTEGER NOT NULL DEFAULT 0,
  candidate_count           INTEGER NOT NULL DEFAULT 0,
  keep_count                INTEGER NOT NULL DEFAULT 0,
  warning_count             INTEGER NOT NULL DEFAULT 0,
  replace_count             INTEGER NOT NULL DEFAULT 0,
  add_count                 INTEGER NOT NULL DEFAULT 0,
  clear_count               INTEGER NOT NULL DEFAULT 0,
  action_summary_json       TEXT NOT NULL DEFAULT '{}',
  total_return_pct          REAL NOT NULL DEFAULT 0,
  max_drawdown_pct          REAL NOT NULL DEFAULT 0,
  created_at                TEXT NOT NULL,
  updated_at                TEXT NOT NULL,
  PRIMARY KEY (portfolio_id, trade_date)
);

CREATE TABLE IF NOT EXISTS portfolio_candidate_snapshot (
  portfolio_id          TEXT NOT NULL,
  trade_date            TEXT NOT NULL,
  strategy_id           TEXT NOT NULL DEFAULT '',
  strategy_label        TEXT NOT NULL DEFAULT '',
  portfolio_type        TEXT NOT NULL DEFAULT '',
  portfolio_type_label  TEXT NOT NULL DEFAULT '',
  candidate_rank        INTEGER NOT NULL DEFAULT 0,
  ts_code               TEXT NOT NULL,
  name                  TEXT NOT NULL DEFAULT '',
  industry              TEXT NOT NULL DEFAULT '',
  market                TEXT NOT NULL DEFAULT '',
  market_state          TEXT NOT NULL DEFAULT '',
  market_state_label    TEXT NOT NULL DEFAULT '',
  active_signal_type    TEXT NOT NULL DEFAULT '',
  active_signal_score   REAL NOT NULL DEFAULT 0,
  active_signal_model_id TEXT NOT NULL DEFAULT '',
  active_signal_model_name TEXT NOT NULL DEFAULT '',
  signal_trade_date     TEXT NOT NULL DEFAULT '',
  signal_age            INTEGER NOT NULL DEFAULT 0,
  character_label       TEXT NOT NULL DEFAULT '',
  best_model_name       TEXT NOT NULL DEFAULT '',
  best_model_win_rate   REAL NOT NULL DEFAULT 0,
  total_score           REAL NOT NULL DEFAULT 0,
  trend_score           REAL NOT NULL DEFAULT 0,
  structure_score       REAL NOT NULL DEFAULT 0,
  rs_score              REAL NOT NULL DEFAULT 0,
  capital_score         REAL NOT NULL DEFAULT 0,
  repair_score          REAL NOT NULL DEFAULT 0,
  research_score        REAL NOT NULL DEFAULT 0,
  board_research_score  REAL NOT NULL DEFAULT 0,
  close_price           REAL NOT NULL DEFAULT 0,
  relative_strength_20d REAL NOT NULL DEFAULT 0,
  drawdown_from_high_120 REAL NOT NULL DEFAULT 0,
  score_summary         TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  PRIMARY KEY (portfolio_id, trade_date, ts_code)
);

CREATE TABLE IF NOT EXISTS rotation_signal (
  portfolio_id          TEXT NOT NULL,
  trade_date            TEXT NOT NULL,
  strategy_id           TEXT NOT NULL DEFAULT '',
  strategy_label        TEXT NOT NULL DEFAULT '',
  portfolio_type        TEXT NOT NULL DEFAULT '',
  portfolio_type_label  TEXT NOT NULL DEFAULT '',
  execution_mode        TEXT NOT NULL DEFAULT '',
  execution_mode_label  TEXT NOT NULL DEFAULT '',
  requires_confirmation INTEGER NOT NULL DEFAULT 0,
  is_executable         INTEGER NOT NULL DEFAULT 0,
  ts_code               TEXT NOT NULL,
  name                  TEXT NOT NULL DEFAULT '',
  action                TEXT NOT NULL DEFAULT '',
  score                 REAL NOT NULL DEFAULT 0,
  score_gap             REAL NOT NULL DEFAULT 0,
  rank_no               INTEGER NOT NULL DEFAULT 0,
  candidate_ts_code     TEXT NOT NULL DEFAULT '',
  candidate_name        TEXT NOT NULL DEFAULT '',
  candidate_score       REAL NOT NULL DEFAULT 0,
  expected_improvement  REAL NOT NULL DEFAULT 0,
  reason                TEXT NOT NULL DEFAULT '',
  source_type           TEXT NOT NULL DEFAULT '',
  current_layers        INTEGER NOT NULL DEFAULT 0,
  target_layers         INTEGER NOT NULL DEFAULT 0,
  layers_delta          INTEGER NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  PRIMARY KEY (portfolio_id, trade_date, ts_code)
);

CREATE TABLE IF NOT EXISTS hold_compare (
  portfolio_id          TEXT NOT NULL,
  trade_date            TEXT NOT NULL,
  strategy_id           TEXT NOT NULL DEFAULT '',
  strategy_label        TEXT NOT NULL DEFAULT '',
  portfolio_type        TEXT NOT NULL DEFAULT '',
  portfolio_type_label  TEXT NOT NULL DEFAULT '',
  holding_ts_code       TEXT NOT NULL,
  holding_name          TEXT NOT NULL DEFAULT '',
  holding_score         REAL NOT NULL DEFAULT 0,
  candidate_ts_code     TEXT NOT NULL DEFAULT '',
  candidate_name        TEXT NOT NULL DEFAULT '',
  candidate_score       REAL NOT NULL DEFAULT 0,
  total_score_gap       REAL NOT NULL DEFAULT 0,
  rs_score_gap          REAL NOT NULL DEFAULT 0,
  repair_score_gap      REAL NOT NULL DEFAULT 0,
  main_reason           TEXT NOT NULL DEFAULT '',
  expected_portfolio_uplift REAL NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  PRIMARY KEY (portfolio_id, trade_date, holding_ts_code)
);

CREATE INDEX IF NOT EXISTS idx_score_snap_date ON portfolio_score_snapshot(portfolio_id, trade_date);
CREATE INDEX IF NOT EXISTS idx_cand_snap_date  ON portfolio_candidate_snapshot(portfolio_id, trade_date);
CREATE INDEX IF NOT EXISTS idx_rot_signal_date ON rotation_signal(portfolio_id, trade_date);
CREATE INDEX IF NOT EXISTS idx_hold_compare    ON hold_compare(portfolio_id, trade_date);
`

// ScoreSnapshot 是组合层面的评分与动作统计。
type ScoreSnapshot struct {
	PortfolioID          string  `json:"portfolioId"`
	TradeDate            string  `json:"tradeDate"`
	StrategyID           string  `json:"strategyId"`
	StrategyLabel        string  `json:"strategyLabel"`
	PortfolioType        string  `json:"portfolioType"`
	PortfolioTypeLabel   string  `json:"portfolioTypeLabel"`
	ExecutionMode        string  `json:"executionMode"`
	ExecutionModeLabel   string  `json:"executionModeLabel"`
	AllowApplyRotation   bool    `json:"allowApplyRotation"`
	RequireManualConfirm bool    `json:"requireManualConfirmation"`
	PortfolioScore       float64 `json:"portfolioScore"`
	HealthLabel          string  `json:"healthLabel"`
	PositionCount        int     `json:"positionCount"`
	CandidateCount       int     `json:"candidateCount"`
	KeepCount            int     `json:"keepCount"`
	WarningCount         int     `json:"warningCount"`
	ReplaceCount         int     `json:"replaceCount"`
	AddCount             int     `json:"addCount"`
	ClearCount           int     `json:"clearCount"`
	ActionSummaryJSON    string  `json:"actionSummaryJson"`
	TotalReturnPct       float64 `json:"totalReturnPct"`
	MaxDrawdownPct       float64 `json:"maxDrawdownPct"`
}

// CandidateSnapshot 是候选池里的一只（带排名）。
type CandidateSnapshot struct {
	PortfolioID         string  `json:"portfolioId"`
	TradeDate           string  `json:"tradeDate"`
	StrategyID          string  `json:"strategyId"`
	StrategyLabel       string  `json:"strategyLabel"`
	PortfolioType       string  `json:"portfolioType"`
	PortfolioTypeLabel  string  `json:"portfolioTypeLabel"`
	CandidateRank       int     `json:"candidateRank"`
	Code                string  `json:"tsCode"`
	Name                string  `json:"name"`
	Industry            string  `json:"industry"`
	Market              string  `json:"market"`
	MarketState         string  `json:"marketState"`
	MarketStateLabel    string  `json:"marketStateLabel"`
	TotalScore          float64 `json:"totalScore"`
	TrendScore          float64 `json:"trendScore"`
	StructureScore      float64 `json:"structureScore"`
	RSScore             float64 `json:"rsScore"`
	CapitalScore        float64 `json:"capitalScore"`
	RepairScore         float64 `json:"repairScore"`
	ClosePrice          float64 `json:"closePrice"`
	RelativeStrength20D float64 `json:"relativeStrength20d"`
	DrawdownFromHigh120 float64 `json:"drawdownFromHigh120"`
	ScoreSummary        string  `json:"scoreSummary"`
}

// RotationSignalRow 是一条调仓信号。
type RotationSignalRow struct {
	PortfolioID         string  `json:"portfolioId"`
	TradeDate           string  `json:"tradeDate"`
	StrategyID          string  `json:"strategyId"`
	StrategyLabel       string  `json:"strategyLabel"`
	PortfolioType       string  `json:"portfolioType"`
	PortfolioTypeLabel  string  `json:"portfolioTypeLabel"`
	ExecutionMode       string  `json:"executionMode"`
	ExecutionModeLabel  string  `json:"executionModeLabel"`
	RequiresConfirm     bool    `json:"requiresConfirmation"`
	IsExecutable        bool    `json:"isExecutable"`
	Code                string  `json:"tsCode"`
	Name                string  `json:"name"`
	Action              string  `json:"action"`
	Score               float64 `json:"score"`
	ScoreGap            float64 `json:"scoreGap"`
	RankNo              int     `json:"rankNo"`
	CandidateCode       string  `json:"candidateTsCode"`
	CandidateName       string  `json:"candidateName"`
	CandidateScore      float64 `json:"candidateScore"`
	ExpectedImprovement float64 `json:"expectedImprovement"`
	Reason              string  `json:"reason"`
	SourceType          string  `json:"sourceType"`
	CurrentLayers       int     `json:"currentLayers"`
	TargetLayers        int     `json:"targetLayers"`
	LayersDelta         int     `json:"layersDelta"`
}

// HoldCompareRow 是持仓与候选的对比。
type HoldCompareRow struct {
	PortfolioID        string  `json:"portfolioId"`
	TradeDate          string  `json:"tradeDate"`
	StrategyID         string  `json:"strategyId"`
	StrategyLabel      string  `json:"strategyLabel"`
	PortfolioType      string  `json:"portfolioType"`
	PortfolioTypeLabel string  `json:"portfolioTypeLabel"`
	HoldingCode        string  `json:"holdingTsCode"`
	HoldingName        string  `json:"holdingName"`
	HoldingScore       float64 `json:"holdingScore"`
	CandidateCode      string  `json:"candidateTsCode"`
	CandidateName      string  `json:"candidateName"`
	CandidateScore     float64 `json:"candidateScore"`
	TotalScoreGap      float64 `json:"totalScoreGap"`
	RSScoreGap         float64 `json:"rsScoreGap"`
	RepairScoreGap     float64 `json:"repairScoreGap"`
	MainReason         string  `json:"mainReason"`
	ExpectedUplift     float64 `json:"expectedPortfolioUplift"`
}
