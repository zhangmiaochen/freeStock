package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/stockdb"
)

// 行情来源类型。
const (
	// SourceKindStockDB 表示行情来自 free-stockdb 的 HTTP 服务
	SourceKindStockDB = "stockdb"
	// SourceKindCnLake 表示行情来自 cnequity 本地数据湖
	SourceKindCnLake = "cnlake"
)

// Provider 是**行情类**数据的来源。
//
// 两个实现：
//   - stockdbProvider：free-stockdb 的 HTTP 服务（本地或远程）
//   - lakeProvider   ：cnequity 本地数据湖（parquet）
//
// 注意：财务数据（财报 / 股本结构 / 股东户数 / 十大股东）**不走这里**。
// 它们始终来自数据湖，与当前选中的行情源无关，见 finance.go。
type Provider interface {
	// Kind 返回来源类型：stockdb / cnlake
	Kind() string
	// Label 用于日志和界面展示
	Label() string

	StockCodes(ctx context.Context) (map[string][]string, error)
	LatestTradeDate(ctx context.Context) (int, error)
	// TradeDateAt 返回**不晚于** date 的最近一个交易日。
	//
	// 条件选股指定日期时用它：用户填的那天可能不是交易日、也可能当天数据还没同步，
	// 所以要落到"该日或之前最近一个有数据的交易日"。date<=0 时等价于 LatestTradeDate。
	TradeDateAt(ctx context.Context, date int) (int, error)
	QuotePoint(ctx context.Context, code string, date int) (*stockdb.Quote, error)
	// LatestQuotes 批量取某个交易日的行情。
	// 数据湖按年分区存放，逐只查询会把同一分区重复扫描上千次，所以列表页走这个接口。
	LatestQuotes(ctx context.Context, codes []string, date int) (map[string]*stockdb.Quote, error)
	QuoteRangeDesc(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error)
	DailyHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error)
	DividendFactors(ctx context.Context, code string) ([]stockdb.Factor, error)
	MoneyFlowRange(ctx context.Context, code string, start, end int) ([]stockdb.MoneyFlow, error)
	Boards(ctx context.Context) ([]stockdb.Board, error)
}

// ---------------------------------------------------------------------------
// free-stockdb
// ---------------------------------------------------------------------------

type stockdbProvider struct {
	client *stockdb.Client
	label  string
}

// NewStockdbProvider 用 free-stockdb 客户端构造行情来源。
func NewStockdbProvider(client *stockdb.Client, label string) Provider {
	return &stockdbProvider{client: client, label: label}
}

func (p *stockdbProvider) Kind() string  { return SourceKindStockDB }
func (p *stockdbProvider) Label() string { return p.label }

func (p *stockdbProvider) StockCodes(ctx context.Context) (map[string][]string, error) {
	return p.client.StockCodes(ctx)
}

func (p *stockdbProvider) LatestTradeDate(ctx context.Context) (int, error) {
	return p.client.LatestTradeDate(ctx)
}

// tradeDateProbes 是用来找交易日的活跃股票。
// 它们几乎从不停牌，所以它们的最新日 K 就能代表市场的最新交易日。
var tradeDateProbes = []string{"000001", "600519"}

// tradeDateWindows 是查找历史交易日时的回溯窗口（自然日），由窄到宽。
// 窄窗口命中是常态（周末/节假日最多隔几天），宽窗口兜底长假与冷门时段。
var tradeDateWindows = []int{20, 60, 400}

func (p *stockdbProvider) TradeDateAt(ctx context.Context, date int) (int, error) {
	if date <= 0 {
		return p.LatestTradeDate(ctx)
	}

	// 用区间查询取"不晚于 date 的最近一根 K 线"。
	// 不能直接用 date 做点查询：那天可能不是交易日，或者数据还没同步。
	var lastErr error
	for _, window := range tradeDateWindows {
		start := shiftDate(date, -window)
		best := 0
		for _, code := range tradeDateProbes {
			list, err := p.client.QuoteRangeDesc(ctx, code, start, date)
			if err != nil {
				lastErr = err
				continue
			}
			if len(list) > 0 && list[0] != nil {
				// QuoteRangeDesc 是倒序的，第一条就是最近的一根
				if d := int(list[0].Date); d > best {
					best = d
				}
			}
		}
		if best > 0 {
			return best, nil
		}
	}

	if lastErr != nil {
		return 0, fmt.Errorf("找不到 %d 或之前的交易日：%w", date, lastErr)
	}
	return 0, fmt.Errorf("找不到 %d 或之前的交易日（该日期之前的日 K 数据可能未同步）", date)
}

