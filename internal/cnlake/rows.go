package cnlake

import (
	"fmt"
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
	if workers > 8 {
		workers = 8
	}
	if workers > fileCount {
		workers = fileCount
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

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
	rows, err := l.Rows(dataset, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]float64, 4096)
	for _, row := range rows {
		if !strings.EqualFold(asString(row["statement_type"]), "income") {
			continue
		}
		if !strings.EqualFold(asString(row["item_code"]), "net_profit") {
			continue
		}
		code := BareSymbol(asString(row["symbol"]))
		period := asString(row["report_period"])
		if code == "" || period == "" {
			continue
		}
		if out[code] == nil {
			out[code] = make(map[string]float64, 16)
		}
		out[code][period] = asFloat(row["item_value"])
	}
	return out, nil
}

// CorporateActions 读取某只股票的分红送转记录，按除权日升序。
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

// LatestTradeDate 从 trading_calendar 找出最新交易日（YYYYMMDD）。
func (l *Lake) LatestTradeDate() (int64, error) {
	return l.TradeDateAt(0)
}

// TradeDateAt 找出**不晚于** before 的最近交易日（YYYYMMDD）。
// before <= 0 时等价于 LatestTradeDate。
//
// 条件选股指定日期时用它：用户填的那天可能不是交易日，
// 也可能当天数据还没同步，所以要落到该日或之前最近的一个交易日。
func (l *Lake) TradeDateAt(before int64) (int64, error) {
	type calendarRow struct {
		TradeDate int32 `parquet:"trade_date,date"`
		IsTrading bool  `parquet:"is_trading"`
	}
	rows, err := readTyped(l.DatasetFiles("trading_calendar"), func(row *calendarRow) bool {
		return row.IsTrading
	})
	if err != nil {
		return 0, err
	}
	best := int64(0)
	for _, row := range rows {
		date := daysToDate(row.TradeDate)
		if before > 0 && date > before {
			continue
		}
		if date > best {
			best = date
		}
	}
	return best, nil
}
