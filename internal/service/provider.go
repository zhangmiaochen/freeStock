package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
	// KlineHistory 取**单只股票的全历史**日线（K 线页专用）。
	//
	// **为什么要与 DailyHistory 分开**：数据湖上 `DailyHistory` 是给批量流程
	// （回填 / 评分 / 筛选 / 回测，形态都是"一批代码 × 一段区间"）用的，
	// 内部走"一段区间 × 全市场"的批量缓存；而 K 线是"一只股票 × 全历史"，
	// 走那条路会为了 1396 行把全市场 726 万行灌进内存 ——
	// 实测第一次打开 K 线 11.9 秒、堆峰值 1.5GB。
	// 单只全历史另有"按股票"派生缓存，实测 ~2ms 且不占常驻内存。
	KlineHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error)
	// AdjustQuotes 按复权方式（none / qfq / hfq）折算**原始**行情，只改 OHLC 与昨收。
	//
	// 复权只能在**出口**做一次：KlineHistory 返回原始价，K 线页按用户选择的
	// 前复权/后复权/不复权在这里折算。以前数据湖的 KlineHistory 自己按全局
	// 口径（默认 hfq）复权过一次，GetKline 又折算一次，结果"前复权"显示的是
	// 后复权价（000969 实际 14.81 显示成 104.99，600519 实际 1258 显示成 11804）。
	AdjustQuotes(ctx context.Context, code string, quotes []*stockdb.Quote, mode string) ([]*stockdb.Quote, error)
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
	// TradingDates 返回 [start, end] 内的**交易日**（升序）。
	//
	// 板块区间行情要用它把"N 个交易日"折算成具体日期：
	// 周期是按交易日算的，而取数窗口是自然日，中间隔着周末与长假。
	TradingDates(ctx context.Context, start, end int) ([]int, error)
	// ClosesBefore 批量取"若干交易日 → 代码 → 收盘价"，语义是**不晚于该日**的最近一根。
	//
	// 为什么是"不晚于"而不是"等于"：停牌股在基准日往往没有成交，
	// 而区间涨跌幅要用它停牌前的价格作基准（行情软件的通行口径）。
	// 完全没有数据的代码不会出现在结果里。
	//
	// **为什么不直接给"一段区间的全部日线"**：free-stockdb 的区间查询会把
	// 每条记录的 21 个字段全返回（实测 106 个交易日 = 34KB/只，全市场约 190MB，
	// 16 秒），而它真正便宜的原语是**点查询**（约 320 字节/只，7500 只 1 秒）。
	// 于是这里要的正好是点查询能给的东西：几个日期上的收盘价。
	ClosesBefore(ctx context.Context, codes []string, dates []int) (map[int]map[string]float64, error)
	DividendFactors(ctx context.Context, code string) ([]stockdb.Factor, error)
	MoneyFlowRange(ctx context.Context, code string, start, end int) ([]stockdb.MoneyFlow, error)
	Boards(ctx context.Context) ([]stockdb.Board, error)
}

// miniBar 是只保留**区间计算**所需字段的日线（日期 / 收盘）。
//
// 为什么单独一个类型：板块的「最近 N 个交易日涨跌幅」只需要基准日与最新日的收盘价。
// 数据湖那条路一次扫全市场几十万行，用 `stockdb.Quote` 会多解码 18 个字段、
// 多组装一次股本/换手率，每行多花的时间会直接乘上百万倍。
type miniBar struct {
	Date  int64
	Close float64
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

// KlineHistory 在 free-stockdb 上与 DailyHistory 是同一条路：
// 它是服务端按代码查的键查询，本来就不存在"扫全表"的问题。
func (p *stockdbProvider) KlineHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.client.DailyHistory(ctx, code, start, end)
}

// AdjustQuotes 用 free-stockdb 的复权表折算（Cum 即后复权因子）。
func (p *stockdbProvider) AdjustQuotes(ctx context.Context, code string, quotes []*stockdb.Quote, mode string) ([]*stockdb.Quote, error) {
	factors, err := p.DividendFactors(ctx, code)
	if err != nil {
		return nil, err
	}
	return adjustQuotes(quotes, factors, mode), nil
}

// TradingDates 用两只"从不停牌"的活跃股票推断交易日历。
//
// free-stockdb 没有交易日历表，而 `keys 日k key:000001 N` 能一次拿回该股全部日期键。
// 取两只并集是为了防止某一天恰好只有其中一只停牌 —— 那样周期边界会整体错一天。
func (p *stockdbProvider) TradingDates(ctx context.Context, start, end int) ([]int, error) {
	seen := make(map[int]struct{}, 512)
	var lastErr error

	for _, code := range tradeDateProbes {
		list, err := p.client.QuoteRangeDesc(ctx, code, start, end)
		if err != nil {
			lastErr = err
			continue
		}
		for _, quote := range list {
			if quote == nil {
				continue
			}
			date := int(quote.Date)
			if date < start || date > end {
				continue
			}
			seen[date] = struct{}{}
		}
	}

	if len(seen) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("读取 %d~%d 的交易日失败：%w", start, end, lastErr)
		}
		return nil, fmt.Errorf("在 %d~%d 内没有找到任何交易日（日 K 数据可能未同步）", start, end)
	}

	dates := make([]int, 0, len(seen))
	for date := range seen {
		dates = append(dates, date)
	}
	sort.Ints(dates)
	return dates, nil
}

// closesLookbackDays 是"某天没成交时往前找最近一根"的回看自然日数。
//
// 与 `QuotePoint` 的窗口一致（长期停牌的票找不到基准价时，它这个周期就不参与统计，
// 覆盖率会如实反映出来）。
const closesLookbackDays = 120

