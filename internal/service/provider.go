package service

import (
	"context"
	"fmt"
	"log/slog"
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
	// shareOnce 让"全量股本索引"只建一次
	shareOnce sync.Once

	// bulk 是**按交易日批量读**的缓存。
	//
	// 数据湖的 `daily_bars` **按交易日分区**（每个日分区约 40KB = 一天的全市场），
	// 所以"一段区间 × 全市场"一次读只要秒级（实测 320 个交易日 / 174 万行 / 1.56s）；
	// 而按代码读 `DailyBars(code, year, year)` 会把区间内**每个日文件**都打开一遍
	// 再按代码过滤（实测 861ms/只，全市场 1h20m）。
	//
	// 所以第一次有人要某段区间时，就把这段区间的**全市场**一次读进来，
	// 后续所有代码直接从内存取 —— **对调用方完全透明**。
	bulkMu   sync.Mutex
	bulkFrom int
	bulkTo   int
	bulkBars map[string][]cnlake.BarRow

	// valuations 是 `code -> 日期 -> 估值行` 的索引。
	//
	// `Valuations(code, ...)` 是**每只股票把整个数据集读一遍再过滤**，
	// 数据集虽小（0.2MB），但 parquet 打开+解码是固定开销，
	// 逐只调用时实测约 99ms/只 —— 比日线还慢。
	// 一次读完建索引后，命中是 O(1)。
	valMu      sync.Mutex
	valuations map[string]map[int64]cnlake.ValuationRow
	valLoaded  bool
}

// valuationFor 取某只股票在 [start, end] 内的估值行（按日期）。
func (p *lakeProvider) valuationFor(code string, start, end int) map[int64]cnlake.ValuationRow {
	p.valMu.Lock()
	defer p.valMu.Unlock()
	if !p.valLoaded {
		rows, err := p.lake.AllValuations()
		if err == nil {
			index := make(map[string]map[int64]cnlake.ValuationRow, 8192)
			for _, row := range rows {
				bare := cnlake.BareSymbol(row.Symbol)
				if bare == "" {
					continue
				}
				byDate := index[bare]
				if byDate == nil {
					byDate = map[int64]cnlake.ValuationRow{}
					index[bare] = byDate
				}
				byDate[row.Date()] = row
			}
			p.valuations = index
			p.valLoaded = true
			slog.Info("数据湖估值索引就绪", "codes", len(index), "rows", len(rows))
		}
	}
	byDate := p.valuations[cnlake.BareSymbol(code)]
	if len(byDate) == 0 {
		return nil
	}
	out := make(map[int64]cnlake.ValuationRow, len(byDate))
	for date, row := range byDate {
		if date < int64(start) || date > int64(end) {
			continue
		}
		out[date] = row
	}
	return out
}

// bulkMaxDays 是允许走批量读的**最长区间**（交易日数）。
//
// 上限的意义是**给内存封顶**：一天约 5500 只股票，
// 400 个交易日约 220 万行。再长就会把整段历史读进内存
// （K 线页可能一次要十年），所以那种情况仍走按代码读。
const bulkMaxDays = 400

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
// barsFor 取某只股票 [start, end] 的日线，**优先走批量缓存**。
//
// 返回的行是 `[start, end]`（调用方还会再按日期过滤一遍，所以这里多给也无妨）。
// 走不了批量就回落到按代码读 —— 结果必须完全一致。
func (p *lakeProvider) barsFor(code string, start, end int) ([]cnlake.BarRow, error) {
	if bars, ok := p.cachedBars(code, start, end); ok {
		return bars, nil
	}

	// 区间够短 → 一次把**全市场**读进来，后续所有代码直接命中内存
	if p.bulkFillAllowed(start, end) {
		if err := p.fillBulk(start, end); err == nil {
			if bars, ok := p.cachedBars(code, start, end); ok {
				return bars, nil
			}
		}
		// 批量失败不算错 —— 回落到按代码读，只是慢
	}

	return p.lake.DailyBars(code, start/10000, end/10000)
}

