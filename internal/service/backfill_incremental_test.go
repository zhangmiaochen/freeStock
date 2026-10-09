package service

import (
	"os"
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 区间回填必须**增量**：重跑一个已经填过的区间应当近乎瞬间，
// 而且不改变任何已落库的评分。
func TestBackfillIsIncremental(t *testing.T) {
	if testing.Short() || os.Getenv("FREESTOCK_TIMING") != "1" {
		t.Skip("要打数据源（约 1 分钟）：设 FREESTOCK_TIMING=1 才跑")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	universe, err := svc.stock.GetStockUniverse(false)
	if err != nil || universe == nil || universe.TradeDate <= 0 {
		t.Skipf("数据源不可用：%v", err)
	}
	latest := int(universe.TradeDate)
	start := shiftDays(t, latest, -12)
	const limit = 60

	// ① 全量算一遍
	began := time.Now()
	first, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, true)
	if err != nil {
		t.Fatalf("首次回填失败：%v", err)
	}
	firstElapsed := time.Since(began)
	if first.Scored == 0 {
		t.Skipf("首次回填没出数据：%v", first.Skipped)
	}
	t.Logf("① 全量：实算 %d 天，复用 %d 条，跳过 %d 天，耗时 %s",
		first.Scored, first.Reused, first.ScoredFromCache, firstElapsed.Round(time.Millisecond))

	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore, err := store.LoadStrengthRange(score.StrategyTrendGrowth, int64(start), int64(latest))
	if err != nil {
		t.Fatal(err)
	}

	// ② 再跑一遍（增量）—— 应当全部跳过
	began = time.Now()
	second, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, latest, limit, false)
	if err != nil {
		t.Fatalf("增量回填失败：%v", err)
	}
	secondElapsed := time.Since(began)

	if second.ScoredFromCache == 0 {
		t.Errorf("第二次应当全部命中缓存，实际 实算 %d 天、跳过 %d 天",
			second.Scored, second.ScoredFromCache)
	}
	if second.Scored != 0 {
		t.Errorf("第二次不该实算任何一天，实际 %d 天", second.Scored)
	}
	t.Logf("② 增量：实算 %d 天，复用 %d 条，跳过 %d 天，耗时 %s（快 %.0f 倍）",
		second.Scored, second.Reused, second.ScoredFromCache,
		secondElapsed.Round(time.Millisecond),
		float64(firstElapsed)/float64(secondElapsed))

	// ③ 落库数据必须一模一样
	rowsAfter, err := store.LoadStrengthRange(score.StrategyTrendGrowth, int64(start), int64(latest))
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsBefore) != len(rowsAfter) {
		t.Fatalf("增量后评分条数变了：%d → %d", len(rowsBefore), len(rowsAfter))
	}
	for i := range rowsBefore {
		a, b := rowsBefore[i], rowsAfter[i]
		if a.Code != b.Code || a.TradeDate != b.TradeDate || a.TotalScore != b.TotalScore {
			t.Errorf("第 %d 条变了：%s/%d/%.4f → %s/%d/%.4f",
				i, a.Code, a.TradeDate, a.TotalScore, b.Code, b.TradeDate, b.TotalScore)
		}
	}
	t.Logf("③ 落库 %d 条评分，增量前后逐条一致", len(rowsAfter))
}