// ClosesBefore 对每个交易日做一次**点查询**批量取回收盘价。
//
// 为什么不是区间查询：free-stockdb 的区间查询会把每条记录的 21 个字段全返回，
// 实测 106 个交易日 = 34KB/只（全市场 190MB、16 秒），而点查询只有 320 字节
// （7500 只 1 秒）。周期只需要几个日期上的收盘价，点查询正好。
func (p *stockdbProvider) ClosesBefore(ctx context.Context, codes []string, dates []int) (map[int]map[string]float64, error) {
	out := make(map[int]map[string]float64, len(dates))
	if len(codes) == 0 || len(dates) == 0 {
		return out, nil
	}

	var lastErr error
	for _, date := range dates {
		quotes, err := p.LatestQuotes(ctx, codes, date)
		if err != nil {
			lastErr = err
		}

		byCode := make(map[string]float64, len(quotes))
		for code, quote := range quotes {
			if quote != nil && quote.Close > 0 {
				byCode[code] = quote.Close
			}
		}

		// 该日没有记录的（停牌 / 未上市 / 退市）：回溯取"不晚于该日的最近一根"。
		// 区间查询是倒序的，第一条就是最近的一根。
		misses := make([]string, 0, 64)
		for _, code := range codes {
			if _, ok := byCode[code]; !ok {
				misses = append(misses, code)
			}
		}
		if len(misses) > 0 {
			windowStart := shiftBack(date, closesLookbackDays)
			var mu sync.Mutex
			runWorkers(ctx, fallbackWorkers, len(misses), func(i int) {
				list, err := p.client.QuoteRangeDesc(ctx, misses[i], windowStart, date)
				if err != nil || len(list) == 0 || list[0] == nil || list[0].Close <= 0 {
					return
				}
				mu.Lock()
				byCode[misses[i]] = list[0].Close
				mu.Unlock()
			})
		}

		out[date] = byCode
		slog.Debug("基准日收盘价就绪", "date", date, "codes", len(byCode), "misses", len(misses))
	}
	return out, lastErr
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

	// retryCache 是"没有近期行情的代码 → 历史最后几根 K 线"的缓存。
	//
	// 见 retryQuotes：这一步要扫整个 daily_bars（本机 2614 个日分区、约 4s），
	// 而它 10 分钟后就会被全量列表重建再问一次。缓存按交易日失效，
	// 切换数据源时整个 provider 会被换掉，所以不需要额外清理。
	retryMu    sync.Mutex
	retryCache *retryQuoteCache

	// symbolBars 是「一个文件 = 一只股票」的派生缓存（可选，见 cnlake.SymbolBarsCache）。
	//
	// 它解决的是**单只股票全历史**这类查询：daily_bars 按交易日分区，
	// 直读要打开全部 2614 个分区（实测 3.97s），走全市场批量缓存又会灌进
	// 1.5GB 内存（实测 11.9s）——而 K 线只需要一只股票的 1396 行。
	symbolMu   sync.Mutex
	symbolBars *cnlake.SymbolBarsCache
	// 数据湖当前水位（最后一个有日线的交易日）缓存
	symbolLatest   int64
	symbolLatestAt time.Time
	// 重建失败后的退避时刻（零值表示没失败过）
	symbolBuildRetryAt time.Time
	// 下一次允许重建的最早时刻（重建限流，见 symbolBuildCooldown）
	symbolBuildNextAt time.Time
}

// retryCacheTTL 是"不限起点补抓"结果的存活时间。
//
// 取 6 小时：它是**历史** K 线（不会再变），交易日一变（cache.date 不等）立刻失效，
// 所以长一点只赚不赔。
const retryCacheTTL = 6 * time.Hour

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

// ---------------------------------------------------------------------------
// 「按股票」日线派生缓存
// ---------------------------------------------------------------------------

// symbolLatestTTL 是"数据湖当前分区水位"的缓存时间。
//
// 取它要列 daily_bars 的 2614 个分区目录（实测 26~350ms，取决于文件系统冷热）。
// 取 1 分钟：它决定"缓存是不是已经见到全部分区"（股票列表快路的前提），
// 短一点只会多几次目录列举，不会让结果变旧 —— 数据本身的新旧由指纹与现读保证。
const symbolLatestTTL = time.Minute

// symbolBuildRetryDelay 是重建失败后的退避时间（见 EnsureSymbolBars）。
const symbolBuildRetryDelay = 10 * time.Minute

// symbolBuildCooldown 是两次重建之间的最小间隔。
//
// 数据湖盘中是**边写边变**的：指纹随时可能变。没有这个限流，"发现指纹变了
// 就重建"会退化成一直重建。冷却期内查询走直读（慢但对），数据不会因此变旧。
const symbolBuildCooldown = 2 * time.Minute

// symbolCacheState 判断缓存当前能不能用、以及是不是已经覆盖到最新。
//
// 返回：
//   - manifest：缓存清单（可能为零值）；
//   - trusted：**正确性** —— 缓存里那段"冻结历史"的源文件指纹没变，
//     可以拿它当源头用；
//   - covered：**完整性** —— 缓存已经见到数据湖当前全部分区。
//     "缓存里没有某只股票的文件 = 它整段历史都没有 K 线"这个推断需要它。
func (p *lakeProvider) symbolCacheState() (cnlake.SymbolBarsManifest, bool, bool) {
	cache := p.symbolCache()
	if cache == nil {
		return cnlake.SymbolBarsManifest{}, false, false
	}
	manifest, ok := cache.Manifest()
	if !ok {
		return manifest, false, false
	}
	covered := manifest.LakeRoot == p.lake.Root() && manifest.SourceThrough >= p.lakeLatestDate()

	// ⚠ **每次读之前都重算指纹**：补跑历史、重算某一天、盘中追加、
	// 甚至删了重写，都会让文件的 size/mtime 变化。指纹不一致就说明缓存里
	// 那份复制品可能已经不是源头的样子 —— 这次直读，并挂后台重建。
	//
	// 代价实测：2614 个分区文件一次 (路径,大小,mtime) 哈希 —— 冷启动首次
	// 约 238ms（文件系统元数据还没进缓存），之后约 20~45ms。
	// 这是"缓存里永远是最新的"所付的钱：K 线首开因此从 25ms 变成 ~70ms。
	signature, err := p.lake.SourceSignature(manifest.SourceThrough)
	if err != nil {
		slog.Warn("计算数据湖源分区指纹失败，本次不用缓存", "err", err)
		return manifest, false, covered
	}
	return manifest, signature == manifest.Signature, covered
}

