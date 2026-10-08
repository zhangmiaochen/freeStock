package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"freestock/internal/score"
	"freestock/internal/stockdb"
)

// 评分编排：把真实行情喂进 internal/score 的纯计算，产出可用于轮动决策的行。
//
// 与参考实现的对应关系：
//   - 这里对应 PortfolioService._build_strength_history_frame（627 行）
//     中被本项目覆盖得到的部分：指标 + 基础分 + 横截面分位 + 市场状态。
//   - **未覆盖**：research_score / board_research_score / active_signal_*
//     —— 那几个依赖参考项目的 research_service.py（351 KB）与 ML 模型信号，
//     本项目没有对应数据源。这些字段留 0，并在结果里标注。
//
// 复刻口径（与参考实现一致）：
//   - **全程不复权**：直接取日线原始 OHLC
//   - 指标窗口按**交易日**回看，start 用日历日近似放宽
const (
	// scoreLookbackTradingDays 是最长均线（MA250）+ 修复能力窗口（41）之后的余量
	scoreLookbackTradingDays = 320
	// scoreCalendarPerTrading 日历日 / 交易日 的经验折算（含周末与节假日）
	scoreCalendarPerTrading = 1.5
	// scoreWorkers 是并发取历史行情的协程数
	scoreWorkers = 32
	// scoreBenchmarkCode 是相对强度的基准（沪深300）
	scoreBenchmarkCode = "000300.SH"
)

// ScoringResult 是一次批量评分的结果。
type ScoringResult struct {
	TradeDate   int               `json:"tradeDate"`
	Rows        []score.Row       `json:"rows"`
	MarketState score.MarketState `json:"marketState"`
	// Scored / Skipped 是被评上 / 数据不足被跳过的股票数
	Scored  int `json:"scored"`
	Skipped int `json:"skipped"`
	// Unavailable 列出本次没能计算的字段分组（数据源不支持）
	Unavailable []string `json:"unavailable"`
}

// scoreWindowStart 把交易日回看窗口折算成起始日历日（YYYYMMDD）。
//
// 用固定折算系数而不是精确交易日历：多取一些没有害处（多余的 K 线不会影响
// 滚动窗口的最后一段），少取才会让 MA250 算不出来。
func scoreWindowStart(tradeDate int, tradingDays int) int {
	t, err := time.Parse("20060102", fmt.Sprintf("%08d", tradeDate))
	if err != nil {
		return tradeDate
	}
	back := time.Duration(float64(tradingDays)*scoreCalendarPerTrading*24) * time.Hour
	value, err := strconv.Atoi(t.Add(-back).Format("20060102"))
	if err != nil {
		return tradeDate
	}
	return value
}

