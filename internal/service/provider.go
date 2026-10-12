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
	"freestock/internal/score"
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
	// DailyBars 只取 OHLCV（评分算指标只需要这些）。
	//
	// **为什么要单独一条路径**：`DailyHistory` 会为**每一根 K 线**组装完整的
	// `stockdb.Quote` —— 估值、市值、股本、PE/PB、换手率、IsST 等，
	// 而 `score.Bar` 只用 Date/Open/High/Low/Close/Volume/Amount 七个字段。
	// 实测组装 Quote 每根 26.2µs，而反过来剥字段只要 47ns：
	// 全市场回填 30 秒里 99% 花在这上面（指标计算只占 0.5%）。
	//
	// 语义必须与 `barsFromQuotes(DailyHistory(...))` **完全一致**：
	// 同样按 [start, end] 过滤、同样丢弃 Close<=0 的行、同样按日期升序。
	DailyBars(ctx context.Context, code string, start, end int) ([]score.Bar, error)
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
	// bulkScored 是 bulkBars 的"已组装成 score.Bar"版本，**与它同步维护**。
	//
	// 为什么需要：`DailyBars` 若每次从 bulkBars 逐行组装，回填每天要重做
	// 窗口内每根行（全市场约 174 万行/天，实测约 110ms/天）。
	// 批量读是按日分区**增量**来的，所以组装也增量做，之后按区间二分子切片。
	bulkScored map[string][]score.Bar
	bulkFilled bool
	bulkBars   map[string][]cnlake.BarRow

	// valuations 是 `code -> 日期 -> 估值行` 的索引。
	//
	// `Valuations(code, ...)` 是**每只股票把整个数据集读一遍再过滤**，
	// 数据集虽小（0.2MB），但 parquet 打开+解码是固定开销，
	// 逐只调用时实测约 99ms/只 —— 比日线还慢。
	// 一次读完建索引后，命中是 O(1)。
	metaOnce sync.Once
	metaErr  error

	valMu      sync.Mutex
	valuations map[string]map[int64]cnlake.ValuationRow
	valLoaded  bool
}

// ⚠ **数据缺口（不是代码问题，也不补假数据）**：
//
// 本机数据湖的 `valuation_metrics` **只有一个分区 `trade_date=2026-10-08`**，
// 也就是**只有最新一天**的 PE / PB / 总市值 / 流通市值。
// （对比：`daily_bars` 有 2612 个日分区、`share_structure` 按年份分区。）
//
// 后果：**历史日期的这四项必然是空的** —— K 线页看历史某天时
// 「股票信息显示不全」就是这个原因。
//
// 这里**不填 0 也不推算**：估值算错比留空危险得多。
// 要补上得让同步侧把历史估值一并落进湖里（属于 cne 项目的事，本次不动）。
//
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

// bulkChunkDays 是**单次**批量读最多覆盖的交易日数（分块大小）。
//
// ⚠ 这里原本是 `bulkMaxDays = 700`，语义是"区间**超过就不许批量读**"。
// 于是长区间会整段退化成逐只读 —— 而代价是量级差：
//
//	批量读（全市场）：1 年 ≈ 1.2 秒，3 年 ≈ 3.4 秒
//	逐只读（每只一次窗口读）：1 年 546ms/只、7 年 2.8s/只
//	→ 全市场 7600 只外推：1 年 1 小时、7 年 6 小时
//
// 组合回测「全量重算区间 2022~2026」的回填窗口约 **1500 个交易日**，
// 恰好超过 700，于是每次都走逐只慢读 —— 这就是"耗时太久"的根因。
//
// 现在改成：**照样批量读，但一次不超过这么多天**。单次读的规模有上限，
// 内存总量由 `TrimBulk`（调用方按滑动窗口裁剪）控制。
const bulkChunkDays = 250

// NewLakeProvider 用数据湖构造行情来源。
func NewLakeProvider(lake *cnlake.Lake) Provider {
	return &lakeProvider{lake: lake}
}

func (p *lakeProvider) Kind() string  { return SourceKindCnLake }
func (p *lakeProvider) Label() string { return "本地数据湖 " + p.lake.Root() }

