package stockdb

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// 取数吞吐基准（**只用基线就有的 API**，方便在改动前后同一份代码上对比）。
//
// 问题：客户端解码快了 4 倍，取数吞吐会不会跟着涨？
// 如果吞吐不变，说明瓶颈在服务端，解码优化只省 CPU、不省墙钟。
//
// 设 FREESTOCK_FETCH_THROUGHPUT=1 开启。
func TestFetchThroughput(t *testing.T) {
	if os.Getenv("FREESTOCK_FETCH_THROUGHPUT") != "1" {
		t.Skip("要打 stockdb：设 FREESTOCK_FETCH_THROUGHPUT=1 才跑")
	}
	client := NewClient(DefaultLocalURL)
	ctx := context.Background()

	buckets, err := client.StockCodes(ctx)
	if err != nil || len(buckets) == 0 {
		t.Skipf("stockdb 不可用：%v", err)
	}
	all := make([]string, 0, 8192)
	for _, list := range buckets {
		all = append(all, list...)
	}
	sort.Strings(all)
	if len(all) < 200 {
		t.Skip("代码太少")
	}
	latest, err := client.LatestTradeDate(ctx)
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	start := latest - 20000
	if start < 19900101 {
		start = 19900101
	}
	codes := all[:200]

	best := map[int]time.Duration{}
	for round := 0; round < 3; round++ {
		for _, workers := range []int{1, 8, 32} {
			begin := time.Now()
			jobs := make(chan string)
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for code := range jobs {
						_, _ = client.DailyHistory(ctx, code, start, latest)
					}
				}()
			}
			for _, code := range codes {
				jobs <- code
			}
			close(jobs)
			wg.Wait()
			elapsed := time.Since(begin)
			if best[workers] == 0 || elapsed < best[workers] {
				best[workers] = elapsed
			}
		}
	}
	for _, workers := range []int{1, 8, 32} {
		t.Logf("并发 %2d：%s（%.0f 只/秒）—— 取 %d 只全年日线",
			workers, best[workers].Round(time.Millisecond),
			float64(len(codes))/best[workers].Seconds(), len(codes))
	}
}
