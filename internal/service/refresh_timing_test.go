package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/score"
)

// 对比**两个数据源**下的评分耗时 —— 决定"要不要把评分取数换成本地直读"。
//
// 已实测：本机 stockdb 与数据湖的日线**逐日完全一致**（见 TestProviderPriceParity，
// 1567 行 0 差异）。所以这里只需比较速度，就能给出明确建议。
// 对比**两个数据源**下的评分耗时 —— 决定"要不要把评分取数换成本地直读"。
//
// ⚠ **结论：不要换。** 实测（2026-10-09，本机）：
//
//	stockdb(HTTP)   400 只 /  20.4s → 单只   51ms → 外推 7546 只约 6 分 25 秒
//	cnlake(本地)    356 只 / 10m24s → 单只 1753ms → 外推 7546 只约 3 小时 40 分
//
// **本地数据湖比 HTTP 慢 34 倍。** 原因不是"读本地慢"，而是湖的 `daily_bars`
// 是**按交易日分区**的（2600+ 个日文件）：查一只股票一年的历史要打开并扫描
// 约 250 个日文件。HTTP 那边的 free-stockdb 有按代码的索引，所以每次往返虽慢，
// 总量却小得多。
//
// 顺带说明：日线**口径**本身是一致的（`TestProviderPriceParity`：1567 行 0 差异），
// 所以"不换"纯粹是因为换了会更慢，不是因为数据对不上。
//
// 这个用例要跑 10 分钟以上，用 FREESTOCK_TIMING=1 显式开启。
func TestRefreshTimingBothSources(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("计时对比（10 分钟以上）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	const limit = 400

	// ① 本机 stockdb（HTTP，每只一次往返）
	if _, err := svc.stock.SetDataSource("stockdb", "http://127.0.0.1:7899"); err != nil {
		t.Skipf("本机 stockdb 不可用：%v", err)
	}
	began := time.Now()
	httpResult, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, true)
	httpElapsed := time.Since(began)
	if err != nil || httpResult.Saved == 0 {
		t.Skipf("HTTP 侧评分失败：%v", err)
	}
	t.Logf("stockdb(HTTP)：%d 只，耗时 %s（单只 %s）",
		httpResult.Saved, httpElapsed.Round(time.Millisecond),
		(httpElapsed / time.Duration(httpResult.Saved)).Round(time.Millisecond))

	// ② 数据湖（本地 parquet 直读）
	lake := svc.stock.lakeHandle()
	if lake == nil || !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars，跳过对比")
	}
	if _, err := svc.stock.SetDataSource("cnlake", lake.Root()); err != nil {
		t.Skipf("切数据湖失败：%v", err)
	}
	began = time.Now()
	lakeResult, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, true)
	lakeElapsed := time.Since(began)
	if err != nil || lakeResult.Saved == 0 {
		t.Skipf("数据湖侧评分失败：%v", err)
	}
	t.Logf("cnlake(本地)：%d 只，耗时 %s（单只 %s）",
		lakeResult.Saved, lakeElapsed.Round(time.Millisecond),
		(lakeElapsed / time.Duration(lakeResult.Saved)).Round(time.Millisecond))

	ratio := float64(httpElapsed) / float64(lakeElapsed)
	t.Logf("→ 数据湖快 %.2f 倍；按 7546 只外推：HTTP 约 %s，湖约 %s",
		ratio,
		(time.Duration(7546) * httpElapsed / time.Duration(httpResult.Saved)).Round(time.Second),
		(time.Duration(7546) * lakeElapsed / time.Duration(lakeResult.Saved)).Round(time.Second))
}
func TestRefreshStrengthTiming(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("计时（约 40 秒）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	for _, limit := range []int{200, 800} {
		began := time.Now()
		result, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, true)
		elapsed := time.Since(began)
		if err != nil {
			t.Skipf("评分失败：%v", err)
		}
		if result.Saved == 0 {
			t.Skip("一只都没算出来")
		}
		perCode := elapsed / time.Duration(result.Saved)
		t.Logf("limit=%d：新算 %d 只，耗时 %s（单只 %s）；按 7546 只外推约 %s",
			limit, result.Saved, elapsed.Round(time.Millisecond),
			perCode.Round(time.Millisecond),
			(time.Duration(7546) * perCode).Round(time.Second))

		// 紧接着再刷一次（增量）—— 应当是"几乎零耗时"
		again := time.Now()
		cached, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, false)
		if err != nil {
			t.Fatalf("增量刷新失败：%v", err)
		}
		t.Logf("  增量刷新：新算 %d 只，命中缓存 %d 只，耗时 %s",
			cached.Saved, cached.Reused, time.Since(again).Round(time.Millisecond))
	}
}