func (p *stockdbProvider) QuotePoint(ctx context.Context, code string, date int) (*stockdb.Quote, error) {
	return p.client.QuotePoint(ctx, code, date)
}

// LatestQuotes free-stockdb 没有批量接口，但每次都是独立的小请求，
// 所以并发逐只查即可（比串行快一个数量级）。
func (p *stockdbProvider) LatestQuotes(ctx context.Context, codes []string, date int) (map[string]*stockdb.Quote, error) {
	out := make(map[string]*stockdb.Quote, len(codes))
	if len(codes) == 0 {
		return out, nil
	}

	var (
		mu       sync.Mutex
		firstErr error
	)
	runWorkers(ctx, fetchWorkers, len(codes), func(i int) {
		quote, err := p.client.QuotePoint(ctx, codes[i], date)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		if quote != nil {
			out[codes[i]] = quote
		}
	})
	return out, firstErr
}

func (p *stockdbProvider) QuoteRangeDesc(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.client.QuoteRangeDesc(ctx, code, start, end)
}

func (p *stockdbProvider) DailyHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.client.DailyHistory(ctx, code, start, end)
}

func (p *stockdbProvider) DividendFactors(ctx context.Context, code string) ([]stockdb.Factor, error) {
	return p.client.DividendFactors(ctx, code)
}

func (p *stockdbProvider) MoneyFlowRange(ctx context.Context, code string, start, end int) ([]stockdb.MoneyFlow, error) {
	return p.client.MoneyFlowRange(ctx, code, start, end)
}

func (p *stockdbProvider) Boards(ctx context.Context) ([]stockdb.Board, error) {
	return p.client.Boards(ctx)
}

// ---------------------------------------------------------------------------
// cnequity 数据湖
// ---------------------------------------------------------------------------

// lakeProvider 把数据湖的若干数据集拼成行情源。
//
// 拼装关系（这是数据湖与 free-stockdb 最大的不同：行情和估值是两张表）：
//
//	instruments        -> 标的清单 / 名称
//	daily_bars         -> OHLCV
//	valuation_metrics  -> PE_TTM / PB / 市值   （按 symbol + trade_date 关联）
//	corporate_actions  -> 分红送转 -> 复权因子
//	fund_flow          -> 资金流
//	industry_members   -> 行业板块
//	sector_members     -> 概念板块
//	share_structure    -> 总股本 / 流通股本（算换手率用）
type lakeProvider struct {
	lake *cnlake.Lake

	mu     sync.Mutex
	names  map[string]string // bare code -> 名称
	assets map[string]string // bare code -> asset_type
	boards []stockdb.Board   // 行业 + 概念板块（缓存）
	shares map[string][]cnlake.ShareStructureRow
}

// NewLakeProvider 用数据湖构造行情来源。
func NewLakeProvider(lake *cnlake.Lake) Provider {
	return &lakeProvider{lake: lake}
}

func (p *lakeProvider) Kind() string  { return SourceKindCnLake }
func (p *lakeProvider) Label() string { return "本地数据湖 " + p.lake.Root() }

// ensureMeta 载入并缓存标的名称与资产类型。
func (p *lakeProvider) ensureMeta() error {
	p.mu.Lock()
	if p.names != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	rows, err := p.lake.Instruments()
	if err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	assets := make(map[string]string, len(rows))
	for _, row := range rows {
		code := cnlake.BareSymbol(row.Symbol)
		names[code] = row.Name
		assets[code] = row.AssetType
	}

	p.mu.Lock()
	p.names = names
	p.assets = assets
	p.mu.Unlock()
	return nil
}

func (p *lakeProvider) name(code string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.names[cnlake.BareSymbol(code)]
}

