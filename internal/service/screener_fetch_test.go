package service

import (
	"context"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
)

func newLocalLakeProviderForScreen(t *testing.T) *lakeProvider {
	t.Helper()
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	lake := cnlake.Open(root)
	if !lake.HasDataset("daily_bars") {
		t.Skip("数据湖没有 daily_bars")
	}
	return &lakeProvider{lake: lake}
}

// **复现条件选股"卡在 0 / 678"**：几十个 worker 同时开始要各自的历史日线。
//
// 关键指标不是总耗时，而是**第一个结果要等多久** —— 进度条卡在 0
// 就是因为它一直等不到第一个完成。
//
// 这里同时钉住两件事：
//   - 首个结果必须很快（缓存填充只做一次）
//   - 总耗时必须是秒级，而不是"每只 861ms × 678"
func TestScreenerFetchPatternIsFast(t *testing.T) {
	provider := newLocalLakeProviderForScreen(t)

	dates, err := provider.lake.BarPartitionDates()
	if err != nil || len(dates) < 60 {
		t.Skipf("分区不足：%v", err)
	}
	// 模拟条件选股要的窗口（MA250 之类需要一年出头）
	window := dates
	if len(window) > 380 {
		window = window[len(window)-380:]
	}
	start, end := int(window[0]), int(window[len(window)-1])

	// 取一批真实代码（条件选股会先按条件粗筛，这里直接拿全市场）
	rows, err := provider.lake.DailyBarsInRange(nil, window[len(window)-5], int64(end))
	if err != nil {
		t.Fatalf("读区间失败：%v", err)
	}
	seen := map[string]bool{}
	codes := []string{}
	for _, row := range rows {
		code := cnlake.BareSymbol(row.Symbol)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	sort.Strings(codes)
	if len(codes) > 678 {
		codes = codes[:678]
	}
	if len(codes) < 50 {
		t.Skipf("代码太少（%d）", len(codes))
	}
	t.Logf("样本 %d 只，窗口 %d~%d（%d 个交易日）", len(codes), start, end, len(window))

	// 并发模式与条件选股一致
	const workers = 16
	var firstOnce sync.Once
	var firstElapsed time.Duration
	var done atomic.Int64

	began := time.Now()
	var wg sync.WaitGroup
	jobs := make(chan string)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for code := range jobs {
				quotes, err := provider.DailyHistory(context.Background(), code, start, end)
				if err != nil {
					t.Errorf("%s 失败：%v", code, err)
					continue
				}
				if len(quotes) == 0 {
					t.Errorf("%s 没有行情", code)
				}
				firstOnce.Do(func() { firstElapsed = time.Since(began) })
				done.Add(1)
			}
		}()
	}
	for _, code := range codes {
		jobs <- code
	}
	close(jobs)
	wg.Wait()
	total := time.Since(began)

	perCode := total / time.Duration(len(codes))
	t.Logf("① **首个结果**：%s（进度条就是从这时开始动的）", firstElapsed.Round(time.Millisecond))
	t.Logf("② 全部 %d 只：%s（每只 %s）", len(codes), total.Round(time.Millisecond), perCode.Round(time.Microsecond))

	// 进度条卡在 0 的直接判据：第一个结果不能等太久
	if firstElapsed > 10*time.Second {
		t.Errorf("首个结果等了 %s —— 界面会一直显示 0/%d",
			firstElapsed.Round(time.Second), len(codes))
	}
	// 总耗时必须是秒级；退化到"每只 861ms"的话这条会红
	if total > 30*time.Second {
		t.Errorf("%d 只用了 %s —— 说明批量缓存没生效（退回了每只约 861ms 的按代码读）",
			len(codes), total.Round(time.Second))
	}
	if done.Load() != int64(len(codes)) {
		t.Errorf("只完成 %d / %d", done.Load(), len(codes))
	}
}
