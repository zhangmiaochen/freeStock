package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
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
func (s *PortfolioService) CreateBacktest(req BacktestRequest) (runResult *BacktestRunResult, err error) {
	// ⚠ **失败时把当时的事实记下来**。
	//
	// 之前我把诊断挂在 `portfolio.Open` 上 —— 但 `s.db()` 会**缓存** store，
	// 开库全程只调一次。所以只要失败发生在之后某次查询/写入上，
	// 那个诊断就永远不会触发（用户白报了几次错，日志里什么都没有）。
	//
	// 这里用 defer 兜住整个回测过程：无论哪一步失败，
	// 都会带上当时的**进程句柄数**与 goroutine 数 —— 用来判断
	// `SQLITE_CANTOPEN` 是不是 `open()` 返回 EMFILE 造成的。
	// 失败时把事实**直接拼进错误信息**，而不是只写日志。
	//
	// 之前失败信息只进日志，但界面上看到的是错误消息本体，用户未必找得到日志文件 ——
	// 结果连着报了几次错，诊断数据一次也没拿到。拼进错误里就没这个问题。
	defer func() {
		if err != nil {
			handles := openHandleCount()
			goroutines := runtime.NumGoroutine()
			logged := fmt.Errorf("%w｜诊断：openHandles=%d goroutines=%d 区间=%d~%d",
				err, handles, goroutines, req.StartDate, req.EndDate)
			slog.Error("回测失败", "err", err,
				"openHandles", handles, "goroutines", goroutines,
				"startDate", req.StartDate, "endDate", req.EndDate,
				"portfolioId", req.PortfolioID)
			err = logged
		}
	}()
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
	//
	// ⚠ 下面这些阶段计时是**为了定位"回测很慢"**加的：单看总耗时分不清
	// 是取评分、贴信号、拉日线、还是引擎逐日跑计划慢。跑一次看日志即可。
	phaseBegan := time.Now()
	strengths, err := store.LoadStrengthRange(item.StrategyID, int64(req.StartDate), int64(req.EndDate))
	if err != nil {
		return nil, err
	}
	if len(strengths) == 0 {
		return nil, fmt.Errorf("回测区间内没有历史评分数据，请先执行历史回填")
	}
	loadStrengthElapsed := time.Since(phaseBegan)

	phaseBegan = time.Now()
	days, codes, scopeErrors := s.buildStrengthDays(store, strengths, item)
	if len(days) == 0 {
		return nil, fmt.Errorf("回测区间内没有可用的评分交易日")
	}
	buildDaysElapsed := time.Since(phaseBegan)

	slog.Info("回测阶段耗时（前半）",
		"评分区间", fmt.Sprintf("%d~%d", req.StartDate, req.EndDate),
		"评分行数", len(strengths), "交易日", len(days), "代码数", len(codes),
		"读评分", loadStrengthElapsed.Round(time.Millisecond),
		"组装评分日", buildDaysElapsed.Round(time.Millisecond))

	// ①.5 **按整个区间贴一次信号**（原版 portfolio_service.py:1004
	// `scope_history = self._attach_signal_candidates(scope_history, start_date, end_date)`
	// 就在回测路径上）。
	//
	// ⚠ 这一步曾经整块缺失：回测把 `nil` 传给候选池 → 每一行的
	// `SignalType/SignalAge/SignalScore` 恒为空/0，于是
	//   - 熊市闸门（`candidate.go` 的 `HasSignalSource=true` 分支）把
	//     **每一只**候选都判成"无新鲜信号"而拒 → 整段熊市零成交、净值一条直线；
	//   - `signal_bonus` 恒为 0 → 该放宽 2 分的候选被拒；
	//   - 候选排序的第 2/3/4 号键（signal_priority / signal_score / signal_age）
	//     全相等 → 平局时选出的票与原版不同；
	//   - `is_strong_stock_for_layering` 的信号分支走不到 → 少加一层。
	//
	// 一次取整个区间的候选 + 日历，**按 20 天分块**贴（分块而不是整段摊平、
	// 也不是逐日，理由见 `attachSignalsToDays` 的注释）。
	attachBegan := time.Now()
	days = s.attachSignalsToDays(store, days)
	attachedRows := 0
	for _, day := range days {
		attachedRows += len(day.Rows)
	}
	slog.Info("回测阶段耗时（贴信号）",
		"交易日", len(days), "行数", attachedRows,
		"elapsed", time.Since(attachBegan).Round(time.Millisecond))

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
	// ⚠ 初始资金要**在这里就定下来**，不能等到算指标时再回落默认值。
	//
	// `backtest.New` 内部会对 cfg 副本补默认值（<1 → 100000），
	// 但外层的 cfg 仍是原值 —— 于是 `persistBacktest` 把
	// `initial_capital=0`、`final_value=0` 写进 `portfolio_backtest_run`，
	// 回测记录页显示"初始资金 0 / 期末 0"。引擎与指标本身是对的，
	// 只有落库这一行是错的。
	initialCapital := req.InitialCapital
	if initialCapital < 1 {
		initialCapital = 100000
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
		InitialCapital:    initialCapital,
		MarketScope:       scopeOfPortfolio(item),
	}

	engine := backtest.New(cfg, days, bars, benchmark)
	// ★ 低吸的两道闸门必须**逐日**喂进去（广度就绪数 + 指数闸门）。
	//
	// 不喂的后果（此前就是这样）：`score.BuildRotationPlan` 拿到
	// `LowBuyBreadthReadyCount=0`、`LowBuyIndex` 零值 →
	//   - `lowBuyForceClear = 0 < 900` 恒真 → 每天把持仓全部清仓
	//   - `LowBuyDynamicTargetCount(0,…) = 0` → 目标只数 0
	//   - 广度/指数闸门都不过 → `canAddNewPosition=false`
	// 于是**低吸回测从纯现金起步必然 0 成交、净值一条直线** ——
	// 这正是"回测结果一直是 0"。
	//
	// 口径与原实现一致：广度就绪数是**全市场**（当日该策略的全部评分行）里
	// `close>MA60 且 MA20>MA60` 的只数；指数闸门同日同值。
	indexGates := map[string]score.LowBuyIndexGate{}
	if item.StrategyID == portfolio.StrategyTrendLowBuy {
		dates := make([]string, 0, len(days))
		for _, day := range days {
			dates = append(dates, day.Date)
		}
		if s.stock != nil {
			indexGates = s.stock.lowBuyIndexGatesForRange(dates)
		}
	}
	buildPlan := s.backtestPlanBuilder(item, store, marketStateByDate(store, days), targetCount, maxStocks, indexGates)
	// ★ 评分日与价格日**必须有交集**，否则整个回测一条计划都产不出来。
	//
	// 原实现直接抛错（portfolio_service.py:1011-1018
	// "回测区间内评分结果为空，无法执行无未来函数回测"）。
	// 此前 Go 只在"一根 K 线都没有"时报错 —— 评分日与价格日错开时
	// 会**安静地跑完**，落一条 `status=finished`、总收益 0、零成交的记录，
	// 界面上看起来"回测成功但结果是 0"，完全看不出是数据没对上。
	if overlap := strengthPriceOverlap(days, bars); overlap == 0 {
		return nil, fmt.Errorf(
			"回测区间内评分日与日线价格没有重合的交易日（评分 %d 天、价格 %d 天，区间 %d~%d）——"+
				"请先对同一区间执行历史回填，或确认数据源的交易日历一致",
			len(days), distinctBarDates(bars), req.StartDate, req.EndDate)
	}
	// 引擎逐日跑计划 —— 这一步**过去没有任何日志**，250 天跑起来界面上
	// 就是"卡住不动"，分不清是在跑还是死了。所以在这里按天打印进度与单日耗时，
	// 既能看进度，也能一眼定位"是不是某一天的计划特别慢"。
	runBegan := time.Now()
	plansBuilt := 0
	innerBuildPlan := buildPlan
	if innerBuildPlan != nil {
		innerBuildPlan = func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
			dayBegan := time.Now()
			out := buildPlan(held, day)
			plansBuilt++
			if elapsed := time.Since(dayBegan); elapsed > 3*time.Second {
				slog.Warn("回测单日计划耗时偏长",
					"tradeDate", day.Date, "rows", len(day.Rows),
					"elapsed", elapsed.Round(time.Millisecond))
			}
			if plansBuilt%25 == 0 {
				total := time.Since(runBegan)
				slog.Info("回测进度",
					"已跑交易日", plansBuilt, "共", len(days),
					"累计", total.Round(time.Second),
					"单日均值", (total / time.Duration(plansBuilt)).Round(time.Millisecond))
			}
			return out
		}
	}
	result, err := engine.Run(innerBuildPlan)
	if err != nil {
		return nil, fmt.Errorf("回测执行失败：%w", err)
	}
	slog.Info("回测引擎跑完",
		"交易日", len(days), "计划次数", plansBuilt,
		"elapsed", time.Since(runBegan).Round(time.Millisecond),
		"单日均值", func() time.Duration {
			if plansBuilt == 0 {
				return 0
			}
			return (time.Since(runBegan) / time.Duration(plansBuilt)).Round(time.Millisecond)
		}())
	if len(result.Snapshots) == 0 {
		return nil, fmt.Errorf(
			"回测区间内没有可用的交易日（区间 %d~%d），没有产生任何净值曲线", req.StartDate, req.EndDate)
	}
	// 初始资金已在组装 cfg 时定好（回落到 100000），这里直接用 ——
	// 引擎与指标、落库三处必须是同一个数。
	metrics := backtest.CalculateMetrics(result.Snapshots, result.Trades, cfg.InitialCapital)
	metrics.ScopeWarning = s.backtestScopeWarning(codes, len(result.Trades), days)

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
func (s *PortfolioService) GetBacktestPayload(runID string) (payload *BacktestPayload, err error) {
	// 失败时带上诊断事实（同 CreateBacktest）—— 回测页会调多个后端方法，
	// 任何一个失败在界面上都是同一句 unable to open database file，
	// 必须让错误自己说清是哪一步、当时句柄多少。
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w｜诊断：%s openHandles=%d goroutines=%d",
				err, "GetBacktestPayload", openHandleCount(), runtime.NumGoroutine())
		}
	}()
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
func (s *PortfolioService) ListBacktests(portfolioID string) (runs []portfolio.BacktestRun, err error) {
	// 失败时带上诊断事实（同 CreateBacktest）—— 回测页会调多个后端方法，
	// 任何一个失败在界面上都是同一句 unable to open database file，
	// 必须让错误自己说清是哪一步、当时句柄多少。
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w｜诊断：%s openHandles=%d goroutines=%d",
				err, "ListBacktests", openHandleCount(), runtime.NumGoroutine())
		}
	}()
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
// **同时按组合范围过滤** —— 原实现是先 `_scope_filter` 再交给引擎的
// （portfolio_service.py:1001 `scope_history = self._scope_filter(scope_history, portfolio)`），
// 引擎的 `_held_score_map` 就建在这个过滤后的帧上
// （portfolio_backtest_engine.py:86-92）。所以：
//   - 范围外的持仓在引擎里"左连接落空"→ 评分/现价记 0 → 被判弱持仓清仓/换出
//   - 广度帧 `universe_count` 也只数范围**内**的票
//
// 范围外的行被剔掉后，如果某天一只都不剩，这一天就**不产出计划**
// （等价于原实现 `daily_strength.empty` → `pending_plan = None`）。
//
// 同时收集"有价格但没评分"的日期，供调用方记日志 ——
// 那些日期回测不会生成新计划（原实现同样要求评分日与价格日都有）。
func (s *PortfolioService) buildStrengthDays(
	store *portfolio.Store, strengths []portfolio.StrengthRow, pf portfolio.Portfolio,
) ([]backtest.StrengthDay, []string, []string) {
	byDate := map[string][]score.Row{}
	marketByDate := map[string]string{}
	var order []string
	matcher := s.scopeMatcher()
	// 范围值解析提到循环外。
	//
	// ⚠ 更正一条**我写错的归因**：我一度把"组装评分日"测得 380ms/1485 行
	// 说成是"每行一次 JSON 反序列化、外推 1 年要 5 分钟"。**那是算错了** ——
	// 把 380ms 当成了按行成本。实测把 `ParseScopeValue` 提到循环外之后，
	// 这一项**仍然是 382ms**：这 380ms 是 `s.scopeMatcher()` 首次构建匹配器
	//（加载行业/成员数据集）的**一次性**开销，与行数无关。
	//
	// 所以这次提前解析只是顺手去掉 125 万次 JSON 反序列化（真实但不大的收益），
	// **不是**"回测慢"的根因。真正定位靠下面的分阶段计时。
	scope := portfolio.ParseScopeValue(pf.ScopeValueJSON)

	for _, row := range strengths {
		if !inScopeFieldsParsed(row.Code, row.Industry, row.Market, pf.ScopeType, scope, matcher) {
			continue
		}
		date := fmt.Sprintf("%d", row.TradeDate)
		if _, seen := byDate[date]; !seen {
			order = append(order, date)
			if state, err := store.LoadMarketState(row.TradeDate); err == nil {
				marketByDate[date] = state.State
			}
		}
		byDate[date] = append(byDate[date], strengthToRow(row))
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
//
// targetCount / maxStocks 传的是**回测请求生效后**的数值（请求可覆盖组合配置），
// 原实现同样把生效值传进 `build_strategy_config` 与 `build_rotation_plan`
// （portfolio_backtest_engine.py:436-437,555-565）。传组合行里的原始值会让
// `ready_score` / `max_replace_count` / 期望只数 / 开仓上限与引擎的买入上限不一致。
//
// indexGates 是逐日的低吸指数闸门（按交易日 YYYYMMDD 索引）。
func (s *PortfolioService) backtestPlanBuilder(
	item portfolio.Portfolio, store *portfolio.Store, states map[string]string,
	targetCount, maxStocks int, indexGates map[string]score.LowBuyIndexGate,
) backtest.PlanBuilder {
	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		marketState := day.MarketState
		// 当日评分行**直接进候选池**。
		//
		// ⚠ 这里原来绕了一趟 `rowToStrength(row, "")`，有两个静默后果：
		//  1. `Industry` 被写成空串 → 行业范围从"单值 OR 成员表**并集**"
		//     退化成"只靠成员表"（严格子集）。而 `buildStrengthDays` 已经用
		//     真实行业放行过一遍 → 同一天先放行、又被内层砍掉，两层口径不一致；
		//  2. 整套 `active_signal_*` 被清零 → 熊市闸门把**每一只**候选都判成
		//     "无新鲜信号"而拒（`candidate.go` 的 `HasSignalSource=true` 分支）。
		//     原版回测会贴信号（portfolio_service.py:1004），所以这是实打实的差异。
		// `day.Rows` 自身的信号列由 `CreateBacktest` 按区间预贴，见那里。
		all := day.Rows
		// ⚠ **持仓但当天没评分的行不进 `all`**。
		//
		// 它们本来就会被"剔除已持仓"过滤掉（`buildCandidatePoolFrom` 的 held 集合），
		// 所以对候选池毫无影响；唯一的副作用是**把 0 分塞进广度帧**，
		// 把 `universe_count` 抬高（能翻转 `universe_count >= 100` 的启用判定）。
		// 原实现的广度帧就是当日的评分帧（portfolio_rotation_logic.py:1749），
		// 不含这类行。
		heldCodes := make([]string, 0, len(held))
		for _, row := range held {
			heldCodes = append(heldCodes, row.Code)
		}

		// 低吸广度就绪数：**逐日**在当日评分行上数，口径与
		// `Store.LowBuyBreadthReadyCount` 的 SQL 逐字一致。
		// 原实现把它挂在每一行上（portfolio_rotation_logic.py:2044），
		// 所以候选池过滤（dd20 档位）与建仓额度都要用它 ——
		// **必须在建候选池之前写进 cfg**。
		breadthReady := lowBuyBreadthReadyCountOf(day.Rows)

		cfg := score.BuildStrategyConfig(
			item.StrategyID, marketState,
			targetCount, item.TotalLayers,
			maxLayersForBacktest(item.StrategyID, item.TotalLayers),
			marketScopeKey(item),
		)
		cfg.LowBuyBreadthReadyCount = breadthReady
		poolBegan := time.Now()
		candidates, universeScores, _ := s.buildCandidatePoolFrom(nil, all, item, cfg, heldCodes)
		poolElapsed := time.Since(poolBegan)
		slog.Info("回测候选池完成",
			"date", day.Date, "持仓", len(held),
			"评分行", len(all), "候选池", len(candidates),
			"低吸广度就绪", breadthReady,
			"耗时", poolElapsed.Round(time.Millisecond))

		planBegan := time.Now()
		plan := score.BuildRotationPlan(score.RotationInput{
			Holdings: held, Candidates: candidates, Config: cfg,
			MarketState: marketState,
			// 与原实现 :2492 一致：max_stock_count = max(target_count, max_stock_count)
			MaxStockLimit:  maxInt(maxStocks, targetCount),
			UniverseScores: universeScores,
			// 低吸的两道闸门 —— 缺了它们低吸回测必然零成交
			LowBuyBreadthReadyCount: breadthReady,
			LowBuyIndex:             indexGates[day.Date],
		})
		planElapsed := time.Since(planBegan)

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
		gate := indexGates[day.Date]
		breadthOK := score.LowBuyBreadthEntryAllowed(breadthReady, cfg.LowBuyPositionMode)
		indexOK := score.LowBuyIndexEntryAllowed(gate, breadthReady)
		slog.Info("回测逐日诊断",
			"date", day.Date, "state", marketState,
			"评分行", len(day.Rows), "持仓", len(held),
			"候选池", len(candidates), "广度可用", len(universeScores),
			"entryAllowed", plan.EntryAllowed,
			"低吸广度就绪", breadthReady, "低吸广度放行", breadthOK,
			"低吸指数就绪", gate.Ready, "低吸指数放行", indexOK,
			"指数收盘", gate.Close, "指数MA20", gate.Ma20, "指数MA60", gate.Ma60,
			"计划动作", len(plan.Rows), "可买入", adds, "可执行", exec,
			"无建议原因", plan.NoActionReasons,
			"计划耗时", planElapsed.Round(time.Millisecond))

		return plan
	}
}

// backtestScopeWarning 在"回填范围远小于全市场"时给出警告。
//
// **为什么必须提示**：广度闸门（`market_breadth_status`）判的是
// "**全市场**里评分 ≥70 的股票数 ≥ 20"。回填只覆盖一部分时，
// 这个数被系统性低估 → 闸门恒为"广度不足" → 所有 `add`/`replace_in` 行被删光
// → 从纯现金起步的回测**一笔都不成交、净值是一条直线**。
//
// 实测（2026-10-09，本机 stockdb）：回填 799 只时，四个交易日的
// `entryAllowed` 全是 false、候选池 58~73 只却一条动作都产不出来；
// 放到 2500 只时 `entryAllowed` 翻成 true 并产出 5 条可买入行。
//
// ⚠ 这里**不修改**闸门算法 —— 原版就是拿手里那份评分帧算的，
// 改了才是偏离。只把"样本不够"这件事明确讲出来。
func (s *PortfolioService) backtestScopeWarning(
	codes []string, trades int, days []backtest.StrengthDay,
) string {
	// ---- ① 评分表"每天只覆盖一小段"的检测：各日行数差异悬殊 ----
	//
	// 判据是**每天的行数**，不是"有没有被删"。实测本机库的形态：
	//   健康日 4517 行（覆盖 000/001/002/003/300/301/302/600/601/603/605/688/689/920）
	//   异常日 1000 行，且**只有 000/001/002** —— 正是"代码序前 1000 只"
	//
	// 成因有两类，都真实存在（两处都已修，但**已落库的旧数据要重算才恢复**）：
	//   ① 「回填股票数」填了非 0：旧版取的是代码序前 N 只 → 只覆盖深市主板
	//   ② 增量刷新的落库曾是整分区替换 → 把当天其余行删掉
	//
	// 危害不是"少几只票"，而是**范围整体偏了**：低吸广度按"全市场
	// close>MA60 且 MA20>MA60 的只数"统计，样本偏了就会恒判「广度不足 < 900」
	// → 清仓 + 禁止开新仓 → **回测恒为 0 成交**。
	thinWarning := ""
	if minRows, maxRows, thinDays := strengthRowSpread(days); maxRows > 0 && thinDays > 0 {
		thinWarning = fmt.Sprintf(
			"评分表里有 %d 个交易日明显只覆盖了一部分股票（最少 %d 行，最多 %d 行）——"+
				"常见成因是「回填股票数」填了非 0（旧版取的是**代码序前 N 只**，"+
				"往往只覆盖到 000/001/002 这一段），或早期增量落库把当天其余行删掉了。"+
				"广度类判定按**全市场**统计，样本偏了会恒判「广度不足」→ 清仓且不开新仓 → 回测零成交。"+
				"请把「回填股票数」填 0（全市场）并对区间执行一次「全量重算区间」。",
			thinDays, minRows, maxRows)
	}

	if s.stock == nil || len(codes) == 0 {
		return thinWarning
	}
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil || universe == nil || len(universe.Stocks) == 0 {
		return thinWarning
	}
	market := 0
	for _, item := range universe.Stocks {
		if item.HasQuote {
			market++
		}
	}
	if market == 0 || len(codes) >= market {
		return thinWarning
	}
	ratio := float64(len(codes)) / float64(market)
	// 覆盖到一半以上就不提示了（广度指标的偏差已不显著）
	if ratio >= 0.5 {
		return thinWarning
	}
	msg := fmt.Sprintf(
		"回填只覆盖 %d / %d 只（%.0f%%）—— 广度闸门按**全市场**统计「评分≥70 的只数」，"+
			"样本不足会让它一直判「广度不足」，从而删掉所有买入信号。",
		len(codes), market, ratio*100)
	if trades == 0 {
		msg += "本次回测一笔都没成交，很可能就是这个原因；请把「回填股票数」填 0（全市场）后重跑。"
	} else {
		msg += "本次虽有成交，但结果仍可能偏保守；建议用全市场回填后复核。"
	}
	if thinWarning != "" {
		return thinWarning + " " + msg
	}
	return msg
}

// strengthRowSpread 看各交易日的评分行数是否**参差不齐**。
//
// 返回 (最少行数, 最多行数, "明显偏少"的天数)。判据用最多行的 60%：
// 正常的行数波动（新股上市、停牌）远小于增量写入造成的量级差异。
func strengthRowSpread(days []backtest.StrengthDay) (int, int, int) {
	if len(days) == 0 {
		return 0, 0, 0
	}
	minRows, maxRows := -1, 0
	for _, day := range days {
		n := len(day.Rows)
		if minRows < 0 || n < minRows {
			minRows = n
		}
		if n > maxRows {
			maxRows = n
		}
	}
	if minRows < 0 || maxRows <= 0 {
		return 0, 0, 0
	}
	floor := int(float64(maxRows) * 0.6)
	thin := 0
	for _, day := range days {
		if len(day.Rows) < floor {
			thin++
		}
	}
	if thin == 0 {
		return minRows, maxRows, 0
	}
	return minRows, maxRows, thin
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
	summary := metricsToJSON(metrics, result.Trades)
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
//
// ⚠ **必须逐字段镜像 `strengthToRow`（plan.go）**。它是它的逆变换，
// 少抄一个字段就会在回测里静默改变行为 —— 这里曾经漏了两个：
//
//   - `Market`：漏了就是空串 → `MarketInScope("")` 恒 false →
//     只要组合设了市场范围（界面默认就是「主板+创业板」），
//     **候选池与广度帧全为空** → 回测零成交、净值一条直线。
//   - `Amount`：低吸候选是**按成交额**排入场顺序的
//     （`prioritizeLowBuy` 的 amount_entry / deep_amount / board_norm_amount），
//     漏了就是全部 0 → 排序退化成"按代码"，与原版选出的票完全不同。
func rowToStrength(row score.Row, industry string) portfolio.StrengthRow {
	return portfolio.StrengthRow{
		Code: row.Code, Name: row.Name, Industry: industry,
		// ⚠ `TradeDate` 也必须镜像：贴信号要靠它算 `signal_age`，
		// 漏了就是 0 → attach 的日期区间非法 → **静默不贴信号**。
		TradeDate:  row.TradeDate,
		Market:     row.Market,
		TotalScore: row.TotalScore, TrendScore: row.TrendScore,
		StructureScore: row.StructureScore, RSScore: row.RSScore,
		CapitalScore: row.CapitalScore, RepairScore: row.RepairScore,
		Close: row.Close, High: row.High, Low: row.Low, Open: row.Open, PctChg: row.PctChg,
		Amount: row.Amount,
		Ma5:    row.Ma5, Ma20: row.Ma20, Ma30: row.Ma30,
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
//
// ⚠ 两处必须对齐，否则界面上永远是"—"或空：
//  1. **相对基准的四项**（excess_return / beta / alpha / information_ratio）
//     在 `CalculateMetrics` 里算了，但此前根本没写进来 —— 前端与库里都看不到。
//  2. **scope_warning 用的是蛇形键**。`BacktestView.vue` 读的是
//     `metrics["scope_warning"]`，而 Go 字段的 json tag 是 `scopeWarning`，
//     两边都不匹配 → 那条"回填范围太小导致广度闸门误判"的提示**永远不显示**。
//     这里显式写 `scope_warning`（同时保留驼峰，谁读都不落空）。
//
// 另外把原实现里有、而 `Metrics` 结构体没有的成本项也补上
// （portfolio_service.py:1059-1068 写 total_commission / total_tax /
// total_transaction_cost 与费率），便于对账手续费。
func metricsToJSON(m backtest.Metrics, trades []backtest.Trade) string {
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
		"max_drawdown_duration": m.MaxDrawdownDuration,
		"max_profit":            m.MaxProfit, "max_loss": m.MaxLoss,
		// 相对基准（此前算了却没写进来）
		"excess_return": m.ExcessReturn, "beta": m.Beta,
		"alpha": m.Alpha, "information_ratio": m.InformationRatio,
		"is_short_period": m.IsShortPeriod, "annual_return_warning": m.AnnualReturnWarning,
		// 成本项（原实现 :1059-1068）
		"total_commission":       totalCommission(trades),
		"total_tax":              totalTax(trades),
		"total_transaction_cost": round4(totalCommission(trades) + totalTax(trades)),
		// 回填范围过小的提示 —— **蛇形键**，前端读的就是这个
		"scope_warning": m.ScopeWarning,
		"scopeWarning":  m.ScopeWarning,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// strengthPriceOverlap 数"既是评分日、又有日线价格"的交易日个数。
//
// 原实现（portfolio_service.py:1011-1018）在交集为空时直接抛
// "回测区间内评分结果为空，无法执行无未来函数回测" ——
// 这里先用它把"看起来成功、其实一条计划都产不出来"的情况挡掉。
func strengthPriceOverlap(days []backtest.StrengthDay, bars []backtest.PriceBar) int {
	if len(days) == 0 || len(bars) == 0 {
		return 0
	}
	strengthDates := make(map[string]bool, len(days))
	for _, day := range days {
		strengthDates[day.Date] = true
	}
	priceDates := make(map[string]bool, len(bars))
	for _, bar := range bars {
		priceDates[bar.Date] = true
	}
	overlap := 0
	for date := range strengthDates {
		if priceDates[date] {
			overlap++
		}
	}
	return overlap
}

// distinctBarDates 数不同的价格交易日个数（只用于错误信息）。
func distinctBarDates(bars []backtest.PriceBar) int {
	seen := make(map[string]bool, len(bars))
	for _, bar := range bars {
		seen[bar.Date] = true
	}
	return len(seen)
}

// totalCommission / totalTax 汇总成交里的手续费与印花税
// （原实现 portfolio_service.py:1059-1060）。
func totalCommission(trades []backtest.Trade) float64 {
	sum := 0.0
	for _, trade := range trades {
		sum += trade.Commission
	}
	return round4(sum)
}

func totalTax(trades []backtest.Trade) float64 {
	sum := 0.0
	for _, trade := range trades {
		sum += trade.Tax
	}
	return round4(sum)
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
