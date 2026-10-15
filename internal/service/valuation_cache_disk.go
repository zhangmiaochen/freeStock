package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/valuation"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
)

// ---------------------------------------------------------------------------
// 派生数据快照（把"评估所需的原始数据"物化到磁盘）
//
// ## 为什么必须做这一步
//
// 多期限性价比评估要从数据湖读：
//
//	daily_bars                 703 万行（1390 个交易日分区）
//	financial_statement_items  652 万行（47 个报告期分区）
//	share_structure            16.8 万行
//	复权因子                    8068 个文件（仅被评分的标的）
//
// 实测这份数据**冷读一次 16 秒**（已开 32 路并发解码，是全市场评估的硬下限）。
// 用户要求"全市场刷新 ≤ 10 秒"，所以不能每次都重读原始分区 ——
// 必须在**第一次**读完之后，把已经落位好的结果物化下来，之后直接读这份快照。
//
// 这与数据湖自己的 `meta/adj_factors_cache`（把每只股票的复权因子缓存成
// `<code>_<ex>_hfq.parquet`）是同一套思路，区别只是缓存的是"评估输入"。
//
// ## 缓存形状
//
// 一个目录 `<lake>/valuation_cache/`，四个紧凑 parquet + 一份元数据：
//
//	bars.parquet       每只标的一段行情（symbol/date/OHLC/volume/amount）
//	statements.parquet 财报科目（symbol/report_period/item_code/announce/value）
//	shares.parquet     股本变更（symbol/date/total/float）
//	actions.parquet    近 400 天公司行为（symbol/date/... 已展开的分红）
//	meta.json          {schema, builtAt, evalDate, counts, startDate}
//
// ⚠ **刻意保持"未落位"的扁平形状**：不在磁盘上存 map、也不存已经算好的
// 市值/PE/TTM。原因是那些派生值依赖 `valuationPriceStart`、报告期口径、
// 股本口径等一堆会因为改代码而变的东西 —— 一旦把它们固化到磁盘，
// 改口径时就得靠"记得升 schema 版本"来避免读到过期结果，太容易出错。
// 存扁平原始行则只要数据湖没变、代码怎么改都能安全重建。
//
// ## 失效
//
// 两道判据，任一不满足就重建：
//
//	schema 版本不同（代码改了取数口径）
//	数据湖的**最新交易日**比缓存的 evalDate 新（数据湖更新了）
//
// 后者是核心：`LatestTradeDate` 只读一个分区，约 4ms，比读 703 万行便宜五个数量级，
// 所以每次都能便宜地校验，不需要 TTL、也不需要 mtime 陷阱。
// ---------------------------------------------------------------------------

// valuationCacheSchema 是派生快照的格式版本。
//
// ⚠ **任何影响这份快照内容的改动都必须 +1**，否则老进程/新代码会读到
// 形状对但口径旧的数据。具体包括：改变要读的科目、改变年份裁剪边界、
// 改变复权因子的取法、改变 CSV/parquet 列定义。
const valuationCacheSchema = 1

// valuationCacheDirName 是派生快照的目录名（放在数据湖根目录下）。
//
// 不用 `meta/` 前缀：`meta/` 是数据湖自己的命名空间（cne 在管），
// 派生缓存属于本应用，混进去容易被 cne 的清理逻辑或用户的备份脚本误伤。
const valuationCacheDirName = "valuation_cache"

// ---------------------------------------------------------------------------
// 磁盘行类型
// ---------------------------------------------------------------------------

// cacheBarRow 是一根日线。
//
// 只保留评估真正用到的列：开高低收用于收益/波动/回撤，成交量与成交额用于
// 流动性与"量价背离"类的辅助判断。故意不带复权因子 ——
// 因子单独按需读，不放进这个快照（它在 meta/adj_factors_cache 里已有缓存）。
type cacheBarRow struct {
	Symbol string  `parquet:"symbol"`
	Date   int64   `parquet:"date"`
	Open   float64 `parquet:"open"`
	High   float64 `parquet:"high"`
	Low    float64 `parquet:"low"`
	Close  float64 `parquet:"close"`
	Volume int64   `parquet:"volume"`
	Amount float64 `parquet:"amount"`
}

// cacheStatementRow 是一条财报科目。
type cacheStatementRow struct {
	Symbol       string  `parquet:"symbol"`
	ReportPeriod string  `parquet:"report_period"`
	ItemCode     string  `parquet:"item_code"`
	AnnounceDate int64   `parquet:"announce_date"`
	Value        float64 `parquet:"value"`
}

