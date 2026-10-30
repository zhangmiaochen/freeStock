package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
	"freestock/internal/qgrowth"
	"freestock/internal/score"
)

// 质量成长 · 趋势回调策略的编排层。
//
// 纯计算在 internal/qgrowth；这里负责：取数（日线 + PIT 财报 + 基准）、
// 逐日组装候选、调用打分、把目标名单转成 backtest 的调仓计划。

type qgBar struct {
	date   int
	open   float64
	close  float64
	amount float64
}

type qgFund struct {
	period   string
	announce int64
	roe      float64
	rev      float64
	np       float64
	gm       float64
	debt     float64
	ocf      float64
}

// loadQGrowthBars 取复权日线（与回测撮合同一口径），按代码分组、按日期升序。
func (s *PortfolioService) loadQGrowthBars(codes []string, start, end int) (map[string][]qgBar, error) {
	if s.stock == nil {
		return nil, fmt.Errorf("缺少股票服务依赖")
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return nil, fmt.Errorf("数据源未就绪")
	}
	type job struct{ code string }
	jobs := make(chan job)
	var (
		mu  sync.Mutex
		out = make(map[string][]qgBar, len(codes))
		wg  sync.WaitGroup
	)
	workers := scoreWorkers
	if workers > len(codes) {
		workers = len(codes)
	}
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				quotes, err := provider.DailyHistory(context.Background(), j.code, start, end)
				if err != nil || len(quotes) == 0 {
					continue
				}
				bars := make([]qgBar, 0, len(quotes))
				for _, q := range quotes {
					if q == nil || q.Close <= 0 {
						continue
					}
					bars = append(bars, qgBar{date: int(q.Date), open: q.Open, close: q.Close, amount: q.Amount})
				}
				if len(bars) == 0 {
					continue
				}
				sort.Slice(bars, func(a, b int) bool { return bars[a].date < bars[b].date })
				mu.Lock()
				out[j.code] = bars
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

// loadQGrowthFundamentals 读取 PIT 财报指标，按代码分组、按公告日升序。
func (s *PortfolioService) loadQGrowthFundamentals(startYear, endYear int) (map[string][]qgFund, error) {
	lake := s.stock.lakeHandle()
	if lake == nil {
		return nil, fmt.Errorf("数据湖未就绪")
	}
	items := []string{
		"roe", "revenue_yoy", "net_profit_yoy", "gross_margin",
		"total_assets", "total_liabilities", "net_cash_operate", "net_profit",
	}
	rows, err := lake.AllStatementItems(items, startYear, endYear)
	if err != nil {
		return nil, err
	}
	// 先按 (symbol, report_period) 聚合，再拼成每期一条记录。
	type key struct{ sym, period string }
	agg := make(map[key]map[string]float64, len(rows)/6)
	ann := make(map[key]int64, len(rows)/6)
	for _, r := range rows {
		k := key{portfolio.BareCode(r.Symbol), r.ReportPeriod}
		m := agg[k]
		if m == nil {
			m = make(map[string]float64, 8)
			agg[k] = m
		}
		m[r.ItemCode] = r.ItemValue
		if r.AnnounceDate > ann[k] {
			ann[k] = r.AnnounceDate
		}
	}
	out := make(map[string][]qgFund, 6000)
	for k, m := range agg {
		q := int(k.period[len(k.period)-1] - '0')
		if q < 1 || q > 4 {
			continue
		}
		np := m["net_profit"]
		f := qgFund{
			period:   k.period,
			announce: ann[k],
			roe:      m["roe"] * 4 / float64(q),
			rev:      m["revenue_yoy"],
			np:       m["net_profit_yoy"],
			gm:       m["gross_margin"],
		}
		if ta := m["total_assets"]; ta > 0 {
			f.debt = m["total_liabilities"] / ta * 100
		} else {
			f.debt = 1e9 // 无资产数据视为不合格
		}
		if np > 0 {
			f.ocf = m["net_cash_operate"] / np
		} else {
			f.ocf = -1e9
		}
		out[k.sym] = append(out[k.sym], f)
	}
	for sym := range out {
		sort.Slice(out[sym], func(i, j int) bool {
			if out[sym][i].period != out[sym][j].period {
				return out[sym][i].period < out[sym][j].period
			}
			return out[sym][i].announce < out[sym][j].announce
		})
	}
	return out, nil
}

// latestFundamental 取"公告日 <= date 的最新报告期"。
func latestFundamental(list []qgFund, date int) (qgrowth.Fundamental, bool) {
	best := -1
	for i := range list {
		if list[i].announce > 0 && int(list[i].announce) <= date {
			if best < 0 || list[i].period > list[best].period {
				best = i
			}
		}
	}
	if best < 0 {
		return qgrowth.Fundamental{}, false
	}
	f := list[best]
	return qgrowth.Fundamental{
		ROE: f.roe, RevYoY: f.rev, NPYoY: f.np, GrossMargin: f.gm,
		DebtRatio: f.debt, OCFRatio: f.ocf, Period: f.period, Valid: true,
	}, true
}

// buildQGrowthDays 逐交易日组装候选、打分，产出回测需要的 days + bars + 基准。
func (s *PortfolioService) buildQGrowthDays(
	start, end int, cfg qgrowth.Config,
) ([]backtest.StrengthDay, []backtest.PriceBar, map[string]float64, error) {
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil {
		return nil, nil, nil, err
	}
	codes := make([]string, 0, len(universe.Stocks))
	names := make(map[string]string, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if !isQGrowthCode(item.Code) {
			continue
		}
		codes = append(codes, item.Code)
		names[item.Code] = item.Name
	}
	barsByCode, err := s.loadQGrowthBars(codes, shiftBack(start, 400), end)
	if err != nil {
		return nil, nil, nil, err
	}
	funds, err := s.loadQGrowthFundamentals(start/10000-2, end/10000)
	if err != nil {
		return nil, nil, nil, err
	}
	// 上市日期：用数据湖 instruments 的 list_date（日历日近似 250 个交易日 = 365 天）
	listDate := map[string]int64{}
	if lake := s.stock.lakeHandle(); lake != nil {
		if instruments, err := lake.Instruments(); err == nil {
			for _, ins := range instruments {
				if ins.ListDate <= 0 {
					continue
				}
				// parquet date32（自 1970-01-01 起的天数）→ YYYYMMDD
				t := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(ins.ListDate))
				if v, err := strconv.Atoi(t.Format("20060102")); err == nil {
					listDate[portfolio.BareCode(ins.Symbol)] = int64(v)
				}
			}
		}
	}

	type dayCand struct {
		cands []qgrowth.Candidate
	}
	byDate := make(map[int]*dayCand, 4000)
	var priceBars []backtest.PriceBar

	for code, bars := range barsByCode {
		if len(bars) < cfg.TrendMA {
			continue
		}
		// 逐日滚动：MA200 / 20 日均额；20 日最高价用滑动窗口直接算
		var sum200, sum20 float64
		for i, b := range bars {
			sum200 += b.close
			if i >= cfg.TrendMA {
				sum200 -= bars[i-cfg.TrendMA].close
			}
			sum20 += b.amount
			if i >= 20 {
				sum20 -= bars[i-20].amount
			}
			if b.date >= start && b.date <= end {
				priceBars = append(priceBars, backtest.PriceBar{
					Date: strconv.Itoa(b.date), Code: code, Open: b.open, Close: b.close, Name: names[code],
				})
			}
			if b.date < start || b.date > end {
				continue
			}
			if i < cfg.TrendMA-1 || i < 19 {
				continue
			}
			max20 := 0.0
			for j := i - 19; j <= i; j++ {
				if bars[j].close > max20 {
					max20 = bars[j].close
				}
			}
			if max20 <= 0 {
				continue
			}
			ma200 := sum200 / float64(cfg.TrendMA)
			dd20 := (b.close/max20 - 1) * 100
			amt20 := sum20 / 20
			fund, ok := latestFundamental(funds[code], b.date)
			if !ok {
				continue
			}
			listed := cfg.MinListedDays // 默认视为老股
			if ld, ok := listDate[code]; ok && ld > 0 {
				if int64(b.date)-ld < 365 {
					listed = 0 // 上市不足一年
				}
			}
			cand := qgrowth.Candidate{
				Code: code, Name: names[code], Close: b.close, MA200: ma200,
				DD20: dd20, Amount20: amt20, ListedDays: listed, Fund: fund,
			}
			if !cfg.Eligible(cand) || !cfg.PullbackOK(cand) {
				continue
			}
			dc := byDate[b.date]
			if dc == nil {
				dc = &dayCand{}
				byDate[b.date] = dc
			}
			dc.cands = append(dc.cands, cand)
		}
	}

	// 交易日历（区间内全部有行情的日期），用于识别"每月最后一个交易日"
	calSet := make(map[int]bool, 600)
	for _, b := range priceBars {
		if d, err := strconv.Atoi(b.Date); err == nil {
			calSet[d] = true
		}
	}
	calendar := make([]int, 0, len(calSet))
	for d := range calSet {
		calendar = append(calendar, d)
	}
	sort.Ints(calendar)
	isMonthEnd := make(map[int]bool, 24)
	for i, d := range calendar {
		if i == len(calendar)-1 || d/100 != calendar[i+1]/100 {
			isMonthEnd[d] = true
		}
	}

	dates := make([]int, 0, len(byDate))
	for d := range byDate {
		if isMonthEnd[d] {
			dates = append(dates, d)
		}
	}
	// 没有候选的月末也要留一天（空名单 = 清仓）
	for d := range isMonthEnd {
		if byDate[d] == nil {
			dates = append(dates, d)
		}
	}
	sort.Ints(dates)

	days := make([]backtest.StrengthDay, 0, len(dates))
	for _, d := range dates {
		dc := byDate[d]
		var cands []qgrowth.Candidate
		if dc != nil {
			cands = dc.cands
		}
		qgrowth.Score(cands)
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].Score != cands[j].Score {
				return cands[i].Score > cands[j].Score
			}
			return cands[i].Code < cands[j].Code
		})
		limit := cfg.N
		if len(cands) < limit {
			limit = len(cands)
		}
		rows := make([]score.Row, 0, limit)
		for _, c := range cands[:limit] {
			rows = append(rows, score.Row{
				Code: c.Code, Name: c.Name, Close: c.Close,
				TotalScore: c.Score * 100,
			})
		}
		days = append(days, backtest.StrengthDay{Date: strconv.Itoa(d), Rows: rows})
	}

	// 固定行情顺序：barsByCode 是 map，遍历顺序随机；引擎虽然按 (date, code) 建索引，
	// 但重复行/边界值会让结果依赖顺序，这里统一按 (date, code) 排序。
	sort.Slice(priceBars, func(i, j int) bool {
		if priceBars[i].Date != priceBars[j].Date {
			return priceBars[i].Date < priceBars[j].Date
		}
		return priceBars[i].Code < priceBars[j].Code
	})

	benchmark := map[string]float64{}
	if closes, err := s.stock.loadBenchmark(score.LowBuyIndexBenchmarkCode, shiftBack(start, 400), end); err == nil {
		for date, close := range closes {
			benchmark[strconv.Itoa(int(date))] = close
		}
	}
	return days, priceBars, benchmark, nil
}

