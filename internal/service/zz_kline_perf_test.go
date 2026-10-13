package service

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"freestock/internal/cnlake"
)

// **K 线冷启动探针**：量"第一次打开一只股票的 K 线"到底花在哪、按股票缓存省了多少。
//
//	FREESTOCK_KLINE_PERF=1 go test ./internal/service/ -run TestKlinePerfProbe -v -count=1 -timeout 30m
//
// 量四件事：
//  1. 没缓存时打开 K 线（退化为"只读这一只股票"的区间读）；
//  2. 直读基线：一只股票的全历史要打开多少个分区文件、多久；
//  3. 建一次「按股票」缓存要多久、占多少内存；
//  4. 缓存建好后，任意股票首开 + 堆内存。
//
// 日志统一用 ASCII 前缀 `PROBE`：中文经 PowerShell 管道会被按本地代码页解码，
// `Select-String` 匹配不到。
func TestKlinePerfProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_KLINE_PERF") != "1" {
		t.Skip("K 线性能探针：设 FREESTOCK_KLINE_PERF=1 才跑")
	}
	// 关掉 Info 日志：这条路径会打上千行"分块读完成"，把探针自己的输出淹掉
	slog.SetLogLoggerLevel(slog.LevelError)

	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}
	provider, _ := svc.snapshot()
	lake, ok := provider.(*lakeProvider)
	if !ok {
		t.Skip("当前数据源不是数据湖，探针无意义")
	}
	// 先用**没有缓存**的状态量一遍（这才是用户第一次用时的样子）
	lake.SetSymbolBarsCache(nil)

	heap := func() string {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return fmt.Sprintf("%.0fMB", float64(stats.HeapAlloc)/1024/1024)
	}

	began := time.Now()
	dates, _ := lake.lake.BarPartitionDates()
	t.Logf("PROBE partitions=%d listCost=%s heap=%s",
		len(dates), time.Since(began).Round(time.Millisecond), heap())
	if len(dates) == 0 {
		t.Skip("数据湖没有日分区")
	}
	from, to := dates[0], dates[len(dates)-1]

	// 基线：逐个分区按代码过滤（"按股票缓存"要替掉的就是这一段）
	began = time.Now()
	rows, err := lake.lake.DailyBarsInRange([]string{"600519"}, from, to)
	if err != nil {
		t.Fatalf("DailyBarsInRange: %v", err)
	}
	t.Logf("PROBE directSingleCode rows=%d elapsed=%s heap=%s",
		len(rows), time.Since(began).Round(time.Millisecond), heap())

	// "缓存还能不能用"每次查询都要算的源分区指纹（(路径,大小,mtime) 哈希）
	began = time.Now()
	fingerprint, err := lake.lake.SourceSignature(lake.lake.LatestPartitionDate())
	if err != nil {
		t.Fatalf("SourceSignature: %v", err)
	}
	t.Logf("PROBE sourceSignature files=%d elapsed=%s sig=%s",
		len(lake.lake.DatasetFiles("daily_bars")), time.Since(began).Round(time.Millisecond), fingerprint[:8])

	open := func(label, code string) {
		started := time.Now()
		bundle, err := svc.GetKline(code, "qfq", 1990)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		lake.bulkMu.Lock()
		bulkCodes, scored := len(lake.bulkBars), len(lake.bulkScored)
		lake.bulkMu.Unlock()
		t.Logf("PROBE %-16s code=%-7s candles=%-5d elapsed=%-9s bulkCodes=%-5d scoredCodes=%-5d heap=%s",
			label, code, len(bundle.Candles), elapsed.Round(time.Millisecond), bulkCodes, scored, heap())
	}

	open("no-cache", "600519")

	// 建一次缓存（这一步在真实运行里是后台跑的）
	cache := cnlake.NewSymbolBarsCache(filepath.Join(t.TempDir(), "bars-by-symbol"))
	began = time.Now()
	manifest, err := cache.Build(lake.lake)
	if err != nil {
		t.Fatalf("建按股票缓存失败：%v", err)
	}
	t.Logf("PROBE buildCache symbols=%d rows=%d latest=%d elapsed=%s heap=%s",
		manifest.Symbols, manifest.Rows, manifest.LatestDate,
		time.Since(began).Round(time.Millisecond), heap())

	lake.SetSymbolBarsCache(cache)
	open("cache-first", "600519")
	open("cache-second", "600519")
	open("cache-other", "000001")
	open("cache-other2", "300750")
	open("cache-other3", "002594")

	// 股票列表那段"不限起点补抓"：全表扫描 vs 缓存
	universe, err := svc.GetStockUniverse(false)
	if err != nil {
		t.Fatalf("取全量列表失败：%v", err)
	}
	noQuote := make([]string, 0, 256)
	for _, item := range universe.Stocks {
		if !item.HasQuote {
			noQuote = append(noQuote, item.Code)
		}
	}
	if len(noQuote) > 0 {
		lake.retryMu.Lock()
		lake.retryCache = nil
		lake.retryMu.Unlock()
		began = time.Now()
		if _, _, err := lake.retryFill(noQuote, int(universe.TradeDate)); err != nil {
			t.Fatalf("retryFill 失败：%v", err)
		}
		t.Logf("PROBE retryFillWithCache codes=%d elapsed=%s", len(noQuote), time.Since(began).Round(time.Millisecond))

		lake.SetSymbolBarsCache(nil)
		began = time.Now()
		if _, _, err := lake.bulkQuotes(noQuote, 0, int(universe.TradeDate)); err != nil {
			t.Fatalf("bulkQuotes 失败：%v", err)
		}
		t.Logf("PROBE retryFillFullScan codes=%d elapsed=%s", len(noQuote), time.Since(began).Round(time.Millisecond))
	}
}
