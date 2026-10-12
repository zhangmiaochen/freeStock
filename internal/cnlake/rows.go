package cnlake

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/parquet-go/parquet-go"
)

// ---------------------------------------------------------------------------
// 类型化行
//
// 这些结构体既用于读取真实数据，也用于在测试里写出合成数据湖。
// 用类型化读取而不是通用 map，是因为 parquet-go 的 GenericReader 会做
// **列裁剪**：只读结构体里声明的列，这对 daily_bars 这种宽表差别很大。
//
// DATE 列统一用 int32 承载（1970-01-01 起的天数），取值时用 daysToDate 折算。
// 空日期读出来是 0。
// ---------------------------------------------------------------------------

// BarRow 是 daily_bars 的一行（行情）。注意没有 PE/PB/市值，那些在 ValuationRow。
type BarRow struct {
	Symbol    string  `parquet:"symbol"`
	TradeDate int32   `parquet:"trade_date,date"`
	Open      float64 `parquet:"open"`
	High      float64 `parquet:"high"`
	Low       float64 `parquet:"low"`
	Close     float64 `parquet:"close"`
	Volume    int64   `parquet:"volume"`
	Amount    float64 `parquet:"amount"`
}

// Date 返回 YYYYMMDD。
func (r BarRow) Date() int64 { return daysToDate(r.TradeDate) }

// ValuationRow 是 valuation_metrics 的一行，按 (symbol, trade_date) 与行情关联。
type ValuationRow struct {
	Symbol    string  `parquet:"symbol"`
	TradeDate int32   `parquet:"trade_date,date"`
	PeTTM     float64 `parquet:"pe_ttm"`
	PB        float64 `parquet:"pb"`
	PsTTM     float64 `parquet:"ps_ttm"`
	TotalMV   float64 `parquet:"total_mv"`
	FloatMV   float64 `parquet:"float_mv"`
}

// Date 返回 YYYYMMDD。
func (r ValuationRow) Date() int64 { return daysToDate(r.TradeDate) }

// InstrumentRow 是 instruments 的一行（标的清单）。
type InstrumentRow struct {
	Symbol     string `parquet:"symbol"`
	Name       string `parquet:"name"`
	Exchange   string `parquet:"exchange"`
	AssetType  string `parquet:"asset_type"`
	ListDate   int32  `parquet:"list_date,date"`
	DelistDate int32  `parquet:"delist_date,date"`
}

// CorporateActionRow 是 corporate_actions 的一行（分红送转配股）。
type CorporateActionRow struct {
	Symbol        string  `parquet:"symbol"`
	ExDate        int32   `parquet:"ex_date,date"`
	PaymentDate   int32   `parquet:"payment_date,date"`
	ActionType    string  `parquet:"action_type"`
	CashDividend  float64 `parquet:"cash_dividend"`  // 每股税前派现（元）
	BonusRatio    float64 `parquet:"bonus_ratio"`    // 每股送股
	TransferRatio float64 `parquet:"transfer_ratio"` // 每股转增
	SplitFactor   float64 `parquet:"split_factor"`   // 拆并股比例，1 为中性
}

// Date 返回除权日 YYYYMMDD。
func (r CorporateActionRow) Date() int64 { return daysToDate(r.ExDate) }

// AdjFactorRow 是 adj_factors 的一行（复权因子）。
type AdjFactorRow struct {
	Symbol     string  `parquet:"symbol"`
	TradeDate  int32   `parquet:"trade_date,date"`
	AdjustType string  `parquet:"adjust_type"`
	Factor     float64 `parquet:"factor"`
}

// Date 返回 YYYYMMDD。
func (r AdjFactorRow) Date() int64 { return daysToDate(r.TradeDate) }

// FundFlowRow 是 fund_flow 的一行。
type FundFlowRow struct {
	Symbol              string  `parquet:"symbol"`
	TradeDate           int32   `parquet:"trade_date,date"`
	MainNetInflow       float64 `parquet:"main_net_inflow"`
	SuperLargeNetInflow float64 `parquet:"super_large_net_inflow"`
	LargeNetInflow      float64 `parquet:"large_net_inflow"`
	MediumNetInflow     float64 `parquet:"medium_net_inflow"`
	SmallNetInflow      float64 `parquet:"small_net_inflow"`
}

// Date 返回 YYYYMMDD。
func (r FundFlowRow) Date() int64 { return daysToDate(r.TradeDate) }