// SetSymbolBarsCache 挂上「按股票」派生缓存（nil = 禁用，退回直读）。
func (p *lakeProvider) SetSymbolBarsCache(cache *cnlake.SymbolBarsCache) {
	p.symbolMu.Lock()
	p.symbolBars = cache
	p.symbolMu.Unlock()
}

// symbolCache 返回当前挂着的缓存（可能为 nil）。
func (p *lakeProvider) symbolCache() *cnlake.SymbolBarsCache {
	p.symbolMu.Lock()
	defer p.symbolMu.Unlock()
	return p.symbolBars
}

// lakeLatestDate 返回数据湖最后一个**有日线**的交易日（YYYYMMDD，带缓存）。
func (p *lakeProvider) lakeLatestDate() int64 {
	p.symbolMu.Lock()
	cached, at := p.symbolLatest, p.symbolLatestAt
	p.symbolMu.Unlock()
	if cached > 0 && time.Since(at) < symbolLatestTTL {
		return cached
	}

	dates, err := p.lake.BarPartitionDates()
	if err != nil || len(dates) == 0 {
		return cached
	}
	latest := dates[len(dates)-1]
	p.symbolMu.Lock()
	p.symbolLatest, p.symbolLatestAt = latest, time.Now()
	p.symbolMu.Unlock()
	return latest
}

// EnsureSymbolBars 在缓存缺失/不可信/没覆盖到最新时**后台**重建一次。
//
// 返回 true 表示缓存现在**既可信又已覆盖到数据湖最新分区**；
// 返回 false 表示还没好 —— 调用方要么走直读、要么用"缓存 + 补尾"。
func (p *lakeProvider) EnsureSymbolBars() bool {
	_, ok := p.symbolCacheReady()
	return ok
}

// symbolCacheReady 返回"可信且已覆盖到最新"的缓存清单；不满足时挂后台重建。
func (p *lakeProvider) symbolCacheReady() (cnlake.SymbolBarsManifest, bool) {
	manifest, trusted, covered := p.symbolCacheState()
	if trusted && covered {
		return manifest, true
	}
	p.triggerSymbolBuild()
	return manifest, false
}

// triggerSymbolBuild 启动一次后台重建（限流 + 失败退避）。
func (p *lakeProvider) triggerSymbolBuild() {
	cache := p.symbolCache()
	if cache == nil {
		return
	}

	p.symbolMu.Lock()
	retryAt := p.symbolBuildRetryAt
	nextAllowed := p.symbolBuildNextAt
	p.symbolMu.Unlock()
	now := time.Now()
	// 失败退避（磁盘满、权限…）与"刚重建完又立刻过期"的限流。
	//
	// 限流是必需的：数据湖在盘中是**边写边变**的，若每次查询都发现指纹变了
	// 就重跑一次 30 秒的重建，会变成一直重建。所以一次重建之后至少隔
	// symbolBuildCooldown 才允许下一次 —— 这段时间查询走直读（慢但对）。
	if now.Before(retryAt) || now.Before(nextAllowed) {
		return
	}

	// "同一个缓存目录只重建一次"这道闸在 cnlake 里：一个进程可能有多个
	// provider（切换数据源、测试里每个用例一个服务），不能让它们同时开跑
	// 几十秒的重建、一起抢磁盘、一起写 staging。
	if !cache.AcquireBuild() {
		return
	}
	go func() {
		defer cache.ReleaseBuild()
		began := time.Now()
		manifest, err := cache.Build(p.lake)
		p.symbolMu.Lock()
		p.symbolBuildNextAt = time.Now().Add(symbolBuildCooldown)
		if err != nil {
			// 重建失败不影响取数：查询会走直读，只是慢
			p.symbolBuildRetryAt = time.Now().Add(symbolBuildRetryDelay)
		} else {
			p.symbolBuildRetryAt = time.Time{}
		}
		p.symbolMu.Unlock()
		if err != nil {
			slog.Warn("按股票日线缓存重建失败（查询会退回直读）", "err", err, "retryAfter", symbolBuildRetryDelay)
			return
		}
		// 数据湖在构建期间又长了也不要紧：水位记的是构建时那个分区水位，
		// 读取时会把水位之后的交易日补上
		slog.Info("按股票日线缓存就绪",
			"symbols", manifest.Symbols, "rows", manifest.Rows,
			"latest", manifest.LatestDate, "sourceThrough", manifest.SourceThrough,
			"sourceFiles", manifest.SourceFiles,
			"elapsed", time.Since(began).Round(time.Millisecond).String())
	}()
}

// InvalidateSymbolBars 让缓存立刻失效并触发一次重建。
//
// 数据更新完成后调用它，省掉"等下一次查询才发现源头变了"的空窗期。
func (p *lakeProvider) InvalidateSymbolBars() {
	p.symbolMu.Lock()
	// 清掉退避与限流：更新刚结束，现在重建正是时候
	p.symbolBuildRetryAt = time.Time{}
	p.symbolBuildNextAt = time.Time{}
	p.symbolMu.Unlock()
	p.triggerSymbolBuild()
}

