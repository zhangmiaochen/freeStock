// Package cnlake 只读访问 cnequity 数据湖的 curated parquet。
//
// 为什么需要它：free-stockdb 的默认数据集不含财务报表，
// 而 cnequity 的数据湖定义了 financial_statement_items、share_structure、
// shareholder_counts、top_holders、valuation_metrics 等数据集，
// 落盘为 curated/<数据集>/<分区>/part-merged.parquet。
//
// 这里刻意做成「按需直读 parquet」而不是写死结构体：
// 数据湖的 schema 会演进（比如加字段），按 schema 动态取值更耐用。
package cnlake

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// 常见的数据湖位置，Open 时按顺序探测。
var defaultRoots = []string{
	`D:\workspace\stockTool\CNEquity\data\cnequity`,
	`C:\workspace\stockTool\CNEquity\data\cnequity`,
}

// 财务相关数据集，按重要性排序。
var financeDatasets = []string{
	"financial_statement_items",
	"earnings_disclosure_schedule",
	"valuation_metrics",
	"share_structure",
	"shareholder_counts",
	"top_holders",
}

// Lake 是数据湖的只读句柄。
type Lake struct {
	root string

	mu    sync.Mutex
	files map[string][]string // 数据集 -> parquet 文件列表（带缓存）

	// 静态数据集缓存：内容几乎不变，读一次就留着
	instruments []InstrumentRow
	industries  []IndustryMemberRow
	sectors     []SectorMemberRow
}

// Open 打开指定根目录的数据湖；root 为空时自动探测。
func Open(root string) *Lake {
	if root == "" {
		root = Detect()
	}
	return &Lake{root: root, files: map[string][]string{}}
}

// Detect 依次探测环境变量与常见路径，返回第一个存在且带 curated 子目录的位置。
func Detect() string {
	if env := strings.TrimSpace(os.Getenv("FREESTOCK_CNLAKE")); env != "" {
		if hasCurated(env) {
			return env
		}
	}
	for _, candidate := range defaultRoots {
		if hasCurated(candidate) {
			return candidate
		}
	}
	return ""
}

func hasCurated(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "curated"))
	return err == nil && info.IsDir()
}

// Root 返回当前使用的数据湖根目录（可能为空）。
func (l *Lake) Root() string { return l.root }

// Available 判断数据湖是否可用。
func (l *Lake) Available() bool { return hasCurated(l.root) }

// DatasetDir 返回某个数据集的 curated 目录。
func (l *Lake) DatasetDir(name string) string {
	return filepath.Join(l.root, "curated", name)
}

// HasDataset 判断数据集目录是否存在。
func (l *Lake) HasDataset(name string) bool {
	info, err := os.Stat(l.DatasetDir(name))
	return err == nil && info.IsDir()
}

// Datasets 列出 curated 下已有的数据集名。
func (l *Lake) Datasets() []string {
	if !l.Available() {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(l.root, "curated"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

// DatasetFiles 列出某个数据集下的所有 parquet 文件（结果带缓存）。
func (l *Lake) DatasetFiles(name string) []string {
	l.mu.Lock()
	if cached, ok := l.files[name]; ok {
		l.mu.Unlock()
		return cached
	}
	l.mu.Unlock()

	dir := l.DatasetDir(name)
	var found []string
	filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".parquet") {
			found = append(found, path)
		}
		return nil
	})
	sort.Strings(found)

	l.mu.Lock()
	l.files[name] = found
	l.mu.Unlock()
	return found
}

// Partition 是数据集下的一个分区目录，例如 report_period=2024Q4。
type Partition struct {
	Key   string `json:"key"`   // 分区字段名，如 report_period
	Value string `json:"value"` // 分区值，如 2024Q4
	Files int    `json:"files"`
}