// barsFromQuotes 把行情转换成评分用的日线（**按日期升序**）。
func barsFromQuotes(quotes []*stockdb.Quote) []score.Bar {
	bars := make([]score.Bar, 0, len(quotes))
	for _, q := range quotes {
		if q == nil || q.Close <= 0 {
			continue
		}
		bars = append(bars, score.Bar{
			Date:   q.Date,
			Open:   q.Open,
			High:   q.High,
			Low:    q.Low,
			Close:  q.Close,
			Volume: q.Volume,
			Amount: q.Amount,
		})
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	return bars
}

// scoreInput 是单只股票的中间计算结果（横截面分位还没填）。
type scoreInput struct {
	code       string
	industry   string
	market     string
	indicators score.Indicators
	repair     score.RepairFeatures
	prevHigh60 float64
	bars       int
	// 横截面排序用（取自最后一根 K 线）
	amount   float64
	turnover float64
}

// ScoringService 提供股票评分。
type ScoringService struct {
	stock *StockService
}

// NewScoringService 构造评分服务。
func NewScoringService(stock *StockService) *ScoringService {
	return &ScoringService{stock: stock}
}

// ScoreStocks 对指定股票计算评分行。
//
// codes 为空时取全市场 —— 那会对数据源发起**每只股票一次**的历史请求
// （7000+ 次），耗时数分钟且可能压垮数据源。因此 limit > 0 时只取前 limit 只；
// 调用方（含测试）应当**总是**给出 limit 或显式列出 codes。
//
// tradeDate 为 0 时取最新交易日。
func (s *ScoringService) ScoreStocks(codes []string, tradeDate int, limit int) (*ScoringResult, error) {
	if s.stock == nil {
		return nil, fmt.Errorf("评分服务缺少股票服务依赖")
	}
	provider, source := s.stock.snapshot()
	if provider == nil {
		return nil, fmt.Errorf("数据源未就绪")
	}

	// 解析评分日
	date := tradeDate
	if date <= 0 {
		resolved, err := provider.LatestTradeDate(context.Background())
		if err != nil {
			return nil, fmt.Errorf("取最新交易日失败：%w", err)
		}
		date = resolved
	}
	_ = source

	result := &ScoringResult{
		TradeDate: date,
		Unavailable: []string{
			"研究评分（research_score）：参考项目依赖 research_service.py，本项目无对应数据源",
			"板块研究评分（board_research_score）：同上",
			"模型信号（active_signal_*）：参考项目依赖 ML 模型信号表，本项目无对应数据源",
		},
	}

	// 名称与行业
	universe, _ := s.stock.GetStockUniverse(false)
	nameByCode := map[string]string{}
	marketByCode := map[string]string{}
	if universe != nil {
		for _, item := range universe.Stocks {
			nameByCode[item.Code] = item.Name
			marketByCode[item.Code] = item.Market
		}
	}

	targets := codes
	if len(targets) == 0 && universe != nil {
		for _, item := range universe.Stocks {
			if item.HasQuote {
				targets = append(targets, item.Code)
			}
		}
	}
	// 保护数据源：全市场评分是 7000+ 次请求
	if limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	if len(targets) == 0 {
		return result, nil
	}

	// 基准：沪深300 的 20 日收益
	benchmarkReturn := s.benchmarkReturn20D(provider, date)

	start := scoreWindowStart(date, scoreLookbackTradingDays)

	// 并发取每只股票的历史
	inputs := s.loadScoreInputs(provider, targets, start, date)

	// 行业归属
	industryByCode := map[string]string{}
	if len(inputs) > 0 {
		codeList := make([]string, 0, len(inputs))
		for _, in := range inputs {
			codeList = append(codeList, in.code)
		}
		if membership, err := s.stock.boardsForCodes(codeList); err == nil {
			for code, boards := range membership {
				if len(boards) > 0 {
					industryByCode[code] = boards[0] // 行业板块排在最前
				}
			}
		}
	}

	// 行业 20 日收益（同日同行业的均值）
	industryReturns := map[string]float64{}
	groupSum := map[string]float64{}
	groupCount := map[string]int{}
	for _, in := range inputs {
		industry := industryByCode[in.code]
		if industry == "" {
			continue
		}
		groupSum[industry] += in.indicators.StockReturn20D
		groupCount[industry]++
	}
	for industry, count := range groupCount {
		if count > 0 {
			industryReturns[industry] = groupSum[industry] / float64(count)
		}
	}

	// 横截面分位：成交额与换手
	amounts := make([]float64, 0, len(inputs))
	turns := make([]float64, 0, len(inputs))
	for _, in := range inputs {
		amounts = append(amounts, quoteAmountOf(in))
		turns = append(turns, quoteTurnoverOf(in))
	}
	// 行业强度分位：用行业的 20 日收益排名
	industryValues := make([]float64, 0, len(industryReturns))
	for _, value := range industryReturns {
		industryValues = append(industryValues, value)
	}

	// 组装行
	rows := make([]score.Row, 0, len(inputs))
	for _, in := range inputs {
		ind := in.indicators
		industry := industryByCode[in.code]

		// 横截面
		ind.AmountRankPct = score.RankPct(amounts, quoteAmountOf(in))
		ind.TurnRankPct = score.RankPct(turns, quoteTurnoverOf(in))
		ind.IndustryReturn20D = industryReturns[industry]
		ind.IndustryStrengthPct = score.RankPct(industryValues, ind.IndustryReturn20D)
		ind.BenchmarkReturn20D = benchmarkReturn
		ind.RelativeStrength20D = round4(ind.StockReturn20D - benchmarkReturn)

		// 基础分
		trend := score.TrendBase(ind, 0, 0)
		structure := score.StructureBase(ind, in.prevHigh60, 0)
		rs := score.RSBase(ind)
		capital := score.CapitalBase(ind)
		nearRepair := score.NearTermRepairScore(ind, in.prevHigh60)
		// 趋势成长的第五项用**修复能力分**（其余策略才用近期修复分）
		adjustment := trendGrowthAdjustment(ind, in.repair, nearRepair)
		total := score.TotalScore(trend, structure, rs, capital, in.repair.AbilityScore, adjustment)

		row := score.Row{
			Code:       in.code,
			Name:       nameByCode[in.code],
			TotalScore: total,
			TrendScore: trend, StructureScore: structure, RSScore: rs,
			CapitalScore: capital,
			// 参考实现：趋势成长的 repair_score 列存的是修复能力分
			RepairScore: in.repair.AbilityScore,

			Close: ind.Close, Open: ind.Open,
			Ma5: ind.Ma5, Ma20: ind.Ma20, Ma30: ind.Ma30,
			Ma60: ind.Ma60, Ma120: ind.Ma120, Ma250: ind.Ma250,
			PctChg: ind.PctChg,

			RelativeStrength20D: ind.RelativeStrength20D,
			DrawdownFromHigh120: ind.DrawdownFromHigh120,
			DrawdownFromHigh20:  ind.DrawdownFromHigh20,
			ReboundFromLow60:    ind.ReboundFromLow60,
			StockReturn5D:       ind.StockReturn5D,
			StockReturn10D:      ind.StockReturn10D,
			VolumeRatio20:       ind.VolumeRatio20,
		}
		rows = append(rows, row)
	}

	// 市场状态（全市场宽度）
	state := s.marketState(provider, date)
	result.MarketState = state
	result.Rows = rows
	result.Scored = len(rows)
	result.Skipped = len(targets) - len(rows)

	slog.Info("股票评分完成",
		"tradeDate", date, "scored", result.Scored, "skipped", result.Skipped,
		"marketState", state.State)
	return result, nil
}

// trendGrowthAdjustment 是趋势成长的"主线修正"分。
//
// 参考实现：
//
//	adj = clip(max(mainline分, industry_strength*100)/100, 0, 1) * 6
//	    + score_0_1(rs20, 5, 30) * 3
//	    - 4 * (mainline_raw < 40 且 rs20 < 12)
//	    - 5 * (dd120 > -2 且 rs20 > 30)
//
// mainline_raw = max(mainline_board_score, industry_board_score, board_research_score)。
// 本项目没有板块研究分，mainline_raw 只能取行业强度近似 —— 这里**不使用**它，
// 避免用一个语义不同的值冒充。见函数内注释。
func trendGrowthAdjustment(ind score.Indicators, repair score.RepairFeatures, nearRepair float64) float64 {
	// industry_strength_pct 是 0..1 横截面分位，乘 100 得到"分位分"
	mainline := ind.IndustryStrengthPct * 100
	adj := clampF(mainline/100, 0, 1)*6 + scoreWindow01(ind.RelativeStrength20D, 5, 30)*3
	if mainline < 40 && ind.RelativeStrength20D < 12 {
		adj -= 4
	}
	if ind.DrawdownFromHigh120 > -2 && ind.RelativeStrength20D > 30 {
		adj -= 5
	}
	return math.Round(adj*100) / 100
}

func scoreWindow01(value, low, high float64) float64 {
	if high <= low {
		return 0
	}
	return clampF((value-low)/(high-low), 0, 1)
}

func clampF(value, low, high float64) float64 {
	if math.IsNaN(value) {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func round4(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10000) / 10000
}

// amountOf / turnoverOf 从原始行情取横截面排序用的值。
//
// scoreInput 里没存 Quote，这里用指标里的成交额；换手用不到就返回 0。
func quoteAmountOf(in scoreInput) float64   { return in.amount }
func quoteTurnoverOf(in scoreInput) float64 { return in.turnover }

// loadScoreInputs 并发取每只股票的历史并算出指标。
func (s *ScoringService) loadScoreInputs(provider Provider, codes []string, start, end int) []scoreInput {
	type job struct{ code string }
	jobs := make(chan job)
	results := make([]scoreInput, 0, len(codes))
	var mu sync.Mutex
	var wg sync.WaitGroup

	workers := scoreWorkers
	if workers > len(codes) {
		workers = len(codes)
	}
	ctx := context.Background()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				quotes, err := provider.DailyHistory(ctx, item.code, start, end)
				if err != nil || len(quotes) == 0 {
					continue
				}
				bars := barsFromQuotes(quotes)
				if len(bars) < 30 {
					// 数据太短的股票算不出有意义的指标
					continue
				}
				ind, ok := score.ComputeIndicators(bars)
				if !ok {
					continue
				}
				in := scoreInput{
					code:       item.code,
					indicators: ind,
					repair:     score.BuildRepairFeatures(bars),
					prevHigh60: score.PrevHigh60(bars),
					bars:       len(bars),
				}
				// 成交额与换手取最后一根
				last := quotes[len(quotes)-1]
				for _, q := range quotes {
					if q != nil && q.Date > last.Date {
						last = q
					}
				}
				if last != nil {
					in.amount = last.Amount
					in.turnover = last.Turnover
				}
				mu.Lock()
				results = append(results, in)
				mu.Unlock()
			}
		}()
	}
	for _, code := range codes {
		jobs <- job{code: code}
	}
	close(jobs)
	wg.Wait()
	return results
}

