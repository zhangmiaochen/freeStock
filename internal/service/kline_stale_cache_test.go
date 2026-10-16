package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"github.com/parquet-go/parquet-go"
)

// 本文件复现并钉住用户报告的问题：
//
//	「软件重新打开 K 线能读到最新数据，但一直开着不重启，就一直读缓存、读不到最新数据」
//
// 触发条件不是"数据被改写"（那由 TestKlineHistorySeesRewrittenSource 覆盖），
// 而是**数据湖多了一个新的交易日分区** —— 日更每天做的就是这件事。
// 进程重启后一切重新列目录，所以正常；不重启时必须有东西让缓存失效。
//
// ⚠ 刻意**不用** `cnlaketest.Build`：合成湖的 daily_bars 是按**年**分区的
// （`trade_date=2026`），而真实数据湖是按**日**分区（`trade_date=2026-03-20`）。
// 两种布局混在一棵树里时，`cnlake.BarPartitionDates` 会走进"分区名不是日期"
// 的退化分支，测出来的就不是真实路径了（第一版这么写，诊断信息里
// `others` 非空直接走了退化分支）。所以这里手工造一份"按日分区"的最小数据湖。

// writeDailyBarsPartition 写一个"某交易日"的分区。
func writeDailyBarsPartition(t *testing.T, root string, date int, rows []cnlake.BarRow) {
	t.Helper()
	dir := filepath.Join(root, "curated", "daily_bars", "trade_date="+formatDateHyphen(date))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建分区目录失败: %v", err)
	}
	if err := parquet.WriteFile(filepath.Join(dir, "part-merged.parquet"), rows); err != nil {
		t.Fatalf("写分区失败: %v", err)
	}
}

// formatDateHyphen 把 20261015 变成 2026-10-15（真实数据湖的分区命名）。
func formatDateHyphen(date int) string {
	text := strconv.Itoa(date)
	return text[:4] + "-" + text[4:6] + "-" + text[6:]
}

// buildDailyLakes 造一个"按日分区"的最小数据湖：连续 5 个交易日，每天一只股票。
func buildDailyLakes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	curated := filepath.Join(root, "curated")

	// 标的清单
	instrumentsDir := filepath.Join(curated, "instruments")
	if err := os.MkdirAll(instrumentsDir, 0o755); err != nil {
		t.Fatalf("建 instruments 目录失败: %v", err)
	}
	if err := parquet.WriteFile(filepath.Join(instrumentsDir, "part-merged.parquet"), []cnlake.InstrumentRow{
		{Symbol: "600519.SH", Name: "贵州茅台", Exchange: "SH", AssetType: "stock"},
	}); err != nil {
		t.Fatalf("写 instruments 失败: %v", err)
	}

	// 5 个交易日：2026-10-09 ~ 10-15（跳过周末）
	for i, date := range []int{20261009, 20261012, 20261013, 20261014, 20261015} {
		price := 1500.0 + float64(i)
		writeDailyBarsPartition(t, root, date, []cnlake.BarRow{{
			Symbol: "600519.SH", TradeDate: daysFromYMD(date),
			Open: price, High: price + 5, Low: price - 5, Close: price + 1,
			Volume: 1000, Amount: price * 1000,
		}})
	}
	return root
}

// daysFromYMD 把 20261015 转成 parquet DATE 的天数。
func daysFromYMD(date int) int32 {
	year := date / 10000
	month := date / 100 % 100
	day := date % 100
	// 与 cnlake 内部同一套换算（用它的导出函数最稳妥）
	return cnlake.DaysFromDate(int64(year*10000 + month*100 + day))
}

