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

// adjustedCloseFor 取指定日期（含）之前最后一根**复权**收盘价（与评分表同源）。
func (s *PortfolioService) adjustedCloseFor(code string, date int) (float64, bool) {
	if s.stock == nil {
		return 0, false
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return 0, false
	}
	quotes, err := provider.DailyHistory(context.Background(), code, shiftBack(date, 15), date)
	if err != nil || len(quotes) == 0 {
		return 0, false
	}
	for i := len(quotes) - 1; i >= 0; i-- {
		q := quotes[i]
		if q != nil && q.Close > 0 && int(q.Date) <= date {
			return q.Close, true
		}
	}
	return 0, false
}

// rawCloseFor 取指定日期（含）之前最后一根**不复权**收盘价。
//
// 回测/打分用复权价（算收益必须如此），但组合里展示和记账要用真实价格。
// 直接用数据湖的原始日线（DailyBarsInRange 不做复权）；拿不到湖时退回
// KlineHistory（多数情况下也是原始价）。
func (s *PortfolioService) rawCloseFor(code string, date int) (float64, bool) {
	if s.stock == nil {
		return 0, false
	}
	if lake := s.stock.lakeHandle(); lake != nil {
		bars, err := lake.DailyBarsInRange([]string{code}, int64(shiftBack(date, 15)), int64(date))
		if err == nil {
			best := int64(0)
			px := 0.0
			for _, b := range bars {
				d := b.Date()
				if d <= int64(date) && d >= best && b.Close > 0 {
					best, px = d, b.Close
				}
			}
			if px > 0 {
				return px, true
			}
		}
	}
	provider, _ := s.stock.snapshot()
	if provider == nil {
		return 0, false
	}
	quotes, err := provider.KlineHistory(context.Background(), code, shiftBack(date, 30), date)
	if err != nil || len(quotes) == 0 {
		return 0, false
	}
	for i := len(quotes) - 1; i >= 0; i-- {
		q := quotes[i]
		if q != nil && q.Close > 0 && int(q.Date) <= date {
			return q.Close, true
		}
	}
	return 0, false
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
	// 同一科目可能因"部分科目后补公告"出现多次：保留 announce_date 最新的一条，
	// 避免后到的空值/旧值覆盖掉有效值。
	type key struct{ sym, period string }
	type itemVal struct {
		value  float64
		announce int64
	}
	agg := make(map[key]map[string]itemVal, len(rows)/6)
	ann := make(map[key]int64, len(rows)/6)
	for _, r := range rows {
		k := key{portfolio.BareCode(r.Symbol), r.ReportPeriod}
		m := agg[k]
		if m == nil {
			m = make(map[string]itemVal, 8)
			agg[k] = m
		}
		if cur, ok := m[r.ItemCode]; !ok || r.AnnounceDate >= cur.announce {
			m[r.ItemCode] = itemVal{value: r.ItemValue, announce: r.AnnounceDate}
		}
		if r.AnnounceDate > ann[k] {
			ann[k] = r.AnnounceDate
		}
	}
	out := make(map[string][]qgFund, 6000)
	for k, raw := range agg {
		m := make(map[string]float64, len(raw))
		for code, v := range raw {
			m[code] = v.value
		}
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
//
// markets 是组合配置的市场范围（main/gem/star）；为空时不过滤。
// 质量成长只在「主池」里选股，必须尊重组合的板块范围——
// 否则主板+创业板的组合会选出科创板股票。
func (s *PortfolioService) buildQGrowthDays(
	start, end int, cfg qgrowth.Config, markets []string,
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
		if !qGrowthMarketAllowed(item.Code, markets) {
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
		if cfg.Capital > 0 {
			// 可负担过滤需要"候补名单"：多留几倍候选，供跳过贵票后补位
			limit = cfg.N * 4
		}
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

// boardOfCode 按代码前缀判断板块。
func boardOfCode(code string) string {
	c := strings.TrimSpace(code)
	if len(c) < 3 {
		return "other"
	}
	switch c[:3] {
	case "688", "689":
		return "star"
	case "300", "301", "302":
		return "gem"
	case "600", "601", "603", "605", "000", "001", "002", "003":
		return "main"
	}
	return "other"
}

// qGrowthMarketAllowed 判断股票是否在组合配置的市场范围内；markets 为空表示不限。
func qGrowthMarketAllowed(code string, markets []string) bool {
	if len(markets) == 0 {
		return true
	}
	board := boardOfCode(code)
	for _, m := range markets {
		if strings.EqualFold(strings.TrimSpace(m), board) {
			return true
		}
	}
	return false
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
	// 可负担过滤：一手成本 > 单仓预算（本金 × 单票上限）的候选跳过，
	// 用后面的候选补位。小资金下这是"能不能按策略建仓"的关键。
	selected := rows
	if cfg.Capital > 0 && cfg.LotSize > 0 {
		budget := cfg.Capital * cfg.MaxWeight
		picked := make([]score.Row, 0, cfg.N)
		for _, row := range rows {
			if len(picked) >= cfg.N {
				break
			}
			if row.Close*float64(cfg.LotSize) > budget {
				continue
			}
			picked = append(picked, row)
		}
		selected = picked
	} else if len(selected) > cfg.N {
		selected = selected[:cfg.N]
	}
	ws := qgrowth.WeightsFor(scoresOf(selected), cfg.MaxWeight)
	out := make(map[string]int, len(selected))
	for i, row := range selected {
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
	// 注意：**没有候选时清仓**是策略本身的口径（回测同样如此）。
	// 防止"数据缺口把仓清掉"的责任不在这一层，而在调用方：
	//   - 非调仓日只展示持仓（不生成计划行）；
	//   - 自动应用要求计划里至少有一个建仓动作（只有清仓不自动执行）。
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

	// 实盘也用"可负担过滤"：用当前权益（现金 + 持仓市值）判断一手买不买得起，
	// 与回测的 Affordable 口径一致（小资金否则会大量买不进）。
	if money, e := store.GetMoney(item.ID); e == nil {
		shares, _ := store.SharesMap(item.ID)
		positionsEarly, _ := store.ListPositions(item.ID)
		equity := money.Cash
		for _, p := range positionsEarly {
			if n := shares[p.Code]; n > 0 {
				equity += float64(n) * s.rawPriceOr(p.Code, p.EntryPrice)
			}
		}
		if equity > 0 {
			cfg.Capital = equity
			cfg.LotSize = 100
		}
	}

	days, priceBars, benchmark, err := s.buildQGrowthDays(latest, latest, cfg, scopeOfPortfolio(item))
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

	// ⚠ 回测用的是**复权价**（算收益必须如此），但组合里展示/记账要的是
	// "看到的真实价格"。这里把候选行的价格换回复权前的原始收盘价，
	// 否则像 688091 这种历史分红/送转多的票，推荐价会和行情软件差一倍。
	rawClose := make(map[string]float64, len(rows))
	for _, row := range rows {
		if v, ok := s.rawCloseFor(row.Code, latest); ok {
			rawClose[row.Code] = v
		}
	}
	for i := range rows {
		if v, ok := rawClose[rows[i].Code]; ok {
			rows[i].Close = v
		}
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
	// 组合不一定用 100 层制：按组合总层数换算，保证目标权重与回测口径一致。
	// （新建组合时质量成长会强制 100 层；这里兜底历史数据/手工改过层数的组合）
	if item.TotalLayers > 0 && item.TotalLayers != 100 {
		scale := float64(item.TotalLayers) / 100.0
		for code, layers := range target {
			scaled := int(float64(layers)*scale + 0.5)
			if scaled < 1 && layers > 0 {
				scaled = 1
			}
			target[code] = scaled
		}
	}

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
		closePx := closeByCode[p.Code]
		if v, ok := rawClose[p.Code]; ok {
			closePx = v
		} else if v, ok := s.rawCloseFor(p.Code, latest); ok {
			rawClose[p.Code] = v
			closePx = v
		}
		holdingRows = append(holdingRows, score.Row{
			Code: p.Code, Name: p.Name, Close: closePx,
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

	// 方案 A：只在月末调仓窗口展示本次目标；其余日子只显示持仓，
	// 不再展示"每天重算一次"的临时名单，保证"看到的 = 月末执行的"。
	candidates := rows
	if !isRebalanceWindowDate(latest) {
		keepRows := make([]score.RotationRow, 0, len(positions))
		for _, p := range positions {
			cur := curLayers(p)
			keepRows = append(keepRows, score.RotationRow{
				Code: p.Code, Name: p.Name, Action: score.ActionKeep,
				CurrentLayers: cur, TargetLayers: cur, SourceType: "holding",
				Reason: "月末调仓，期间不动",
			})
		}
		plan.Rows = keepRows
		candidates = nil
		notes = append(notes, fmt.Sprintf(
			"质量成长每月最后一个交易日调仓：下一调仓日 %s，届时按当日数据重算；期间持仓不动，不展示临时名单。",
			nextRebalanceDate(latest)))
	} else {
		notes = append(notes, "今日在月末调仓窗口内：下面就是本次调仓的目标名单。")
	}

	res := &PlanResult{
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
		Candidates:     candidates,
		Notes:          notes,
	}
	// 资金视图：目标股数 / 金额 / 买不起 标记（失败不影响计划本身）
	_ = s.ApplyMoneyToPlan(item.ID, res)
	return res, nil
}

// isRebalanceWindowDate 判断 date 是否是本月的调仓日。
//
// 回测的口径是"每月最后一个交易日"；这里用"本月最后一个工作日"对齐，
// 避免把月末前 4 天都当成调仓日（那样会在 11-26 就调仓，而回测是 11-30）。
//
// 不能用"当月最后一天回退到最近交易日"判断 —— 数据只到最新交易日时，
// 那个查询永远返回最新日，会把任意一天误判成月末。
func isRebalanceWindowDate(date int) bool {
	if date <= 0 {
		return false
	}
	t, err := time.Parse("20060102", strconv.Itoa(date))
	if err != nil {
		return false
	}
	return t.Equal(lastWeekdayOfMonth(t))
}

// lastWeekdayOfMonth 返回 t 所在月份的最后一个工作日（周一~周五）。
func lastWeekdayOfMonth(t time.Time) time.Time {
	last := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location())
	for last.Weekday() == time.Saturday || last.Weekday() == time.Sunday {
		last = last.AddDate(0, 0, -1)
	}
	return last
}

// nextRebalanceDate 返回 date 之后最近的一个月末调仓日（当月最后一个工作日）。
func nextRebalanceDate(date int) string {
	t, err := time.Parse("20060102", strconv.Itoa(date))
	if err != nil {
		return ""
	}
	last := lastWeekdayOfMonth(t)
	if !t.Before(last) {
		last = lastWeekdayOfMonth(t.AddDate(0, 1, 0))
	}
	return last.Format("2006-01-02")
}

// lastTradingDayOfMonth 用数据湖的交易日分区取某月最后一个交易日（YYYYMMDD）。
func (s *PortfolioService) lastTradingDayOfMonth(year int, month time.Month) int {
	if s.stock == nil {
		return 0
	}
	lake := s.stock.lakeHandle()
	if lake == nil {
		return 0
	}
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local)
	d1, err1 := strconv.Atoi(first.Format("20060102"))
	d2, err2 := strconv.Atoi(last.Format("20060102"))
	if err1 != nil || err2 != nil {
		return 0
	}
	dates, err := lake.DatesInRange(int64(d1), int64(d2))
	if err != nil || len(dates) == 0 {
		return 0
	}
	return int(dates[len(dates)-1])
}

// applyTxnReasonText 与 portfolio.applyTxnReason 保持一致（那边未导出）。
const applyTxnReasonText = "一键应用调仓建议"

// appliedSince 判断 portfolio 在 date（含）之后是否已经应用过调仓。
func (s *PortfolioService) appliedSince(portfolioID string, date int) bool {
	store, err := s.db()
	if err != nil {
		return false
	}
	var n int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM portfolio_position_txn WHERE portfolio_id=? AND trade_date>=? AND reason=?",
		portfolioID, strconv.Itoa(date), applyTxnReasonText).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// hasAnyTxn 判断组合是否有过任何调仓流水（新建组合不应被"补做"回填）。
func (s *PortfolioService) hasAnyTxn(portfolioID string) bool {
	store, err := s.db()
	if err != nil {
		return false
	}
	var n int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM portfolio_position_txn WHERE portfolio_id=?", portfolioID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// catchUpQualityGrowthRebalance 补做上一个已结束月份的月度调仓。
//
// 触发条件：组合有历史、上个调仓日之后没有任何应用记录（节假日月末没有数据、
// 或月末当晚数据未同步）。用那个月最后一个交易日的数据补算并应用，
// 与回测"每月最后一个交易日调仓"完全对齐。
func (s *PortfolioService) catchUpQualityGrowthRebalance(item portfolio.Portfolio) string {
	if !s.hasAnyTxn(item.ID) {
		return ""
	}
	now := time.Now()
	prev := now.AddDate(0, -1, 0)
	rd := s.lastTradingDayOfMonth(prev.Year(), prev.Month())
	if rd == 0 || s.appliedSince(item.ID, rd) {
		return ""
	}
	// 先按那个交易日算一遍计划：没有建仓动作（例如数据缺口）就不补做，
	// 避免"只清仓不建仓"。
	store, err := s.db()
	if err != nil {
		return ""
	}
	plan, err := s.buildQualityGrowthPlanAt(item, store, rd)
	if err != nil {
		return "补做月度调仓失败：" + err.Error()
	}
	if !planHasBuy(plan) {
		return ""
	}
	res, err := s.applyRotationWithPlan(item.ID, func(_ portfolio.Portfolio, _ *portfolio.Store) (*PlanResult, error) {
		return plan, nil
	})
	if err != nil {
		return "补做月度调仓失败：" + err.Error()
	}
	return fmt.Sprintf("已补做 %d 的月度调仓（新增 %d 只 / 减仓 %d 只 / 清仓 %d 只）",
		rd, res.Applied.Added, res.Applied.Reduced, res.Applied.Cleared)
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
	if req.Affordable {
		cfg.Capital = initialCapital
		cfg.LotSize = 100
	}
	days, bars, benchmark, err := s.buildQGrowthDays(req.StartDate, req.EndDate, cfg, scopeOfPortfolio(item))
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
		LotSize:             100, // A 股按手下单（100 股整数倍）
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