func isQGrowthCode(code string) bool {
	c := strings.TrimSpace(code)
	if len(c) < 6 {
		return false
	}
	head := c[:3]
	switch head {
	case "600", "601", "603", "605", "000", "001", "002", "003", "300", "301", "302", "688", "689":
		return true
	}
	return false
}

// qGrowthTargets 计算"目标层数"（1 层 = 1% 总资产）。
//
// **回测与实盘建议共用这一个函数**：同样的 rows + 同样的 expo，必须得到同样的层数。
func qGrowthTargets(rows []score.Row, cfg qgrowth.Config, expo float64) map[string]int {
	ws := qgrowth.WeightsFor(scoresOf(rows), cfg.MaxWeight)
	out := make(map[string]int, len(rows))
	for i, row := range rows {
		layers := int(ws[i]*100*expo + 0.5)
		if layers < 1 && ws[i] > 0 {
			layers = 1
		}
		if layers > 0 {
			out[row.Code] = layers
		}
	}
	return out
}

// qGrowthPlanRows 把"目标层数 vs 当前持仓"翻译成 RotationRow。
//
// **回测与实盘建议共用**：不在目标里的清仓，目标里的加/减/保持；
// 层数变化为 0 不出行，等价于 1% 免打扰带。
func qGrowthPlanRows(held []score.Row, rows []score.Row, target map[string]int) score.RotationPlan {
	plan := score.RotationPlan{EntryAllowed: true}
	// 固定卖出顺序，避免 map 迭代顺序导致现金顺序不同、进而成交笔数抖动
	held = append([]score.Row(nil), held...)
	sort.SliceStable(held, func(i, j int) bool { return held[i].Code < held[j].Code })
	heldByCode := make(map[string]score.Row, len(held))
	for _, h := range held {
		heldByCode[h.Code] = h
	}
	for _, h := range held {
		if _, ok := target[h.Code]; ok {
			continue
		}
		plan.Rows = append(plan.Rows, score.RotationRow{
			Code: h.Code, Name: h.Name, Action: score.ActionClear,
			CurrentLayers: h.PositionLayers, TargetLayers: 0,
			LayersDelta: -h.PositionLayers, SourceType: "holding",
			Reason: "不在本期质量成长名单", IsExecutable: true,
		})
	}
	for _, row := range rows {
		want, ok := target[row.Code]
		if !ok {
			continue
		}
		cur := 0
		if h, ok := heldByCode[row.Code]; ok {
			cur = h.PositionLayers
		}
		delta := want - cur
		if delta == 0 {
			continue
		}
		action := score.ActionAdd
		source := "candidate"
		reason := fmt.Sprintf("质量成长买入|评分%.2f", row.TotalScore)
		if cur > 0 {
			source = "holding"
			if delta > 0 {
				action = score.ActionAddLayer
				reason = fmt.Sprintf("加仓至目标权重|评分%.2f", row.TotalScore)
			} else {
				action = score.ActionReduce
				reason = fmt.Sprintf("减仓至目标权重|评分%.2f", row.TotalScore)
				delta = -delta
			}
		}
		plan.Rows = append(plan.Rows, score.RotationRow{
			Code: row.Code, Name: row.Name, Action: action,
			CurrentLayers: cur, TargetLayers: want, LayersDelta: delta,
			SourceType: source, Reason: reason, IsExecutable: true,
			Score: row.TotalScore,
		})
	}
	return plan
}

