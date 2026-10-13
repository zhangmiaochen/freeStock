package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"freestock/internal/cnlake"
	"freestock/internal/stockdb"
)

// 「按股票」缓存是加速层，**不是另一份数据**：
// 命中缓存与直读必须逐字段一致，否则 K 线会随"缓存建没建好"而变。
func TestKlineHistoryMatchesDirectRead(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)
	ctx := context.Background()

	// ① 还没挂缓存：走退化路径（只读这一只股票的区间读）
	if rows, _, _ := provider.symbolBarsFor("600519", 20261231); len(rows) != 0 {
		t.Fatal("还没挂缓存，不该读到东西")
	}
	withoutCache, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("无缓存时 KlineHistory 失败：%v", err)
	}
	if len(withoutCache) == 0 {
		t.Fatal("合成湖里 600519 应当有日线")
	}

	// ② 建好缓存后逐字段一致
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(provider.lake); err != nil {
		t.Fatalf("建缓存失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)

	withCache, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("命中缓存时 KlineHistory 失败：%v", err)
	}
	assertSameQuotes(t, withoutCache, withCache)

	// ③ 另一只也一致；没有日线的标的在两条路上必须"同样地失败"
	for _, code := range []string{"000001", "300750"} {
		viaCache, cacheErr := provider.KlineHistory(ctx, code, 19900101, 20261231)
		provider.SetSymbolBarsCache(nil)
		direct, directErr := provider.KlineHistory(ctx, code, 19900101, 20261231)
		provider.SetSymbolBarsCache(cache)

		if (cacheErr == nil) != (directErr == nil) {
			t.Fatalf("%s 两条路的成败不一致：缓存路 %v / 直读路 %v", code, cacheErr, directErr)
		}
		if cacheErr != nil {
			continue
		}
		assertSameQuotes(t, direct, viaCache)
	}
}

// 缓存水位比数据湖旧时，必须用"缓存正文 + 水位之后补尾"拼出完整结果 ——
// 既不能少最后几天，也不能因为补尾而重复。
func TestKlineHistoryFillsTailAfterStaleCache(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)
	ctx := context.Background()

	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(provider.lake); err != nil {
		t.Fatalf("建缓存失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)

	full, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("KlineHistory 失败：%v", err)
	}

	// 模拟"缓存是旧水位建的、数据湖又长了"：把缓存文件**截到** 2025-03-01，
	// 水位也改到那天。数据湖里 2025-03/2026-03 的行情还在，应当靠补尾取回来。
	rows, ok, err := cache.Read("600519")
	if err != nil || !ok {
		t.Fatalf("读缓存失败：%v ok=%v", err, ok)
	}
	cut := 0
	for cut < len(rows) && rows[cut].Date() <= 20250301 {
		cut++
	}
	if cut == 0 || cut == len(rows) {
		t.Fatalf("合成湖的日期不适合这条用例：cut=%d / 共 %d 行", cut, len(rows))
	}
	if err := parquet.WriteFile(filepath.Join(cache.BarsDir(), "600519.parquet"), rows[:cut]); err != nil {
		t.Fatalf("截断缓存文件失败：%v", err)
	}
	manifest, ok := cache.Manifest()
	if !ok {
		t.Fatal("manifest 应当存在")
	}
	manifest.LatestDate = rows[cut-1].Date()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化 manifest 失败：%v", err)
	}
	if err := os.WriteFile(cache.ManifestPath(), raw, 0o644); err != nil {
		t.Fatalf("改写 manifest 失败：%v", err)
	}

	stale, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("水位变旧后 KlineHistory 失败：%v", err)
	}
	assertSameQuotes(t, full, stale)
}

// 没挂缓存（拿不到配置目录 / 用户把缓存删了）时，K 线仍然必须出得来。
func TestKlineHistoryWithoutCache(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)
	provider.SetSymbolBarsCache(nil)

	quotes, err := provider.KlineHistory(context.Background(), "600519", 20240101, 20261231)
	if err != nil {
		t.Fatalf("无缓存时应当退化为直读，而不是报错：%v", err)
	}
	if len(quotes) == 0 {
		t.Fatal("无缓存时也应当取到日线")
	}
}