// cachedBars 命中批量缓存时返回该股票的日线。
func (p *lakeProvider) cachedBars(code string, start, end int) ([]cnlake.BarRow, bool) {
	p.bulkMu.Lock()
	defer p.bulkMu.Unlock()
	if p.bulkBars == nil || start < p.bulkFrom || end > p.bulkTo {
		return nil, false
	}
	bars, ok := p.bulkBars[cnlake.BareSymbol(code)]
	if !ok {
		// 缓存**覆盖了这个区间**但该股票没数据 —— 这也是有效答案
		return nil, true
	}
	return bars, true
}

// bulkFillAllowed 判断这个区间是否适合批量读（按"区间内的交易日数"算）。
func (p *lakeProvider) bulkFillAllowed(start, end int) bool {
	p.bulkMu.Lock()
	covered := p.bulkBars != nil && start >= p.bulkFrom && end <= p.bulkTo
	p.bulkMu.Unlock()
	if covered {
		return false // 已经覆盖，不必再灌
	}
	dates, err := p.lake.DatesInRange(int64(start), int64(end))
	if err != nil {
		return false
	}
	return len(dates) > 0 && len(dates) <= bulkMaxDays
}

// fillBulk 把 [start, end] 的**全市场**日线一次读进内存。
//
// 全程持锁：并发调用时只灌一次，其它 goroutine 等它完成即可
// （读一遍约 1.5 秒，比各自重复读划算得多）。
func (p *lakeProvider) fillBulk(start, end int) error {
	p.bulkMu.Lock()
	defer p.bulkMu.Unlock()
	if p.bulkBars != nil && start >= p.bulkFrom && end <= p.bulkTo {
		return nil
	}

	began := time.Now()
	rows, err := p.lake.DailyBarsInRange(nil, int64(start), int64(end))
	if err != nil {
		return fmt.Errorf("批量读取数据湖 daily_bars 失败：%w", err)
	}

	byCode := make(map[string][]cnlake.BarRow, 8192)
	for _, row := range rows {
		bare := cnlake.BareSymbol(row.Symbol)
		if bare == "" {
			continue
		}
		byCode[bare] = append(byCode[bare], row)
	}

	// 这里**替换**而不是合并：换了区间就重建，
	// 否则缓存会随区间扩张无限增长
	p.bulkBars = byCode
	p.bulkFrom = start
	p.bulkTo = end
	slog.Info("数据湖批量读完成",
		"from", start, "to", end,
		"codes", len(byCode), "rows", len(rows),
		"elapsed", time.Since(began).Round(time.Millisecond))
	return nil
}

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

	// ⚠ 这一行是**唯一**的取数步骤。走批量缓存时它由"扫区间内每个日文件"
	// 变成"一次读全市场 + 内存命中"（实测快 3 个数量级，且逐字段一致）。
	// 下面的估值/股本/前收/过滤/排序**完全不变**，保证输出一致。
	bars, err := p.barsFor(code, start, end)
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 daily_bars 失败：%w", err)
	}

	// 估值表可能缺失（或只有部分年份），缺失时行情照样出，只是没有 PE/PB
	// ⚠ 改成走**一次建好的索引**：原来每只股票都把这个数据集读一遍再过滤，
	// parquet 的固定开销乘以股票数（实测约 99ms/只），比日线还慢。
	byDate := p.valuationFor(code, start, end)

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

	// ⚠ **一次读出全部股本行建索引**，而不是每只扫一遍整个数据集。
	// `ShareStructures(code)` 每次都要读 6.1MB / 26 个分区再过滤，
	// 实测约 84ms/只 —— 在日线和估值都批量化之后，这是最后一个按代码的慢路径。
	p.shareOnce.Do(func() {
		rows, err := p.lake.AllShareStructures()
		if err != nil {
			return
		}
		index := make(map[string][]cnlake.ShareStructureRow, 8192)
		for _, row := range rows {
			code := cnlake.BareSymbol(row.Symbol)
			if code == "" {
				continue
			}
			index[code] = append(index[code], row)
		}
		p.mu.Lock()
		p.shares = index
		p.mu.Unlock()
		slog.Info("数据湖股本索引就绪", "codes", len(index), "rows", len(rows))
	})

	p.mu.Lock()
	cached = p.shares[bare]
	p.mu.Unlock()
	return cached
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