// ensureMeta 载入并缓存标的名称与资产类型。
func (p *lakeProvider) ensureMeta() error {
	// ⚠ 原来这里是"双重检查锁"，但**只保护了标志、没保护加载**：
	// 并发进来时 N 个 worker 会同时看到 names == nil，然后**各读一遍** instruments。
	// 条件选股/评分都是几十个 worker 一起进来，等于把一次读放大成几十次。
	// 改用 sync.Once：只有一个 goroutine 真正加载，其余等待。
	p.metaOnce.Do(func() {
		rows, err := p.lake.Instruments()
		if err != nil {
			p.metaErr = err
			return
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
	})
	if p.metaErr != nil {
		return p.metaErr
	}
	if p.names == nil {
		return fmt.Errorf("数据湖标的清单为空")
	}
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

// mergeSortedBars 把已排序的 `add` 并入已排序的 `have`，结果仍按日期升序。
//
// ⚠ **不能直接 append。** 分块读只有在"块按日期递增"时才有序，
// 但缓存里可能**已经有更晚的一段**：实测回填时先填了
// `20260825~20261009`（`marketState`/基准先访问），再填
// `20231026~20260824`，直接 append 会让每条切片变成
// "2026 在前、2023 在后" —— 后果有两个，都是静默的：
//
//  1. `quoteRange` 靠升序推导前收盘 → 前收盘取错；
//  2. `TrimBulk` 用二分找裁剪点 → 找不到，裁剪形同失效（实测只丢掉 2017 行）。
//
// 快路径：新块整体晚于已有数据时直接 append（正常向前推进就是这条）。
func mergeSortedBars(have, add []cnlake.BarRow) []cnlake.BarRow {
	if len(have) == 0 {
		return add
	}
	if len(add) == 0 {
		return have
	}
	if have[len(have)-1].TradeDate <= add[0].TradeDate {
		return append(have, add...)
	}
	merged := make([]cnlake.BarRow, 0, len(have)+len(add))
	i, j := 0, 0
	for i < len(have) && j < len(add) {
		if have[i].TradeDate <= add[j].TradeDate {
			merged = append(merged, have[i])
			i++
		} else {
			merged = append(merged, add[j])
			j++
		}
	}
	merged = append(merged, have[i:]...)
	merged = append(merged, add[j:]...)
	return merged
}

// barsForRange 与 `barsFor` 相同，但把结果**裁到 [start, end]**。
//
// 为什么要单独一个：批量缓存命中时 `cachedBars` 返回的是该股票的**整条切片**
// （回填场景约 640 根），而 `DailyBars` 会逐根遍历、且**每根都要查一次股本算换手率**。
// 于是"窗口 640 根、实际只要 320 根"时白做一倍的行级工作 ——
// 全市场 5441 只 × 每天多遍历 320 根 ≈ 170 万行/天，实测就是数据湖比
// stockdb 每天多出的那 150ms（stockdb 那条路是 `sliceBars` 二分子切片，不做遍历）。
//
// ⚠ 只有 `DailyBars` 能用它：`quoteRange` 要用 `list[len(list)-2]` 推导**前收盘**，
// 裁掉 start 之前的那一根会让 `start == end` 时前收变成 0。
func (p *lakeProvider) barsForRange(code string, start, end int) ([]cnlake.BarRow, error) {
	rows, err := p.barsFor(code, start, end)
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	from := sort.Search(len(rows), func(i int) bool { return int(rows[i].Date()) >= start })
	to := sort.Search(len(rows), func(i int) bool { return int(rows[i].Date()) > end })
	if from >= to {
		return nil, nil
	}
	return rows[from:to], nil
}

// mergeSortedScored 与 `mergeSortedBars` 同构，用于已组装的 `score.Bar`。
func mergeSortedScored(have, add []score.Bar) []score.Bar {
	if len(have) == 0 {
		return add
	}
	if len(add) == 0 {
		return have
	}
	if have[len(have)-1].Date <= add[0].Date {
		return append(have, add...)
	}
	merged := make([]score.Bar, 0, len(have)+len(add))
	i, j := 0, 0
	for i < len(have) && j < len(add) {
		if have[i].Date <= add[j].Date {
			merged = append(merged, have[i])
			i++
		} else {
			merged = append(merged, add[j])
			j++
		}
	}
	merged = append(merged, have[i:]...)
	merged = append(merged, add[j:]...)
	return merged
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

// bulkFillAllowed 判断这次请求是否**需要**（且可以）走批量读。
//
// ⚠ 不再按区间长度拒绝：长度只决定"分几块读"（见 bulkChunkDays），
// 不决定"读不读"。拒绝的条件只剩两个：
//   - 现有缓存已经覆盖了这次请求；
//   - 区间内一个日分区都没有。
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
	return len(dates) > 0
}

// TrimBulk 丢弃早于 before 的日线，把内存压回"滑动窗口"大小。
//
// 回填是**按交易日往前推**的：第 i 天只需要 [D_i-320, D_i]，更早的数据
// 再也不会被读到。不裁剪的话，970 天的区间会把约 1300 个交易日 × 5500 只
// 全留在内存里（实测约 600MB）。
//
// 语义安全：只丢 `date < before` 的行，而调用方保证 `before` 不晚于
// 之后任何一次请求的 `start` —— 所以不会丢掉还会被读到的数据。
func (p *lakeProvider) TrimBulk(before int) {
	if before <= 0 {
		return
	}
	p.bulkMu.Lock()
	defer p.bulkMu.Unlock()
	if p.bulkBars == nil || before <= p.bulkFrom {
		return
	}
	dropped := 0
	for bare, bars := range p.bulkBars {
		if len(bars) == 0 {
			delete(p.bulkBars, bare)
			continue
		}
		// 该股票最新的数据都早于裁剪线 → 整条移除（后续读到它是"无数据"，正确）
		if int(bars[len(bars)-1].Date()) < before {
			dropped += len(bars)
			delete(p.bulkBars, bare)
			continue
		}
		if int(bars[0].Date()) >= before {
			continue
		}
		idx := sort.Search(len(bars), func(i int) bool { return int(bars[i].Date()) >= before })
		if idx > 0 {
			dropped += idx
			// 复制而不是重新切片：重新切片会继续引用整个底层数组，
			// 那样"裁剪"就省不下内存，等于白做。
			p.bulkBars[bare] = append([]cnlake.BarRow(nil), bars[idx:]...)
		}
	}
	p.bulkFrom = before
	slog.Info("数据湖批量缓存裁剪",
		"before", before, "droppedRows", dropped, "codes", len(p.bulkBars))
	// bulkScored 必须**同步裁剪**：它和 bulkBars 是同一批数据的两种形态，
	// 只裁一边会让"已组装缓存"比覆盖区间更宽 —— 那样 `cachedScoredBars`
	// 会返回已经不在 bulkFrom 之后的数据，而调用方以为那是当前窗口。
	scoredDropped := 0
	for bare, bars := range p.bulkScored {
		if len(bars) == 0 {
			delete(p.bulkScored, bare)
			continue
		}
		if int(bars[len(bars)-1].Date) < before {
			scoredDropped += len(bars)
			delete(p.bulkScored, bare)
			continue
		}
		if int(bars[0].Date) >= before {
			continue
		}
		idx := sort.Search(len(bars), func(i int) bool { return int(bars[i].Date) >= before })
		if idx > 0 {
			scoredDropped += idx
			p.bulkScored[bare] = append([]score.Bar(nil), bars[idx:]...)
		}
	}
	if scoredDropped > 0 {
		slog.Info("数据湖已组装缓存同步裁剪", "dropped", scoredDropped)
	}
}

// fillBulk 把 [start, end] 的**全市场**日线一次读进内存。
//
// 全程持锁：并发调用时只灌一次，其它 goroutine 等它完成即可
// （读一遍约 1.5 秒，比各自重复读划算得多）。
func (p *lakeProvider) fillBulk(start, end int) error {
	p.bulkMu.Lock()
	defer p.bulkMu.Unlock()

	// 已完全覆盖 → 不用动
	if p.bulkBars != nil && start >= p.bulkFrom && end <= p.bulkTo {
		return nil
	}

	// ⚠ **与已有缓存重叠时，只读新增的那一段，然后合并**。
	//
	// 历史回填是按交易日**逐天往前推**的：第 1 天要 [D0-320, D0]，
	// 第 2 天要 [D0-319, D1]，第 3 天 [D0-318, D2] ……
	// 如果每次"不覆盖就整段重灌 + 替换缓存"，就会变成**每天都重灌一遍**
	// 整个 320 天窗口 —— 这正是组合回测页回填慢的根源。
	//
	// 合并之后，每往后推一天只需读那**一个**新的日分区。
	readFrom, readTo := start, end
	if p.bulkBars != nil && end >= p.bulkFrom && start <= p.bulkTo {
		switch {
		case start < p.bulkFrom:
			readFrom, readTo = start, p.bulkFrom-1 // 只补左边
		case end > p.bulkTo:
			readFrom, readTo = p.bulkTo+1, end // 只补右边
		default:
			return nil
		}
	}

	// **按日分区列表分块读。**
	//
	// 块与块之间日期递增且不重叠，所以"每块内部各代码排好序 → 直接 append"
	// 之后整体仍然有序（quoteRange 依赖这个顺序推导前收盘），
	// 不需要每块都对全量重排。
	dates, err := p.lake.DatesInRange(int64(readFrom), int64(readTo))
	if err != nil {
		return fmt.Errorf("读取数据湖日分区列表失败：%w", err)
	}
	if p.bulkBars == nil {
		p.bulkBars = make(map[string][]cnlake.BarRow, 8192)
	}
	if p.bulkScored == nil {
		p.bulkScored = make(map[string][]score.Bar, 8192)
	}
	if len(dates) == 0 {
		// ⚠ **"这一段没有日分区"不是失败，不能返回 error。**
		//
		// 这是**回测点下去卡住不动**的根因（我自己引入的）：
		//
		// 增量补齐时缺的那一段常常天然为空 —— 比如回填到 20261009、
		// 回测请求到 20261012，缺的就是 [20261010, 20261012]
		//（周末 + 没有数据），一个日分区都没有。
		//
		// 原实现在这种情况下 `DailyBarsInRange` 返回空行，随后**照样把
		// `bulkFrom/bulkTo` 推进到请求的区间**，于是后续每一只都命中缓存。
		// 我改成分块读之后在这里直接 `return error` —— `barsFor` 把它当成
		// "批量读失败"，**逐只回落到 `lake.DailyBars(code, 年, 年)` 慢路径**
		//（实测 546ms/只；4600 只 ≈ 40 分钟，而且中间**一行日志都没有**，
		// 表现就是"卡住不动"）。
		//
		// 正确语义：把覆盖区间推进到请求的区间（"这里没有数据"也是一个有效答案，
		// `cachedBars` 对缓存覆盖范围内查不到的股票返回"无数据"），然后返回。
		if !p.bulkFilled {
			p.bulkFrom, p.bulkTo = readFrom, readTo
			p.bulkFilled = true
		} else {
			if readFrom < p.bulkFrom {
				p.bulkFrom = readFrom
			}
			if readTo > p.bulkTo {
				p.bulkTo = readTo
			}
		}
		slog.Info("数据湖批量读：该段没有日分区，直接推进覆盖区间",
			"read", fmt.Sprintf("%d~%d", readFrom, readTo),
			"covered", fmt.Sprintf("%d~%d", p.bulkFrom, p.bulkTo))
		return nil
	}

	began := time.Now()
	totalRows, blocks := 0, 0
	for offset := 0; offset < len(dates); offset += bulkChunkDays {
		last := offset + bulkChunkDays
		if last > len(dates) {
			last = len(dates)
		}
		chunkFrom, chunkTo := int(dates[offset]), int(dates[last-1])
		blockBegan := time.Now()
		rows, err := p.lake.DailyBarsInRange(nil, int64(chunkFrom), int64(chunkTo))
		if err != nil {
			return fmt.Errorf("批量读取数据湖 daily_bars 失败（%d~%d）：%w", chunkFrom, chunkTo, err)
		}
		chunked := make(map[string][]cnlake.BarRow, 8192)
		for _, row := range rows {
			bare := cnlake.BareSymbol(row.Symbol)
			if bare == "" {
				continue
			}
			chunked[bare] = append(chunked[bare], row)
		}
		for bare, bars := range chunked {
			sort.Slice(bars, func(i, j int) bool { return bars[i].TradeDate < bars[j].TradeDate })
			p.bulkBars[bare] = mergeSortedBars(p.bulkBars[bare], bars)
			// **同步增量组装 score.Bar**：只组装这一块（1 天或 1 个分块），
			// 而不是每天把整个窗口重组装一遍。
			scored := scoredBarsFrom(bars, p.shareHistory(bare))
			p.bulkScored[bare] = mergeSortedScored(p.bulkScored[bare], scored)
		}
		totalRows += len(rows)
		blocks++
		slog.Info("数据湖批量读分块完成",
			"read", fmt.Sprintf("%d~%d", chunkFrom, chunkTo),
			"codes", len(chunked), "rows", len(rows),
			"elapsed", time.Since(blockBegan).Round(time.Millisecond))
	}

	if p.bulkBars == nil || !p.bulkFilled {
		p.bulkFrom, p.bulkTo = readFrom, readTo
		p.bulkFilled = true
	} else {
		if readFrom < p.bulkFrom {
			p.bulkFrom = readFrom
		}
		if readTo > p.bulkTo {
			p.bulkTo = readTo
		}
	}
	slog.Info("数据湖批量读完成",
		"read", fmt.Sprintf("%d~%d", readFrom, readTo),
		"covered", fmt.Sprintf("%d~%d", p.bulkFrom, p.bulkTo),
		"blocks", blocks, "codes", len(p.bulkBars), "rows", totalRows,
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

	// ⚠ 用**按交易日分区**的读法：只打开 [start, end] 内的日分区。
	//
	// `DailyBarsMulti(codes, startYear, endYear)` 会把**整年**的日文件全读一遍
	// 再按代码过滤 —— 股票列表和条件选股每次都走这条路，一个"取最新价"
	// 的请求要扫 250 个文件。而区间读只打开真正需要的那几个。
	bars, err := p.lake.DailyBarsInRange(codes, int64(start), int64(end))
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
		// ⚠ **必须按日期排序**。
		//
		// `AllShareStructures` 不保证顺序（原来的 `ShareStructures(code)`
		// 是排过序再返回的），而 `sharesFor` 依赖"截至 date 的最后一行" ——
		// 顺序一乱就会提前中断、返回错误的股本，进而让换手率与评分都错。
		for code, list := range index {
			sort.Slice(list, func(i, j int) bool { return list[i].Date() < list[j].Date() })
			index[code] = list
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
	// **二分查找"最后一个 <= date"的行**。
	//
	// 原来是线性扫描（遇到第一个 > date 就 break），语义相同 ——
	// rows 已按日期升序（见 shareHistory 建索引处）。
	// CPU profile 里这个函数占 11.5%（每根 K 线都要查一次股本），
	// 从 O(n) 降到 O(log n)。
	i := sort.Search(len(rows), func(i int) bool { return rows[i].Date() > date })
	if i == 0 {
		return 0, 0, false
	}
	last := rows[i-1]
	return last.TotalShares, last.FloatShares, true
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

// ---------------------------------------------------------------------------
// DailyBars：只要 OHLCV 的轻量路径
// ---------------------------------------------------------------------------

// scoredBarsFrom 把已按日期升序的原始日线组装成 `score.Bar`。
//
// **必须只有这一份实现**：`DailyBars` 的即时路径与 `fillBulk` 的增量缓存
// 都调它，否则"过滤规则/换手率算法"两处会漂移，而这种漂移是静默的
// （同一只票走缓存与不走缓存算出不同的评分）。
func scoredBarsFrom(rows []cnlake.BarRow, shares []cnlake.ShareStructureRow) []score.Bar {
	bars := make([]score.Bar, 0, len(rows))
	for _, row := range rows {
		// 与 barsFromQuotes 一致：收盘价无效的行不要
		if row.Close <= 0 {
			continue
		}
		date := row.Date()
		// **换手率必须算出来** —— 评分里 `TurnRankPct` 用的是全市场换手率排名，
		// 少了它结果会变。算法与 buildQuote **逐字一致**：
		//   Turnover = round(Volume / FloatShare * 100, 4)，FloatShare<=0 时为 0。
		turnover := 0.0
		if _, floatShare, ok := sharesFor(shares, date); ok && floatShare > 0 {
			turnover = round(float64(row.Volume)/floatShare*100, 4)
		}
		bars = append(bars, score.Bar{
			Date:     date,
			Open:     row.Open,
			High:     row.High,
			Low:      row.Low,
			Close:    row.Close,
			Volume:   float64(row.Volume),
			Amount:   row.Amount,
			Turnover: turnover,
		})
	}
	if !sort.SliceIsSorted(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date }) {
		sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	}
	return bars
}

// cachedScoredBars 从"已组装的批量缓存"里按区间切一段。
//
// ok=false 表示缓存没覆盖这个区间（调用方需要先填充）；ok=true 且返回空
// 表示"覆盖了、但这只票在该区间没有有效数据"——这与 `cachedBars` 的语义一致。
func (p *lakeProvider) cachedScoredBars(code string, start, end int) ([]score.Bar, bool) {
	p.bulkMu.Lock()
	defer p.bulkMu.Unlock()
	if p.bulkScored == nil || start < p.bulkFrom || end > p.bulkTo {
		return nil, false
	}
	bars := p.bulkScored[cnlake.BareSymbol(code)]
	if len(bars) == 0 {
		return nil, true
	}
	from := sort.Search(len(bars), func(i int) bool { return int(bars[i].Date) >= start })
	to := sort.Search(len(bars), func(i int) bool { return int(bars[i].Date) > end })
	if from >= to {
		return nil, true
	}
	return bars[from:to], true
}

// DailyBars 直接读数据湖的 BarRow，**不组装 Quote**。
//
// 组件 Quote 的每根成本实测 26.2µs，而这里只搬 7 个字段 —— 这是回填
// 从 30 秒级降下来的关键。过滤与排序规则与 barsFromQuotes 保持一致。
func (p *lakeProvider) DailyBars(ctx context.Context, code string, start, end int) ([]score.Bar, error) {
	if end <= 0 {
		end = 20991231
	}
	if start <= 0 {
		start = 19900101
	}

	// **先试"已组装的批量缓存"。**
	//
	// 原来这里每天都要把窗口内每根行重新组装一遍（查股本算换手率 + 建结构体）：
	// 全市场 5441 只 × 320 根 ≈ 174 万行/天，实测就是数据湖比 stockdb
	// 每天多出的约 110ms（stockdb 那条路是 `sliceBars` 二分子切片，零行级工作）。
	// 数据湖是按日分区**增量**读的，所以组装也可以增量做（见 fillBulk）。
	if bars, ok := p.cachedScoredBars(code, start, end); ok {
		return bars, nil
	}

	rows, err := p.barsForRange(code, start, end)
	if err != nil {
		return nil, err
	}
	// 填充之后应当已经覆盖 —— 再试一次，命中就完全不必逐行组装
	if bars, ok := p.cachedScoredBars(code, start, end); ok {
		return bars, nil
	}
	// 股本结构与 quoteRange 走同一份缓存（shareHistory 内部只读一次数据集）
	shares := p.shareHistory(cnlake.BareSymbol(code))
	return scoredBarsFrom(rows, shares), nil
}

// DailyBars 对 free-stockdb 走原路径再剥字段。
//
// 这里没有可省的组装开销（服务端直接返回行情），保留是为了让评分代码
// 只有一条取数路径，不必按数据源分支。
func (p *stockdbProvider) DailyBars(ctx context.Context, code string, start, end int) ([]score.Bar, error) {
	quotes, err := p.DailyHistory(ctx, code, start, end)
	if err != nil {
		return nil, err
	}
	return barsFromQuotes(quotes), nil
}