func (p *lakeProvider) StockCodes(ctx context.Context) (map[string][]string, error) {
	if err := p.ensureMeta(); err != nil {
		return nil, fmt.Errorf("读取数据湖标的清单失败：%w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	buckets := make(map[string][]string, 8)
	for code := range p.names {
		if code == "" {
			continue
		}
		buckets[code[:1]] = append(buckets[code[:1]], code)
	}
	return buckets, nil
}

func (p *lakeProvider) LatestTradeDate(ctx context.Context) (int, error) {
	date, err := p.lake.LatestTradeDate()
	if err != nil {
		return 0, err
	}
	if date == 0 {
		return 0, fmt.Errorf("数据湖的 trading_calendar 没有可用交易日，请确认已同步")
	}
	return int(date), nil
}

func (p *lakeProvider) TradeDateAt(ctx context.Context, date int) (int, error) {
	at, err := p.lake.TradeDateAt(int64(date))
	if err != nil {
		return 0, err
	}
	if at == 0 {
		if date > 0 {
			return 0, fmt.Errorf("数据湖里找不到 %d 或之前的交易日", date)
		}
		return 0, fmt.Errorf("数据湖的 trading_calendar 没有可用交易日，请确认已同步")
	}
	return int(at), nil
}

// quoteLookbackDays 取单点行情时向前回看的自然日数。
//
// 两个作用：
//  1. 只有拿到前一根 K 线才能算出昨收 / 涨跌幅 / 振幅；
//  2. 交易日历可能比 K 线跑得靠前（日历是预生成的），该日没有 K 线时
//     就回退到最近一根，而不是返回空。
const quoteLookbackDays = 120

func (p *lakeProvider) QuotePoint(ctx context.Context, code string, date int) (*stockdb.Quote, error) {
	quotes, err := p.quoteRange(code, shiftBack(date, quoteLookbackDays), date, false)
	if err != nil {
		return nil, err
	}
	if len(quotes) == 0 {
		// 回看窗口内一根都没有：说明日历比 K 线进度超前很多（例如刚建库、
		// 或某只股票长期停牌），这次不限起点重取一遍。
		// 代价是要扫更多分区，所以只在这条冷路径上做。
		if quotes, err = p.quoteRange(code, 0, date, false); err != nil {
			return nil, err
		}
	}
	if len(quotes) == 0 {
		return nil, nil
	}
	// 最后一根就是「不晚于 date 的最近一根」，它带着正确的昨收
	return quotes[len(quotes)-1], nil
}

func (p *lakeProvider) QuoteRangeDesc(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.quoteRange(code, start, end, true)
}

func (p *lakeProvider) DailyHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.quoteRange(code, start, end, false)
}

// buildQuote 把一根 K 线 + 估值 + 股本组装成行情。
// 数据湖的 daily_bars 只有 OHLCV，其余字段都在别处或需要推导。
func (p *lakeProvider) buildQuote(bare, name string, bar cnlake.BarRow, prevClose float64, valuation *cnlake.ValuationRow, shares []cnlake.ShareStructureRow) *stockdb.Quote {
	quote := &stockdb.Quote{
		Date:     bar.Date(),
		Code:     bare,
		Name:     name,
		Open:     bar.Open,
		High:     bar.High,
		Low:      bar.Low,
		Close:    bar.Close,
		Volume:   float64(bar.Volume),
		Amount:   bar.Amount,
		IsST:     strings.Contains(strings.ToUpper(name), "ST"),
		PreClose: prevClose,
	}
	if valuation != nil {
		quote.PeTTM = valuation.PeTTM
		quote.PB = valuation.PB
		quote.TotalMV = valuation.TotalMV
		quote.FloatMV = valuation.FloatMV
	}
	if total, float, ok := sharesFor(shares, quote.Date); ok {
		quote.TotalShare = total
		quote.FloatShare = float
	}
	if quote.PreClose > 0 {
		quote.PctChg = round((quote.Close-quote.PreClose)/quote.PreClose*100, 4)
		quote.Amplitude = round((quote.High-quote.Low)/quote.PreClose*100, 4)
	}
	if quote.FloatShare > 0 {
		quote.Turnover = round(quote.Volume/quote.FloatShare*100, 4)
	}
	return quote
}

// quoteRange 组装一只股票的区间行情。
func (p *lakeProvider) quoteRange(code string, start, end int, desc bool) ([]*stockdb.Quote, error) {
	if err := p.ensureMeta(); err != nil {
		return nil, err
	}
	if end <= 0 {
		end = 20991231
	}
	if start <= 0 {
		start = 19900101
	}

	bars, err := p.lake.DailyBars(code, start/10000, end/10000)
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 daily_bars 失败：%w", err)
	}

	// 估值表可能缺失（或只有部分年份），缺失时行情照样出，只是没有 PE/PB
	valuations, _ := p.lake.Valuations(code, start/10000, end/10000)
	byDate := make(map[int64]cnlake.ValuationRow, len(valuations))
	for _, row := range valuations {
		byDate[row.Date()] = row
	}

	bare := cnlake.BareSymbol(code)
	shares := p.shareHistory(bare)
	name := p.name(bare)

	out := make([]*stockdb.Quote, 0, len(bars))
	var prevClose float64
	for _, bar := range bars {
		date := bar.Date()
		if date < int64(start) || date > int64(end) {
			continue
		}
		var valuation *cnlake.ValuationRow
		if found, ok := byDate[date]; ok {
			valuation = &found
		}
		out = append(out, p.buildQuote(bare, name, bar, prevClose, valuation, shares))
		prevClose = bar.Close
	}

	if desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// LatestQuotes 批量取某个交易日的行情。
//
// 这是数据湖模式下能不能用股票列表的关键：7500 只股票逐只查会把同一个
// 年分区重复扫描 7500 次，批量读只扫一遍。
func (p *lakeProvider) LatestQuotes(ctx context.Context, codes []string, date int) (map[string]*stockdb.Quote, error) {
	out := make(map[string]*stockdb.Quote, len(codes))
	if len(codes) == 0 || date <= 0 {
		return out, nil
	}
	if err := p.ensureMeta(); err != nil {
		return nil, err
	}

	start := shiftBack(date, quoteLookbackDays)
	quotes, missed, err := p.bulkQuotes(codes, start, date)
	if err != nil {
		return nil, err
	}
	for code, quote := range quotes {
		out[code] = quote
	}

	// 回看窗口内一根都没有的（日历远超 K 线进度、长期停牌等），不限起点重取
	if len(missed) > 0 {
		filled, _, retryErr := p.bulkQuotes(missed, 0, date)
		if retryErr != nil {
			return out, retryErr
		}
		for code, quote := range filled {
			out[code] = quote
		}
	}
	return out, nil
}

// bulkQuotes 一次读完区间内所有相关股票，返回每只（不晚于 end 的）最新一根行情，
// 以及在这个区间里一根都没找到的代码。
func (p *lakeProvider) bulkQuotes(codes []string, start, end int) (map[string]*stockdb.Quote, []string, error) {
	if start <= 0 {
		start = 19900101
	}
	startYear, endYear := start/10000, end/10000

	bars, err := p.lake.DailyBarsMulti(codes, startYear, endYear)
	if err != nil {
		return nil, nil, fmt.Errorf("读取数据湖 daily_bars 失败：%w", err)
	}

	grouped := make(map[string][]cnlake.BarRow, len(codes))
	for _, bar := range bars {
		date := bar.Date()
		if date < int64(start) || date > int64(end) {
			continue
		}
		code := cnlake.BareSymbol(bar.Symbol)
		grouped[code] = append(grouped[code], bar)
	}

	valuations, _ := p.lake.ValuationsMulti(codes, startYear, endYear)
	valuationIndex := make(map[string]map[int64]cnlake.ValuationRow, len(codes))
	for _, row := range valuations {
		code := cnlake.BareSymbol(row.Symbol)
		if valuationIndex[code] == nil {
			valuationIndex[code] = map[int64]cnlake.ValuationRow{}
		}
		valuationIndex[code][row.Date()] = row
	}

	out := make(map[string]*stockdb.Quote, len(codes))
	missed := make([]string, 0, 16)
	for _, code := range codes {
		bare := cnlake.BareSymbol(code)
		list := grouped[bare]
		if len(list) == 0 {
			missed = append(missed, bare)
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].TradeDate < list[j].TradeDate })

		last := list[len(list)-1]
		var prevClose float64
		if len(list) >= 2 {
			prevClose = list[len(list)-2].Close
		}
		var valuation *cnlake.ValuationRow
		if found, ok := valuationIndex[bare][last.Date()]; ok {
			valuation = &found
		}
		out[bare] = p.buildQuote(bare, p.name(bare), last, prevClose, valuation, p.shareHistory(bare))
	}
	return out, missed, nil
}