// cacheShareRow 是一条股本变更。
type cacheShareRow struct {
	Symbol      string  `parquet:"symbol"`
	Date        int64   `parquet:"date"`
	TotalShares float64 `parquet:"total_shares"`
	FloatShares float64 `parquet:"float_shares"`
}

// cacheActionRow 是一条公司行为（只保留评估用得到的字段）。
type cacheActionRow struct {
	Symbol string `parquet:"symbol"`
	// Date 是**除权日 YYYYMMDD**（不是 parquet 的天数）。
	//
	// 取的是 `ActionsByDate` 返回 map 的键，那已经是 YYYYMMDD；
	// 而 `CorporateAction.ExDateRaw` 是天数，直接存它、读回来又当 YYYYMMDD 用，
	// 会让派息全部落到 1970 年附近（实测分红恒为 0）。
	Date int64 `parquet:"date"`
	// CashPerShare 是每股税前派现（已由 CashPerShare() 计算好）。
	CashPerShare float64 `parquet:"cash_per_share"`
}

// cacheMeta 是派生快照的元数据。
type cacheMeta struct {
	// Schema 是格式版本（见 valuationCacheSchema）。
	Schema int `json:"schema"`
	// BuiltAt 是构建时间（RFC3339）。
	BuiltAt string `json:"builtAt"`
	// EvalDate 是构建时数据湖的最新交易日（YYYYMMDD）。
	//
	// 这是失效判据的核心：重新读取时先问一次 LatestTradeDate（约 4ms），
	// 只要它比这个值新，就说明数据湖更新了，必须重建。
	EvalDate int64 `json:"evalDate"`
	// StartDate 是行情起点（记录了它，口径一变就会与当前常量不符 → 重建）。
	StartDate int64 `json:"startDate"`
	// Bars / Statements / Shares / Actions 是各表的行数（用于自检与诊断）。
	Bars       int64 `json:"bars"`
	Statements int64 `json:"statements"`
	Shares     int64 `json:"shares"`
	Actions    int64 `json:"actions"`
	// Indicators 是本次构建时选的行业分类体系（仅用于日志诊断）。
	IndustrySystem string `json:"industrySystem"`
	// BuildMS 是构建耗时（诊断用）。
	BuildMS int64 `json:"buildMs"`
	// Shards 是每张表的分片数。分片数不一致时快照不可读（文件名都对不上）。
	Shards int `json:"shards"`
	// Compression 是写入时用的 parquet 压缩算法。
	//
	// 必须比对：换 codec 之后老文件仍然"存在且 schema 相同"，但按新 codec
	// 解会直接报错或解出垃圾。写成字段比"每次换 codec 就 +1 schema"更好，
	// 因为它让"哪份快照是什么压缩"这件事在磁盘上可查。
	Compression string `json:"compression"`
}

// ---------------------------------------------------------------------------
// 目录与失效判定
// ---------------------------------------------------------------------------

// valuationCacheDir 返回派生快照目录。
func valuationCacheDir(lake *cnlake.Lake) string {
	root := lake.Root()
	if root == "" {
		return ""
	}
	return filepath.Join(root, valuationCacheDirName)
}

// cacheCompression 是派生快照用的 parquet 压缩算法。
//
// ## 选型依据：实测过两种，留 zstd
//
//	codec      行情分片合计   读入（含落位）
//	zstd          116 MB       5.1 s
//	snappy        167 MB       5.0 s
//
// 本来预期 snappy 明显更快（解压接近 memcpy），但实测只差 0.1 秒 ——
// 说明这 5 秒的大头**不是解压**，而是落位（700 万行建桶 + 复制成 BarRow）。
// 既然速度没差别，就选体积小 30% 的 zstd。
//
// 换 codec 必须让老快照失效（否则会拿 zstd 的文件按 snappy 解），
// 所以它写进 meta 并在 derivedCacheUsableAt 里比对。
const cacheCompression = "zstd"

// cacheWriteOptions 是写派生快照的 parquet 选项。
//
// ⚠ RowGroup 设大一些（20 万行）：读的时候是按 row group 并行解码的，
// 组太小会让"每个组一次调度"的开销盖过解码本身。
func cacheWriteOptions() []parquet.WriterOption {
	var codec compress.Codec = &parquet.Snappy
	if cacheCompression == "zstd" {
		codec = &parquet.Zstd
	}
	return []parquet.WriterOption{
		parquet.Compression(codec),
		parquet.MaxRowsPerRowGroup(200_000),
	}
}