// Partitions 列出数据集的分区。cnequity 按报告期/日期分区存放 parquet，
// 先看分区就能知道有哪些报告期，代价极低（不用读文件内容）。
func (l *Lake) Partitions(name string) []Partition {
	dir := l.DatasetDir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	out := make([]Partition, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key, value, found := strings.Cut(entry.Name(), "=")
		if !found {
			continue
		}
		files := 0
		filepath.WalkDir(filepath.Join(dir, entry.Name()), func(path string, item fs.DirEntry, err error) error {
			if err == nil && !item.IsDir() && strings.HasSuffix(item.Name(), ".parquet") {
				files++
			}
			return nil
		})
		out = append(out, Partition{Key: key, Value: value, Files: files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// DatasetStatus 描述一个数据集的可用情况。
type DatasetStatus struct {
	Name       string      `json:"name"`
	Exists     bool        `json:"exists"`
	Files      int         `json:"files"`
	Rows       int64       `json:"rows"`
	LatestDate int64       `json:"latestDate"` // 仅在探测到日期列时有值
	Partitions []Partition `json:"partitions"`
	Error      string      `json:"error"`
}

// Status 汇总财务相关数据集的可用情况。
func (l *Lake) Status(ctx context.Context) []DatasetStatus {
	out := make([]DatasetStatus, 0, len(financeDatasets))
	for _, name := range financeDatasets {
		out = append(out, l.DatasetStatus(name))
	}
	return out
}

// DatasetStatus 统计单个数据集：文件数、总行数、分区、最新的日期列值。
func (l *Lake) DatasetStatus(name string) DatasetStatus {
	status := DatasetStatus{Name: name}
	files := l.DatasetFiles(name)
	status.Files = len(files)
	status.Exists = len(files) > 0
	if !status.Exists {
		return status
	}
	status.Partitions = l.Partitions(name)

	for _, path := range files {
		handle, err := openParquet(path)
		if err != nil {
			status.Error = err.Error()
			continue
		}
		status.Rows += handle.file.NumRows()
		handle.Close()
	}
	status.LatestDate = l.latestDate(name)
	return status
}

// latestDate 从数据集里找出最大的日期列值（YYYYMMDD）。
// 只扫最后两个文件，够用且不会为了一个展示值把整库读一遍。
func (l *Lake) latestDate(name string) int64 {
	files := l.DatasetFiles(name)
	if len(files) == 0 {
		return 0
	}
	if len(files) > 2 {
		files = files[len(files)-2:]
	}

	latest := int64(0)
	for _, path := range files {
		rows, err := readParquet(path, 0)
		if err != nil {
			continue
		}
		for _, row := range rows {
			for key, value := range row {
				if !strings.HasSuffix(key, "_date") && key != "report_period" {
					continue
				}
				if num, ok := toDateNumber(value); ok && num > latest {
					latest = num
				}
			}
		}
	}
	return latest
}

// toDateNumber 把日期值折算成可比较的 YYYYMMDD。
// 支持 20240131 这类整数、2024-01-31 这类字符串、以及 "2024Q4" 这类报告期。
func toDateNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, typed)
		if len(digits) >= 6 {
			var num int64
			fmt.Sscanf(digits[:min(8, len(digits))], "%d", &num)
			return num, true
		}
	}
	return 0, false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Rows 读取某个数据集的行（limit <= 0 表示不限）。
//
// 返回的 map 键为 parquet 的列名；DATE 列折算成 YYYYMMDD 整数，
// 其它数值/字符串按原始类型返回。
func (l *Lake) Rows(name string, limit int) ([]map[string]any, error) {
	files := l.DatasetFiles(name)
	if len(files) == 0 {
		return nil, fmt.Errorf("cnlake: 数据集 %s 尚未入库", name)
	}

	var out []map[string]any
	for _, path := range files {
		rows, err := readParquet(path, remaining(limit, len(out)))
		if err != nil {
			return out, err
		}
		out = append(out, rows...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
	}
	return out, nil
}

func remaining(limit, got int) int {
	if limit <= 0 {
		return 0
	}
	left := limit - got
	if left < 0 {
		return 0
	}
	return left
}

// StatementItem 是财务报表的一个科目行（financial_statement_items 的扁平化视图）。
type StatementItem struct {
	Symbol        string  `json:"symbol"`
	ReportPeriod  string  `json:"reportPeriod"`
	StatementType string  `json:"statementType"`
	ItemCode      string  `json:"itemCode"`
	ItemValue     float64 `json:"itemValue"`
	AnnounceDate  int64   `json:"announceDate"`
	Source        string  `json:"source"`
}

// StatementItems 读取某只股票的财报科目。
// symbol 支持 600519 与 600519.SH 两种写法。
func (l *Lake) StatementItems(ctx context.Context, symbol string, limit int) ([]StatementItem, error) {
	const dataset = "financial_statement_items"
	if !l.HasDataset(dataset) {
		return nil, fmt.Errorf("cnlake: %s 尚未入库", dataset)
	}

	bare := strings.SplitN(strings.ToUpper(symbol), ".", 2)[0]
	rows, err := l.Rows(dataset, 0)
	if err != nil {
		return nil, err
	}

	out := make([]StatementItem, 0, 64)
	for _, row := range rows {
		code := strings.SplitN(strings.ToUpper(asString(row["symbol"])), ".", 2)[0]
		if code != bare {
			continue
		}
		item := StatementItem{
			Symbol:        asString(row["symbol"]),
			ReportPeriod:  asString(row["report_period"]),
			StatementType: asString(row["statement_type"]),
			ItemCode:      asString(row["item_code"]),
			ItemValue:     asFloat(row["item_value"]),
			Source:        asString(row["source"]),
		}
		if date, ok := toDateNumber(row["announce_date"]); ok {
			item.AnnounceDate = date
		}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// parquet 底层读取
// ---------------------------------------------------------------------------

// parquetHandle 把 parquet.File 和它底层的文件句柄绑在一起。
// parquet.File 自己不带 Close，但底层的 os.File 必须关，否则会漏句柄。
type parquetHandle struct {
	file *parquet.File
	raw  *os.File
}

func (h *parquetHandle) Close() error { return h.raw.Close() }

func openParquet(path string) (*parquetHandle, error) {
	raw, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := raw.Stat()
	if err != nil {
		raw.Close()
		return nil, err
	}
	parsed, err := parquet.OpenFile(raw, info.Size())
	if err != nil {
		raw.Close()
		return nil, err
	}
	return &parquetHandle{file: parsed, raw: raw}, nil
}

// readParquet 按 schema 动态把 parquet 行读成 map。limit <= 0 表示不限。
func readParquet(path string, limit int) ([]map[string]any, error) {
	handle, err := openParquet(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	schema := handle.file.Schema()
	fields := schema.Fields()
	names := make([]string, len(fields))
	dateColumns := make([]bool, len(fields))
	for i, field := range fields {
		names[i] = field.Name()
		dateColumns[i] = isDateField(field)
	}

	reader := parquet.NewReader(handle.file)
	defer reader.Close()

	const batch = 512
	out := make([]map[string]any, 0, batch)
	buffer := make([]parquet.Row, batch)

	for {
		if limit > 0 && len(out) >= limit {
			break
		}
		count, readErr := reader.ReadRows(buffer)
		for i := 0; i < count; i++ {
			row := buffer[i]
			record := make(map[string]any, len(names))
			for j, value := range row {
				if j >= len(names) {
					break
				}
				record[names[j]] = decodeValue(value, dateColumns[j])
			}
			out = append(out, record)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		if readErr != nil {
			// io.EOF 是正常结束
			if readErr.Error() == "EOF" {
				break
			}
			return out, nil
		}
		if count == 0 {
			break
		}
	}
	return out, nil
}

func isDateField(field parquet.Field) bool {
	logical := field.Type().LogicalType()
	if logical == nil {
		return false
	}
	_, ok := logical.Value.(*format.DateType)
	return ok
}

func decodeValue(value parquet.Value, isDate bool) any {
	if value.IsNull() {
		return nil
	}
	switch value.Kind() {
	case parquet.Boolean:
		return value.Boolean()
	case parquet.Int32:
		raw := value.Int32()
		if isDate {
			return daysToDate(raw)
		}
		return int64(raw)
	case parquet.Int64:
		return value.Int64()
	case parquet.Float:
		return float64(value.Float())
	case parquet.Double:
		return value.Double()
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return string(value.ByteArray())
	default:
		return value.String()
	}
}

// daysToDate 把 parquet 的 DATE（1970 起的天数）折算成 YYYYMMDD。
func daysToDate(days int32) int64 {
	t := time.Unix(0, 0).UTC().AddDate(0, 0, int(days))
	return int64(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// dateToDays 是 daysToDate 的逆运算：YYYYMMDD -> 1970 起的天数。
// 用来把 DATE 列的值和调用方给的日期做等值比较。
func dateToDays(date int64) int32 {
	if date <= 0 {
		return 0
	}
	year, month, day := int(date/10000), int(date/100%100), int(date%100)
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return int32(t.Sub(time.Unix(0, 0).UTC()).Hours() / 24)
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func asFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case int32:
		return float64(typed)
	default:
		return 0
	}
}