// qGrowthPlanBuilder 把"当日目标名单"转成调仓计划（逻辑见 qGrowthTargets / qGrowthPlanRows）。
func (s *PortfolioService) qGrowthPlanBuilder(
	item portfolio.Portfolio, days []backtest.StrengthDay,
	benchmark map[string]float64, cfg qgrowth.Config,
) backtest.PlanBuilder {
	// 目标层数按日期预先算好
	targets := make(map[string]map[string]int, len(days))
	benchMA := benchmarkMA200(benchmark)
	for _, day := range days {
		expo := 1.0
		if close, ok := benchmark[day.Date]; ok && benchMA[day.Date] > 0 && close < benchMA[day.Date] {
			expo = cfg.BearExposure
		}
		targets[day.Date] = qGrowthTargets(day.Rows, cfg, expo)
	}

	return func(held []score.Row, day backtest.StrengthDay) score.RotationPlan {
		return qGrowthPlanRows(held, day.Rows, targets[day.Date])
	}
}

func scoresOf(rows []score.Row) []float64 {
	out := make([]float64, len(rows))
	for i := range rows {
		out[i] = rows[i].TotalScore
	}
	return out
}

// buildQualityGrowthPlan 生成"最新交易日的质量成长建议"。
//
// 与回测共用同一套候选/打分/权重逻辑，区别只是：
//   - 只取最新一个交易日（不要求是月末）；
//   - 当前持仓用组合里记录的层数/权重，而不是回测的虚拟持仓。
func (s *PortfolioService) buildQualityGrowthPlan(item portfolio.Portfolio, store *portfolio.Store) (*PlanResult, error) {
	universe, err := s.stock.GetStockUniverse(false)
	if err != nil {
		return nil, err
	}
	latest := int(universe.TradeDate)
	if latest <= 0 {
		return nil, fmt.Errorf("没有可用的最新交易日")
	}
	return s.buildQualityGrowthPlanAt(item, store, latest)
}