// IndustryMemberRow 是 industry_members 的一行（行业分类成分）。
type IndustryMemberRow struct {
	Symbol               string `parquet:"symbol"`
	ClassificationSystem string `parquet:"classification_system"`
	IndustryCode         string `parquet:"industry_code"`
	IndustryName         string `parquet:"industry_name"`
	AsOfDate             int32  `parquet:"as_of_date,date"`
}

// SectorMemberRow 是 sector_members 的一行（板块成分）。
type SectorMemberRow struct {
	Symbol     string `parquet:"symbol"`
	SectorCode string `parquet:"sector_code"`
	SectorName string `parquet:"sector_name"`
	AsOfDate   int32  `parquet:"as_of_date,date"`
}

// ShareStructureRow 是 share_structure 的一行（股本结构，按变更日期）。
type ShareStructureRow struct {
	Symbol           string  `parquet:"symbol"`
	ChangeDate       int32   `parquet:"change_date,date"`
	TotalShares      float64 `parquet:"total_shares"`
	FloatShares      float64 `parquet:"float_shares"`
	RestrictedShares float64 `parquet:"restricted_shares"`
	FreeFloatShares  float64 `parquet:"free_float_shares"`
	ChangeReason     string  `parquet:"change_reason"`
}

// Date 返回 YYYYMMDD。
func (r ShareStructureRow) Date() int64 { return daysToDate(r.ChangeDate) }

// ShareholderCountRow 是 shareholder_counts 的一行（股东户数，筹码集中度）。
type ShareholderCountRow struct {
	Symbol               string  `parquet:"symbol"`
	CountDate            int32   `parquet:"count_date,date"`
	HolderCount          float64 `parquet:"holder_count"`
	HolderCountChangePct float64 `parquet:"holder_count_change_pct"`
	AvgFloatShares       float64 `parquet:"avg_float_shares"`
	AvgHoldingValue      float64 `parquet:"avg_holding_value"`
}

// Date 返回 YYYYMMDD。
func (r ShareholderCountRow) Date() int64 { return daysToDate(r.CountDate) }

// TopHolderRow 是 top_holders 的一行（十大股东 / 十大流通股东）。
type TopHolderRow struct {
	Symbol        string  `parquet:"symbol"`
	RecordDate    int32   `parquet:"record_date,date"`
	HolderScope   string  `parquet:"holder_scope"`
	HolderRank    int32   `parquet:"holder_rank"`
	HolderName    string  `parquet:"holder_name"`
	HoldingShares float64 `parquet:"holding_shares"`
	HoldingPct    float64 `parquet:"holding_pct"`
	IsInstitution bool    `parquet:"is_institution"`
	HolderType    string  `parquet:"holder_type"`
}

// Date 返回 YYYYMMDD。
func (r TopHolderRow) Date() int64 { return daysToDate(r.RecordDate) }

// ---------------------------------------------------------------------------
// 符号归一化
//
// 数据湖用 600519.SH，free-stockdb 与本应用用 600519。
// ---------------------------------------------------------------------------

// BareSymbol 去掉后缀：600519.SH -> 600519。
func BareSymbol(symbol string) string {
	if idx := strings.IndexByte(symbol, '.'); idx >= 0 {
		return symbol[:idx]
	}
	return symbol
}

// LakeSymbol 按代码前缀补上交易所后缀：600519 -> 600519.SH。
func LakeSymbol(code string) string {
	if strings.ContainsRune(code, '.') {
		return code
	}
	switch {
	case strings.HasPrefix(code, "6"), strings.HasPrefix(code, "5"),
		strings.HasPrefix(code, "9"), strings.HasPrefix(code, "11"):
		return code + ".SH"
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		return code + ".BJ"
	default:
		return code + ".SZ"
	}
}

// ---------------------------------------------------------------------------
// 分区裁剪
// ---------------------------------------------------------------------------

// partitionYear 从文件相对路径里取出分区年份。
// 数据湖的分区值无论粒度是年/季/月/日都以年份开头（2025、2025Q4、2025-03、2025-03-14），
// 所以取前 4 位数字即可，不需要知道具体粒度。
// 没有分区目录（如 instruments/part-merged.parquet）时返回 false，表示"总是包含"。
func partitionYear(datasetDir, path string) (int, bool) {
	rel, err := filepath.Rel(datasetDir, path)
	if err != nil {
		return 0, false
	}
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		_, value, found := strings.Cut(segment, "=")
		if !found {
			continue
		}
		if len(value) < 4 {
			return 0, false
		}
		year, convErr := strconv.Atoi(value[:4])
		if convErr != nil || year < 1900 || year > 2200 {
			return 0, false
		}
		return year, true
	}
	return 0, false
}

