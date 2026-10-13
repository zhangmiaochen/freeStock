// 「按股票」派生缓存：把 daily_bars 从**按交易日分区**重排成**一个文件一只股票**。
//
// 为什么需要它（本机实测，数据湖 2614 个日分区、合计约 20MB）：
//
//	单只股票全历史直读（逐个分区按代码过滤）  3.97s   ← 为了 1396 行要打开 2614 个文件
//	冷启动第一次打开 K 线                    11.9s   ← 走的是"全市场灌内存"那条路，堆峰值 1.5GB
//	命中内存批量缓存后                        12ms   ← 但重启就没了
//	命中本缓存                                2ms   ← 落盘、重启不退化、常驻内存为 0
//
// 关键认识：这份数据**只有 20MB**，慢的不是数据量，是"每个分区一个文件"带来的
// 固定开销（open + footer 解析 + 全量解码再过滤）。既然总数据这么小，
// 花一次十几秒把它重排成按股票存放，之后每次单只查询就只是打开一个小文件。

package cnlake

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
)

// SymbolBarsVersion 是缓存布局版本：布局改了就整体失效、下次重建。
const SymbolBarsVersion = 1

// symbolBarsBuckets 是构建时的分桶数。
//
// 构建要按代码重排 700 多万行。如果全部读进内存再分组，峰值是 1GB 级；
// 分桶之后内存里同时只有一个桶的行（本机约 20MB），代价是多一轮
// 20MB 级的临时文件读写。
const symbolBarsBuckets = 32

// SymbolBarsCache 是 daily_bars 的「一个文件 = 一只股票」派生副本。
//
// 目录布局：
//
//	<root>/manifest.json          构建水位（版本 / 数据湖根 / 最后交易日 / 行数）
//	<root>/bars/600519.parquet    一只股票的全部日线（按日期升序）
//
// 约定：
//   - **只读**：界面上的一切查询都只读它，写入只发生在 Build；
//   - **原子替换**：Build 先写 `<root>.building-<pid>`，成功后整体换名，
//     所以读者任何时候看到的都是"完整且与 manifest 一致"的一份；
//   - **可随时删除**：删掉之后会退化成直读（慢但结果一样），下次再重建。
type SymbolBarsCache struct {
	root string

	// codes 是"缓存里有哪些代码"的进程内索引（按 manifest.BuiltAt 失效）。
	// 股票列表的补抓一次要问两千多个代码"你有没有历史 K 线"，
	// 逐个 os.Stat 在 Windows 上是秒级，读一次目录只要几十毫秒。
	codesMu      sync.Mutex
	codes        map[string]struct{}
	codesBuiltAt string
}

// buildGate 保证**同一个缓存目录**在一个进程里只会有一个重建在跑。
//
// 为什么需要：一个进程里可能同时有多个行情源实例（切换数据源、测试里每个
// 用例一个服务实例），它们都会发现"缓存过期"。没有这道闸，几个几十秒的重建
// 会同时开跑，互相抢磁盘，还会一起写同一个 staging 目录。
var buildGate = struct {
	sync.Mutex
	inflight map[string]struct{}
}{inflight: map[string]struct{}{}}

// AcquireBuild 尝试占用"重建中"标记；返回 false 表示已有重建在跑，直接放弃。
func (c *SymbolBarsCache) AcquireBuild() bool {
	if c == nil {
		return false
	}
	buildGate.Lock()
	defer buildGate.Unlock()
	if _, busy := buildGate.inflight[c.root]; busy {
		return false
	}
	buildGate.inflight[c.root] = struct{}{}
	return true
}

// ReleaseBuild 释放"重建中"标记。
func (c *SymbolBarsCache) ReleaseBuild() {
	if c == nil {
		return
	}
	buildGate.Lock()
	delete(buildGate.inflight, c.root)
	buildGate.Unlock()
}

// NewSymbolBarsCache 在 root 下建立缓存句柄；root 为空时返回 nil（表示不用缓存）。
func NewSymbolBarsCache(root string) *SymbolBarsCache {
	if root == "" {
		return nil
	}
	return &SymbolBarsCache{root: root}
}

// Root 返回缓存目录（未启用时为空）。
func (c *SymbolBarsCache) Root() string {
	if c == nil {
		return ""
	}
	return c.root
}

// ManifestPath 是水位文件路径。
func (c *SymbolBarsCache) ManifestPath() string {
	return filepath.Join(c.root, "manifest.json")
}

// BarsDir 是"按股票"文件的目录。
func (c *SymbolBarsCache) BarsDir() string {
	return filepath.Join(c.root, "bars")
}