// cacheShards 是每张表的分片数。
//
// ## 为什么必须分片
//
// `parquet.ReadFile` 是**单线程**解码。实测一份 116 MB 的行情快照
// （703 万行、zstd）解出来要 **4.6 秒**，财报（652 万行）2.8 秒 ——
// 合计 7.4 秒，几乎吃光"全市场刷新 ≤ 10 秒"的全部预算。
//
// 而这两件事是**完全独立**的：把每张表切成 N 份、并行读，
// 墙钟时间降到 1/N（受核数限制）。本机 16 逻辑核，取 8 份足够，
// 留出余量给后面的评估阶段。
//
// 分片键用**代码的稳定哈希**（不是行数均分）：这样同一只标的的全部行
// 一定落在同一个分片里，各分片可以独立解码、独立落位，不需要跨片合并。
const cacheShards = 8

// cacheShardOf 返回一个代码属于哪个分片（稳定哈希，跨进程一致）。
//
// 不用 `hash/maphash`（每次进程启动的种子不同），因为分片数变了会导致
// 老快照无法读取 —— 而我们要的是"同样的代码永远进同样的片子"。
func cacheShardOf(symbol string) int {
	// FNV-1a 的简易版：输入全是 6 位数字，这个分布已经足够均匀
	hash := uint32(2166136261)
	for i := 0; i < len(symbol); i++ {
		hash ^= uint32(symbol[i])
		hash *= 16777619
	}
	return int(hash % cacheShards)
}

// cacheShardName 拼分片文件名，例如 `bars.0.parquet`。
func cacheShardName(table string, shard int) string {
	return fmt.Sprintf("%s.%d.parquet", table, shard)
}

// derivedCacheUsable 判断数据湖自带的派生快照是否可用（取目录后转调 derivedCacheUsableAt）。
func derivedCacheUsable(lake *cnlake.Lake, latestTradeDate int64) (string, bool, cacheMeta) {
	dir := valuationCacheDir(lake)
	if dir == "" {
		return "", false, cacheMeta{}
	}
	_, usable, meta := derivedCacheUsableAt(dir, latestTradeDate)
	return dir, usable, meta
}

// derivedCacheUsableAt 判断指定目录里的派生快照是否可用。
//
// 判据（任一不满足即不可用 → 重建）：
//
//	schema 与当前代码一致
//	数据湖的最新交易日**没有比缓存构建时更新**
//	行情起点与当前常量一致
//	四个 parquet 文件都在
//
// 独立成"吃目录"的函数是为了让单测能用 t.TempDir() 验证失效判据 ——
// 失效判据错了的后果是"用户看到过期数据"，比慢更严重，必须可测。
func derivedCacheUsableAt(dir string, latestTradeDate int64) (string, bool, cacheMeta) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return dir, false, cacheMeta{}
	}
	var meta cacheMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		slog.Debug("估值派生快照：元数据损坏，将重建", "err", err)
		return dir, false, cacheMeta{}
	}
	if meta.Schema != valuationCacheSchema {
		slog.Debug("估值派生快照：schema 不符，将重建",
			"缓存", meta.Schema, "当前", valuationCacheSchema)
		return dir, false, cacheMeta{}
	}
	// 压缩算法变了 → 老文件不能按新 codec 解，必须重建
	if meta.Compression != cacheCompression {
		slog.Debug("估值派生快照：压缩算法不符，将重建",
			"缓存", meta.Compression, "当前", cacheCompression)
		return dir, false, cacheMeta{}
	}
	// 分片数变了 → 文件名对不上，必须重建（不能只看 schema：分片数是运行时参数）
	if meta.Shards != cacheShards {
		slog.Debug("估值派生快照：分片数不符，将重建",
			"缓存", meta.Shards, "当前", cacheShards)
		return dir, false, cacheMeta{}
	}
	// ⚠ 失效判据：数据湖的最新交易日比缓存构建时新 → 数据更新了。
	// 这一条是必须的：数据湖每天盘后会变，而派生快照不会自己变。
	if latestTradeDate > 0 && meta.EvalDate < latestTradeDate {
		slog.Debug("估值派生快照：数据湖已更新，将重建",
			"缓存评估日", meta.EvalDate, "数据湖最新", latestTradeDate)
		return dir, false, cacheMeta{}
	}
	if meta.StartDate != valuationPriceStart {
		slog.Debug("估值派生快照：行情起点口径变了，将重建",
			"缓存", meta.StartDate, "当前", valuationPriceStart)
		return dir, false, cacheMeta{}
	}
	for _, name := range cacheTableFiles("bars", "statements", "shares", "actions") {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			slog.Debug("估值派生快照：缺文件，将重建", "file", name)
			return dir, false, cacheMeta{}
		}
	}
	return dir, true, meta
}