// buildQualityGrowthPlanAt 是指定日期的版本（测试用：验证与回测同一天的结论一致）。
func (s *PortfolioService) buildQualityGrowthPlanAt(item portfolio.Portfolio, store *portfolio.Store, latest int) (*PlanResult, error) {
	cfg := qgrowth.DefaultConfig()

	days, priceBars, benchmark, err := s.buildQGrowthDays(latest, latest, cfg)
	if err != nil {
		return nil, fmt.Errorf("质量成长取数失败：%w", err)
	}
	var rows []score.Row
	if len(days) > 0 {
		rows = days[0].Rows
	}
	closeByCode := make(map[string]float64, len(priceBars))
	for _, b := range priceBars {
		closeByCode[b.Code] = b.Close
	}

	// 熊市减半
	benchMA := benchmarkMA200(benchmark)
	dateKey := strconv.Itoa(latest)
	bear := false
	expo := 1.0
	if c, ok := benchmark[dateKey]; ok && benchMA[dateKey] > 0 && c < benchMA[dateKey] {
		bear, expo = true, cfg.BearExposure
	}

	// 目标层数（1 层 = 1% 总资产）——与回测共用 qGrowthTargets
	target := qGrowthTargets(rows, cfg, expo)

	positions, err := store.ListPositions(item.ID)
	if err != nil {
		return nil, err
	}
	// 当前层数：优先用持仓权重（%），否则按组合总层数换算成 100 层制
	curLayers := func(p portfolio.Position) int {
		if p.PositionWeight > 0 {
			return int(p.PositionWeight + 0.5)
		}
		if item.TotalLayers > 0 {
			return int(float64(p.PositionLayers)*100/float64(item.TotalLayers) + 0.5)
		}
		return p.PositionLayers
	}

	holdingRows := make([]score.Row, 0, len(positions))
	scoreByCode := make(map[string]float64, len(rows))
	for _, r := range rows {
		scoreByCode[r.Code] = r.TotalScore
	}
	for _, p := range positions {
		cur := curLayers(p)
		holdingRows = append(holdingRows, score.Row{
			Code: p.Code, Name: p.Name, Close: closeByCode[p.Code],
			TotalScore: scoreByCode[p.Code], PositionLayers: cur, PositionWeight: float64(cur),
		})
	}
	// 计划行——与回测共用 qGrowthPlanRows
	plan := qGrowthPlanRows(holdingRows, rows, target)

	portfolioScore := 0.0
	if len(holdingRows) > 0 {
		sum := 0.0
		for _, h := range holdingRows {
			sum += h.TotalScore
		}
		portfolioScore = sum / float64(len(holdingRows))
	}
	health := "弱势"
	switch {
	case portfolioScore >= 85:
		health = "强势"
	case portfolioScore >= 72:
		health = "稳健"
	case portfolioScore >= 58:
		health = "观察"
	}

	state := score.MarketState{State: score.StateBull, Label: "多头"}
	if bear {
		state = score.MarketState{State: score.StateBear, Label: "空头"}
	}
	notes := []string{
		fmt.Sprintf("质量成长·趋势回调：基于 %d 最新快照；权重制（100 层，1 层 = 1%% 总资产），单票上限 15%%。", latest),
		fmt.Sprintf("目标持仓 %d 只，当前 %d 只；当日候选 %d 只。", cfg.N, len(positions), len(rows)),
	}
	if bear {
		notes = append(notes, "沪深300 在 MA200 下方：目标仓位减半（×0.5）。")
	}
	if len(rows) == 0 {
		notes = append(notes, "当日没有同时满足「基本面 + 趋势 + 回调」的候选，建议保持现金/现有仓位。")
	}

	return &PlanResult{
		PortfolioID:   item.ID,
		PortfolioName: item.Name,
		StrategyID:    item.StrategyID,
		StrategyLabel: item.StrategyLabel,
		Config: score.StrategyConfig{
			StrategyID:          item.StrategyID,
			MarketState:         state.State,
			TotalLayers:         100,
			TargetCount:         cfg.N,
			DesiredPositionCount: cfg.N,
			DesiredTotalLayers:  100,
			MaxLayersPerStock:   int(cfg.MaxWeight * 100),
			AllowNewAdds:        true,
			MarketScopeKey:      "all",
			MaxReplaceCount:     cfg.N,
		},
		MarketState:    state,
		StrengthDate:   latest,
		PortfolioScore: portfolioScore,
		HealthLabel:    health,
		Plan:           plan,
		Holdings:       holdingRows,
		Candidates:     rows,
		Notes:          notes,
	}, nil
}