// shareHistory 取某只股票的股本结构历史（带缓存）。
func (p *lakeProvider) shareHistory(code string) []cnlake.ShareStructureRow {
	bare := cnlake.BareSymbol(code)

	p.mu.Lock()
	cached, ok := p.shares[bare]
	p.mu.Unlock()
	if ok {
		return cached
	}

	rows, err := p.lake.ShareStructures(bare)
	if err != nil {
		rows = nil
	}

	p.mu.Lock()
	if p.shares == nil {
		p.shares = map[string][]cnlake.ShareStructureRow{}
	}
	p.shares[bare] = rows
	p.mu.Unlock()
	return rows
}

// sharesFor 找出截至 date 生效的股本。
func sharesFor(rows []cnlake.ShareStructureRow, date int64) (total, float float64, ok bool) {
	for _, row := range rows {
		if row.Date() > date {
			break
		}
		total, float, ok = row.TotalShares, row.FloatShares, true
	}
	return total, float, ok
}

func (p *lakeProvider) DividendFactors(ctx context.Context, code string) ([]stockdb.Factor, error) {
	// 以 corporate_actions 为主：它既有分红明细，也能推出复权因子，
	// 一个接口同时喂饱「前/后复权」和「股息率」两个用途。
	//
	// 不能反过来优先用 adj_factors：那张表只有因子、没有每股派息，
	// 一旦优先，股息率会静默变成 0。
	actions, err := p.lake.CorporateActions(code)
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 corporate_actions 失败：%w", err)
	}
	if len(actions) > 0 {
		out := make([]stockdb.Factor, 0, len(actions))
		cum := 1.0
		for _, action := range actions {
			// 与 free-stockdb「复权」表对齐：mult 是本次除权的股数放大倍数
			mult := 1 + action.BonusRatio + action.TransferRatio
			if action.SplitFactor > 0 {
				mult *= action.SplitFactor
			}
			if mult <= 0 {
				mult = 1
			}
			cum *= mult
			out = append(out, stockdb.Factor{
				Date:  action.Date(),
				Div:   action.CashDividend,
				Give:  action.BonusRatio,
				Trans: action.TransferRatio,
				Mult:  round(mult, 4),
				Cum:   round(cum, 6),
			})
		}
		return out, nil
	}

	// corporate_actions 为空时（有些标的只同步了因子）退回 adj_factors
	return p.adjFactorSeries(code), nil
}