// cacheTableFiles 列出各表全部分片文件名（含没有行的分片 —— 它们也要存在，
// 否则"缺文件"判据会在空分片被清理后误判为损坏）。
func cacheTableFiles(tables ...string) []string {
	out := make([]string, 0, len(tables)*cacheShards)
	for _, table := range tables {
		for shard := 0; shard < cacheShards; shard++ {
			out = append(out, cacheShardName(table, shard))
		}
	}
	return out
}

// writeDerivedCache 把一份数据集写成派生快照（分片写）。
//
// ## 写法要点
//
//  1. **先写全部分片与 meta 到临时名，最后一次改名**。写到一半被中断
//     （关窗口、断电）时留下的是 `.tmp`，下次照样判"缺文件"并重建，
//     不会出现"meta 说好了但分片是半截"的坏状态。
//  2. **按代码哈希分片**（见 cacheShards）：同一只标的的全部行进同一片，
//     各片独立可读，读的时候并行。
//  3. 大表（行情）**边分桶边写**，不先攒一个 700 万行的总切片 ——
//     那等于把内存占用翻倍，而这份数据本来就已经在内存里了。
func writeDerivedCache(dir string, data *valuationDataset, industrySystem string, buildCost time.Duration) error {
	if dir == "" {
		return fmt.Errorf("数据湖根目录未知，无法写派生快照")
	}
	started := time.Now()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// ---- 行情：分桶到 8 个分片 ----
	barShards := make([][]cacheBarRow, cacheShards)
	barCounts := make([]int, cacheShards)
	for _, rows := range data.bars {
		for _, bar := range rows {
			shard := cacheShardOf(cnlake.BareSymbol(bar.Symbol))
			barCounts[shard]++
		}
	}
	for i := range barShards {
		barShards[i] = make([]cacheBarRow, 0, barCounts[i])
	}
	for code, rows := range data.bars {
		shard := cacheShardOf(code)
		for _, bar := range rows {
			barShards[shard] = append(barShards[shard], cacheBarRow{
				Symbol: code, Date: bar.Date(),
				Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close,
				Volume: bar.Volume, Amount: bar.Amount,
			})
		}
	}
	if err := writeShards(dir, "bars", barShards); err != nil {
		return fmt.Errorf("写行情分片失败：%w", err)
	}
	barShards = nil

	// ---- 财报：分桶到 8 个分片 ----
	statementShards := make([][]cacheStatementRow, cacheShards)
	statementCounts := make([]int, cacheShards)
	for code, reports := range data.reports {
		shard := cacheShardOf(code)
		for _, report := range reports {
			statementCounts[shard] += len(report.Values)
		}
	}
	for i := range statementShards {
		statementShards[i] = make([]cacheStatementRow, 0, statementCounts[i])
	}
	for code, reports := range data.reports {
		shard := cacheShardOf(code)
		for _, report := range reports {
			for item, value := range report.Values {
				statementShards[shard] = append(statementShards[shard], cacheStatementRow{
					Symbol: code, ReportPeriod: report.Period, ItemCode: item,
					AnnounceDate: report.AnnounceDate, Value: value,
				})
			}
		}
	}
	if err := writeShards(dir, "statements", statementShards); err != nil {
		return fmt.Errorf("写财报分片失败：%w", err)
	}
	statementShards = nil

	// ---- 股本（小表，仍然分片以保持读路径统一）----
	shareShards := make([][]cacheShareRow, cacheShards)
	for i := range shareShards {
		shareShards[i] = []cacheShareRow{}
	}
	for code, timeline := range data.shares {
		shard := cacheShardOf(code)
		for _, point := range timeline {
			shareShards[shard] = append(shareShards[shard], cacheShareRow{
				Symbol: code, Date: point.Date,
				TotalShares: point.TotalShares, FloatShares: point.FloatShares,
			})
		}
	}
	if err := writeShards(dir, "shares", shareShards); err != nil {
		return fmt.Errorf("写股本分片失败：%w", err)
	}

	// ---- 公司行为（小表）----
	actionShards := make([][]cacheActionRow, cacheShards)
	for i := range actionShards {
		actionShards[i] = []cacheActionRow{}
	}
	for date, list := range data.actionDetails {
		for _, action := range list {
			shard := cacheShardOf(action.symbol)
			actionShards[shard] = append(actionShards[shard], cacheActionRow{
				Symbol: action.symbol, Date: date, CashPerShare: action.cash,
			})
		}
	}
	if err := writeShards(dir, "actions", actionShards); err != nil {
		return fmt.Errorf("写公司行为分片失败：%w", err)
	}

	meta := cacheMeta{
		Schema: valuationCacheSchema, BuiltAt: time.Now().Format(time.RFC3339),
		EvalDate: data.evalDate, StartDate: valuationPriceStart,
		IndustrySystem: industrySystem,
		BuildMS:        buildCost.Milliseconds(),
		Shards:         cacheShards,
		Compression:    cacheCompression,
	}
	meta.Bars = sumCacheRows(dir, "bars")
	meta.Statements = sumCacheRows(dir, "statements")
	meta.Shares = sumCacheRows(dir, "shares")
	meta.Actions = sumCacheRows(dir, "actions")

	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "meta.json"), raw); err != nil {
		return err
	}
	slog.Debug("估值派生快照：已写入",
		"目录", dir, "行情", meta.Bars, "财报", meta.Statements,
		"写盘耗时", time.Since(started).Milliseconds())
	return nil
}