// TestKlineSeesNewTradingDayWithoutRestart 是用户报告问题的回归用例。
//
// 不重启进程、不手工失效任何缓存，数据湖多了一个交易日分区，
// K 线必须读到它。
func TestKlineSeesNewTradingDayWithoutRestart(t *testing.T) {
	root := buildDailyLakes(t)
	provider := NewLakeProvider(cnlake.Open(root)).(*lakeProvider)
	ctx := context.Background()

	// 挂上"按股票"派生缓存 —— 这就是不重启时被反复读的那份
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	if _, err := cache.Build(provider.lake); err != nil {
		t.Fatalf("建缓存失败：%v", err)
	}
	provider.SetSymbolBarsCache(cache)

	before, err := provider.KlineHistory(ctx, "600519", 19900101, 20291231)
	if err != nil {
		t.Fatalf("首次 KlineHistory 失败：%v", err)
	}
	if len(before) != 5 {
		t.Fatalf("初始应有 5 根，实际 %d", len(before))
	}
	lastBefore := before[len(before)-1].Date
	t.Logf("① 建好缓存：%d 根，最后一根 %d", len(before), lastBefore)

	// 日更：新增 2026-10-16 分区（收盘价明显不同）
	writeDailyBarsPartition(t, root, 20261016, []cnlake.BarRow{{
		Symbol: "600519.SH", TradeDate: daysFromYMD(20261016),
		Open: 1600, High: 1610, Low: 1590, Close: 1605,
		Volume: 2000, Amount: 1600 * 2000,
	}})

	// 唯一的允许延迟是 lakeLatestDate 的 1 分钟水位缓存 —— 清掉它，
	// 模拟"那一刻刚好过期"，这样测的是失效链路本身而不是等了多久。
	provider.symbolMu.Lock()
	provider.symbolLatestAt = time.Time{}
	provider.symbolMu.Unlock()

	after, err := provider.KlineHistory(ctx, "600519", 19900101, 20291231)
	if err != nil {
		t.Fatalf("新增分区后 KlineHistory 失败：%v", err)
	}
	lastAfter := after[len(after)-1].Date
	t.Logf("② 新增 2026-10-16 分区后再读：%d 根，最后一根 %d", len(after), lastAfter)

	if lastAfter <= lastBefore {
		t.Fatalf("数据湖新增了 2026-10-16 分区，K 线仍停在 %d —— "+
			"进程不重启就读不到最新数据（这正是用户报告的问题）", lastAfter)
	}
	if lastAfter != 20261016 {
		t.Errorf("最后一根应为 20261016，实际 %d", lastAfter)
	}
	if len(after) != 6 {
		t.Errorf("新增一根后应有 6 根，实际 %d", len(after))
	}
}

// TestLakeSeesNewPartitionWithoutRestart 把问题下推到数据湖层：
// 分区列表与"最新交易日"必须能看见新分区。
//
// 这一层是根因所在：`BarPartitionDates` 曾经把分区列表**永久缓存**，
// 于是进程启动后新落的分区永远看不见 —— 下游所有"是否已覆盖到最新"
// 的判定都跟着失效。
func TestLakeSeesNewPartitionWithoutRestart(t *testing.T) {
	root := buildDailyLakes(t)
	lake := cnlake.Open(root)

	latestBefore, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatalf("LatestTradeDate 失败：%v", err)
	}
	if latestBefore != 20261015 {
		t.Fatalf("初始最新交易日应为 20261015，实际 %d", latestBefore)
	}
	t.Logf("① 初始最新交易日 %d", latestBefore)

	writeDailyBarsPartition(t, root, 20261016, []cnlake.BarRow{{
		Symbol: "600519.SH", TradeDate: daysFromYMD(20261016),
		Open: 1600, High: 1610, Low: 1590, Close: 1605, Volume: 2000, Amount: 3.2e6,
	}})

	// ⚠ 同一个 lake 句柄、不重启、不重新 Open
	latestAfter, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatalf("新增分区后 LatestTradeDate 失败：%v", err)
	}
	t.Logf("② 新增分区后最新交易日 %d（分区的水位 %d）",
		latestAfter, lake.LatestPartitionDate())

	if latestAfter != 20261016 {
		t.Errorf("新增 2026-10-16 分区后，LatestTradeDate 应为 20261016，实际 %d —— "+
			"分区列表被永久缓存了（这就是不重启读不到新数据的根因）", latestAfter)
	}

	// 区间读取也必须看到新分区
	bars, err := lake.DailyBarsInRange([]string{"600519"}, 20261016, 20261016)
	if err != nil {
		t.Fatalf("DailyBarsInRange 失败：%v", err)
	}
	if len(bars) != 1 {
		t.Errorf("按日期区间读新分区应得 1 行，实际 %d 行", len(bars))
	}
}