// symbolBarsFromCache 从**已经校验过指纹**的缓存里取一只股票的日线。
//
// 规则：冻结段（<= manifest.LatestDate）用缓存，之后**一律从日分区现读**。
// 于是"盘中追加、当天被重算、新交易日"都不需要等重建就能看到最新数据。
func (p *lakeProvider) symbolBarsFromCache(manifest cnlake.SymbolBarsManifest, code string, end int) ([]cnlake.BarRow, bool, error) {
	rows, ok, err := p.symbolCache().Read(code)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if end > 0 && manifest.LatestDate < int64(end) {
		tail, err := p.lake.DailyBarsInRange([]string{code}, manifest.LatestDate+1, int64(end))
		if err != nil {
			return nil, false, err
		}
		for _, row := range tail {
			if row.Date() > manifest.LatestDate {
				rows = append(rows, row)
			}
		}
	}
	return rows, true, nil
}

// symbolBarsFor 取一只股票的**全部**日线：缓存里那段冻结历史 + 水位之后现读。
//
// 返回 ok=false 表示缓存这次不能用（没启用、没建过、这只票在缓存里没有、
// 或者**源头指纹已经变了**），调用方需要走直读 —— 那是"慢但正确"的路。
func (p *lakeProvider) symbolBarsFor(code string, end int) ([]cnlake.BarRow, bool, error) {
	if p.symbolCache() == nil {
		return nil, false, nil
	}
	manifest, trusted, covered := p.symbolCacheState()
	if !trusted || !covered {
		// 源头变了 / 缓存没跟上：挂后台重建；这次不信任复制品
		p.triggerSymbolBuild()
	}
	if !trusted {
		return nil, false, nil
	}
	return p.symbolBarsFromCache(manifest, code, end)
}

// symbolQuotesFromCache 用「按股票」缓存回答"这些代码不晚于 end 的最后一根行情"。
//
// 与 `bulkQuotes` 的语义完全一致（都是取区间内最后一根、前收取倒数第二根、
// 估值按最后一根的日期查），区别只是日线从缓存来 —— 于是不再需要扫全表。
//
// 调用方必须先把"可信且已覆盖到最新"这份清单传进来（见 symbolCacheReady）：
// 指纹校验每次查询只做一次，不能在这 2000+ 个代码的循环里反复做。
func (p *lakeProvider) symbolQuotesFromCache(manifest cnlake.SymbolBarsManifest, codes []string, end int) (map[string]*stockdb.Quote, []string, error) {
	// 一次要问两千多个代码，用"目录索引"而不是逐个 stat（Windows 上差一个数量级）
	known := p.symbolCache().Codes()
	if known == nil {
		return nil, nil, fmt.Errorf("按股票缓存的代码索引不可用")
	}

	out := make(map[string]*stockdb.Quote, len(codes))
	missed := make([]string, 0, 16)
	for _, code := range codes {
		bare := cnlake.BareSymbol(code)
		if _, ok := known[bare]; !ok {
			// 缓存里没有这个文件 = **整段历史里一根 K 线都没有**
			//（这个推断要求缓存覆盖到最新，调用方已经保证）
			missed = append(missed, bare)
			continue
		}
		rows, ok, err := p.symbolBarsFromCache(manifest, bare, end)
		if err != nil {
			return nil, nil, err
		}
		if !ok || len(rows) == 0 {
			missed = append(missed, bare)
			continue
		}
		last := rows[len(rows)-1]
		var prevClose float64
		if len(rows) >= 2 {
			prevClose = rows[len(rows)-2].Close
		}
		var valuation *cnlake.ValuationRow
		if found, ok := p.valuationFor(bare, 0, end)[last.Date()]; ok {
			valuation = &found
		}
		// rows 是"不晚于 end 的全部日线"（升序），末根就是当日，
		// 前面那些正好是量比分母要的前 N 日
		out[bare] = p.buildQuote(
			bare, p.name(bare), last, prevClose, valuation,
			p.shareHistory(bare), priorVolumes(rows))
	}
	return out, missed, nil
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
	// 单只股票的点查询优先走「按股票」派生缓存：它给的是整条序列，
	// 最后一根天然就是"不晚于 date 的最近一根"。
	//
	// 为什么值得单开一条：窗口读（下面那条路）会触发"**全市场 × 120 天**"
	// 的批量灌入 —— 实测 0.5~6 秒，而调用方只是想知道"这只票最后收在哪"。
	// 只有 `000001/000016/000905/000688` 这类"股票与指数同名"的裸码会走到
	// 这里（每次进程只算一次），但它们都是常用代码（平安银行就是 000001）。
	if bars, ok, err := p.symbolBarsFor(code, date); err == nil && ok {
		quotes := p.assembleQuotes(code, bars, 0, date, false)
		if len(quotes) == 0 {
			return nil, nil
		}
		// 最后一根带着正确的昨收
		return quotes[len(quotes)-1], nil
	}

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

// DailyHistory 取一只股票的日线区间（**批量流程共用**：回填 / 评分 / 筛选 / 回测）。
//
// ⚠ 它走的是"一段区间 × 全市场"的批量缓存（`barsFor`），因为调用方总是
// "一批代码 × 一段区间"。**K 线页不要用它** —— 那是"一只股票 × 全历史"，
// 用批量缓存会为了 1396 行把 726 万行灌进内存（实测 11.9s / 堆 1.5GB）。
// 单只全历史请用 `KlineHistory`。
func (p *lakeProvider) DailyHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	return p.quoteRange(code, start, end, false)
}

