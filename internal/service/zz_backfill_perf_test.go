package service

import (
	"os"
	"strings"
	"testing"
	"time"

	"freestock/internal/cnlake"
	"freestock/internal/config"
	"freestock/internal/score"
)

// **回填性能探针**：全市场规模下测"单日成本"，再外推整个区间。
//
// 用途：用户反馈"2022→2026 全量重算区间耗时太久"。要看的是
// **每个交易日 × 全市场**的真实成本，而 `TestBackfillAB` 默认只跑几百只，
// 线性外推不可靠（引擎里还有横截面排序等超线性部分）。
//
//	FREESTOCK_BFP=1 FREESTOCK_BFP_DAYS=3 go test ./internal/service/ -run TestBackfillFullMarketProbe -v -count=1
func TestBackfillFullMarketProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_BFP") != "1" {
		t.Skip("回填性能探针（全市场，要打数据源）：设 FREESTOCK_BFP=1 才跑")
	}
	dayCount := abEnvInt("FREESTOCK_BFP_DAYS", 3)

	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	// ⚠ `newPortfolioService` 装的是 **stockdb** 源，而"重算区间耗时太久"
	// 那条路（`bulkMaxDays` 把长区间的批量读关掉）只出现在**数据湖**源上。
	// 所以要显式切到数据湖，否则测的根本不是同一个场景。
	if strings.EqualFold(os.Getenv("FREESTOCK_BFP_SOURCE"), "cnlake") {
		root := cnlake.Detect()
		if root == "" {
			t.Skip("没有数据湖：设 FREESTOCK_CNLAKE 或确认默认路径存在")
		}
		svc.stock.applySource(config.KindCnLake, root)
		t.Logf("数据源已切到数据湖：%s", root)
	}

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	store := mustStore(t, svc)

	// 全市场：limit=0
	all, _, err := svc.scoringUniverse(store, 0)
	if err != nil || len(all) == 0 {
		t.Skipf("拿不到全市场代码：%v", err)
	}
	latest, err := provider.LatestTradeDate(t.Context())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	dates, err := svc.tradingDates(shiftDays(t, latest, -dayCount*3-10), latest)
	if err != nil || len(dates) < 2 {
		t.Skipf("交易日不足：%v", err)
	}
	if len(dates) > dayCount {
		dates = dates[len(dates)-dayCount:]
	}
	start, end := dates[0], dates[len(dates)-1]

	t.Logf("全市场 %d 只 × %d 个交易日（%d ~ %d）", len(all), len(dates), start, end)
	began := time.Now()
	result, err := svc.RefreshBackfill(score.StrategyTrendGrowth, start, end, 0, true)
	if err != nil {
		t.Fatalf("回填失败：%v", err)
	}
	coldElapsed := time.Since(began)

	// ---- 热态单日：同一个缓存、同样全市场 ----
	//
	// ⚠ 这是**必须分开测**的一段。整段回填耗时里含一次性的冷取数；
	// 直接拿"总耗时 / 天数"外推到 970 天，等于把冷取数乘了 970 遍 ——
	// 我第一版就是这么算的，得出"3 小时 37 分"，把结论带偏。
	cache := svc.backfillCacheFor(provider, start, end)
	if cache == nil {
		t.Skip("拿不到区间缓存，无法测热态")
	}
	scoring := NewScoringService(svc.stock)
	scoring.override = cache

	var engineTotal, saveTotal time.Duration
	rows := 0
	for _, date := range dates {
		step := time.Now()
		res, scoreErr := scoring.ScoreStocks(all, date, 0, score.StrategyTrendGrowth)
		engineTotal += time.Since(step)
		if scoreErr != nil || res == nil {
			continue
		}
		rows += len(res.Rows)
		step = time.Now()
		if err := svc.saveScoringResult(store, res, score.StrategyTrendGrowth, false); err != nil {
			t.Fatalf("%d 落库失败：%v", date, err)
		}
		saveTotal += time.Since(step)
	}
	days := int64(bfpMax(len(dates), 1))
	enginePerDay := engineTotal / time.Duration(days)
	savePerDay := saveTotal / time.Duration(days)
	warmPerDay := enginePerDay + savePerDay

	t.Logf("冷态整段：%d 天耗时 %s（含一次性冷取数）", len(dates), coldElapsed.Round(time.Millisecond))
	t.Logf("热态单日：引擎 %s + 落库 %s = %s/天（%d 行/天）",
		enginePerDay.Round(time.Millisecond), savePerDay.Round(time.Millisecond),
		warmPerDay.Round(time.Millisecond), rows/bfpMax(len(dates), 1))
	t.Logf("→ 970 个交易日（2022~2026）按「冷取数一次 + 970×热态」外推：%s",
		(coldElapsed + warmPerDay*970).Round(time.Second))
	t.Logf("结果：scored=%d skipped=%d perDay=%v", result.Scored, len(result.Skipped), result.PerDay)
}

func bfpMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
