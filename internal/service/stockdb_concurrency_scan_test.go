package service

import (
	"context"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"freestock/internal/config"
)

// **stockdb 取数的并发扫描**（用在 stockdb 数据源上，不碰数据湖）。
//
// 背景：全市场评分/回填的时间几乎全花在"每只股票一次 HTTP 往返"上
// （实测全市场单日约 34 秒、约 7563 次请求 ≈ 4.4ms/次）。
// 客户端连接池已经配好（`MaxIdleConnsPerHost: 128`），服务端也没有
// 多代码批量接口，所以**唯一能调的杠杆就是并发度**。
//
// ⚠ 之前我测过一次并发，但那个测试**把错误全吞了** ——
// 失败/被拒的请求耗时很短，于是"并发越高越快"的假象就出来了。
// 所以这次**必须记错误数**，并且把并发上限压在不会打爆服务端的范围。
//
// 另外：这台服务端曾经因为我用 32~64 并发压测而出现大量
// `failed to register new link fd`，并导致 `data1` 损坏。
// 所以这里**默认不跑**，要显式设 FREESTOCK_CONCURRENCY_SCAN=1。
func TestStockdbConcurrencyScan(t *testing.T) {
	if os.Getenv("FREESTOCK_CONCURRENCY_SCAN") != "1" {
		t.Skip("会向 stockdb 发大量请求：设 FREESTOCK_CONCURRENCY_SCAN=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, source := svc.stock.snapshot()
	if source.Kind != config.KindStockDB {
		t.Skipf("当前数据源不是 stockdb（是 %s），本用例只在 stockdb 上测", source.Kind)
	}
	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil {
		t.Skipf("拿不到股票列表：%v", err)
	}
	codes := make([]string, 0, len(universe.Stocks))
	for _, stock := range universe.Stocks {
		if stock.HasQuote {
			codes = append(codes, stock.Code)
		}
	}
	sort.Strings(codes)
	if len(codes) > 1200 {
		codes = codes[:1200] // 采样，别对着服务端猛打
	}
	latest := int(universe.TradeDate)

	t.Logf("数据源 %s，样本 %d 只，最新交易日 %d", source.Kind, len(codes), latest)
	t.Logf("%-8s %-10s %-10s %-10s %-12s", "并发", "总耗时", "成功", "失败", "每只")

	type row struct {
		workers int
		elapsed time.Duration
		ok      int64
		fail    int64
	}
	rows := make([]row, 0, 5)

	for _, workers := range []int{1, 4, 8, 16, 32} {
		var ok, fail int64
		began := time.Now()
		var wg sync.WaitGroup
		jobs := make(chan string)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for code := range jobs {
					// ⚠ **必须检查 err** —— 之前吞掉错误导致结论完全错
					_, err := provider.DailyHistory(context.Background(), code, latest-30, latest)
					if err != nil {
						atomic.AddInt64(&fail, 1)
						continue
					}
					atomic.AddInt64(&ok, 1)
				}
			}()
		}
		for _, code := range codes {
			jobs <- code
		}
		close(jobs)
		wg.Wait()
		elapsed := time.Since(began)

		rows = append(rows, row{workers: workers, elapsed: elapsed, ok: ok, fail: fail})
		perCode := elapsed / time.Duration(len(codes))
		t.Logf("%-8d %-10s %-10d %-10d %-12s",
			workers, elapsed.Round(time.Millisecond), ok, fail, perCode.Round(time.Microsecond))

		if fail > 0 {
			t.Logf("  ⚠ 并发 %d 出现 %d 个失败 —— 不该再往上加", workers, fail)
			break // 服务端开始拒绝就不再加压
		}
	}

	// 找出"零失败里最快"的那一档 —— 这就是应该用的并发度
	best := row{}
	found := false
	for _, cur := range rows {
		if cur.fail > 0 {
			continue
		}
		if !found || cur.elapsed < best.elapsed {
			best, found = cur, true
		}
	}
	if found {
		t.Logf("★ 零失败中最快：并发 %d，%s（每只 %s）",
			best.workers, best.elapsed.Round(time.Millisecond),
			(best.elapsed / time.Duration(len(codes))).Round(time.Microsecond))
		// 全市场外推
		full := best.elapsed * time.Duration(7563) / time.Duration(len(codes))
		t.Logf("  按此并发外推全市场 7563 只：约 %s", full.Round(time.Second))
	}
}
