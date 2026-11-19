package cnlake

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
// FilesForYears 按分区里的**年份**裁剪文件列表。
//
// 分区名形如 `report_period=2023Q1`、`change_date=2018`，所以用前缀解析出年份。
//
// ⚠ 两个边界可以**只给一个**，另一个传 0 表示不设限：
//
//	FilesForYears(x, 2015, 0)     → 2015 年及以后（最常用）
//	FilesForYears(x, 0, 2020)     → 2020 年及以前
//	FilesForYears(x, 2015, 2024)  → 闭区间
//	FilesForYears(x, 0, 0)        → 全部
//
// 早期版本在 `endYear <= 0` 时直接返回**全部**文件，导致 `(2015, 0)` 这种
// 显然合理的调用静默失效（裁剪没发生，白读 60 个分区）。单边界必须支持。
//
// 没有年份分区的数据集（`partitioned == false`）一律保留：裁不掉也不能丢数据。
func (l *Lake) FilesForYears(name string, startYear, endYear int) []string {
	all := l.DatasetFiles(name)
	if startYear <= 0 && endYear <= 0 {
		return all
	}
	dir := l.DatasetDir(name)
	out := make([]string, 0, len(all))
	for _, path := range all {
		year, partitioned := partitionYear(dir, path)
		if !partitioned {
			out = append(out, path)
			continue
		}
		if startYear > 0 && year < startYear {
			continue
		}
		if endYear > 0 && year > endYear {
			continue
		}
		out = append(out, path)
	}
	return out
}

// ---------------------------------------------------------------------------
// 快照裁剪
// ---------------------------------------------------------------------------

// latestSnapshot 是「一个数据集 = 一批**快照**」时通用的裁剪：
// 每个实体只保留它自己最新一份快照里的成员，并去掉同一份快照内的重复成员。
//
// 为什么必须有它：`industry_members` / `sector_members` 在数据湖里是
// **按 as_of_date 分区的每日全量快照**（本机是 2026-10-08 / 10-09 / 10-12 三份），
// 而 `readTyped(DatasetFiles(...), nil)` 读的是**三份快照的并集** —— 同一只股票
// 于是出现在同一个板块里 3 次。表现就是股票列表里每只票重复 3 行、
// 板块成分数被放大 3 倍（融资融券 3877 → 11610）、板块成交额/流通市值被重复累加。
//
// 关键点是**按实体（板块）各取各的最新快照**，而不是取全局最新日期：
// 概念板块会新增/下线，某个板块在最新分区里缺席时，全局最大值会让它整块消失。
// 成员的代码先用 BareSymbol 归一化，避免 "000001" 与 "000001.SZ" 被当成两个人。
//
// 参数：entity 取板块身份，asOf 取快照日期，member 取成分代码。
func latestSnapshot[T any](rows []T, entity func(T) string, asOf func(T) int32, member func(T) string) []T {
	newest := make(map[string]int32, 256)
	for _, row := range rows {
		key := entity(row)
		if date := asOf(row); date > newest[key] {
			newest[key] = date
		}
	}

	seen := make(map[string]struct{}, len(rows))
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		key := entity(row)
		if asOf(row) != newest[key] {
			continue
		}
		slot := key + "\x00" + member(row)
		if _, dup := seen[slot]; dup {
			continue
		}
		seen[slot] = struct{}{}
		out = append(out, row)
	}
	return out
}

// ---------------------------------------------------------------------------
// 类型化读取
// ---------------------------------------------------------------------------

// readFileGuarded 读一个 parquet 文件，并把**解析期的 panic 转成 error**。
//
// 为什么必须兜住：cne 正在更新数据时，分区文件可能只写了一半，
// 而 parquet-go 遇到截断/损坏的 footer 不一定返回 error —— 有些路径会 panic。
// Go 里**任何 goroutine panic 都会终止整个进程**，表现就是：
// 页面请求的连接被重置（前端 "Failed to fetch"）、容器重启、内存回落。
// 一个坏文件不该带走整个应用。
func readFileGuarded[T any](path string, keep func(*T) bool) (rows []T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("解析 %s 失败（文件可能正在被 cne 写入）：%v", filepath.Base(path), r)
		}
	}()
	handle, err := openParquet(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	out := make([]T, 0, 256)
	if err := readTypedFile(handle, keep, &out); err != nil {
		return out, err
	}
	return out, nil
}