// SymbolBarsManifest 记录这份缓存是在什么水位上建的。
//
// 「永远是最新的」靠三件事一起成立：
//  1. `LatestDate` 是**缓存里实际写到最后一根 K 线的日期**（不是分区名日期），
//     所以不变式"缓存只包含 <= LatestDate 的行"由构建过程保证；
//  2. 读取时 `LatestDate` 之后的部分**一律从日分区现读**（见 service 层），
//     新增交易日、盘中追加的数据不需要等重建；
//  3. `Signature` 是构建时那段"冻结历史"的源文件指纹，读之前必须重新算一遍 ——
//     补跑 / 重算 / 重写任何一天都会让它不一致，于是缓存立刻不再被信任。
type SymbolBarsManifest struct {
	Version  int    `json:"version"`
	LakeRoot string `json:"lakeRoot"`
	// LatestDate 是缓存里最后一根 K 线的日期（YYYYMMDD）。
	LatestDate int64 `json:"latestDate"`
	// SourceThrough 是构建时数据湖最后一个**分区**日期：指纹覆盖的文件集合
	// 由它界定（分区日期 <= SourceThrough 的文件）。
	SourceThrough int64 `json:"sourceThrough"`
	// Signature 是上述文件集合的 (路径, 大小, mtime) 指纹，构建开始前算好。
	Signature string `json:"signature"`
	// SourceFiles 是构建时读了多少个源分区文件（用于判断"数据湖是不是换了"）。
	SourceFiles int     `json:"sourceFiles"`
	Symbols     int     `json:"symbols"`
	Rows        int64   `json:"rows"`
	BuiltAt     string  `json:"builtAt"`
	Seconds     float64 `json:"seconds"`
}

// Manifest 读水位；第二个返回值表示"这份缓存是完整可用的"。
func (c *SymbolBarsCache) Manifest() (SymbolBarsManifest, bool) {
	var out SymbolBarsManifest
	if c == nil {
		return out, false
	}
	raw, err := os.ReadFile(c.ManifestPath())
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false
	}
	if out.Version != SymbolBarsVersion || out.LatestDate <= 0 {
		return out, false
	}
	return out, true
}

// CoversThrough 表示这份缓存已经覆盖到"分区水位 latestPartition"。
//
// ⚠ 这只是**性能判断**，不是正确性判断：正确性还要看 `Signature` 是否仍然
// 等于源头（见 service 层的 symbolCacheState）。缓存"没覆盖到最新"时结果
// 照样是对的（读取方会把 LatestDate 之后的部分从日分区现读）。
func (c *SymbolBarsCache) CoversThrough(lakeRoot string, latestPartition int64) bool {
	manifest, ok := c.Manifest()
	if !ok || latestPartition <= 0 {
		return false
	}
	return manifest.LakeRoot == lakeRoot && manifest.SourceThrough >= latestPartition
}

// Has 判断这只股票在缓存里有没有数据。
//
// 语义很关键：**没有文件 = 它在整段历史里一根 K 线都没有**（构建时扫的是
// 全部分区）。股票列表的"不限起点补抓"正是靠这一点省掉整表扫描。
func (c *SymbolBarsCache) Has(code string) bool {
	if c == nil {
		return false
	}
	bare := BareSymbol(code)
	if bare == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(c.BarsDir(), bare+".parquet"))
	return err == nil && !info.IsDir()
}

// Codes 返回缓存里**有日线**的全部代码（进程内缓存，按构建水位失效）。
//
// 一次要问两千多个代码时用它，别逐个 Has（逐个 stat 在 Windows 上是秒级）。
// 返回 nil 表示"问不出来"（没建过缓存、目录读不了）—— 调用方必须回退，
// 不能把 nil 当成"一个都没有"。
func (c *SymbolBarsCache) Codes() map[string]struct{} {
	if c == nil {
		return nil
	}
	manifest, ok := c.Manifest()
	if !ok {
		return nil
	}

	c.codesMu.Lock()
	defer c.codesMu.Unlock()
	if c.codes != nil && c.codesBuiltAt == manifest.BuiltAt {
		return c.codes
	}

	entries, err := os.ReadDir(c.BarsDir())
	if err != nil {
		return nil
	}
	codes := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".parquet") {
			continue
		}
		codes[strings.TrimSuffix(name, ".parquet")] = struct{}{}
	}
	c.codes, c.codesBuiltAt = codes, manifest.BuiltAt
	return codes
}