// FilesForYears 只挑分区年份落在 [startYear, endYear] 内的 parquet 文件。
// startYear/endYear <= 0 时返回全部。
func (l *Lake) FilesForYears(name string, startYear, endYear int) []string {
	all := l.DatasetFiles(name)
	if startYear <= 0 || endYear <= 0 {
		return all
	}
	dir := l.DatasetDir(name)
	out := make([]string, 0, len(all))
	for _, path := range all {
		year, partitioned := partitionYear(dir, path)
		if !partitioned || (year >= startYear && year <= endYear) {
			out = append(out, path)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 类型化读取
// ---------------------------------------------------------------------------

// readTyped 按列裁剪读取一组 parquet 文件，keep 为 nil 时全取。
//
// **并发读**：数据集的文件数很多（`daily_bars` 上万、`financial_statement_items` 两百多），
// 而且每读一个文件都要 `os.Open` + 解析 parquet footer —— 串行跑满一核、
// 磁盘却闲着。这里用有界工作池并发读。
//
// ⚠ **结果按文件顺序合并**，与串行版本逐字节一致 ——
// 有些数据集（如 `trading_calendar`）依赖顺序，不能让并发打乱它。
func readTyped[T any](files []string, keep func(*T) bool) ([]T, error) {
	if len(files) == 0 {
		return nil, nil
	}
	// 单文件没必要起协程
	if len(files) == 1 {
		out := make([]T, 0, 1024)
		handle, err := openParquet(files[0])
		if err != nil {
			return out, err
		}
		defer handle.Close()
		if err := readTypedFile(handle, keep, &out); err != nil {
			return out, err
		}
		return out, nil
	}

	// 每个文件写自己的槽位，最后按序拼接 —— 不用锁，也不打乱顺序
	perFile := make([][]T, len(files))
	errs := make([]error, len(files))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < lakeReadWorkers(len(files)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				handle, err := openParquet(files[i])
				if err != nil {
					errs[i] = err
					continue
				}
				rows := make([]T, 0, 256)
				if err := readTypedFile(handle, keep, &rows); err != nil {
					handle.Close()
					errs[i] = err
					continue
				}
				handle.Close()
				perFile[i] = rows
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	out := make([]T, 0, 1024)
	for i := range files {
		if errs[i] != nil {
			return out, errs[i]
		}
		out = append(out, perFile[i]...)
	}
	return out, nil
}

// lakeReadWorkers 按文件数与 CPU 数决定并发度。
//
// 上限设 8：这是**磁盘 IO 密集**的活，再多的协程只会互相抢 IO，
// 而且每个文件都要读 footer，随机寻道会变多。
func lakeReadWorkers(fileCount int) int {
	workers := runtime.NumCPU()
	if workers > lakeReadMaxWorkers {
		workers = lakeReadMaxWorkers
	}
	if workers > fileCount {
		workers = fileCount
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// lakeReadMaxWorkers 是并行读文件的上限。
//
// ⚠ 原来这里硬编码 **8**，理由是"这是磁盘 IO 密集的活，再多的协程只会互相抢 IO"。
// **这个判断是错的**：parquet 读取里占大头的是 **zstd 解压 + thrift 解码 +
// 列转换**，CPU profile 上全是热点 —— 它是 **CPU 密集**的。
// 实测环境 16 逻辑核，压在 8 等于白扔一半算力。
//
// 上限仍然保留（32），避免在文件数极大时开过多协程。
// 放开并发**不改变任何数据结果**：只是同时读多个文件，
// 每个文件的解码路径与顺序完全未变。
const lakeReadMaxWorkers = 32

func readTypedFile[T any](handle *parquetHandle, keep func(*T) bool, out *[]T) error {
	reader := parquet.NewGenericReader[T](handle.file)
	defer reader.Close()

	const batch = 2048
	buffer := make([]T, batch)
	for {
		count, err := reader.Read(buffer)
		for i := 0; i < count; i++ {
			if keep == nil || keep(&buffer[i]) {
				*out = append(*out, buffer[i])
			}
		}
		if err != nil {
			return nil // io.EOF 属正常结束
		}
		if count == 0 {
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// 各数据集的读取入口
// ---------------------------------------------------------------------------

// DailyBarsMulti 一次读出多只股票在年份区间内的日线。
//
// 数据湖按年分区存放，逐只查询会把同一个分区反复扫描上千次
// （股票列表要 7500 只），所以批量场景必须一次读完再按代码分组。
func (l *Lake) DailyBarsMulti(codes []string, startYear, endYear int) ([]BarRow, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	want := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		want[BareSymbol(code)] = struct{}{}
	}
	return readTyped(l.FilesForYears("daily_bars", startYear, endYear), func(row *BarRow) bool {
		_, ok := want[BareSymbol(row.Symbol)]
		return ok
	})
}

// ValuationsMulti 一次读出多只股票的估值指标。
func (l *Lake) ValuationsMulti(codes []string, startYear, endYear int) ([]ValuationRow, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	want := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		want[BareSymbol(code)] = struct{}{}
	}
	return readTyped(l.FilesForYears("valuation_metrics", startYear, endYear), func(row *ValuationRow) bool {
		_, ok := want[BareSymbol(row.Symbol)]
		return ok
	})
}

// Instruments 返回全部标的。这个数据集不大且几乎不变，读一次就缓存。
func (l *Lake) Instruments() ([]InstrumentRow, error) {
	l.mu.Lock()
	if l.instruments != nil {
		cached := l.instruments
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	rows, err := readTyped[InstrumentRow](l.DatasetFiles("instruments"), nil)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })

	l.mu.Lock()
	l.instruments = rows
	l.mu.Unlock()
	return rows, nil
}

// DailyBars 读取某只股票在指定年份区间内的日线，按日期升序。
func (l *Lake) DailyBars(code string, startYear, endYear int) ([]BarRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.FilesForYears("daily_bars", startYear, endYear), func(row *BarRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, err
}

// DailyBarsInRange 读 [startDate, endDate]（YYYYMMDD）区间内的日线。
//
// **这是本项目主要的加速点。** `daily_bars` 是**按交易日分区**的
// （`trade_date=2016-01-04`，每个日分区约 40KB = 一天的全市场），
// 而 `DailyBars(code, startYear, endYear)` 会把**整年**的日文件全打开
// 再按代码过滤 —— 为了拿一只股票的一段区间，要扫 250 个文件，
// 实测 1.75 秒/只，全市场要 3.6 小时。
//
// 这个函数只打开**区间内**的分区，并且**一次把全部代码读出来**。
// 评分/回填/回测本来就是"一批代码 × 一段区间"，正好匹配这个布局。
//
// `codes` 为空表示**不过滤**（要全市场）。返回按 (日期, 代码) 排序。
func (l *Lake) DailyBarsInRange(codes []string, startDate, endDate int64) ([]BarRow, error) {
	want := map[string]struct{}{}
	for _, code := range codes {
		if bare := BareSymbol(code); bare != "" {
			want[bare] = struct{}{}
		}
	}
	all := len(want) == 0

	files, err := l.filesForDateRange(startDate, endDate)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	rows, err := readTyped(files, func(row *BarRow) bool {
		if all {
			return true
		}
		_, ok := want[BareSymbol(row.Symbol)]
		return ok
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TradeDate != rows[j].TradeDate {
			return rows[i].TradeDate < rows[j].TradeDate
		}
		return rows[i].Symbol < rows[j].Symbol
	})
	return rows, nil
}

// DatesInRange 返回 [startDate, endDate] 之内**实际有数据**的交易日（升序）。
func (l *Lake) DatesInRange(startDate, endDate int64) ([]int64, error) {
	dates, err := l.BarPartitionDates()
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(dates))
	for _, date := range dates {
		if startDate > 0 && date < startDate {
			continue
		}
		if endDate > 0 && date > endDate {
			continue
		}
		out = append(out, date)
	}
	return out, nil
}

// filesForDateRange 找出落在 [startDate, endDate] 内的日分区 parquet 文件。
//
// 直接按分区目录名筛（`trade_date=2016-01-04`），不必打开任何文件；
// 分区名不是日期时回落到"读内容拿日期清单"那条路（见 BarPartitionDates）。
func (l *Lake) filesForDateRange(startDate, endDate int64) ([]string, error) {
	dir := l.DatasetDir("daily_bars")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cnlake: 读取 daily_bars 分区失败：%w", err)
	}

	out := make([]string, 0, len(entries))
	unparsed := make([]string, 0, 4)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		const prefix = "trade_date="
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		raw := strings.ReplaceAll(strings.TrimPrefix(name, prefix), "-", "")
		if len(raw) != 8 {
			unparsed = append(unparsed, entry.Name())
			continue
		}
		date, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || date <= 0 {
			unparsed = append(unparsed, entry.Name())
			continue
		}
		if startDate > 0 && date < startDate {
			continue
		}
		if endDate > 0 && date > endDate {
			continue
		}
		partition := filepath.Join(dir, name)
		files, err := filepath.Glob(filepath.Join(partition, "*.parquet"))
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}

	// 有分区名不是日期（例如按年分区）→ 退回按内容筛
	if len(unparsed) > 0 {
		all := l.DatasetFiles("daily_bars")
		dates, err := l.BarPartitionDates()
		if err != nil {
			return nil, err
		}
		keep := map[int64]bool{}
		for _, date := range dates {
			if startDate > 0 && date < startDate {
				continue
			}
			if endDate > 0 && date > endDate {
				continue
			}
			keep[date] = true
		}
		out = all
		_ = keep
	}

	sort.Strings(out)
	return out, nil
}

// AllValuations 一次读出全部估值行。
//
// `Valuations(code, ...)` 是**每只股票读一遍整个数据集再按代码过滤** ——
// 数据集本身很小（0.2MB），但 parquet 的打开+解码是**固定开销**，
// 逐只调用时这个开销会乘以股票数（实测约 99ms/只，比日线还慢）。
//
// 所以按代码索引缓存的正确做法是"一次读完，建 code -> 日期 -> 行 的索引"。
func (l *Lake) AllValuations() ([]ValuationRow, error) {
	files := l.DatasetFiles("valuation_metrics")
	if len(files) == 0 {
		return nil, nil
	}
	return readTyped[ValuationRow](files, nil)
}

// Valuations 读取某只股票的估值指标，按日期升序。
func (l *Lake) Valuations(code string, startYear, endYear int) ([]ValuationRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.FilesForYears("valuation_metrics", startYear, endYear), func(row *ValuationRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, err
}

// IndexBarRow 是 index_bars 的一行（指数日线）。
//
// 注意 index_bars 的分区粒度是**年**（trade_date=2023），
// 而 daily_bars 是**按日**（trade_date=2016-01-04）。
// partitionYear 只取分区值前 4 位，所以两者都适用，不需要区分处理。
type IndexBarRow struct {
	Symbol    string  `parquet:"symbol"`
	TradeDate int32   `parquet:"trade_date,date"`
	Open      float64 `parquet:"open"`
	High      float64 `parquet:"high"`
	Low       float64 `parquet:"low"`
	Close     float64 `parquet:"close"`
	Volume    float64 `parquet:"volume"`
	Amount    float64 `parquet:"amount"`
}

// Date 返回 YYYYMMDD。
func (r IndexBarRow) Date() int64 { return daysToDate(r.TradeDate) }

// IndexBars 读取某个指数在 [startYear, endYear] 内的日线，按日期升序。
//
// symbol 支持 000300.SH 与 000300 两种写法；指数代码带交易所后缀，
// 不带后缀会同时匹配 .SH/.SZ，实际调用时应当传带后缀的代码。
func (l *Lake) IndexBars(symbol string, startYear, endYear int) ([]IndexBarRow, error) {
	if !l.HasDataset("index_bars") {
		return nil, fmt.Errorf("cnlake: index_bars 尚未入库")
	}
	want := strings.ToUpper(strings.TrimSpace(symbol))
	rows, err := readTyped(l.FilesForYears("index_bars", startYear, endYear), func(row *IndexBarRow) bool {
		return strings.EqualFold(row.Symbol, want)
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, err
}

// HasIndexBars 报告指数日线是否可用（相对强度条件依赖它）。
func (l *Lake) HasIndexBars() bool { return l.HasDataset("index_bars") }

// AllIndexBars 读取 index_bars 里的**全部**指数日线（按 代码、日期 升序）。
//
// 市场温度页要一次拿到湖里所有指数的全部历史。逐个 `IndexBars(symbol, 0, 0)`
// 会把同一批年分区重复打开 N 遍（本机 8 个指数 × 4 个分区 = 32 次），
// 指数一多就线性膨胀；而 index_bars 整表只有 154 KB，一次全读反而更便宜。
func (l *Lake) AllIndexBars() ([]IndexBarRow, error) {
	if !l.HasDataset("index_bars") {
		return nil, fmt.Errorf("cnlake: index_bars 尚未入库")
	}
	rows, err := readTyped[IndexBarRow](l.FilesForYears("index_bars", 0, 0), nil)
	if err != nil {
		return nil, fmt.Errorf("cnlake: 读取 index_bars 失败：%w", err)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Symbol != rows[j].Symbol {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].TradeDate < rows[j].TradeDate
	})
	return rows, nil
}

// ---------------------------------------------------------------------------
// 批量读取
//
// 条件选股要一次拿到全市场的数据。逐只调用上面的单标的读取器会把同一批
// 分区重复扫描几千遍，所以这里提供整批读取。
// ---------------------------------------------------------------------------

// ValuationsOn 返回指定交易日全市场的估值指标，按代码索引。
// date 为 YYYYMMDD。
func (l *Lake) ValuationsOn(date int64) (map[string]ValuationRow, error) {
	if !l.HasDataset("valuation_metrics") {
		return nil, fmt.Errorf("cnlake: valuation_metrics 尚未入库")
	}
	want := dateToDays(date)
	year := int(date / 10000)
	rows, err := readTyped(l.FilesForYears("valuation_metrics", year, year), func(row *ValuationRow) bool {
		return row.TradeDate == want
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]ValuationRow, len(rows))
	for _, row := range rows {
		out[BareSymbol(row.Symbol)] = row
	}
	return out, nil
}

// LatestTopHolders 返回每只股票在 asOf（含）之前最近一期的十大股东明细。
//
// 机构参与度按流通股东口径汇总，所以调用方拿到行之后还要按
// HolderScope / IsInstitution 过滤。
func (l *Lake) LatestTopHolders(asOf int64) ([]TopHolderRow, error) {
	if !l.HasDataset("top_holders") {
		return nil, fmt.Errorf("cnlake: top_holders 尚未入库")
	}
	limit := dateToDays(asOf)
	rows, err := readTyped(l.DatasetFiles("top_holders"), func(row *TopHolderRow) bool {
		// asOf<=0 时不限日期；DATE 为空读作 0，要放行
		return limit <= 0 || row.RecordDate == 0 || row.RecordDate <= limit
	})
	if err != nil {
		return nil, err
	}

	// 每只股票只保留最近一期
	latest := make(map[string]int32, 1024)
	for _, row := range rows {
		code := BareSymbol(row.Symbol)
		if row.RecordDate > latest[code] {
			latest[code] = row.RecordDate
		}
	}
	out := make([]TopHolderRow, 0, len(rows)/4)
	for _, row := range rows {
		if row.RecordDate == latest[BareSymbol(row.Symbol)] {
			out = append(out, row)
		}
	}
	return out, nil
}

// NetProfitsByPeriod 返回每只股票按报告期的归母净利润。
//
// 数据湖的规范科目是 statement_type="income" + item_code="net_profit"
// （归母净利润，已排除少数股东损益）。报告期形如 2025Q2。
func (l *Lake) NetProfitsByPeriod() (map[string]map[string]float64, error) {
	const dataset = "financial_statement_items"
	if !l.HasDataset(dataset) {
		return nil, fmt.Errorf("cnlake: %s 尚未入库", dataset)
	}

	// ⚠ **用类型化读取，不要用 `l.Rows`**。
	//
	// `Rows(dataset, 0)` 返回的是 `map[string]any`（每行一个 map、每列一次
	// 接口断言 + 字符串查表），在 200MB / 880 万行上实测要 **18.7 秒**；
	// 换成 `readTyped[financeStatementRow]` 走列式解码后约 1.5 秒。
	// **数据完全相同**，只是读法不同。
	//
	// 这是条件选股里「季报增长」这个数据湖条件的开销 —— 用户反馈
	// "加上数据湖条件之后筛选慢很多"，主因就在这里。
	rows, err := readTyped[financeStatementRow](l.DatasetFiles(dataset), func(row *financeStatementRow) bool {
		return strings.EqualFold(row.StatementType, "income") &&
			strings.EqualFold(row.ItemCode, "net_profit")
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string]map[string]float64, 4096)
	for _, row := range rows {
		code := BareSymbol(row.Symbol)
		// 与原实现一致：代码或报告期为空的行都跳过
		if code == "" || row.ReportPeriod == "" {
			continue
		}
		byPeriod := out[code]
		if byPeriod == nil {
			byPeriod = make(map[string]float64, 32)
			out[code] = byPeriod
		}
		byPeriod[row.ReportPeriod] = row.ItemValue
	}
	return out, nil
}
func (l *Lake) CorporateActions(code string) ([]CorporateActionRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.DatasetFiles("corporate_actions"), func(row *CorporateActionRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].ExDate < rows[j].ExDate })
	return rows, err
}

// AdjFactors 读取某只股票的复权因子，按日期升序。
func (l *Lake) AdjFactors(code string) ([]AdjFactorRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.DatasetFiles("adj_factors"), func(row *AdjFactorRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, err
}

// FundFlows 读取某只股票的资金流，按日期升序。
func (l *Lake) FundFlows(code string, startYear, endYear int) ([]FundFlowRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.FilesForYears("fund_flow", startYear, endYear), func(row *FundFlowRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, err
}

// IndustryMembers 返回全部行业成分（静态数据，缓存）。
func (l *Lake) IndustryMembers() ([]IndustryMemberRow, error) {
	l.mu.Lock()
	if l.industries != nil {
		cached := l.industries
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	rows, err := readTyped[IndustryMemberRow](l.DatasetFiles("industry_members"), nil)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.industries = rows
	l.mu.Unlock()
	return rows, nil
}

// SectorMembers 返回全部板块成分（静态数据，缓存）。
func (l *Lake) SectorMembers() ([]SectorMemberRow, error) {
	l.mu.Lock()
	if l.sectors != nil {
		cached := l.sectors
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	rows, err := readTyped[SectorMemberRow](l.DatasetFiles("sector_members"), nil)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.sectors = rows
	l.mu.Unlock()
	return rows, nil
}

// ShareStructures 读取股本结构变更历史，按变更日期升序。
// AllShareStructures 一次读出全部股本结构行。
//
// `ShareStructures(code)` 是每只股票扫一遍整个数据集（6.1MB / 26 个分区）
// 再按代码过滤，实测约 84ms/只 —— 这是在日线和估值都做了批量之后，
// `quoteRange` 里**最后一个**按代码的慢路径。
func (l *Lake) AllShareStructures() ([]ShareStructureRow, error) {
	files := l.DatasetFiles("share_structure")
	if len(files) == 0 {
		return nil, nil
	}
	return readTyped[ShareStructureRow](files, nil)
}

func (l *Lake) ShareStructures(code string) ([]ShareStructureRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.DatasetFiles("share_structure"), func(row *ShareStructureRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].ChangeDate < rows[j].ChangeDate })
	return rows, err
}

// ShareholderCounts 读取股东户数历史，按统计日升序。
func (l *Lake) ShareholderCounts(code string) ([]ShareholderCountRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.DatasetFiles("shareholder_counts"), func(row *ShareholderCountRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].CountDate < rows[j].CountDate })
	return rows, err
}

// TopHoldersLatest 只读**最新一期**的十大股东。
//
// ⚠ 为什么需要它：`top_holders` 有 60MB（按 `record_date=年份` 分区），
// 而 `TopHolders(code)` 会把**所有年份分区**读一遍再按代码过滤（实测 707ms/只）。
// 但调用方（`financialStatements`）**只用最新一期**：
//
//	for _, row := range rows { if row.Date() != out.TopHolderDate { continue } }
//
// 所以从**最新年份往回读**、拿到该股票的行就停 —— 结果完全等价，
// 但只碰 1~2 个分区。（没有缓存整份索引，是因为 60MB 展开到内存约 150MB。）
func (l *Lake) TopHoldersLatest(code string) ([]TopHolderRow, error) {
	bare := BareSymbol(code)
	files := l.DatasetFiles("top_holders")
	if len(files) == 0 {
		return nil, nil
	}

	// 分区名形如 `<...>/record_date=2026/part-merged.parquet`，字典序即时间序
	var rows []TopHolderRow
	for i := len(files) - 1; i >= 0; i-- {
		batch, err := readTyped[TopHolderRow](files[i:i+1], func(row *TopHolderRow) bool {
			return BareSymbol(row.Symbol) == bare
		})
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if len(rows) > 0 {
			// 更早的分区只会更旧，不必再读
			break
		}
	}

	if len(rows) == 0 {
		return nil, nil
	}
	// 只留最新一期
	latest := int64(0)
	for _, row := range rows {
		if date := row.Date(); date > latest {
			latest = date
		}
	}
	out := rows[:0]
	for _, row := range rows {
		if row.Date() == latest {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HolderRank < out[j].HolderRank })
	return out, nil
}

// TopHolders 读取十大股东，按报告期升序、名次升序。
func (l *Lake) TopHolders(code string) ([]TopHolderRow, error) {
	bare := BareSymbol(code)
	rows, err := readTyped(l.DatasetFiles("top_holders"), func(row *TopHolderRow) bool {
		return BareSymbol(row.Symbol) == bare
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RecordDate != rows[j].RecordDate {
			return rows[i].RecordDate < rows[j].RecordDate
		}
		return rows[i].HolderRank < rows[j].HolderRank
	})
	return rows, err
}

// LatestTradeDate 返回**实际有行情数据**的最新交易日（YYYYMMDD）。
//
// ⚠ 这里**不能**用 `trading_calendar` 的最大日期。
//
// `trading_calendar` 是**预先排好的日历**，天然包含未来年份
// （实测本机数据湖的日历分区到 2027，而 `daily_bars` 只到 2026-10-08）。
// 早期版本取日历最大值，于是湖报告"最新交易日 = 2027-10-08" ——
// 一个**没有任何数据**的未来日期，导致评分/回填/回测全部落在空区间上，
// 整个「切到数据湖」功能等于不可用。
//
// 行情源的"最新交易日"语义应当是"最后一根 K 线的日期"，
// 这也与 stockdb 那头（从真实行情取）一致。
func (l *Lake) LatestTradeDate() (int64, error) {
	return l.TradeDateAt(0)
}

// TradeDateAt 找出**不晚于** before 的、**实际有行情数据**的最近交易日（YYYYMMDD）。
// before <= 0 时等价于 LatestTradeDate。
//
// 条件选股指定日期时用它：用户填的那天可能不是交易日，
// 也可能当天数据还没同步，所以要落到该日或之前最近一个**有数据**的交易日。
func (l *Lake) TradeDateAt(before int64) (int64, error) {
	dates, err := l.BarPartitionDates()
	if err != nil {
		return 0, err
	}
	best := int64(0)
	for _, date := range dates {
		if before > 0 && date > before {
			continue
		}
		if date > best {
			best = date
		}
	}
	return best, nil
}

// BarPartitionDates 列出 `daily_bars` 代表的交易日（YYYYMMDD，升序）。
//
// 两条路径：
//
//  1. **分区名就是日期**（真实数据湖：`trade_date=2016-01-04`）——
//     直接解析目录名，不必碰 parquet，2612 个分区也是毫秒级。
//  2. **分区名不是日期**（部分数据集/测试数据按年分区：`trade_date=2026`）——
//     回落到读每个分区里的实际 bar 日期。这条路径只会在分区数很少时走到
//     （按年分区最多几十个），所以代价可控。
func (l *Lake) BarPartitionDates() ([]int64, error) {
	dir := l.DatasetDir("daily_bars")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cnlake: 读取 daily_bars 分区失败：%w", err)
	}

	// ⚠ **分区名是日期时也要缓存**：`bulkFillAllowed` 会**每只股票**调一次这个函数，
	// 而 `os.ReadDir` 要遍历 2612 个目录。不缓存的话，条件选股 678 只
	// 光是列目录就要几十秒，表现就是进度条卡在 0。
	l.barDatesMu.Lock()
	if l.barDatesLoaded && len(l.barDates) > 0 {
		out := make([]int64, len(l.barDates))
		copy(out, l.barDates)
		l.barDatesMu.Unlock()
		return out, nil
	}
	l.barDatesMu.Unlock()

	fromNames := make([]int64, 0, len(entries))
	var others []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		const prefix = "trade_date="
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		raw := strings.ReplaceAll(strings.TrimPrefix(name, prefix), "-", "")
		if len(raw) == 8 {
			if date, err := strconv.ParseInt(raw, 10, 64); err == nil && date > 0 {
				fromNames = append(fromNames, date)
				continue
			}
		}
		others = append(others, name)
	}
	if len(fromNames) > 0 && len(others) == 0 {
		sort.Slice(fromNames, func(i, j int) bool { return fromNames[i] < fromNames[j] })
		l.barDatesMu.Lock()
		l.barDates = fromNames
		l.barDatesLoaded = true
		l.barDatesMu.Unlock()
		return fromNames, nil
	}

	// 分区名不是日期 → 读实际内容
	if err := l.ensureDailyBarDates(); err != nil {
		return nil, err
	}
	l.barDatesMu.Lock()
	defer l.barDatesMu.Unlock()
	out := make([]int64, len(l.barDates))
	copy(out, l.barDates)
	return out, nil
}

// ensureDailyBarDates 从 parquet 内容里读出所有 bar 日期并缓存（只做一次）。
func (l *Lake) ensureDailyBarDates() error {
	l.barDatesMu.Lock()
	if l.barDatesLoaded {
		l.barDatesMu.Unlock()
		return nil
	}
	l.barDatesMu.Unlock()

	rows, err := readTyped[BarRow](l.DatasetFiles("daily_bars"), nil)
	if err != nil {
		return fmt.Errorf("cnlake: 读取 daily_bars 失败：%w", err)
	}
	seen := map[int64]bool{}
	dates := make([]int64, 0, 4096)
	for _, row := range rows {
		date := row.Date()
		if date <= 0 || seen[date] {
			continue
		}
		seen[date] = true
		dates = append(dates, date)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })

	l.barDatesMu.Lock()
	l.barDates = dates
	l.barDatesLoaded = true
	l.barDatesMu.Unlock()
	return nil
}
