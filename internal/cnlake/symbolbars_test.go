package cnlake_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"freestock/internal/cnlake"
)

// buildSymbolCache 用合成数据湖建一份"按股票"缓存。
func buildSymbolCache(t *testing.T) (*cnlake.Lake, *cnlake.SymbolBarsCache) {
	t.Helper()
	lake := cnlake.Open(buildSyntheticLake(t))
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(lake); err != nil {
		t.Fatalf("Build 失败：%v", err)
	}
	return lake, cache
}

// 缓存必须与"直读分区按代码过滤"**逐行一致** —— 它是加速层，不是另一份数据。
func TestSymbolBarsCacheMatchesDirectRead(t *testing.T) {
	lake, cache := buildSymbolCache(t)

	codes := []string{"600519", "000001"}
	for _, code := range codes {
		direct, err := lake.DailyBarsInRange([]string{code}, 19900101, 20991231)
		if err != nil {
			t.Fatalf("直读 %s 失败：%v", code, err)
		}
		cached, ok, err := cache.Read(code)
		if err != nil {
			t.Fatalf("读缓存 %s 失败：%v", code, err)
		}
		if !ok {
			t.Fatalf("缓存里应当有 %s（直读拿到 %d 行）", code, len(direct))
		}
		if len(cached) != len(direct) {
			t.Fatalf("%s 行数不一致：缓存 %d vs 直读 %d", code, len(cached), len(direct))
		}
		for i := range direct {
			if cached[i] != direct[i] {
				t.Fatalf("%s 第 %d 行不一致：\n缓存 %+v\n直读 %+v", code, i, cached[i], direct[i])
			}
		}
		// 必须按日期升序（上层靠它推导前收盘）
		for i := 1; i < len(cached); i++ {
			if cached[i].TradeDate < cached[i-1].TradeDate {
				t.Fatalf("%s 未按日期升序：第 %d 行 %d < 前一行 %d", code, i, cached[i].TradeDate, cached[i-1].TradeDate)
			}
		}
	}
}

// 没有任何 K 线的标的：Has/Read 都必须给出"确认没有"，而不是报错。
func TestSymbolBarsCacheMissingCode(t *testing.T) {
	_, cache := buildSymbolCache(t)

	// 合成湖里 300750（宁德时代）只有标的、没有行情
	if cache.Has("300750") {
		t.Error("300750 没有 K 线，不该在缓存里")
	}
	rows, ok, err := cache.Read("300750")
	if err != nil {
		t.Fatalf("确认没有不该报错：%v", err)
	}
	if ok || len(rows) != 0 {
		t.Errorf("300750 应当返回 ok=false，实际 ok=%v rows=%d", ok, len(rows))
	}
	// 带后缀与带空格都要能归一化
	if !cache.Has("600519.SH") {
		t.Error("带后缀的代码应当命中 600519")
	}
}

// 水位：构建后应当正好等于数据湖最后一个交易日；换个湖根目录就不算新鲜。
func TestSymbolBarsCacheManifestAndFreshness(t *testing.T) {
	lake, cache := buildSymbolCache(t)

	manifest, ok := cache.Manifest()
	if !ok {
		t.Fatal("构建后 manifest 应当可用")
	}
	if manifest.Version != cnlake.SymbolBarsVersion {
		t.Errorf("版本号错误：%d", manifest.Version)
	}
	// 合成湖里只有 000001 / 600519 有日线
	if manifest.Symbols != 2 {
		t.Errorf("应当有 2 只股票（600519/000001），实际 %d", manifest.Symbols)
	}
	if manifest.Rows != 120 {
		t.Errorf("应当有 120 行（2 只 × 3 年 × 20 天），实际 %d", manifest.Rows)
	}
	if manifest.LatestDate != 20260320 {
		t.Errorf("水位应当是缓存里最后一根 K 线的日期 20260320，实际 %d", manifest.LatestDate)
	}
	if manifest.SourceThrough < manifest.LatestDate {
		t.Errorf("分区水位不该早于 K 线水位：%d vs %d", manifest.SourceThrough, manifest.LatestDate)
	}
	if manifest.Signature == "" {
		t.Error("清单里必须有源分区指纹")
	}
	if !cache.CoversThrough(lake.Root(), manifest.SourceThrough) {
		t.Error("刚构建完应当算覆盖到该分区水位")
	}
	if cache.CoversThrough(lake.Root(), manifest.SourceThrough+1) {
		t.Error("数据湖多了一个分区后就不该算覆盖（要走补尾 + 重建）")
	}
	if cache.CoversThrough("D:\\别的数据湖", manifest.SourceThrough) {
		t.Error("换了数据湖根目录不该算覆盖")
	}
}