// KlineHistory 取**单只股票的全历史**日线（K 线页专用）。
//
// 走「按股票」派生缓存：一个文件就是一只股票的全部日线，实测 ~2ms，
// 且不占常驻内存。缓存没建好时退化成"只读这一只股票"的区间读
// （3.97s，比灌全市场的 11.9s 快 3 倍且不吃内存），同时把它挂到后台去建。
func (p *lakeProvider) KlineHistory(ctx context.Context, code string, start, end int) ([]*stockdb.Quote, error) {
	if err := p.ensureMeta(); err != nil {
		return nil, err
	}
	if end <= 0 {
		end = 20991231
	}
	if start <= 0 {
		start = 19900101
	}

	// 缓存过期/缺失时把重建挂到后台（非阻塞）
	p.EnsureSymbolBars()

	bars, ok, err := p.symbolBarsFor(code, end)
	if err != nil {
		slog.Warn("按股票日线缓存读取失败，退回直读", "code", code, "err", err)
		ok = false
	}
	if !ok {
		bars, err = p.lake.DailyBarsInRange([]string{code}, int64(start), int64(end))
		if err != nil {
			return nil, fmt.Errorf("读取数据湖 daily_bars 失败：%w", err)
		}
	}
	// ⚠ 这里**必须返回原始价**：K 线页支持用户切换前复权/后复权/不复权，
	// 复权统一在 `AdjustQuotes` 里按请求做一次。以前在这里按全局口径
	// （默认 hfq）复权，GetKline 又折算一次，导致"前复权"显示成后复权价。
	return p.assembleQuotes(code, bars, start, end, false), nil
}

// AdjustQuotes 用数据湖的复权因子（`meta/adj_factors_cache` 的 hfq 阶梯）折算。
//
// 因子文件缺失时 `AdjScale` 返回 1，等价于不复权 —— 与既有约定一致。
func (p *lakeProvider) AdjustQuotes(ctx context.Context, code string, quotes []*stockdb.Quote, mode string) ([]*stockdb.Quote, error) {
	if p.lake == nil || len(quotes) == 0 {
		return quotes, nil
	}
	adj := cnlake.ParseAdjMode(mode)
	if adj == cnlake.AdjNone {
		return quotes, nil
	}

	out := make([]*stockdb.Quote, len(quotes))
	bare := cnlake.BareSymbol(code)
	for i, quote := range quotes {
		copied := *quote
		scale := p.lake.AdjScale(bare, int64(quote.Date), adj)
		if scale > 0 && scale != 1 {
			copied.Open = round4(copied.Open * scale)
			copied.High = round4(copied.High * scale)
			copied.Low = round4(copied.Low * scale)
			copied.Close = round4(copied.Close * scale)
			if copied.PreClose > 0 {
				copied.PreClose = round4(copied.PreClose * scale)
			}
		}
		out[i] = &copied
	}
	return out, nil
}

// TradingDates 直接读数据湖的日分区目录名（`trade_date=2026-10-09`）。
//
// ⚠ 数据湖的 `trading_calendar` 是**预生成**的，可能跑在 K 线前面
// （见 provider.go 顶部关于数据缺口的说明），所以这里用"实际有日线的日期"
// （`DatesInRange` 读的就是 daily_bars 的分区），而不是日历表 ——
// 否则周期基准日会落到一个还没有行情的日期上。
func (p *lakeProvider) TradingDates(ctx context.Context, start, end int) ([]int, error) {
	if start <= 0 {
		start = 19900101
	}
	if end <= 0 {
		end = 20991231
	}
	dates, err := p.lake.DatesInRange(int64(start), int64(end))
	if err != nil {
		return nil, fmt.Errorf("读取数据湖日分区列表失败：%w", err)
	}
	out := make([]int, 0, len(dates))
	for _, date := range dates {
		out = append(out, int(date))
	}
	return out, nil
}

// ClosesBefore 一次扫完区间内的日分区，再对每个基准日取"不晚于该日的最后一根"。
//
// 数据湖与 free-stockdb 的取数成本结构**正好相反**：它按交易日分区，
// 所以"一批股票 × 一段区间"只该扫一遍分区（实测 250 个交易日全市场约 1.5 秒），
// 而逐只按代码读会把同一批分区重复打开几千次（1.75 秒/只）。
func (p *lakeProvider) ClosesBefore(ctx context.Context, codes []string, dates []int) (map[int]map[string]float64, error) {
	out := make(map[int]map[string]float64, len(dates))
	if len(codes) == 0 || len(dates) == 0 {
		return out, nil
	}

	// 区间要往前留出"停牌兜底"的余量：基准日当天没成交的票要往前找
	sorted := append([]int(nil), dates...)
	sort.Ints(sorted)
	from := shiftBack(sorted[0], closesLookbackDays)

	bars, err := p.dailyBarsMulti(codes, from, sorted[len(sorted)-1])
	if err != nil {
		return nil, err
	}

	for _, date := range dates {
		out[date] = make(map[string]float64, len(codes))
	}
	for code, list := range bars {
		for _, date := range sorted {
			idx := sort.Search(len(list), func(i int) bool { return list[i].Date > int64(date) })
			if idx == 0 {
				continue // 该日之前这根票还没有数据
			}
			out[date][code] = list[idx-1].Close
		}
	}
	return out, nil
}

// dailyBarsMulti 一次扫完区间内的日分区，取回这一批股票的收盘价序列。
//
// `cnlake.Lake.DailyBarsInRange` 返回的行按 (交易日, 代码) 升序，
// 所以同一只股票内部天然按日期升序 —— 不需要再排序。
func (p *lakeProvider) dailyBarsMulti(codes []string, start, end int) (map[string][]miniBar, error) {
	if start <= 0 {
		start = 19900101
	}
	if end <= 0 {
		end = 20991231
	}

	rows, err := p.lake.DailyBarsInRange(codes, int64(start), int64(end))
	if err != nil {
		return nil, fmt.Errorf("读取数据湖 daily_bars 失败：%w", err)
	}

	out := make(map[string][]miniBar, len(codes))
	for _, row := range rows {
		// 与 barsFromQuotes / scoredBarsFrom 一致：收盘价无效的行不要
		if row.Close <= 0 {
			continue
		}
		date := row.Date()
		if date < int64(start) || date > int64(end) {
			continue
		}
		bare := cnlake.BareSymbol(row.Symbol)
		if bare == "" {
			continue
		}
		out[bare] = append(out[bare], miniBar{Date: date, Close: row.Close})
	}
	return out, nil
}

