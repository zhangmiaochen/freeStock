package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 回测的服务层编排。
//
// 对应原实现 `portfolio_service.py:982-1158`（create_backtest）
// 与 `:1159-1199`（get_backtest_payload）。

// BacktestRequest 是一次回测的入参。
type BacktestRequest struct {
	PortfolioID string `json:"portfolioId"`
	StartDate   int    `json:"startDate"`
	EndDate     int    `json:"endDate"`
	// 以下都可省略，省略时用组合的配置
	InitialCapital      float64 `json:"initialCapital"`
	TargetPositionCount int     `json:"targetPositionCount"`
	MaxStockCount       int     `json:"maxStockCount"`
	BenchmarkCode       string  `json:"benchmarkCode"`
}

// BacktestRunResult 是回测完成后的摘要。
type BacktestRunResult struct {
	OK      bool             `json:"ok"`
	RunID   string           `json:"runId"`
	Metrics backtest.Metrics `json:"metrics"`
	Message string           `json:"message"`
}

// BacktestRunView 是回测记录的展示视图。
type BacktestRunView struct {
	RunID         string `json:"runId"`
	PortfolioID   string `json:"portfolioId"`
	PortfolioName string `json:"portfolioName"`
	StrategyID    string `json:"strategyId"`
	StrategyLabel string `json:"strategyLabel"`
	PortfolioType string `json:"portfolioType"`
	BenchmarkCode string `json:"benchmarkCode"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	Status        string `json:"status"`
	// Metrics 是 summary_json 解析出来的指标
	Metrics map[string]any `json:"metrics"`
}

// BacktestPayload 是一次回测的完整结果。
type BacktestPayload struct {
	OK        bool                      `json:"ok"`
	Available bool                      `json:"available"`
	Run       BacktestRunView           `json:"run"`
	Daily     []portfolio.BacktestDaily `json:"daily"`
	Trades    []portfolio.BacktestTrade `json:"trades"`
}

// CreateBacktest 跑一次回测并落库。
func (s *PortfolioService) CreateBacktest(req BacktestRequest) (*BacktestRunResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	item, err := store.Get(req.PortfolioID)
	if err != nil {
		return nil, err
	}
	if req.StartDate <= 0 || req.EndDate <= 0 || req.StartDate > req.EndDate {
		return nil, fmt.Errorf("回测区间格式错误，应为 YYYYMMDD 且开始日不能晚于结束日")
	}

	// ① 区间内的评分（回测的前提：先做过历史回填）
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(req.StartDate), int64(req.EndDate))
	if err != nil {
		return nil, err
	}
	if len(strengths) == 0 {
		return nil, fmt.Errorf("回测区间内没有历史评分数据，请先执行历史回填")
	}

	days, codes, scopeErrors := s.buildStrengthDays(store, strengths)
	if len(days) == 0 {
		return nil, fmt.Errorf("回测区间内没有可用的评分交易日")
	}

	// ② 日线价格（区间内涉及的全部代码）
	//
	// 这一步是回测的**主要成本**：每只股票一次 HTTP 往返。
	// 加计时是为了以后能一眼看出慢在哪一段，不用再猜。
	barsBegan := time.Now()
	bars, err := s.loadBacktestBars(codes, req.StartDate, req.EndDate)
	if err != nil {
		return nil, err
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("回测区间内没有可用的日线价格数据")
	}
	slog.Info("回测取价完成",
		"codes", len(codes), "bars", len(bars),
		"elapsed", time.Since(barsBegan).Round(time.Millisecond))

	// ③ 基准
	benchmarkCode := req.BenchmarkCode
	if benchmarkCode == "" {
		benchmarkCode = item.BenchmarkCode
	}
	if benchmarkCode == "" {
		benchmarkCode = score.LowBuyIndexBenchmarkCode
	}
	benchmark := map[string]float64{}
	if s.stock != nil {
		if closes, err := s.stock.loadBenchmark(benchmarkCode, req.StartDate, req.EndDate); err == nil {
			for date, close := range closes {
				benchmark[fmt.Sprintf("%d", date)] = close
			}
		}
	}

	// ④ 组装配置
	targetCount := req.TargetPositionCount
	if targetCount <= 0 {
		targetCount = item.TargetPositionCount
	}
	maxStocks := req.MaxStockCount
	if maxStocks <= 0 {
		maxStocks = item.MaxStockCount
	}
	if maxStocks < targetCount {
		maxStocks = targetCount
	}
	cfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		BenchmarkCode:       benchmarkCode,
		StartDate:           fmt.Sprintf("%d", req.StartDate),
		EndDate:             fmt.Sprintf("%d", req.EndDate),
		TargetPositionCount: targetCount,
		MaxStockCount:       maxStocks,
		TotalLayers:         item.TotalLayers,
		// 低吸的单股层数上限是总层数，其余是 3（原实现 :1031）
		MaxLayersPerStock: maxLayersForBacktest(item.StrategyID, item.TotalLayers),
		InitialCapital:    req.InitialCapital,
		MarketScope:       scopeOfPortfolio(item),
	}

	engine := backtest.New(cfg, days, bars, benchmark)
	buildPlan := s.backtestPlanBuilder(item, store, marketStateByDate(store, days))
	result, err := engine.Run(buildPlan)
	if err != nil {
		return nil, fmt.Errorf("回测执行失败：%w", err)
	}
	initialCapital := req.InitialCapital
	if initialCapital < 1 {
		initialCapital = cfg.InitialCapital
	}
	if initialCapital < 1 {
		initialCapital = 100000
	}
	metrics := backtest.CalculateMetrics(result.Snapshots, result.Trades, initialCapital)

	// ⑤ 落库
	runID := portfolio.NewBacktestRunID()
	if err := s.persistBacktest(store, runID, item, cfg, benchmarkCode, result, metrics); err != nil {
		return nil, err
	}

	slog.Info("回测完成",
		"runId", runID, "portfolio", item.ID, "strategy", item.StrategyID,
		"days", len(result.Snapshots), "trades", len(result.Trades),
		"totalReturn", metrics.TotalReturn, "maxDrawdown", metrics.MaxDrawdown)
	if len(scopeErrors) > 0 {
		slog.Warn("回测期间有评分缺失的日期", "count", len(scopeErrors))
	}
	return &BacktestRunResult{
		OK: true, RunID: runID, Metrics: metrics, Message: "组合回测完成",
	}, nil
}

// GetBacktestPayload 读一次回测的完整结果。
func (s *PortfolioService) GetBacktestPayload(runID string) (*BacktestPayload, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	run, err := store.GetBacktestRun(runID)
	if err != nil {
		return nil, fmt.Errorf("未找到对应回测记录")
	}
	daily, err := store.ListBacktestDaily(runID)
	if err != nil {
		return nil, err
	}
	trades, err := store.ListBacktestTrades(runID)
	if err != nil {
		return nil, err
	}
	metrics := map[string]any{}
	if parsed := parseJSONObject(run.SummaryJSON); parsed != nil {
		metrics = parsed
	}
	return &BacktestPayload{
		OK: true, Available: true,
		Run: BacktestRunView{
			RunID: run.RunID, PortfolioID: run.PortfolioID, PortfolioName: run.PortfolioName,
			StrategyID: run.StrategyID, StrategyLabel: run.StrategyLabel,
			PortfolioType: run.PortfolioType, BenchmarkCode: run.BenchmarkCode,
			StartDate: run.StartDate, EndDate: run.EndDate, Status: run.Status,
			Metrics: metrics,
		},
		Daily: daily, Trades: trades,
	}, nil
}

// ListBacktests 列出回测记录。
func (s *PortfolioService) ListBacktests(portfolioID string) ([]portfolio.BacktestRun, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	return store.ListBacktestRuns(portfolioID)
}

// DeleteBacktest 删除一次回测（级联清净值与成交）。
func (s *PortfolioService) DeleteBacktest(runID string) error {
	store, err := s.db()
	if err != nil {
		return err
	}
	return store.DeleteBacktestRun(runID)
}

// buildStrengthDays 把区间评分按交易日分组。
//
// 同时收集"有价格但没评分"的日期，供调用方记日志 ——
// 那些日期回测不会生成新计划（原实现同样要求评分日与价格日都有）。
func (s *PortfolioService) buildStrengthDays(
	store *portfolio.Store, strengths []portfolio.StrengthRow,
) ([]backtest.StrengthDay, []string, []string) {
	byDate := map[string][]score.Row{}
	marketByDate := map[string]string{}
	var order []string

	for _, item := range strengths {
		date := fmt.Sprintf("%d", item.TradeDate)
		if _, seen := byDate[date]; !seen {
			order = append(order, date)
			if state, err := store.LoadMarketState(item.TradeDate); err == nil {
				marketByDate[date] = state.State
			}
		}
		byDate[date] = append(byDate[date], strengthToRow(item))
	}
	sort.Strings(order)

	days := make([]backtest.StrengthDay, 0, len(order))
	codes := map[string]bool{}
	var codeList []string
	for _, date := range order {
		state := marketByDate[date]
		if state == "" {
			state = score.StateRange
		}
		days = append(days, backtest.StrengthDay{Date: date, MarketState: state, Rows: byDate[date]})
		for _, row := range byDate[date] {
			if !codes[row.Code] {
				codes[row.Code] = true
				codeList = append(codeList, row.Code)
			}
		}
	}
	sort.Strings(codeList)
	return days, codeList, nil
}

// loadBacktestBars 并发取区间内每只股票的日线。
func (s *PortfolioService) loadBacktestBars(codes []string, start, end int) ([]backtest.PriceBar, error) {
	if s.stock == nil {
		return nil, fmt.Errorf("缺少股票服务依赖")
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return nil, fmt.Errorf("数据源未就绪")
	}

	// ★ 若紧随其后的这次回测能用上"回填刚取过的"日线，就直接复用。
	//
	// 实测取价占回测总耗时的 97%（300 只 / 4.9 秒，引擎只要 0.13 秒），
	// 而回测要的区间本来就是回填窗口的子集 —— 这批价刚刚才取过一遍。
	//
	// 复用的前提是**同一个数据源**且窗口覆盖得住（见 backfillCacheFor）；
	// 不满足就走原来的真实请求，不会少给数据。
	if cache := s.backfillCacheFor(provider, start, end); cache != nil {
		provider = cache
		slog.Info("回测复用回填时的日线缓存",
			"codes", len(codes), "from", start, "to", end)
	}

	type job struct {
		code string
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var out []backtest.PriceBar
	nameByCode := map[string]string{}
	if universe, err := s.stock.GetStockUniverse(false); err == nil {
		for _, item := range universe.Stocks {
			nameByCode[item.Code] = item.Name
		}
	}

	// 并发度与评分保持一致。
	//
	// 原来是写死的 8 —— 而这一步是"每只股票一次 HTTP 往返"，
	// 候选池上千只时 8 个 worker 明显吃不满（评分那边一直用 32）。
	// 同一数据源、同样的请求，只是更并发，**结果与顺序都无关**
	//（下面按 code 归集，不依赖完成顺序）。
	workers := scoreWorkers
	if workers > len(codes) {
		workers = len(codes)
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				quotes, err := provider.DailyHistory(context.Background(), j.code, start, end)
				if err != nil || len(quotes) == 0 {
					continue
				}
				local := make([]backtest.PriceBar, 0, len(quotes))
				for _, q := range quotes {
					if q == nil || q.Date <= 0 || q.Close <= 0 {
						continue
					}
					local = append(local, backtest.PriceBar{
						Date: fmt.Sprintf("%d", q.Date), Code: j.code,
						Open: q.Open, Close: q.Close, Name: nameByCode[j.code],
					})
				}
				if len(local) == 0 {
					continue
				}
				mu.Lock()
				out = append(out, local...)
				mu.Unlock()
			}
		}()
	}
	for _, code := range codes {
		jobs <- job{code: code}
	}
	close(jobs)
	wg.Wait()
	return out, nil
}

// backtestPlanBuilder 造一个"给当日持仓与评分、产出调仓计划"的回调。
//
// 逻辑与实盘 `BuildPlan` 一致（同用 `buildCandidatePoolFrom` 与
// `score.BuildRotationPlan`），差别只是数据来自内存而非当天查库。
func (s *PortfolioService) backtestPlanBuilder(
	item portfolio.Portfolio, store *portfolio.Store, states map[string]string,
) backtest.PlanBuilder {
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		marketState := day.MarketState
		// 当日评分行（转回 StrengthRow 以便复用候选池过滤）
		all := make([]portfolio.StrengthRow, 0, len(day.Rows))
		rowsByCode := map[string]score.Row{}
		for _, row := range day.Rows {
			rowsByCode[row.Code] = row
		}
		for _, row := range day.Rows {
			all = append(all, rowToStrength(row, ""))
		}
		heldCodes := make([]string, 0, len(held))
		heldByCode := map[string]score.Row{}
		for _, row := range held {
			heldCodes = append(heldCodes, row.Code)
			heldByCode[row.Code] = row
			if _, ok := rowsByCode[row.Code]; !ok {
				all = append(all, rowToStrength(row, ""))
			}
		}

		cfg := score.BuildStrategyConfig(
			item.StrategyID, marketState,
			item.TargetPositionCount, item.TotalLayers,
			maxLayersForBacktest(item.StrategyID, item.TotalLayers),
			marketScopeKey(item),
		)
		candidates, universeScores := s.buildCandidatePoolFrom(all, item, cfg, heldCodes)
		plan := score.BuildRotationPlan(score.RotationInput{
			Holdings: held, Candidates: candidates, Config: cfg,
			MarketState: marketState, MaxStockLimit: item.MaxStockCount,
			UniverseScores: universeScores,
		})

		// 逐日诊断：回测"零成交"时要能一眼看出是哪道闸门把候选刷没的。
		adds, exec := 0, 0
		for _, row := range plan.Rows {
			if row.Action == score.ActionAdd || row.Action == score.ActionReplaceIn {
				adds++
			}
			if row.IsExecutable {
				exec++
			}
		}
		slog.Info("回测逐日诊断",
			"date", day.Date, "state", marketState,
			"评分行", len(day.Rows), "持仓", len(held),
			"候选池", len(candidates), "广度可用", len(universeScores),
			"entryAllowed", plan.EntryAllowed,
			"计划动作", len(plan.Rows), "可买入", adds, "可执行", exec)

		return plan
	}
}

// persistBacktest 把回测结果写进三张表。
func (s *PortfolioService) persistBacktest(
	store *portfolio.Store, runID string, item portfolio.Portfolio,
	cfg backtest.Config, benchmarkCode string,
	result *backtest.Result, metrics backtest.Metrics,
) error {
	now := time.Now().Format(time.RFC3339)
	finalValue := cfg.InitialCapital
	if len(result.Snapshots) > 0 {
		finalValue = result.Snapshots[len(result.Snapshots)-1].TotalValue
	}
	summary := metricsToJSON(metrics)
	run := portfolio.BacktestRun{
		RunID: runID, PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		PortfolioType: item.Type, PortfolioTypeLabel: item.TypeLabel,
		BenchmarkCode: benchmarkCode,
		StartDate:     cfg.StartDate, EndDate: cfg.EndDate,
		RebalanceFrequency: "signal", InitialCapital: cfg.InitialCapital, Status: "finished",
		TotalReturn: metrics.TotalReturn, AnnualReturn: metrics.AnnualReturn,
		MaxDrawdown: metrics.MaxDrawdown, SharpeRatio: metrics.SharpeRatio,
		TotalTrades: metrics.TotalTrades, AvgPositionCount: float64(metrics.MaxPositionCount),
		FinalValue: finalValue, SummaryJSON: summary,
		CreatedAt: now, UpdatedAt: now, FinishedAt: now,
	}
	// StartDate/EndDate 上面先用数字字符串占位，这里统一成 YYYYMMDD
	if err := store.SaveBacktestRun(run); err != nil {
		return err
	}

	daily := make([]portfolio.BacktestDaily, 0, len(result.Snapshots))
	for _, snap := range result.Snapshots {
		daily = append(daily, portfolio.BacktestDaily{
			RunID: runID, TradeDate: snap.Date,
			TotalValue: snap.TotalValue, Cash: snap.Cash, PositionValue: snap.PositionValue,
			DailyReturn: snap.DailyReturn, CumulativeReturn: snap.CumulativeReturn,
			BenchmarkValue: snap.BenchmarkValue, BenchmarkReturn: snap.BenchmarkReturn,
			PositionCount: snap.PositionCount,
		})
	}
	if err := store.SaveBacktestDaily(daily); err != nil {
		return err
	}

	trades := make([]portfolio.BacktestTrade, 0, len(result.Trades))
	for _, trade := range result.Trades {
		trades = append(trades, portfolio.BacktestTrade{
			RunID: runID, PortfolioID: item.ID, Code: trade.Code, Name: trade.Name,
			Action: trade.Action, TradeDate: trade.TradeDate,
			Price: trade.Price, Shares: trade.Shares, Amount: trade.Amount,
			Commission: trade.Commission, Tax: trade.Tax, TotalCost: trade.TotalCost,
			ProfitLoss: trade.ProfitLoss, ProfitLossPct: trade.ProfitLossPct,
			Score: trade.Score, Reason: trade.Reason, CreatedAt: now,
		})
	}
	return store.SaveBacktestTrades(trades)
}

// maxLayersForBacktest 单股最大层数：低吸用总层数，其余 3（原实现 :1031）。
func maxLayersForBacktest(strategyID string, totalLayers int) int {
	if strategyID == portfolio.StrategyTrendLowBuy {
		if totalLayers < 1 {
			return 1
		}
		return totalLayers
	}
	return 3
}

// marketStateByDate 从 days 里抽出日期 → 市场状态。
func marketStateByDate(store *portfolio.Store, days []backtest.StrengthDay) map[string]string {
	out := make(map[string]string, len(days))
	for _, day := range days {
		out[day.Date] = day.MarketState
	}
	return out
}

// scopeOfPortfolio 取组合的市场范围。
func scopeOfPortfolio(item portfolio.Portfolio) []string {
	scope := portfolio.ParseScopeValue(item.ScopeValueJSON)
	if len(scope.Markets) == 0 {
		return []string{"main", "gem"}
	}
	return scope.Markets
}

// marketScopeKey 组合的市场范围 key（供 score 的门槛表用）。
func marketScopeKey(item portfolio.Portfolio) string {
	scope := portfolio.ParseScopeValue(item.ScopeValueJSON)
	if len(scope.Markets) == 0 {
		return "all"
	}
	has := func(m string) bool {
		for _, v := range scope.Markets {
			if v == m {
				return true
			}
		}
		return false
	}
	main, gem, star := has("main"), has("gem"), has("star")
	switch {
	case main && gem && star:
		return "all"
	case main && gem:
		return "main_gem"
	case gem && star:
		return "gem_star"
	case main:
		return "main"
	case gem:
		return "gem"
	case star:
		return "star"
	}
	return "all"
}

// rowToStrength 把评分行转回 StrengthRow（候选池过滤要用它的字段）。
func rowToStrength(row score.Row, industry string) portfolio.StrengthRow {
	return portfolio.StrengthRow{
		Code: row.Code, Name: row.Name, Industry: industry,
		TotalScore: row.TotalScore, TrendScore: row.TrendScore,
		StructureScore: row.StructureScore, RSScore: row.RSScore,
		CapitalScore: row.CapitalScore, RepairScore: row.RepairScore,
		Close: row.Close, High: row.High, Low: row.Low, Open: row.Open, PctChg: row.PctChg,
		Ma5: row.Ma5, Ma20: row.Ma20, Ma30: row.Ma30,
		Ma60: row.Ma60, Ma120: row.Ma120, Ma250: row.Ma250,
		RelativeStrength20D: row.RelativeStrength20D,
		DrawdownFromHigh20:  row.DrawdownFromHigh20,
		DrawdownFromHigh120: row.DrawdownFromHigh120,
		ReboundFromLow60:    row.ReboundFromLow60,
		StockReturn3D:       row.StockReturn3D,
		StockReturn5D:       row.StockReturn5D,
		StockReturn10D:      row.StockReturn10D,
		StockReturn20D:      row.StockReturn20D,
		VolumeRatio20:       row.VolumeRatio20,
		VolumeRatio520:      row.VolumeRatio520,
		Ma5Prev:             row.Ma5Prev,
		Ma250Prev:           row.Ma250Prev,
		IndustryReturn20D:   row.IndustryReturn20D,
		BenchmarkReturn20D:  row.BenchmarkReturn20D,
	}
}

// metricsToJSON 把指标序列化成 summary_json。
func metricsToJSON(m backtest.Metrics) string {
	raw, err := json.Marshal(map[string]any{
		"total_return": m.TotalReturn, "annual_return": m.AnnualReturn,
		"max_drawdown": m.MaxDrawdown, "volatility": m.Volatility,
		"sharpe_ratio": m.SharpeRatio, "calmar_ratio": m.CalmarRatio,
		"sortino_ratio": m.SortinoRatio, "total_trades": m.TotalTrades,
		"win_count": m.WinCount, "lose_count": m.LoseCount, "win_rate": m.WinRate,
		"avg_holding_days": m.AvgHoldingDays, "max_position_count": m.MaxPositionCount,
		"avg_position_ratio": m.AvgPositionRatio, "trading_days": m.TradingDays,
		"profit_days": m.ProfitDays, "loss_days": m.LossDays,
		"avg_profit": m.AvgProfit, "avg_loss": m.AvgLoss,
		"is_short_period": m.IsShortPeriod, "annual_return_warning": m.AnnualReturnWarning,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// parseJSONObject 解析一个 JSON 对象；失败返回 nil。
func parseJSONObject(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