// 源分区指纹必须对"落盘变化"敏感：补跑 / 重算 / 重写任何一天都要被发现，
// 而什么都没动时必须稳定（否则缓存会被无谓地判成失效、一直在重建）。
func TestSourceSignatureTracksRewrites(t *testing.T) {
	lake := cnlake.Open(buildSyntheticLake(t))
	first, err := lake.SourceSignature(0)
	if err != nil {
		t.Fatalf("算指纹失败：%v", err)
	}
	again, err := lake.SourceSignature(0)
	if err != nil {
		t.Fatalf("再算一次指纹失败：%v", err)
	}
	if first != again {
		t.Fatal("什么都没动，指纹不该变")
	}

	// 分区名解析：真实湖是 `trade_date=2026-10-12`（能解析出日），
	// 解析不出日期的分区（例如合成湖按年分区）一律计入指纹。
	dir := lake.DatasetDir("daily_bars")
	if got := lake.DailyPartitionDate(filepath.Join(dir, "trade_date=2026-10-12", "part-merged.parquet")); got != 20261012 {
		t.Errorf("按日分区应当解析出日期，实际 %d", got)
	}
	if got := lake.DailyPartitionDate(filepath.Join(dir, "trade_date=2025", "part-merged.parquet")); got != 0 {
		t.Errorf("按年分区解析不出日，应当返回 0（表示始终计入），实际 %d", got)
	}

	// 模拟 cne 重算 2025 年：把某一根的收盘价改掉，写回**同一个分区文件**
	path := filepath.Join(dir, "trade_date=2025", "part-merged.parquet")
	rows, err := parquet.ReadFile[cnlake.BarRow](path)
	if err != nil || len(rows) == 0 {
		t.Fatalf("读不到 2025 年分区：%v rows=%d", err, len(rows))
	}
	rows[0].Close += 1
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatalf("重写分区失败：%v", err)
	}

	changed, err := lake.SourceSignature(0)
	if err != nil {
		t.Fatalf("重写后算指纹失败：%v", err)
	}
	if changed == first {
		t.Error("分区被重写后指纹必须变 —— 否则缓存会把过期数据当成最新的")
	}
}

// 重建必须幂等：第二次构建结果一样，且不会把旧数据弄丢。
func TestSymbolBarsCacheRebuildIsIdempotent(t *testing.T) {
	lake, cache := buildSymbolCache(t)

	before, ok, err := cache.Read("600519")
	if err != nil || !ok {
		t.Fatalf("首次读取失败：%v ok=%v", err, ok)
	}
	if _, err := cache.Build(lake); err != nil {
		t.Fatalf("二次 Build 失败：%v", err)
	}
	after, ok, err := cache.Read("600519")
	if err != nil || !ok {
		t.Fatalf("二次读取失败：%v ok=%v", err, ok)
	}
	if len(before) != len(after) {
		t.Fatalf("重建后行数变了：%d -> %d", len(before), len(after))
	}
	// 换名之后旧目录必须清掉（否则每重建一次就多留一份）
	entries, err := os.ReadDir(filepath.Dir(cache.Root()))
	if err != nil {
		t.Fatalf("读缓存父目录失败：%v", err)
	}
	prefix := filepath.Base(cache.Root()) + ".old"
	building := filepath.Base(cache.Root()) + ".building"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("重建后残留了旧目录：%s", entry.Name())
		}
		if strings.HasPrefix(entry.Name(), building) {
			t.Errorf("重建后残留了 staging 目录：%s", entry.Name())
		}
	}

	// 删掉缓存后必须回到"确认没有"（调用方会退化成直读）
	if err := cache.Remove(); err != nil {
		t.Fatalf("Remove 失败：%v", err)
	}
	if rows, ok, _ := cache.Read("600519"); ok || len(rows) != 0 {
		t.Error("删除后不该再读到数据")
	}
}

// 空缓存（还没建）不能报错，只能"不可用" —— 界面第一次打开 K 线就走这条路。
func TestSymbolBarsCacheNotBuiltYet(t *testing.T) {
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "nothing-here"))
	if _, ok := cache.Manifest(); ok {
		t.Error("没建过缓存不该有 manifest")
	}
	if cache.CoversThrough("anywhere", 20260320) {
		t.Error("没建过缓存不该算覆盖")
	}
	if cache.Has("600519") {
		t.Error("没建过缓存不该命中")
	}
	if cnlake.NewSymbolBarsCache("") != nil {
		t.Error("根目录为空应当返回 nil（表示禁用缓存）")
	}
}

// 同一个缓存目录在一个进程里只能有一个重建在跑 —— 否则切换数据源
// 或测试里多个服务实例会同时开跑几十秒的重建，互相抢磁盘。
func TestSymbolBarsBuildGate(t *testing.T) {
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "gated"))

	if !cache.AcquireBuild() {
		t.Fatal("第一次应当拿得到重建权")
	}
	if cache.AcquireBuild() {
		t.Fatal("重建进行中不该再拿得到")
	}
	cache.ReleaseBuild()
	if !cache.AcquireBuild() {
		t.Fatal("释放之后应当能再拿到")
	}
	cache.ReleaseBuild()

	// 不同目录互不影响
	other := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "gated-other"))
	if !cache.AcquireBuild() {
		t.Fatal("拿不到重建权")
	}
	if !other.AcquireBuild() {
		t.Error("不同缓存目录不该互相阻塞")
	}
	other.ReleaseBuild()
	cache.ReleaseBuild()
}

// Codes 是"哪些代码有历史 K 线"的批量索引（股票列表补抓靠它）。
//
// 关键语义：**"问不出来"必须返回 nil**，不能返回空集合 ——
// 调用方要据此回退到全表扫描，把 nil 当成"一个都没有"会给出错的行情。
func TestSymbolBarsCacheCodes(t *testing.T) {
	_, cache := buildSymbolCache(t)

	codes := cache.Codes()
	if len(codes) != 2 {
		t.Fatalf("合成湖只有 2 只有日线，实际拿到 %d 个：%v", len(codes), codes)
	}
	for _, want := range []string{"600519", "000001"} {
		if _, ok := codes[want]; !ok {
			t.Errorf("索引里缺少 %s", want)
		}
	}
	if _, ok := codes["300750"]; ok {
		t.Error("300750 没有日线，不该出现在索引里")
	}

	// 没建过缓存 → nil（回退信号），而不是空集合
	empty := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "nothing-here"))
	if empty.Codes() != nil {
		t.Error("没建过缓存时应当返回 nil，而不是空集合")
	}
	if cnlake.NewSymbolBarsCache("").Codes() != nil {
		t.Error("禁用缓存时应当返回 nil")
	}
}