// adjFactorSeries 把 adj_factors 表转成与 free-stockdb「复权」表同构的序列。
// 表里可能同时存在 qfq / hfq 等多种口径，优先取 qfq。
func (p *lakeProvider) adjFactorSeries(code string) []stockdb.Factor {
	rows, err := p.lake.AdjFactors(code)
	if err != nil || len(rows) == 0 {
		return nil
	}

	// 挑一种口径：有 qfq 用 qfq，否则用出现最多的那种
	counts := map[string]int{}
	for _, row := range rows {
		counts[strings.ToLower(row.AdjustType)]++
	}
	chosen := ""
	if _, ok := counts["qfq"]; ok {
		chosen = "qfq"
	} else {
		best := 0
		for kind, count := range counts {
			if count > best || (count == best && kind < chosen) {
				best, chosen = count, kind
			}
		}
	}
	if chosen == "" {
		return nil
	}

	out := make([]stockdb.Factor, 0, len(rows))
	for _, row := range rows {
		if strings.ToLower(row.AdjustType) != chosen {
			continue
		}
		// adj_factors 只给因子，没有分红明细；Div/Give/Trans 留空，
		// 股息率会因此取不到 —— 那种情况下仍建议以 corporate_actions 为准。
		out = append(out, stockdb.Factor{Date: row.Date(), Cum: row.Factor})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func (p *lakeProvider) MoneyFlowRange(ctx context.Context, code string, start, end int) ([]stockdb.MoneyFlow, error) {
	if end <= 0 {
		return nil, nil
	}
	if start <= 0 {
		start = 19900101
	}

	rows, err := p.lake.FundFlows(code, start/10000, end/10000)
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 fund_flow 失败：%w", err)
	}

	out := make([]stockdb.MoneyFlow, 0, len(rows))
	for _, row := range rows {
		date := row.Date()
		if date < int64(start) || date > int64(end) {
			continue
		}
		out = append(out, stockdb.MoneyFlow{
			Date:     date,
			MainNet:  row.MainNetInflow,
			JumboNet: row.SuperLargeNetInflow,
			BigNet:   row.LargeNetInflow,
			MidNet:   row.MediumNetInflow,
			SmallNet: row.SmallNetInflow,
		})
	}
	return out, nil
}

// 数据湖的行业分类体系 -> 界面上的分类名
var industrySystemNames = map[string]string{
	"sw_l1": "申万一级",
	"sw_l2": "申万二级",
	"sw_l3": "申万三级",
	"sw":    "申万",
	"csrc":  "证监会",
	"ths":   "同花顺行业",
}

func (p *lakeProvider) Boards(ctx context.Context) ([]stockdb.Board, error) {
	p.mu.Lock()
	if p.boards != nil {
		cached := p.boards
		p.mu.Unlock()
		return cached, nil
	}
	p.mu.Unlock()

	boards := make([]stockdb.Board, 0, 512)

	// 行业成分
	industries, err := p.lake.IndustryMembers()
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 industry_members 失败：%w", err)
	}
	type boardKey struct{ system, code, name string }
	grouped := map[boardKey][]string{}
	order := make([]boardKey, 0, 128)
	for _, row := range industries {
		key := boardKey{system: row.ClassificationSystem, code: row.IndustryCode, name: row.IndustryName}
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], cnlake.BareSymbol(row.Symbol))
	}
	for _, key := range order {
		category := industrySystemNames[strings.ToLower(key.system)]
		if category == "" {
			category = key.system
		}
		boards = append(boards, stockdb.Board{
			Code:     key.code,
			Name:     key.name,
			Source:   "cnequity",
			Type:     "industry",
			Group:    key.system,
			Category: category,
			Symbols:  grouped[key],
		})
	}

	// 概念/板块成分
	if sectors, sectorErr := p.lake.SectorMembers(); sectorErr == nil {
		type sectorKey struct{ code, name string }
		sectorGroups := map[sectorKey][]string{}
		sectorOrder := make([]sectorKey, 0, 128)
		for _, row := range sectors {
			key := sectorKey{code: row.SectorCode, name: row.SectorName}
			if _, seen := sectorGroups[key]; !seen {
				sectorOrder = append(sectorOrder, key)
			}
			sectorGroups[key] = append(sectorGroups[key], cnlake.BareSymbol(row.Symbol))
		}
		for _, key := range sectorOrder {
			boards = append(boards, stockdb.Board{
				Code:     key.code,
				Name:     key.name,
				Source:   "cnequity",
				Type:     "concept",
				Group:    "concept",
				Category: "概念",
				Symbols:  sectorGroups[key],
			})
		}
	}

	p.mu.Lock()
	p.boards = boards
	p.mu.Unlock()
	return boards, nil
}

// shiftBack 把 YYYYMMDD 往前推 n 个自然日。
func shiftBack(date int, days int) int {
	if date <= 0 {
		return date
	}
	year, month, dayOfMonth := date/10000, (date/100)%100, date%100
	t := time.Date(year, time.Month(month), dayOfMonth, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days)
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// clearCaches 在切换数据源后调用，丢掉按来源缓存的数据。
func (p *lakeProvider) clearCaches() {
	p.mu.Lock()
	p.boards = nil
	p.shares = nil
	p.mu.Unlock()
}
