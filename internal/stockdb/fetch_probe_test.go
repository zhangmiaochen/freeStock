package stockdb

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// 冷取数到底卡在哪 —— 单只一次区间请求的成本拆解。
//
// 背景：全市场回填第一天要取 7600 只 × 约 330 个交易日的日线，
// 实测 30 秒上下、且波动很大（±25%）。要判断"客户端还能不能优化"，
// 必须先把**服务端单次请求**与**客户端解码**分开量。
//
// 设 FREESTOCK_FETCH_PROBE=1 开启（会向 stockdb 发请求）。
func TestFetchProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_FETCH_PROBE") != "1" {
		t.Skip("要打 stockdb：设 FREESTOCK_FETCH_PROBE=1 才跑")
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
	if len(all) == 0 {
		t.Skip("代码表为空")
	}
	latest, err := client.LatestTradeDate(ctx)
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	start := latest - 20000 // 约两年前，保证能取到 330 个交易日
	if start < 19900101 {
		start = 19900101
	}

	// ① 单只（服务端 + 客户端一起）：串行取 200 只，报最小值/中位数/最大值
	const sample = 200
	if len(all) > sample {
		all = all[:sample]
	}
	latencies := make([]time.Duration, 0, len(all))
	var totalRows int
	for _, code := range all {
		begin := time.Now()
		quotes, err := client.DailyHistory(ctx, code, start, latest)
		elapsed := time.Since(begin)
		if err != nil {
			t.Fatalf("%s 取数失败：%v", code, err)
		}
		totalRows += len(quotes)
		latencies = append(latencies, elapsed)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("① 串行单只取全年：%d 只、%d 行；最小 %s / 中位 %s / 最大 %s（中位约 %d 行）",
		len(all), totalRows,
		latencies[0].Round(time.Microsecond),
		latencies[len(latencies)/2].Round(time.Microsecond),
		latencies[len(latencies)-1].Round(time.Microsecond),
		totalRows/len(all))

	// ② 延迟是否随"返回记录数"增长（判断瓶颈在服务端编码还是网络往返）
	probe := all[0]
	for _, days := range []int{1, 30, 120, 330} {
		from := latest - days*2 // 粗略回看（够覆盖 days 个交易日）
		var best time.Duration
		for round := 0; round < 3; round++ {
			begin := time.Now()
			body, err := client.callRaw(ctx, "vals", TableDailyK, "key:"+probe, rangeExpr(from, latest))
			if err != nil {
				t.Fatalf("取数失败：%v", err)
			}
			elapsed := time.Since(begin)
			if best == 0 || elapsed < best {
				best = elapsed
			}
			if round == 0 {
				quotes, err := DecodeQuotes(body)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("   窗口 %4d 自然日：%4d 行、%6d 字节", days*2, len(quotes), len(body))
			}
		}
		t.Logf("② 单次请求最小延迟（窗口 %4d 自然日）：%s", days*2, best.Round(time.Microsecond))
	}

	// ③ 并发吞吐：并发上去之后总吞吐还涨不涨（不涨 = 服务端是瓶颈）
	for _, workers := range []int{1, 8, 32, 64} {
		codes := all
		if len(codes) > 400 {
			codes = codes[:400]
		}
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
		t.Logf("③ 并发 %2d 取 %d 只：%s（%.0f 只/秒）",
			workers, len(codes), elapsed.Round(time.Millisecond),
			float64(len(codes))/elapsed.Seconds())
	}
}