// volumeRatioDays 是量比的回看窗口：**前 5 个交易日**。
//
// 与行情软件（同花顺 / 通达信）的日线量比口径一致。
//
// ⚠ 分母**不含当日**。量比的定义是"当日每分钟成交量 ÷ 过去 5 个交易日每分钟成交量"，
// 其中"过去 5 日"按行业惯例**不含当日** —— 含进去等于把今天算进自己的基准，
// 会把倍数系统性压低（放量日尤其明显）。
const volumeRatioDays = 5

// volumeRatio 算量比：`当日成交量 ÷ 前 N 个交易日的平均成交量`。
//
// 两个刻意的边界：
//
//   - 分母**不含当日**（只取 `prior` 里的那几根），理由见 volumeRatioDays；
//   - 前 N 日不足（次新股、长期停牌、刚入库）时返回 **0**，而不是拿不足的样本
//     硬算。界面把 0 渲染成「—」，那是"算不出来"，与"量比是 0 倍"是两件事
//     —— 后者在真实行情里不可能出现（成交量为 0 的股票本来就不会有量比）。
//
// 纯函数，方便单测；三个调用点（按股票缓存 / 全市场批量 / 区间）共用它，
// 这样三条取数路径给出的量比不会互相打架。
func volumeRatio(latestVolume float64, prior []float64) float64 {
	if len(prior) < volumeRatioDays || latestVolume <= 0 {
		return 0
	}
	// 取**紧邻当日**的 N 个（prior 按日期升序，末尾就是昨天）
	start := len(prior) - volumeRatioDays
	sum := 0.0
	for _, v := range prior[start:] {
		sum += v
	}
	avg := sum / float64(volumeRatioDays)
	if avg <= 0 {
		return 0
	}
	return round(latestVolume/avg, 4)
}