// benchmarkMA200 用基准收盘序列算 MA200（按交易日序）。
func benchmarkMA200(benchmark map[string]float64) map[string]float64 {
	dates := make([]string, 0, len(benchmark))
	for d := range benchmark {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	out := make(map[string]float64, len(dates))
	sum := 0.0
	for i, d := range dates {
		sum += benchmark[d]
		if i >= 200 {
			sum -= benchmark[dates[i-200]]
		}
		if i >= 199 {
			out[d] = sum / 200
		}
	}
	return out
}

// createQualityGrowthBacktest 是"质量成长 · 趋势回调"的专用回测入口。
//
// 与 CreateBacktest 的差别只在数据来源与计划构建：
//   - 评分不从 stock_strength 读，而是从数据湖 PIT 财报 + 日线现算；
//   - 计划是"权重制"：TotalLayers=100，单票上限 15 层（15%）。
func (s *PortfolioService) createQualityGrowthBacktest(
	req BacktestRequest, item portfolio.Portfolio,
) (*BacktestRunResult, error) {
	cfg := qgrowth.DefaultConfig()
	initialCapital := req.InitialCapital
	if initialCapital < 1 {
		initialCapital = 100000
	}
	days, bars, benchmark, err := s.buildQGrowthDays(req.StartDate, req.EndDate, cfg)
	if err != nil {
		return nil, fmt.Errorf("质量成长回测取数失败：%w", err)
	}
	if len(days) == 0 || len(bars) == 0 {
		return nil, fmt.Errorf("区间内没有可用的评分交易日或日线（%d~%d）", req.StartDate, req.EndDate)
	}

	btCfg := backtest.Config{
		PortfolioID: item.ID, PortfolioName: item.Name,
		StrategyID: item.StrategyID, StrategyLabel: item.StrategyLabel,
		BenchmarkCode:       score.LowBuyIndexBenchmarkCode,
		StartDate:           strconv.Itoa(req.StartDate),
		EndDate:             strconv.Itoa(req.EndDate),
		TargetPositionCount: cfg.N,
		MaxStockCount:       cfg.N,
		TotalLayers:         100,
		MaxLayersPerStock:   int(cfg.MaxWeight * 100),
		InitialCapital:      initialCapital,
		CommissionRate:      0.0003,
		TaxRate:             0.001,
		SlippagePct:         0.1,
		MarketScope:         scopeOfPortfolio(item),
	}
	engine := backtest.New(btCfg, days, bars, benchmark)
	plan := s.qGrowthPlanBuilder(item, days, benchmark, cfg)
	result, err := engine.Run(plan)
	if err != nil {
		return nil, fmt.Errorf("质量成长回测执行失败：%w", err)
	}
	if len(result.Snapshots) == 0 {
		return nil, fmt.Errorf("回测区间内没有净值快照")
	}
	metrics := backtest.CalculateMetrics(result.Snapshots, result.Trades, initialCapital)
	metrics.ScopeWarning = ""
	metrics.Extra = map[string]any{
		"qualityGrowth": map[string]any{
			"config":      cfg,
			"boardScope":  "全部 A 股（主板+创业板+科创板）",
			"totalLayers": 100,
		},
	}

	store, err := s.db()
	if err != nil {
		return nil, err
	}
	runID := portfolio.NewBacktestRunID()
	if err := s.persistBacktest(store, runID, item, btCfg, score.LowBuyIndexBenchmarkCode, result, metrics); err != nil {
		return nil, err
	}
	return &BacktestRunResult{
		OK: true, RunID: runID, Metrics: metrics, Message: "质量成长回测完成",
	}, nil
}