// **「缓存里的数据永远是最新的」的核心用例。**
//
// 数据湖被补跑 / 重算 / 重写之后（这里直接重写 2025 年那个分区，
// 把某一根的收盘价改掉），缓存必须立刻不再被信任，K 线给出**新**数据 ——
// 而不是"看起来更快"的旧数据。
func TestKlineHistorySeesRewrittenSource(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)
	ctx := context.Background()

	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(provider.lake); err != nil {
		t.Fatalf("建缓存失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)

	before, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("KlineHistory 失败：%v", err)
	}

	// ① 模拟 cne 重算 2025 年：把第一根的收盘价改掉，并写回**同一个分区文件**
	// （直接读写那一个文件，而不是用 DailyBarsInRange —— 合成湖是按年分区的，
	//  区间读会把所有年份都返回，写回去就变成"把三年数据塞进一个分区"。）
	path := filepath.Join(provider.lake.DatasetDir("daily_bars"), "trade_date=2025", "part-merged.parquet")
	rows, err := parquet.ReadFile[cnlake.BarRow](path)
	if err != nil || len(rows) == 0 {
		t.Fatalf("读不到 2025 年分区：%v rows=%d", err, len(rows))
	}
	const bump = 7.5
	target := -1
	for i, row := range rows {
		if cnlake.BareSymbol(row.Symbol) == "600519" && row.Date() == 20250301 {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("2025 年分区里找不到 600519 的 2025-03-01")
	}
	rows[target].Close += bump
	rows[target].High += bump
	if err := parquet.WriteFile(path, rows); err != nil {
		t.Fatalf("重写分区失败：%v", err)
	}

	// ② 缓存里的那份**没变**（这正是危险所在）：它还是老数据
	cached, ok, err := cache.Read("600519")
	if err != nil || !ok {
		t.Fatalf("读缓存失败：%v ok=%v", err, ok)
	}
	if cached[20].Close == before[20].Close+bump {
		t.Fatal("用例前提不成立：缓存里的数据不该自己变")
	}

	// ③ 但 K 线必须给出**重写后**的数据 —— 且与"完全不用缓存"的直读一致
	after, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	if err != nil {
		t.Fatalf("重写后 KlineHistory 失败：%v", err)
	}
	provider.SetSymbolBarsCache(nil)
	direct, err := provider.KlineHistory(ctx, "600519", 19900101, 20261231)
	provider.SetSymbolBarsCache(cache)
	if err != nil {
		t.Fatalf("直读失败：%v", err)
	}
	assertSameQuotes(t, direct, after)

	// 重写的那一根确实被看见了（否则就是拿了旧缓存）
	if after[20].Close != before[20].Close+bump {
		t.Errorf("重写没被看见：第 21 根收盘 %v，期望 %v",
			after[20].Close, before[20].Close+bump)
	}
	if after[20].Close == before[20].Close {
		t.Error("返回的还是重写前的数据 —— 缓存被错误地信任了")
	}
}

// 点查询（QuotePoint）也要走缓存，且结果必须与原来那条"窗口读"逐字段一致。
//
// 它原来会触发"全市场 × 120 天"的批量灌入；而调用方只是想知道
// "这只票最后收在哪"（K 线页判断裸码是股票还是指数时会走这里）。
func TestQuotePointMatchesWindowRead(t *testing.T) {
	provider := newSyntheticLakeProvider(t).(*lakeProvider)
	ctx := context.Background()

	// 先把缓存摘掉，量的是原来那条路
	provider.SetSymbolBarsCache(nil)
	direct, err := provider.QuotePoint(ctx, "600519", 20260320)
	if err != nil {
		t.Fatalf("无缓存时 QuotePoint 失败：%v", err)
	}
	if direct == nil {
		t.Fatal("应当取到行情")
	}

	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(provider.lake); err != nil {
		t.Fatalf("建缓存失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)

	cached, err := provider.QuotePoint(ctx, "600519", 20260320)
	if err != nil {
		t.Fatalf("有缓存时 QuotePoint 失败：%v", err)
	}
	if cached == nil {
		t.Fatal("有缓存时也应当取到行情")
	}
	if *cached != *direct {
		t.Fatalf("两条路结果不一致：\n窗口读 %+v\n缓存   %+v", *direct, *cached)
	}

	// 日历超前（该日没有 K 线）时两条路都要回退到最近一根
	provider.SetSymbolBarsCache(nil)
	directFallback, err := provider.QuotePoint(ctx, "600519", 20260930)
	if err != nil || directFallback == nil {
		t.Fatalf("无缓存回退失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)
	cachedFallback, err := provider.QuotePoint(ctx, "600519", 20260930)
	if err != nil || cachedFallback == nil {
		t.Fatalf("有缓存回退失败：%v", err)
	}
	if *cachedFallback != *directFallback {
		t.Fatalf("回退结果不一致：\n窗口读 %+v\n缓存   %+v", *directFallback, *cachedFallback)
	}
}

func assertSameQuotes(t *testing.T, want, got []*stockdb.Quote) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("条数不一致：直读 %d 条 vs 缓存 %d 条", len(want), len(got))
	}
	for i := range want {
		if *want[i] != *got[i] {
			t.Fatalf("第 %d 条不一致：\n直读 %+v\n缓存 %+v", i, *want[i], *got[i])
		}
	}
}

// 装配必须真的接上：数据湖行情源要挂好「按股票」缓存，且目录按数据湖根区分。
//
// 这条用例防的是"改了半天，服务层根本没把缓存装上"这类静默失效。
func TestLakeServiceAttachesSymbolCache(t *testing.T) {
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	provider, _ := svc.snapshot()
	lake, ok := provider.(*lakeProvider)
	if !ok {
		t.Fatalf("数据湖行情源类型不对：%T", provider)
	}
	cache := lake.symbolCache()
	if cache == nil {
		t.Fatal("数据湖行情源必须挂上按股票日线缓存")
	}
	root := cache.Root()
	if !strings.Contains(root, "bars-by-symbol") {
		t.Errorf("缓存目录不像按股票缓存：%q", root)
	}
	if !strings.Contains(root, "cnequity") {
		t.Errorf("缓存目录应当带数据湖目录名以便区分：%q", root)
	}

	// 换一个数据湖根目录，缓存目录必须跟着变（否则两边的数据会串味）
	other := svc.symbolBarsCacheRoot(`D:\别的地方\cnequity`)
	if other == root {
		t.Errorf("换了数据湖根目录，缓存目录不该相同：%q", other)
	}
	// 拿不到配置目录时禁用缓存，而不是写到意外的地方
	svc.cfg = nil
	if got := svc.symbolBarsCacheRoot(lake.lake.Root()); got != "" {
		t.Errorf("没有配置目录时应当禁用缓存，实际 %q", got)
	}
}