// benchmarkReturn20D 取基准指数的 20 日收益。
func (s *ScoringService) benchmarkReturn20D(provider Provider, date int) float64 {
	start := scoreWindowStart(date, 60)
	quotes, err := provider.DailyHistory(context.Background(), scoreBenchmarkCode, start, date)
	if err != nil || len(quotes) < 21 {
		return 0
	}
	bars := barsFromQuotes(quotes)
	if len(bars) < 21 {
		return 0
	}
	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
	}
	base := closes[len(closes)-21]
	if base <= 0 {
		return 0
	}
	return round4((closes[len(closes)-1]/base - 1) * 100)
}

// marketState 用全市场宽度判断市场状态。
//
// 成本问题：需要窗口内**每天全市场**的平均涨跌幅、涨跌家数、成交额。
// free-stockdb 没有聚合接口，逐股拉 80 天×7500 只不现实，
// 因此这里用**抽样**：从全市场均匀抽 scoreMarketSample 只，用它们的日线聚合。
//
// 这是与本项目其它"如实照搬"不同的**有意妥协**，因为参考实现是拿它已经算好的
// 全市场评分表直接聚合，而本项目没有那张表。抽样会把平均涨跌幅的方差放大，
// 但涨跌家数占比与结构判定仍然稳定。结果里会带上说明。
func (s *ScoringService) marketState(provider Provider, date int) score.MarketState {
	const sampleSize = 400
	const windowDays = 80

	universe, err := s.stock.GetStockUniverse(false)
	if err != nil || universe == nil || len(universe.Stocks) == 0 {
		return score.MarketState{State: score.StateRange, Label: score.StateLabels[score.StateRange]}
	}

	codes := make([]string, 0, len(universe.Stocks))
	for _, item := range universe.Stocks {
		if item.HasQuote {
			codes = append(codes, item.Code)
		}
	}
	if len(codes) == 0 {
		return score.MarketState{State: score.StateRange, Label: score.StateLabels[score.StateRange]}
	}
	// 均匀抽样（不是随机：保证结果可复现）
	step := len(codes) / sampleSize
	if step < 1 {
		step = 1
	}
	sample := make([]string, 0, sampleSize)
	for i := 0; i < len(codes) && len(sample) < sampleSize; i += step {
		sample = append(sample, codes[i])
	}

	start := scoreWindowStart(date, windowDays+5)
	type daily struct {
		pctSum, amountSum float64
		up, down, total   int
	}
	byDate := map[int64]*daily{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	jobs := make(chan string)
	ctx := context.Background()
	workers := scoreWorkers
	if workers > len(sample) {
		workers = len(sample)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for code := range jobs {
				quotes, err := provider.DailyHistory(ctx, code, start, date)
				if err != nil {
					continue
				}
				// 只取窗口内的最后 windowDays 根
				if len(quotes) > windowDays {
					quotes = quotes[len(quotes)-windowDays:]
				}
				mu.Lock()
				for _, q := range quotes {
					if q == nil || q.Close <= 0 {
						continue
					}
					item := byDate[q.Date]
					if item == nil {
						item = &daily{}
						byDate[q.Date] = item
					}
					item.pctSum += q.PctChg
					item.amountSum += q.Amount
					item.total++
					if q.PctChg > 0 {
						item.up++
					} else if q.PctChg < 0 {
						item.down++
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, code := range sample {
		jobs <- code
	}
	close(jobs)
	wg.Wait()

	// 按日期升序聚合
	dates := make([]int64, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })

	days := make([]score.MarketDay, 0, len(dates))
	for _, d := range dates {
		item := byDate[d]
		if item.total == 0 {
			continue
		}
		days = append(days, score.MarketDay{
			Date:        d,
			AvgPct:      item.pctSum / float64(item.total),
			UpRatio:     float64(item.up) / float64(item.total),
			DownRatio:   float64(item.down) / float64(item.total),
			TotalAmount: item.amountSum,
		})
	}
	return score.BuildMarketState(days)
}
