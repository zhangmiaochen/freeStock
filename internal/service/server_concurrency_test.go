package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"freestock/internal/stockdb"
)

// free-stockdb 服务端是不是**串行处理**请求的？
//
// 这直接决定优化方向：
//   - 若服务端串行 → 提高客户端并发**没有意义**，只能**减少请求数**
//   - 若服务端并发 → 那就该加大并发
//
// 实测依据（回填 4 天 × 300 只）：684 次真实取数耗时 15.7s = 23ms/次，
// 而客户端已开到 32 并发、HTTP 连接上限 128 —— 数字上等于串行。
func TestServerConcurrencyProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	codes, _, err := svc.scoringUniverse(mustStore(t, svc), 40)
	if err != nil || len(codes) == 0 {
		t.Skipf("拿不到代码：%v", err)
	}
	latest, err := provider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	start := shiftDays(t, latest, -400)

	// ① 串行
	began := time.Now()
	for _, code := range codes {
		if _, err := provider.DailyHistory(context.Background(), code, start, latest); err != nil {
			t.Fatalf("%s 失败：%v", code, err)
		}
	}
	serial := time.Since(began)

	// ② 32 并发（与评分实际用的并发度一致）
	client := stockdb.NewClient("http://127.0.0.1:7899")
	began = time.Now()
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for code := range jobs {
				_, _ = client.DailyHistory(context.Background(), code, start, latest)
			}
		}()
	}
	for _, code := range codes {
		jobs <- code
	}
	close(jobs)
	wg.Wait()
	parallel := time.Since(began)

	t.Logf("%d 只：串行 %s（%s/只），32 并发 %s（%s/只）",
		len(codes), serial.Round(time.Millisecond),
		(serial / time.Duration(len(codes))).Round(time.Millisecond),
		parallel.Round(time.Millisecond),
		(parallel / time.Duration(len(codes))).Round(time.Millisecond))
	t.Logf("并发提速：%.1f 倍（理论上限 32 倍）", float64(serial)/float64(parallel))
	if float64(serial)/float64(parallel) < 4 {
		t.Logf("→ 提速远低于并发度 ⇒ **服务端是瓶颈（近似串行）**，" +
			"加大客户端并发没有意义，只能减少请求数")
	} else {
		t.Logf("→ 并发有效，瓶颈在客户端")
	}
}