// Read 读出一只股票的**全部**日线（升序）。
//
// 返回 ok=false 表示缓存里没有这只股票 —— 与 Has 的语义一致
// （"确认没有"而不是"读失败"）；读失败会返回 error。
func (c *SymbolBarsCache) Read(code string) ([]BarRow, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	bare := BareSymbol(code)
	if bare == "" {
		return nil, false, nil
	}
	path := filepath.Join(c.BarsDir(), bare+".parquet")
	if _, err := os.Stat(path); err != nil {
		return nil, false, nil
	}
	rows, err := readTyped[BarRow]([]string{path}, nil)
	if err != nil {
		return nil, false, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TradeDate < rows[j].TradeDate })
	return rows, true, nil
}

// Remove 删掉整份缓存（下次查询会退化成直读，之后可重建）。
func (c *SymbolBarsCache) Remove() error {
	if c == nil {
		return nil
	}
	return os.RemoveAll(c.root)
}

// Build 全量重建缓存并原子替换。
//
// 构建过程对读者是透明的：新内容写在 `<root>.building-<pid>`，最后整体换名。
// 内存占用与数据量无关（分桶 + 逐桶排序），本机约 20MB。
func (c *SymbolBarsCache) Build(l *Lake) (SymbolBarsManifest, error) {
	var manifest SymbolBarsManifest
	if c == nil {
		return manifest, fmt.Errorf("cnlake: 未启用按股票缓存")
	}
	if l == nil || !l.Available() {
		return manifest, fmt.Errorf("cnlake: 数据湖不可用")
	}

	began := time.Now()
	files := l.DatasetFiles("daily_bars")
	if len(files) == 0 {
		return manifest, fmt.Errorf("cnlake: daily_bars 还没有数据，无法建立按股票缓存")
	}
	manifest.Version = SymbolBarsVersion
	manifest.LakeRoot = l.Root()
	manifest.SourceFiles = len(files)
	manifest.SourceThrough = l.LatestPartitionDate()

	// ⚠ 指纹必须在**读数据之前**算：如果算在读完以后，构建期间被改写的文件
	// 会被记成"新指纹"，而缓存里存的还是旧内容 —— 那就把"不一致"记成了"一致"。
	signature, err := l.SourceSignature(manifest.SourceThrough)
	if err != nil {
		return manifest, err
	}
	manifest.Signature = signature

	if err := os.MkdirAll(filepath.Dir(c.root), 0o755); err != nil {
		return manifest, err
	}
	// staging 与 old 都带 pid：**两个进程同时重建**（开了两个 freeStock）
	// 时各写各的，不会互相踩。极端情况下某个读者会短暂读不到缓存，
	// 那只是退回直读，结果不受影响。
	suffix := fmt.Sprintf(".building-%d", os.Getpid())
	staging := c.root + suffix
	if err := os.RemoveAll(staging); err != nil {
		return manifest, err
	}
	bucketDir := filepath.Join(staging, "buckets")
	if err := os.MkdirAll(bucketDir, 0o755); err != nil {
		return manifest, err
	}

	// ① 扫一遍全部分区，按代码哈希分桶写临时文件（内存只留正在写的那点行）
	if err := c.bucketize(files, bucketDir); err != nil {
		os.RemoveAll(staging)
		return manifest, err
	}

	// ② 每个桶内部按（代码，日期）排序，再按代码切出最终文件
	barsDir := filepath.Join(staging, "bars")
	if err := os.MkdirAll(barsDir, 0o755); err != nil {
		os.RemoveAll(staging)
		return manifest, err
	}
	symbols, rows, latest, err := splitBuckets(bucketDir, barsDir)
	if err != nil {
		os.RemoveAll(staging)
		return manifest, err
	}
	os.RemoveAll(bucketDir)
	manifest.Symbols, manifest.Rows, manifest.LatestDate = symbols, rows, latest

	// ③ 写水位 + 原子换名（换名成功后旧的才删）
	manifest.BuiltAt = time.Now().Format(time.RFC3339)
	manifest.Seconds = time.Since(began).Seconds()
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		os.RemoveAll(staging)
		return manifest, err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), encoded, 0o644); err != nil {
		os.RemoveAll(staging)
		return manifest, err
	}
	if err := swapDir(c.root, staging); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// bucketize 顺序读完全部分区，按代码哈希把行写进 symbolBarsBuckets 个临时文件。