// buildQuote 把一根 K 线 + 估值 + 股本组装成行情。
// 数据湖的 daily_bars 只有 OHLCV，其余字段都在别处或需要推导。
//
// `priorVolumes` 是**该日期之前**的成交量（按日期升序），用来算量比；
// 传 nil 表示调用方没有历史，量比记 0（界面显示「—」）。
func (p *lakeProvider) buildQuote(bare, name string, bar cnlake.BarRow, prevClose float64, valuation *cnlake.ValuationRow, shares []cnlake.ShareStructureRow, priorVolumes []float64) *stockdb.Quote {
	volume := float64(bar.Volume)
	quote := &stockdb.Quote{
		Date:     bar.Date(),
		Code:     bare,
		Name:     name,
		Open:     bar.Open,
		High:     bar.High,
		Low:      bar.Low,
		Close:    bar.Close,
		Volume:   volume,
		Amount:   bar.Amount,
		IsST:     strings.Contains(strings.ToUpper(name), "ST"),
		PreClose: prevClose,
		// ⚠ 数据湖的 daily_bars **没有 vol_ratio 列**，必须自己按前 5 日均量推导。
		// 早先这里漏了，于是"本地数据湖"模式下 K 线页的量比恒为空 ——
		// 而换回 free-stockdb 就正常，很容易被当成"数据缺失"而不是"没算"。
		VolRatio: volumeRatio(volume, priorVolumes),
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
	// **诊断日志**：把"请求区间 / 已有覆盖 / 实际要补的段"完整打出来。
	//
	// 为什么需要：`cachedBars` 在"缓存覆盖了、但这只票没数据"时会返回
	// `(nil, true)` —— 调用方把 5800 只票**瞬间**判成"无数据"（实测 7.7µs/只，
	// 每天 45ms，`scored=146 skipped=5675`）。那说明**覆盖标记被推到了
	// 实际没有读到数据的地方**，而推进覆盖标记的只有下面两条路。
	// 这条日志 + 空段分支那条 Warn 一起，能把"哪次调用推坏的"直接指出来。
	slog.Info("数据湖批量读开始",
		"请求", fmt.Sprintf("%d~%d", start, end),
		"已有覆盖", fmt.Sprintf("%d~%d(filled=%v)", p.bulkFrom, p.bulkTo, p.bulkFilled),
		"要补的段", fmt.Sprintf("%d~%d", readFrom, readTo))

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
		before := fmt.Sprintf("%d~%d", p.bulkFrom, p.bulkTo)
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
		// ⚠ 这条 Warn 是**诊断用**：它一旦出现，就说明"覆盖标记被推进到了
		// 一个没有读到任何数据的区间" —— 之后 5800 只票会被 `cachedBars`
		// 以 7.7µs/只的速度**瞬间**判成"无数据"（症状：`scored=146 skipped=5675`）。
		// 把 before/after 与请求区间都打出来，就能定位是哪次调用推坏的。
		slog.Warn("⚠ 数据湖批量读：该段没有日分区，却推进了覆盖区间（后续会被判成「无数据」）",
			"请求区间", fmt.Sprintf("%d~%d", start, end),
			"要补的段", fmt.Sprintf("%d~%d", readFrom, readTo),
			"覆盖(前)", before,
			"覆盖(后)", fmt.Sprintf("%d~%d", p.bulkFrom, p.bulkTo))
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
			scored := scoredBarsFrom(p.adjustBars(bare, bars), p.shareHistory(bare))
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

	// **诊断**：区间里有日分区、却一行都没读出来 —— 这同样是"覆盖标记被推进到
	// 没有数据的地方"的信号（会让后续所有代码被判成「无数据」）。
	// 正常读取绝不会 0 行：日分区存在就意味着里面有行。
	if totalRows == 0 {
		slog.Warn("⚠ 数据湖批量读读到 0 行，却推进了覆盖区间（后续会被判成「无数据」）",
			"请求区间", fmt.Sprintf("%d~%d", start, end),
			"实际读的段", fmt.Sprintf("%d~%d", readFrom, readTo),
			"分区块数", blocks, "分区数", len(dates),
			"覆盖(后)", fmt.Sprintf("%d~%d", p.bulkFrom, p.bulkTo))
	}
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
	// 与指标路径用**同一个复权口径**：行情(Quote)与评分(score.Bar)必须同源，
	// 否则 `DailyBars` 与 `DailyHistory` 的等价性测试会失败，K 线与指标也会互相打架。
	// 只复权价格；成交量/成交额保持原始（真实成交口径）。
	bars = p.adjustBars(cnlake.BareSymbol(code), bars)
	return p.assembleQuotes(code, bars, start, end, desc), nil
}

// priorVolumes 把"最后一根之前"的成交量取出来（升序），供量比使用。
//
// 三条取数路径手里都有完整的一段日线，末根是当日、前面是历史；
// 抽成一个小函数是为了让三处口径完全一致。
func priorVolumes(bars []cnlake.BarRow) []float64 {
	if len(bars) < 2 {
		return nil
	}
	out := make([]float64, 0, len(bars)-1)
	for _, bar := range bars[:len(bars)-1] {
		out = append(out, float64(bar.Volume))
	}
	return out
}

// assembleQuotes 把（已按日期升序的）日线组装成对外行情。
//
// 抽出来是为了让"按股票缓存"和"全市场批量缓存"两条取数路线**共用同一段组装**：
// 估值、股本、前收、过滤、排序只要有一处不同，两条路就会给出不一样的 K 线。
func (p *lakeProvider) assembleQuotes(code string, bars []cnlake.BarRow, start, end int, desc bool) []*stockdb.Quote {
	// 估值表可能缺失（或只有部分年份），缺失时行情照样出，只是没有 PE/PB
	// ⚠ 改成走**一次建好的索引**：原来每只股票都把这个数据集读一遍再过滤，
	// parquet 的固定开销乘以股票数（实测约 99ms/只），比日线还慢。
	byDate := p.valuationFor(code, start, end)

	bare := cnlake.BareSymbol(code)
	shares := p.shareHistory(bare)
	name := p.name(bare)

	out := make([]*stockdb.Quote, 0, len(bars))
	var prevClose float64
	// 量比的回看窗口跨过 start 边界：bars 里本来就带着 start 之前的日线
	// （调用方多取了一段），所以这里按**全局下标**往前数，不能用 out 的下标。
	volumes := make([]float64, 0, len(bars))
	for _, bar := range bars {
		date := bar.Date()
		if date < int64(start) || date > int64(end) {
			// ⚠ 区间外的行不进结果，但**要进量比的分母** ——
			// 否则区间起点那几天的量比会因为"前面没有历史"而算不出来
			volumes = append(volumes, float64(bar.Volume))
			continue
		}
		var valuation *cnlake.ValuationRow
		if found, ok := byDate[date]; ok {
			valuation = &found
		}
		out = append(out, p.buildQuote(bare, name, bar, prevClose, valuation, shares, volumes))
		prevClose = bar.Close
		volumes = append(volumes, float64(bar.Volume))
	}

	if desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
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

	// 回看窗口内一根都没有的（日历远超 K 线进度、长期停牌 / 退市的历史遗留标的），
	// 不限起点重取一次。
	//
	// ⚠ **这一步是本机全量列表最贵的一段**：它要把 daily_bars 的**每一个**日分区
	//（本机 2614 个）的 7 列全解码再按代码过滤，实测约 4s，而 2479 个待查代码里
	// 只有 237 个真有历史（其余 2242 个在湖里根本就没有 K 线）。
	//
	// 试过两条加速路都没成：
	//   - 先只读 symbol 列定位"最后两个交易日"再回读命中分区：两遍加起来只快 7%
	//    （列裁剪省下的时间被第二遍的文件打开吃掉）；
	//   - trading_status 能当"这个代码有没有 K 线"的索引（实测"无 status 但有 K 线"
	//     为 0），但它给不出最后交易日，省不掉扫描。
	//
	// 所以改用**结果缓存**：同一天里"历史最后几根 K 线"不会变，
	// 缓住之后 10 分钟后重建全量列表（universeTTL）不必再扫一遍。
	if len(missed) > 0 {
		filled, retryErr := p.retryQuotes(missed, date)
		if retryErr != nil {
			return out, retryErr
		}
		for code, quote := range filled {
			out[code] = quote
		}
	}
	return out, nil
}

// retryQuoteCache 是"不限起点重取"的结果缓存。
//
// key 是交易日：同一天里每只票的历史最后几根 K 线是固定的，
// 而这一步要扫整个 daily_bars（本机约 4s）——缓存之后，10 分钟后重建全量列表
// （`universeTTL`）以及"刷新"都不会再付这笔钱。
//
// ⚠ 缓存里只有**日期 ≤ 该交易日**的 K 线，所以换交易日时旧条目自然失效；
// 同一天内湖里补进历史（罕见）最坏情况是复用一份稍旧的报价，与
// `bulkQuotes` 本来就有的一分钟级缓存同级风险。
type retryQuoteCache struct {
	date   int
	at     time.Time
	quotes map[string]*stockdb.Quote
	empty  map[string]struct{} // 确认"整段历史里一根都没有"的代码
}

// retryQuotes 取"没有近期行情的代码"的历史最后几根 K 线（带缓存）。
func (p *lakeProvider) retryQuotes(codes []string, date int) (map[string]*stockdb.Quote, error) {
	p.retryMu.Lock()
	cached := p.retryCache
	p.retryMu.Unlock()

	out := make(map[string]*stockdb.Quote, len(codes))
	todo := make([]string, 0, len(codes))
	if cached != nil && cached.date == date && time.Since(cached.at) < retryCacheTTL {
		for _, code := range codes {
			bare := cnlake.BareSymbol(code)
			if quote, ok := cached.quotes[bare]; ok {
				out[bare] = quote
				continue
			}
			if _, ok := cached.empty[bare]; ok {
				continue // 已知整段历史都没数据
			}
			todo = append(todo, code)
		}
	} else {
		todo = codes
	}

	if len(todo) > 0 || cached == nil || cached.date != date {
		filled, still, err := p.retryFill(todo, date)
		if err != nil {
			return out, err
		}
		next := &retryQuoteCache{
			date:   date,
			at:     time.Now(),
			quotes: map[string]*stockdb.Quote{},
			empty:  map[string]struct{}{},
		}
		if cached != nil && cached.date == date && time.Since(cached.at) < retryCacheTTL {
			next = cached
			next.at = time.Now()
		}
		for code, quote := range filled {
			out[code] = quote
			next.quotes[code] = quote
		}
		for _, code := range still {
			next.empty[cnlake.BareSymbol(code)] = struct{}{}
		}

		p.retryMu.Lock()
		p.retryCache = next
		p.retryMu.Unlock()
	}

	slog.Info("数据湖不限起点补抓就绪",
		"请求", len(codes), "本次实际扫描", len(todo), "命中", len(out))
	return out, nil
}

// retryFill 取"没有近期行情的代码"各自的最后一根行情。
//
// 优先走「按股票」缓存：这批代码要的是"整段历史里最后一根"，而全表扫描
// （`bulkQuotes(codes, 0, date)` → 区间从 1990 年开始 = 2614 个分区）实测约 3.5 秒，
// 是股票列表冷启动里最贵的一段。缓存里 2242/2479 个代码**连文件都没有**
// （它们本来就没有任何 K 线），一次目录索引就能答完（实测 2ms）。
//
// ⚠ 只在缓存**新鲜**时才走快路。"水位旧的缓存里没有文件"还有另一种解释：
// 这只票是在水位之后才上市/入库的 —— 那样直接判成"没有历史"就会漏掉它
// 最后几根 K 线。所以不新鲜时退回全表扫描（同时后台重建，几十秒后下一次就快）。
func (p *lakeProvider) retryFill(codes []string, date int) (map[string]*stockdb.Quote, []string, error) {
	if len(codes) == 0 {
		return map[string]*stockdb.Quote{}, nil, nil
	}
	if manifest, ok := p.symbolCacheReady(); ok {
		filled, still, err := p.symbolQuotesFromCache(manifest, codes, date)
		if err == nil {
			return filled, still, nil
		}
		slog.Warn("按股票日线缓存读失败，退回全表扫描", "err", err)
	}
	return p.bulkQuotes(codes, 0, date)
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
		// list 是升序的全部日线，末根当日 → 前面的是量比分母
		out[bare] = p.buildQuote(
			bare, p.name(bare), last, prevClose, valuation,
			p.shareHistory(bare), priorVolumes(list))
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
	//
	// ⚠ 走 `CorporateActions`（整表一次载入 + 按代码索引）而不是按代码扫分区：
	// 换算规则统一由 `CorporateAction.ShareMultiplier / CashPerShare` 提供，
	// 避免"这里和别处各算一套"。
	actions := p.lake.CorporateActions(code)
	if len(actions) > 0 {
		out := make([]stockdb.Factor, 0, len(actions))
		cum := 1.0
		for _, action := range actions {
			// 与 free-stockdb「复权」表对齐：mult 是本次除权的股数放大倍数
			mult := action.ShareMultiplier()
			cum *= mult
			out = append(out, stockdb.Factor{
				Date:  action.ExDate(),
				Div:   action.CashPerShare(),
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
	bare := cnlake.BareSymbol(code)
	shares := p.shareHistory(bare)
	return scoredBarsFrom(p.adjustBars(bare, rows), shares), nil
}

// AdjMode 返回当前生效的复权口径（数据湖行情只存不复权，指标必须复权后再算）。
//
// 可用环境变量 `FREESTOCK_ADJUST` 覆盖：`none` / `qfq` / `hfq`，默认 **hfq**（后复权）。
// 为什么默认后复权：`F(t)` 只随时间前进、历史值永不变 → 增量入库安全、回测可复现；
// 前复权锚定"最新因子"，历史行分批入库会出现尺度锚点不一致，MA250 跨锚点会失真。
// 两者信号等价（均线/比值/涨跌幅都是尺度不变的），只差价格水平。
// 为什么必须复权：不复权会让除权日产生假破位（实测 `close < MA30×0.96` 的发生率
// 除权日 44.4% vs 平时 29.8%），送转股更会在其后一年内通不过任何 `close > MA` 条件。
var adjustMode = cnlake.ParseAdjMode(os.Getenv("FREESTOCK_ADJUST"))

// adjustBars 按当前口径复权行情。因子文件缺失时原样返回（合成测试湖即如此）。
//
// ⚠ 只复权价格（OHLC）；成交量/成交额保持原始，因为 F 是**逐股票**的常数，
// 跨股票放大成交额会让"按成交额排序"的候选顺序失真。
func (p *lakeProvider) adjustBars(bare string, bars []cnlake.BarRow) []cnlake.BarRow {
	if adjustMode == cnlake.AdjNone || p.lake == nil || len(bars) == 0 {
		return bars
	}
	return p.lake.AdjustBars(bare, bars, adjustMode)
}

// 启用复权后广度整体上移（复权口径下中位 +4.5%），必须同步抬高低吸的档位阈值，
// 否则等于把 900/1000/1500/2000/2600 悄悄调小一档 —— 那是参数改动而非数据修正。
//
// 1.04 是按分位匹配实测的修正系数（见 docs/复权改造实施记录.md）。
// 关闭复权（FREESTOCK_ADJUST=none）时保持 1.0，行为与既有 golden/parity 测试一致。
func init() {
	if adjustMode != cnlake.AdjNone {
		score.LowBuyBreadthAdjScale = 1.04
	}
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
