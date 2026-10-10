package service

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/score"
)

// **回填 30 秒到底花在哪：取数 vs 指标计算。**
//
// 已有事实（4 天 × 7633 只、数据湖）：合计 30.6s，其中
// `评分(取数+指标)` = 29.9s，落库 0.65s，universe 2ms。
// 区间缓存的「真实取数」= 7634，即**每只只取 1 次** —— 取数次数没问题，
// 但那 7633 个「一次」每个要 3.9ms（320 根 K 线 -> 每根 12µs）。
//
// 这个用例把两段分开量，不靠猜。
func TestBackfillCostSplit(t *testing.T) {
	root := config.NewStore().CnLakeRoot()
	if root == "" {
		root = os.Getenv("FREESTOCK_LAKE_ROOT")
	}
	if root == "" {
		t.Skip("没有数据湖根目录")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)
	if _, err := svc.stock.SetDataSource("cnlake", root); err != nil {
		t.Skipf("切到数据湖失败：%v", err)
	}
	provider, _ := svc.stock.snapshot()

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil {
		t.Skipf("拿不到股票列表：%v", err)
	}
	codes := make([]string, 0, 1024)
	for _, item := range universe.Stocks {
		if item.HasQuote {
			codes = append(codes, item.Code)
		}
	}
	sort.Strings(codes)
	const sample = 300
	if len(codes) > sample {
		codes = codes[:sample]
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -480) // 约 320 个交易日

	ctx := context.Background()
	var fetchNanos, barsNanos, indNanos int64
	quoteCount, barCount := 0, 0

	for _, code := range codes {
		began := time.Now()
		quotes, err := provider.DailyHistory(ctx, code, start, latest)
		fetchNanos += int64(time.Since(began))
		if err != nil || len(quotes) == 0 {
			continue
		}
		quoteCount += len(quotes)

		began = time.Now()
		bars := barsFromQuotes(quotes)
		barsNanos += int64(time.Since(began))
		barCount += len(bars)

		began = time.Now()
		_, _ = score.ComputeIndicators(bars)
		_ = score.PrevHigh60(bars)
		_ = score.BuildRepairFeatures(bars)
		indNanos += int64(time.Since(began))
	}

	n := int64(len(codes))
	t.Logf("样本 %d 只，窗口 %d~%d（约 320 个交易日）", len(codes), start, latest)
	t.Logf("  K 线总数 %d（每只平均 %.0f 根）", quoteCount, float64(quoteCount)/float64(n))
	t.Logf("① 取数 DailyHistory : %8s   每只 %s", time.Duration(fetchNanos).Round(time.Millisecond), (time.Duration(fetchNanos) / time.Duration(n)).Round(time.Microsecond))
	t.Logf("② barsFromQuotes    : %8s   每只 %s", time.Duration(barsNanos).Round(time.Millisecond), (time.Duration(barsNanos) / time.Duration(n)).Round(time.Microsecond))
	t.Logf("③ 指标计算(3 个函数): %8s   每只 %s", time.Duration(indNanos).Round(time.Millisecond), (time.Duration(indNanos) / time.Duration(n)).Round(time.Microsecond))
	total := fetchNanos + barsNanos + indNanos
	t.Logf("合计 %s；取数占 %.0f%%，指标占 %.0f%%",
		time.Duration(total).Round(time.Millisecond),
		100*float64(fetchNanos)/float64(total), 100*float64(indNanos)/float64(total))
	t.Logf("每根 K 线折算：取数 %s，指标 %s",
		(time.Duration(fetchNanos) / time.Duration(maxInt(quoteCount, 1))).Round(time.Nanosecond),
		(time.Duration(indNanos) / time.Duration(maxInt(barCount, 1))).Round(time.Nanosecond))

	// 全市场外推
	full := int64(7563)
	_ = full
	t.Logf("按此外推全市场 %d 只：取数约 %s，指标约 %s",
		full,
		(time.Duration(fetchNanos) * time.Duration(full) / time.Duration(n)).Round(time.Second),
		(time.Duration(indNanos) * time.Duration(full) / time.Duration(n)).Round(time.Second))
}

var _ = cnlake.BareSymbol