func (c *SymbolBarsCache) bucketize(files []string, dir string) error {
	writers := make([]*parquet.GenericWriter[BarRow], symbolBarsBuckets)
	handles := make([]*os.File, symbolBarsBuckets)
	buffers := make([][]BarRow, symbolBarsBuckets)
	closeAll := func() {
		for _, handle := range handles {
			if handle != nil {
				handle.Close()
			}
		}
	}
	defer closeAll()

	for i := range writers {
		handle, err := os.Create(filepath.Join(dir, fmt.Sprintf("bucket-%02d.parquet", i)))
		if err != nil {
			return err
		}
		handles[i] = handle
		writers[i] = parquet.NewGenericWriter[BarRow](handle)
	}

	// 批量写而不是一行一次 Write：后者会为每一行分配一个临时切片。
	const batch = 1024
	flush := func(index int) error {
		if len(buffers[index]) == 0 {
			return nil
		}
		if _, err := writers[index].Write(buffers[index]); err != nil {
			return err
		}
		buffers[index] = buffers[index][:0]
		return nil
	}

	for _, path := range files {
		handle, err := openParquet(path)
		if err != nil {
			return err
		}
		var rows []BarRow
		err = readTypedFile(handle, nil, &rows)
		handle.Close()
		if err != nil {
			return err
		}
		for i := range rows {
			index := symbolBucket(rows[i].Symbol)
			buffers[index] = append(buffers[index], rows[i])
			if len(buffers[index]) >= batch {
				if err := flush(index); err != nil {
					return err
				}
			}
		}
	}

	for i := range writers {
		if err := flush(i); err != nil {
			return err
		}
		if err := writers[i].Close(); err != nil {
			return err
		}
	}
	return nil
}

// splitBuckets 把每个桶读回来排好序，再按代码写成最终文件。
//
// 返回（股票数，行数，最后一根 K 线的日期）。
//
// 最后一个值是**缓存水位**：它取的是真正写进去的最大 `trade_date`，
// 而不是目录名上的分区日期 —— 这样"缓存只包含 <= 水位 的行"这条不变式
// 由构建过程本身保证，读取时按水位切分就不会漏也不会重。
func splitBuckets(bucketDir, barsDir string) (int, int64, int64, error) {
	files, err := filepath.Glob(filepath.Join(bucketDir, "*.parquet"))
	if err != nil {
		return 0, 0, 0, err
	}
	symbols, rows, latest := 0, int64(0), int64(0)
	for _, path := range files {
		bucket, err := readTyped[BarRow]([]string{path}, nil)
		if err != nil {
			return symbols, rows, latest, err
		}
		sort.Slice(bucket, func(i, j int) bool {
			left, right := BareSymbol(bucket[i].Symbol), BareSymbol(bucket[j].Symbol)
			if left != right {
				return left < right
			}
			return bucket[i].TradeDate < bucket[j].TradeDate
		})

		for start := 0; start < len(bucket); {
			code := BareSymbol(bucket[start].Symbol)
			end := start
			for end < len(bucket) && BareSymbol(bucket[end].Symbol) == code {
				end++
			}
			// 代码为空的行走不进缓存：它没有归属，读了也没人要
			if code != "" {
				if err := parquet.WriteFile(filepath.Join(barsDir, code+".parquet"), bucket[start:end]); err != nil {
					return symbols, rows, latest, err
				}
				symbols++
				rows += int64(end - start)
				if date := bucket[end-1].Date(); date > latest {
					latest = date
				}
			}
			start = end
		}
	}
	return symbols, rows, latest, nil
}

// symbolBucket 把代码映射到桶号；代码为空时归到 0 号桶（最终会被丢掉）。
func symbolBucket(symbol string) int {
	bare := BareSymbol(symbol)
	if bare == "" {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(bare))
	return int(hash.Sum32() % symbolBarsBuckets)
}

// swapDir 用 staging 原子替换 target。
//
// Windows 上只要还有别的进程握着目录里的文件句柄，Rename 就会失败 ——
// 而读者随时可能正在读某只股票的小文件。读一次只有几毫秒，所以短暂重试即可。
func swapDir(target, staging string) error {
	old := fmt.Sprintf("%s.old-%d", target, os.Getpid())
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		if err := renameWithRetry(target, old); err != nil {
			return fmt.Errorf("旧缓存挪不动（可能被占用）：%w", err)
		}
	}
	if err := renameWithRetry(staging, target); err != nil {
		// 换名失败必须把旧的放回去，否则缓存就凭空消失了
		_ = renameWithRetry(old, target)
		return err
	}
	// 旧目录的清理是**尽力而为**：Windows 上读者可能还握着里面的文件句柄，
	// 删不掉并不影响新缓存已经生效 —— 残留目录会在下次重建开头被清掉。
	_ = os.RemoveAll(old)
	return nil
}

func renameWithRetry(from, to string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return err
}