// readFileEachGuarded 同 readFileGuarded，但逐行回调。
func readFileEachGuarded[T any](path string, keep func(*T) bool, each func(*T)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("解析 %s 失败（文件可能正在被 cne 写入）：%v", filepath.Base(path), r)
		}
	}()
	handle, err := openParquet(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	return readTypedFileEach(handle, keep, each)
}

// readTyped 并发读多个文件，任一条解析 panic 都只记为该文件的错误。
func readTyped[T any](files []string, keep func(*T) bool) ([]T, error) {
	if len(files) == 0 {
		return nil, nil
	}
	// 单文件没必要起协程
	if len(files) == 1 {
		rows, err := readFileGuarded(files[0], keep)
		return rows, err
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
				rows, err := readFileGuarded(files[i], keep)
				if err != nil {
					errs[i] = err
					continue
				}
				perFile[i] = rows
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// 先汇总再按**精确容量**分配：
	// 用 append 一路增长会让底层数组反复翻倍，峰值是最终大小的 2 倍以上。
	total := 0
	for i := range perFile {
		total += len(perFile[i])
	}
	out := make([]T, 0, total)
	for i := range files {
		if errs[i] != nil {
			return out, errs[i]
		}
		out = append(out, perFile[i]...)
		perFile[i] = nil // 尽早让 GC 回收，别把两份同时留在堆上
	}
	return out, nil
}

// lakeReadWorkers 按文件数与 CPU 数决定并发度。
//
// ⚠ 上限**不是越大越好**，也不是纯粹的"性能参数"：
//
//	parquet 解码是 CPU 密集型（zstd + thrift），并发开满会把所有核吃干。
//	而同一个进程里还有 HTTP 处理与**健康检查**要跑 —— NAS 上核少（常见 4 核），
//	32 路解码会把 /health 拖到几秒不响应，容器被判成不健康、进而重启，
//	前端看到的就是 "Failed to fetch"。这个现象和"内存暴涨"极像，很难分辨。
//
// 所以：默认留出余量（最多 NumCPU-1，且不超过 8），并可用
// FREESTOCK_LAKE_WORKERS 覆盖（批量回填想更快时调大即可）。
func lakeReadWorkers(fileCount int) int {
	workers := lakeReadWorkerLimit()
	if workers > fileCount {
		workers = fileCount
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// lakeReadMaxWorkers 是默认并发上限（不含"留一个核"的调整）。
const lakeReadMaxWorkers = 8

// lakeReadWorkerLimit 计算并发上限：默认 min(NumCPU-1, 8)，可用环境变量覆盖。
func lakeReadWorkerLimit() int {
	if v := strings.TrimSpace(os.Getenv("FREESTOCK_LAKE_WORKERS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 32 {
				n = 32
			}
			return n
		}
	}
	workers := runtime.NumCPU() - 1 // 留一个核给 HTTP / 健康检查
	if workers < 1 {
		workers = 1
	}
	if workers > lakeReadMaxWorkers {
		workers = lakeReadMaxWorkers
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

// readTypedFileEach 与 readTypedFile 同源，但**每行直接回调**，不累积切片。
//
// 给"只用到极少数行"的场景用：全市场 × 一段区间的行数可以是几十万，
// 而调用方真正需要的可能只有每个基准日各一行。
func readTypedFileEach[T any](handle *parquetHandle, keep func(*T) bool, each func(*T)) error {
	reader := parquet.NewGenericReader[T](handle.file)
	defer reader.Close()

	const batch = 2048
	buffer := make([]T, batch)
	for {
		count, err := reader.Read(buffer)
		for i := 0; i < count; i++ {
			if keep == nil || keep(&buffer[i]) {
				each(&buffer[i])
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

// DailyBarsInRangeEach 逐行回调 [startDate, endDate] 内的日线，**不在内存里累积**。
//
// 与 DailyBarsInRange 的唯一区别：不把结果拼成一个 []BarRow。
//
// 存在的理由：板块区间行情要"全市场 × 最近一百多个交易日"的收盘价，
// 那会有几十万行（几十 MB）；而真正用到的只有**每个基准日各取一行**。
// 先把几十万行读进内存再逐行挑，是这个页面内存暴涨的直接原因。
//
// ⚠ 回调会被**多个 worker 并发**调用（读取是并行的），且行序**不保证**有序 ——
// 调用方自己加锁/维护状态。返回值只表示是否读取失败（或 ctx 被取消）。
func (l *Lake) DailyBarsInRangeEach(ctx context.Context, codes []string, startDate, endDate int64, each func(BarRow)) error {
	if each == nil {
		return nil
	}
	want := map[string]struct{}{}
	for _, code := range codes {
		if bare := BareSymbol(code); bare != "" {
			want[bare] = struct{}{}
		}
	}
	all := len(want) == 0

	files, err := l.filesForDateRange(startDate, endDate)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	keep := func(row *BarRow) bool {
		if all {
			return true
		}
		_, ok := want[BareSymbol(row.Symbol)]
		return ok
	}

	// 每行立即回调，峰值只有"一个文件的批缓冲 × 并发数"，与区间长度无关。
	var mu sync.Mutex
	emit := func(row *BarRow) {
		mu.Lock()
		each(*row)
		mu.Unlock()
	}

	errs := make([]error, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < lakeReadWorkers(len(files)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				// ctx 取消后不再开新文件：冷启动扫描可能长达几分钟，
				// 而页面请求有预算 —— 超时就要能真的停下来，不能继续烧 CPU。
				if ctx != nil && ctx.Err() != nil {
					errs[i] = ctx.Err()
					continue
				}
				// 单个文件解析 panic 只记为该文件的错误，不能带走整个进程
				//（cne 正在写分区时，可能读到写了一半的 parquet）。
				if err := readFileEachGuarded(files[i], keep, emit); err != nil {
					errs[i] = err
				}
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
// 直接按分区目录名筛，不必打开任何文件。分区粒度**不一定是日**：
//
//	trade_date=2026-11-14   日
//	trade_date=2026-11      月   ← cne 的按月布局
//	trade_date=2026Q4       季   ← 部分数据集
//	trade_date=2026         年   ← 合成湖/部分数据集
//
// ⚠ 这里曾有个严重 bug：只认"8 位数字"的日分区，其它名字一律记为 unparsed，
// 而只要有 unparsed 就**返回全部文件**（日期范围被彻底忽略）。于是按月分区的
// 数据湖上，**每个**区间查询都会扫全部 323 个分区 —— 数据越多越慢，
// 并发读大文件还会把内存顶到容器上限。用户看到的"以前能开、现在打不开"
// 就是这么来的。
//
// 现在：把分区名解析成**日期区间**（首日, 末日），与 [start,end] 求交集。
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
		if !strings.HasPrefix(name, "trade_date=") {
			continue
		}
		first, last, ok := partitionInterval(name)
		if !ok {
			unparsed = append(unparsed, name)
			continue
		}
		// 区间**不相交**就跳过（分区覆盖 first..last，查询要 start..end）
		if startDate > 0 && last < startDate {
			continue
		}
		if endDate > 0 && first > endDate {
			continue
		}
		partition := filepath.Join(dir, name)
		files, err := filepath.Glob(filepath.Join(partition, "*.parquet"))
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}

	// 仍有无法识别的分区名 → 只能退回"全取"（正确但慢）。
	// 日/月/季/年都能解析，走到这里说明命名很特殊，记一条日志便于排查。
	if len(unparsed) > 0 {
		slog.Warn("daily_bars 存在无法解析的分区名，本次查询只能读取全部分区",
			"count", len(unparsed), "examples", unparsed[:min(len(unparsed), 3)])
		out = out[:0]
		out = append(out, l.DatasetFiles("daily_bars")...)
	}

	sort.Strings(out)
	return out, nil
}

// partitionInterval 把典型的分区目录名解析成"它覆盖的日期区间"（YYYYMMDD 首日/末日）。
//
// 支持：trade_date=2026-11-14 / 20261114 / 2026-11 / 202611 / 2026Q4 / 2026。
// 返回 false 表示名字不是这几种形状（调用方会退回"全取"）。
func partitionInterval(name string) (int64, int64, bool) {
	raw := strings.TrimSpace(strings.TrimPrefix(name, "trade_date="))
	if raw == "" {
		return 0, 0, false
	}
	upper := strings.ToUpper(raw)

	// 季度：2026Q4
	if len(upper) == 6 && upper[4] == 'Q' {
		year, err1 := strconv.Atoi(upper[:4])
		q, err2 := strconv.Atoi(upper[5:])
		if err1 != nil || err2 != nil || q < 1 || q > 4 {
			return 0, 0, false
		}
		firstMonth := time.Month((q-1)*3 + 1)
		first := time.Date(year, firstMonth, 1, 0, 0, 0, 0, time.UTC)
		last := time.Date(year, firstMonth+3, 0, 0, 0, 0, 0, time.UTC)
		return ymdInt(first), ymdInt(last), true
	}

	digits := strings.ReplaceAll(raw, "-", "")
	switch len(digits) {
	case 8: // 日：2026-11-14
		date, err := strconv.Atoi(digits)
		if err != nil || date <= 0 {
			return 0, 0, false
		}
		return int64(date), int64(date), true
	case 6: // 月：2026-11
		year, err1 := strconv.Atoi(digits[:4])
		month, err2 := strconv.Atoi(digits[4:])
		if err1 != nil || err2 != nil || month < 1 || month > 12 {
			return 0, 0, false
		}
		first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		last := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC)
		return ymdInt(first), ymdInt(last), true
	case 4: // 年：2026
		year, err := strconv.Atoi(digits)
		if err != nil || year <= 0 {
			return 0, 0, false
		}
		return int64(year*10000 + 101), int64(year*10000 + 1231), true
	default:
		return 0, 0, false
	}
}

// ymdInt 把 time.Time 变成 YYYYMMDD 整数。
func ymdInt(t time.Time) int64 {
	return int64(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// ---------------------------------------------------------------------------
// 源分区指纹（判断"派生缓存还能不能用"）
// ---------------------------------------------------------------------------

// DailyPartitionDate 从 daily_bars 的文件路径里取出它所属分区的日期（YYYYMMDD）。
//
// 分区名不是日期（例如合成湖按年分区 `trade_date=2024`）时返回 0，
// 表示"这个文件不属于任何具体交易日" —— 指纹计算会把它**始终计入**，
// 构建时与查询时的取法一致，所以不会因此产生误判。
func (l *Lake) DailyPartitionDate(path string) int64 {
	rel, err := filepath.Rel(l.DatasetDir("daily_bars"), path)
	if err != nil {
		return 0
	}
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		_, value, found := strings.Cut(segment, "=")
		if !found {
			continue
		}
		raw := strings.ReplaceAll(value, "-", "")
		if len(raw) != 8 {
			return 0
		}
		date, convErr := strconv.ParseInt(raw, 10, 64)
		if convErr != nil {
			return 0
		}
		return date
	}
	return 0
}

// SourceSignature 返回"截至 through（含）的日分区"的内容指纹。
//
// 用途：判断「按股票」派生缓存里那段**冻结历史**是否还等于源头。
// 缓存是复制品，复制品唯一能保证正确的办法就是"证明源头没变" ——
// 指纹取每个分区文件的 (路径, 大小, mtime)，任何一次落盘变化
// （补跑历史、重算某天、盘中追加、删了重写）都会改变它。
//
// through<=0 表示"全部分区都算进来"。
//
// ⚠ 构建缓存时必须**在读数据之前**算一次并存进清单：如果算在之后，
// 构建期间被改写的文件会被记成"新指纹"，而缓存里存的还是旧内容 ——
// 那就正好把"不一致"记成了"一致"。
//
// 代价：每个分区文件一次 stat（本机 2614 个）。
func (l *Lake) SourceSignature(through int64) (string, error) {
	files := l.DatasetFiles("daily_bars")
	hash := sha256.New()
	var buffer []byte
	for _, path := range files {
		if date := l.DailyPartitionDate(path); date > 0 && through > 0 && date > through {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			// 文件不见了也算"源头变了"：让调用方回退直读
			return "", fmt.Errorf("cnlake: 统计分区文件失败：%w", err)
		}
		buffer = append(buffer[:0], path...)
		buffer = append(buffer, 0)
		buffer = strconv.AppendInt(buffer, info.Size(), 10)
		buffer = append(buffer, 0)
		buffer = strconv.AppendInt(buffer, info.ModTime().UnixNano(), 10)
		buffer = append(buffer, '\n')
		hash.Write(buffer)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16]), nil
}

// LatestPartitionDate 返回 daily_bars 最后一个分区日期（没有分区时 0）。
func (l *Lake) LatestPartitionDate() int64 {
	dates, err := l.BarPartitionDates()
	if err != nil || len(dates) == 0 {
		return 0
	}
	return dates[len(dates)-1]
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
//
// 数据集是按 as_of_date 分区的每日快照，这里只保留**每个行业自己最新一份
// 快照**的成分（见 latestSnapshot）—— 否则同一只股票会在行业里重复多行。
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
	// 板块身份要带分类体系与行业名：同名/同代码的行业可以存在于不同分类体系。
	rows = latestSnapshot(rows,
		func(row IndustryMemberRow) string {
			return row.ClassificationSystem + "\x00" + row.IndustryCode + "\x00" + row.IndustryName
		},
		func(row IndustryMemberRow) int32 { return row.AsOfDate },
		func(row IndustryMemberRow) string { return BareSymbol(row.Symbol) },
	)

	l.mu.Lock()
	l.industries = rows
	l.mu.Unlock()
	return rows, nil
}

// SectorMembers 返回全部板块成分（静态数据，缓存）。
//
// 与 IndustryMembers 同理：只取每个板块最新一份 as_of_date 快照的成分。
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
	rows = latestSnapshot(rows,
		func(row SectorMemberRow) string { return row.SectorCode + "\x00" + row.SectorName },
		func(row SectorMemberRow) int32 { return row.AsOfDate },
		func(row SectorMemberRow) string { return BareSymbol(row.Symbol) },
	)

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

// AllShareStructuresSince 只读 fromYear 起的股本变更。
//
// 数据湖的 share_structure 从 2001 起共 16.8 万行，而"重建历史市值"只需要
// 覆盖评估区间（多期限性价比评估的估值序列起点是 2021，往前留几年余量即可）。
// 按年份裁剪分区可以直接省掉早期年份的解码。
func (l *Lake) AllShareStructuresSince(fromYear int) ([]ShareStructureRow, error) {
	files := l.DatasetFiles("share_structure")
	if len(files) == 0 {
		return nil, nil
	}
	if fromYear > 0 {
		files = l.FilesForYears("share_structure", fromYear, 0)
	}
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
	return l.latestDateFromPartitions()
}

// barPartitionDir 是 daily_bars 下的一个分区目录。
type barPartitionDir struct {
	name string // trade_date=2026-11
	path string
}

// barPartitionDirs 列出 daily_bars 的分区目录（按名字升序）。
func (l *Lake) barPartitionDirs() []barPartitionDir {
	// 结果缓存：NAS 上 os.ReadDir 6000+ 个目录要几十毫秒到几秒，
	// 而这个函数会被 LatestTradeDate / BarPartitionDates 反复调用。
	l.barDatesMu.Lock()
	if l.partsCache != nil && time.Since(l.partsAt) <= datasetFilesTTL {
		out := make([]barPartitionDir, len(l.partsCache))
		copy(out, l.partsCache)
		l.barDatesMu.Unlock()
		return out
	}
	l.barDatesMu.Unlock()

	root := l.DatasetDir("daily_bars")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]barPartitionDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "trade_date=") {
			continue
		}
		out = append(out, barPartitionDir{name: entry.Name(), path: filepath.Join(root, entry.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	l.barDatesMu.Lock()
	l.partsCache = out
	l.partsAt = time.Now()
	l.barDatesMu.Unlock()
	copied := make([]barPartitionDir, len(out))
	copy(copied, out)
	return copied
}

// latestDateFromPartitions 求"实际有行情数据的最新交易日"。
//
// 真实数据湖常常是**混合布局**：历史按天分区（trade_date=2020-01-04），
// 而 cne 的日更先写当期分区（trade_date=2026-11）。所以：
//
//  1. 按天分区的名字直接给出日期，零成本；
//  2. 只读**最新的 1~2 个粗分区**（按月/按年）—— 当期分区必然包含最新数据，
//     读完就停；不读更早的，更不读整个数据集；
//  3. 判据是文件指纹（名字+大小+mtime）而不是目录 mtime：cne 会原地重写
//     当期 parquet，目录 mtime 不变，只靠目录会让进程一直答旧日期。
//
// 之前那版"按分区名倒序取最新目录"会在混合布局里挑到旧的按天分区；
// 全量扫描版则会在 NAS 上读几百 MB~GB（正是用户抱怨的卡 IO 和请求超时）。
func (l *Lake) latestDateFromPartitions() (int64, error) {
	// ⚠ **先查结果缓存，再碰文件系统**。
	//
	// LatestTradeDate 在组合页/评分/回测/邮件里会被反复调用（甚至按持仓逐个调）。
	// 之前把缓存检查放在"列分区 + 读 footer"之后，每次调用都要在 NAS 上
	// 列 6000+ 个目录、再打开当期 parquet 读 footer —— 请求就这样卡到超时。
	// 现在 30 秒内的重复调用零 IO。
	l.barDatesMu.Lock()
	if l.recentBarDate > 0 && time.Since(l.recentBarAt) <= datasetFilesTTL {
		date := l.recentBarDate
		l.barDatesMu.Unlock()
		return date, nil
	}
	l.barDatesMu.Unlock()

	dirs := l.barPartitionDirs()
	if len(dirs) == 0 {
		return 0, fmt.Errorf("数据湖的 daily_bars 没有任何分区，请确认已同步")
	}
	dayBest := int64(0)
	dayMaxName := ""
	coarse := make([]barPartitionDir, 0, 4)
	for _, dir := range dirs {
		raw := strings.ReplaceAll(strings.TrimPrefix(dir.name, "trade_date="), "-", "")
		if len(raw) == 8 {
			if raw > dayMaxName {
				dayMaxName = raw
			}
			if date, err := strconv.ParseInt(raw, 10, 64); err == nil && date > dayBest {
				dayBest = date
			}
			continue
		}
		coarse = append(coarse, dir)
	}
	// 粗分区按名字倒序 = 从新到旧（2026-11 > 2026-10 > 2026）。
	sort.Slice(coarse, func(i, j int) bool { return coarse[i].name > coarse[j].name })
	if len(coarse) > 2 {
		coarse = coarse[:2]
	}

	var key strings.Builder
	key.WriteString(dayMaxName)
	for _, dir := range coarse {
		key.WriteString("|")
		key.WriteString(dir.name)
		key.WriteString(":")
		key.WriteString(filesFingerprint(l.parquetFilesIn(dir.path)))
	}
	cacheKey := key.String()

	l.barDatesMu.Lock()
	if l.recentBarDate > 0 && l.recentBarFP == cacheKey && time.Since(l.recentBarAt) <= datasetFilesTTL {
		date := l.recentBarDate
		l.barDatesMu.Unlock()
		return date, nil
	}
	l.barDatesMu.Unlock()

	best := dayBest
	for _, dir := range coarse {
		if !coarsePartitionMayExceed(dir.name, best) {
			continue
		}
		files := l.parquetFilesIn(dir.path)
		if len(files) == 0 {
			continue
		}
		// 先用 parquet **footer 里的 min/max 统计**求最大日期：只读文件尾部几 KB，
		// 不用把当期分区（几万~几百万行）整个读进内存。cne 的落盘都会写这个统计。
		if date, ok := maxDateFromFooters(files); ok {
			if date > best {
				best = date
			}
			break
		}
		// 统计缺失（少数写入器不写 stats）才退化为读内容。
		// ⚠ 必须**流式**逐文件读：当期分区可能是几百万行的按年分区，
		// readTyped 会把全部行堆进内存（几百 MB），在 NAS 上直接把进程顶到
		// GOMEMLIMIT/mem_limit，容器被 OOM kill → 前端只看到 "Failed to fetch"。
		partitionBest := int64(0)
		for _, file := range files {
			_ = readFileEachGuarded(file, nil, func(row *BarRow) {
				if date := row.Date(); date > partitionBest {
					partitionBest = date
				}
			})
		}
		if partitionBest > best {
			best = partitionBest
		}
		// 当期分区已经处理过：更早的分区不可能更新，停止。
		break
	}
	if best == 0 {
		return 0, fmt.Errorf("数据湖的 daily_bars 分区里没有可用的行情日期，请确认已同步")
	}
	l.barDatesMu.Lock()
	l.recentBarDate = best
	l.recentBarFP = cacheKey
	l.recentBarAt = time.Now()
	l.barDatesMu.Unlock()
	return best, nil
}

// coarsePartitionMayExceed 判断一个粗粒度分区（2026 / 2026-11）是否可能含有
// 比 best 更晚的日期。用分区能表示的最大日期做上界，避免无谓地读 parquet。
func coarsePartitionMayExceed(name string, best int64) bool {
	digits := strings.ReplaceAll(strings.TrimPrefix(name, "trade_date="), "-", "")
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return true
	}
	upper := int64(0)
	switch len(digits) {
	case 4:
		upper = value*10000 + 1231
	case 6:
		upper = value*100 + 31
	case 8:
		upper = value
	default:
		return true
	}
	return upper > best
}

// maxDateFromFooters 只读 parquet footer 的列统计，求 trade_date 的最大值。
//
// 代价与文件大小无关（几 KB 元数据），所以月度/年度分区也能瞬间回答
// "这里最新是哪一天"，不必把整月/整年行情读进内存 —— 在 NAS 上这决定了
// 评分请求是 50ms 还是几分钟。
//
// 返回 ok=false 表示这些文件没写统计（或找不到 trade_date 列），调用方应退化为读内容。
func maxDateFromFooters(files []string) (best int64, found bool) {
	// cne 正在写文件时，parquet footer 可能读到半截；解析器若因此 panic，
	// 必须兜住 —— 这个函数是在 HTTP 请求的 goroutine 里跑的，
	// panic 会直接把连接断掉，前端只看到 "Failed to fetch"。
	defer func() {
		if r := recover(); r != nil {
			best, found = 0, false
		}
	}()
	best = 0
	found = false
	for _, path := range files {
		handle, err := openParquet(path)
		if err != nil {
			continue
		}
		metadata := handle.file.Metadata()
		for _, group := range metadata.RowGroups {
			for _, column := range group.Columns {
				meta := column.MetaData
				if len(meta.PathInSchema) == 0 {
					continue
				}
				if meta.PathInSchema[len(meta.PathInSchema)-1] != "trade_date" {
					continue
				}
				stats := meta.Statistics
				raw := stats.MaxValue
				if len(raw) == 0 {
					raw = stats.Max
				}
				if len(raw) < 4 {
					continue
				}
				days := int32(binary.LittleEndian.Uint32(raw[:4]))
				if date := daysToDate(days); date > best {
					best = date
					found = true
				}
			}
		}
		_ = handle.Close()
	}
	return best, found
}

// parquetFilesIn 列出目录下的 parquet 文件（升序，保证指纹稳定）。
func (l *Lake) parquetFilesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".parquet") {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(out)
	return out
}

// filesFingerprint 用"文件名 + 大小 + mtime"给一组文件做指纹。
func filesFingerprint(files []string) string {
	var b strings.Builder
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%d|%d;", filepath.Base(file), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
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
	//
	// ⚠⚠ 但缓存**必须能被"数据湖落了新分区"打破**。这里原本是永久缓存
	// （`barDatesLoaded` 一旦为 true 就再也不过问），后果是：
	//
	//	进程启动后列一次目录 → 记住"最新交易日 = 昨天"；
	//	日更落下今天的分区；
	//	之后所有"最新交易日"仍然答昨天 —— **直到进程重启**。
	//
	// 这会让下游一连串缓存都认为"自己已经覆盖到最新"，全都不会失效
	// （`symbolCacheState` 的 covered 判定、`cachedTradeDate`、
	//  K 线的补尾区间…），表现就是用户说的"不重启就一直读缓存"。
	//
	// 判据同 DatasetFiles：目录 mtime 变了就重列（stat 微秒级，
	// 比 ReadDir 2612 个目录便宜得多），再加一道 TTL 兜底。
	dirMod := time.Time{}
	if info, statErr := os.Stat(dir); statErr == nil {
		dirMod = info.ModTime()
	}
	l.barDatesMu.Lock()
	// ⚠ 只有当日期是**从分区名读出来**的时候，目录 mtime 才是充分判据
	// （新增一天 = 新增一个 trade_date=YYYY-MM-DD 目录）。
	// 按年/按月分区的退化布局下，日期是从 parquet 内容里读的，
	// cne 会**原地重写**当前分区文件而目录 mtime 不变 —— 这里若直接返回缓存，
	// 下面基于文件指纹的失效逻辑根本没机会执行（现象：数据更到 1118，评分还是 1113）。
	if l.barDatesLoaded && len(l.barDates) > 0 && l.barDatesDirMod.Equal(dirMod) && l.barDatesFromNames {
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
		l.barDatesFromNames = true
		l.barDatesDirMod = dirMod
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

// ensureDailyBarDates 从 parquet 内容里读出所有 bar 日期并缓存。
//
// 只在"分区名不是日期"这种退化布局下才会走到（正常布局由
// `BarPartitionDates` 直接读目录名）。判据与它保持一致：
// 目录 mtime 变了就必须重读，否则新增分区看不见。
func (l *Lake) ensureDailyBarDates() error {
	dir := l.DatasetDir("daily_bars")
	dirMod := time.Time{}
	if info, statErr := os.Stat(dir); statErr == nil {
		dirMod = info.ModTime()
	}

	// 快路径：目录没变、最近一次核对也在 TTL 内 → 直接用缓存。
	// 这里只做一次 stat + 一次加锁；指纹要 stat 每个 parquet 文件，
	// 不能每次调用都算（bulkFillAllowed 会按股票逐只调用）。
	l.barDatesMu.Lock()
	loaded := l.barDatesLoaded
	sameDir := l.barDatesDirMod.Equal(dirMod)
	recent := !l.barDatesFPAt.IsZero() && time.Since(l.barDatesFPAt) <= datasetFilesTTL
	l.barDatesMu.Unlock()
	if loaded && sameDir && recent {
		return nil
	}

	// 过了 TTL（或目录变了）才看文件指纹：
	// cne 日更常常**原地重写**当月/当年分区里的 parquet，目录 mtime 不变，
	// 只按目录 mtime 失效会一直答旧日期（"数据更到 1118，评分还是 1113"）。
	fp := l.dailyBarFilesFingerprint()
	l.barDatesMu.Lock()
	loaded = l.barDatesLoaded
	sameFP := l.barDatesFP == fp && fp != ""
	l.barDatesMu.Unlock()
	if loaded && sameFP {
		l.barDatesMu.Lock()
		l.barDatesFPAt = time.Now()
		l.barDatesMu.Unlock()
		return nil
	}

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
	l.barDatesFromNames = false
	l.barDatesDirMod = dirMod
	l.barDatesFP = fp
	l.barDatesFPAt = time.Now()
	l.barDatesMu.Unlock()
	return nil
}

// dailyBarFilesFingerprint 用"文件路径 + 大小 + mtime"给 daily_bars 做指纹。
//
// 为什么需要它：cne 的日更经常**原地重写**当月分区里的 parquet，
// 目录 mtime 不变 → 只按目录 mtime 失效的缓存会一直认为"最新交易日还是旧的那天"。
// 文件级指纹能发现原地重写；代价是对每个 parquet 做一次 stat
// （按月分区约几百个文件，毫秒级），且只在数据集文件列表缓存过期时才重新计算。
func (l *Lake) dailyBarFilesFingerprint() string {
	// 只统计**最新两个分区**的文件：cne 的日更只改当期分区，
	// 而给 6000+ 个文件逐个 stat 在 NAS 上就是几秒到几十秒的卡顿。
	dirs := l.barPartitionDirs()
	var b strings.Builder
	from := len(dirs) - 2
	if from < 0 {
		from = 0
	}
	for _, dir := range dirs[from:] {
		b.WriteString(filesFingerprint(l.parquetFilesIn(dir.path)))
	}
	return b.String()
}