// writeShards 把一批分片写成文件（并行写，各片互不相干）。
func writeShards[T any](dir, table string, shards [][]T) error {
	var wg sync.WaitGroup
	errs := make([]error, len(shards))
	for i := range shards {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			rows := shards[shard]
			if rows == nil {
				rows = []T{}
			}
			errs[shard] = writeParquetAtomic(filepath.Join(dir, cacheShardName(table, shard)), rows)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// sumCacheRows 汇总一张表所有分片的行数。
func sumCacheRows(dir, table string) int64 {
	total := int64(0)
	for shard := 0; shard < cacheShards; shard++ {
		total += countCacheRows(filepath.Join(dir, cacheShardName(table, shard)))
	}
	return total
}

// countCacheRows 数一个 parquet 的行数。
//
// 用它而不是在内存里数：`writeDerivedCache` 里那些切片写完之后就置 nil 了
// （700 万行的驻留没必要为了写日志留着），所以行数从文件读回来。
//
// 走 `parquet.OpenFile` + `Metadata().NumRows`：只解析 footer，
// **不碰数据页**，所以对 700 万行的文件也是毫秒级。
func countCacheRows(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	opened, err := parquet.OpenFile(file, info.Size())
	if err != nil {
		return 0
	}
	return opened.Metadata().NumRows
}

// writeParquetAtomic 先写临时文件再改名。
func writeParquetAtomic[T any](path string, rows []T) error {
	tmp := path + ".tmp"
	if err := parquet.WriteFile(tmp, rows, cacheWriteOptions()...); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeFileAtomic 先写临时文件再改名。
func writeFileAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------------------
// 读派生快照
// ---------------------------------------------------------------------------

// readDerivedCache 从派生快照装载数据集（**并行读分片**，不做任何磁盘扫描）。
//
// 并行读是这里的关键：单线程解码 703 万行行情要 4.6 秒、652 万行财报要 2.8 秒，
// 合计 7.4 秒就吃光了"全市场刷新 ≤ 10 秒"的预算。
// 分片之后 8 份并行解码，墙钟时间降到 1/N。
//
// 参照数据（名称 / 资产类型 / 行业归属）不进快照，由 fillReferenceData 现读
// —— 它们每天都可能变，而且加起来只要几百毫秒。
func readDerivedCache(lake *cnlake.Lake, dir string, meta cacheMeta) (*valuationDataset, error) {
	started := time.Now()

	bars, err := readShards[cacheBarRow](dir, "bars")
	if err != nil {
		return nil, fmt.Errorf("读行情分片失败：%w", err)
	}
	statements, err := readShards[cacheStatementRow](dir, "statements")
	if err != nil {
		return nil, fmt.Errorf("读财报分片失败：%w", err)
	}
	shares, err := readShards[cacheShareRow](dir, "shares")
	if err != nil {
		return nil, fmt.Errorf("读股本分片失败：%w", err)
	}
	actions, err := readShards[cacheActionRow](dir, "actions")
	if err != nil {
		return nil, fmt.Errorf("读公司行为分片失败：%w", err)
	}
	slog.Debug("估值派生快照：分片已读入",
		"行情", len(bars), "财报", len(statements), "股本", len(shares), "公司行为", len(actions),
		"耗时", time.Since(started).Milliseconds())

	data := newValuationDataset(lake)
	data.evalDate = meta.EvalDate
	data.latestBarDate = meta.EvalDate
	data.industrySystem = meta.IndustrySystem

	// ---- 行情：按代码分桶（两遍走，先数再分配，省掉反复扩容）----
	counts := make(map[string]int32, 8192)
	for i := range bars {
		counts[bars[i].Symbol]++
	}
	data.bars = make(map[string][]cnlake.BarRow, len(counts))
	for code, n := range counts {
		data.bars[code] = make([]cnlake.BarRow, 0, n)
	}
	for i := range bars {
		bar := &bars[i]
		data.bars[bar.Symbol] = append(data.bars[bar.Symbol], cnlake.BarRow{
			// ⚠ TradeDate 是"自 1970-01-01 起的天数"，不是 YYYYMMDD。
			// 直接塞 YYYYMMDD 会让 BarRow.Date() 解出荒谬年月日（实测 574421010）。
			Symbol: bar.Symbol, TradeDate: cnlake.DaysFromDate(bar.Date),
			Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close,
			Volume: bar.Volume, Amount: bar.Amount,
		})
	}
	bars = nil

	// ---- 财报：合回 Report ----
	//
	// 写快照时已经按 (代码, 报告期) 整期保留过修订（只留 announce_date 最大的那批），
	// 所以这里直接累加即可，不需要再判修订。
	values := make(map[string]map[string]float64, len(statements)/8)
	announce := make(map[string]int64, len(statements)/8)
	for i := range statements {
		row := &statements[i]
		key := row.Symbol + "\x00" + row.ReportPeriod
		items := values[key]
		if items == nil {
			items = make(map[string]float64, 8)
			values[key] = items
			announce[key] = row.AnnounceDate
		}
		items[row.ItemCode] = row.Value
	}
	statements = nil
	for key, items := range values {
		sep := strings.IndexByte(key, 0)
		code, period := key[:sep], key[sep+1:]
		data.reports[code] = append(data.reports[code], valuation.Report{
			Period: period, AnnounceDate: announce[key], Values: items,
		})
	}
	values, announce = nil, nil

	// ---- 股本 ----
	grouped := make(map[string][]cnlake.SharePoint, len(shares)/8)
	for i := range shares {
		row := &shares[i]
		grouped[row.Symbol] = append(grouped[row.Symbol], cnlake.SharePoint{
			Date: row.Date, TotalShares: row.TotalShares, FloatShares: row.FloatShares,
		})
	}
	for code, rows := range grouped {
		data.shares[code] = cnlake.NewShareTimeline(rows)
	}

	// ---- 公司行为（分红）----
	for i := range actions {
		action := &actions[i]
		data.actionDetails[action.Date] = append(data.actionDetails[action.Date], dividendAction{
			symbol: action.Symbol, cash: action.CashPerShare,
		})
		if action.CashPerShare > 0 {
			data.dividendPaid[action.Symbol] += action.CashPerShare
		}
	}

	if err := fillReferenceData(lake, data); err != nil {
		return nil, err
	}
	slog.Debug("估值评估：派生快照装载完成", "总耗时", time.Since(started).Milliseconds())
	return data, nil
}

// readShards 并行读一张表的所有分片并拼成一个切片。
//
// 拼接顺序不影响正确性：下游一律按代码分桶，不依赖行序。
func readShards[T any](dir, table string) ([]T, error) {
	type result struct {
		rows []T
		err  error
	}
	results := make([]result, cacheShards)
	var wg sync.WaitGroup
	for shard := 0; shard < cacheShards; shard++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			path := filepath.Join(dir, cacheShardName(table, shard))
			rows, err := parquet.ReadFile[T](path)
			if err != nil {
				// 空分片可能被实现成"文件不存在"，这不算错误
				if os.IsNotExist(err) {
					return
				}
				results[shard] = result{err: err}
				return
			}
			results[shard] = result{rows: rows}
		}(shard)
	}
	wg.Wait()

	total := 0
	for _, item := range results {
		if item.err != nil {
			return nil, item.err
		}
		total += len(item.rows)
	}
	out := make([]T, 0, total)
	for _, item := range results {
		out = append(out, item.rows...)
	}
	return out, nil
}
