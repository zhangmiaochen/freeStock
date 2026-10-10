package service

import (
	"os"
	"strconv"
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// abEnvInt 读整型环境变量（自带一份，方便把这个用例单独拷到别的分支上跑）。
func abEnvInt(key string, def int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return def
}

// 回填 A/B 的**唯一度量**：真实路径 + 热缓存下的单日引擎成本。
//
// 为什么不能只看 `RefreshBackfill` 的总耗时：那里面混着一次性的"整窗口取数"
// （全市场 7600 次请求，实测 30 秒上下，波动 ±25%），它跟被优化的逐日计算无关。
// 240 天的回填里只有 1 天是冷的，**热态单日成本 × 天数**才决定总耗时。
//
// 所以这里分两段量：
//
//	A 真实路径：RefreshBackfill 一次（含冷取数），报它自己的日志数字
//	B 热态单日：逐日 ScoreStocks（引擎）+ saveScoringResult（落库），各报均值
//
// 用法（改前 / 改后各跑一次，参数保持一致）：
//
//	FREESTOCK_AB=1 FREESTOCK_AB_CODES=7600 FREESTOCK_AB_DAYS=12 \
//	  go test ./internal/service/ -run TestBackfillAB -v -count=1
func TestBackfillAB(t *testing.T) {
	if os.Getenv("FREESTOCK_AB") != "1" {
		t.Skip("回填 A/B（要打数据源，1~2 分钟）：设 FREESTOCK_AB=1 才跑")
	}
	codeCount := abEnvInt("FREESTOCK_AB_CODES", 2000)
	dayCount := abEnvInt("FREESTOCK_AB_DAYS", 10)

	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	store := mustStore(t, svc)
	codes, _, err := svc.scoringUniverse(store, codeCount)
	if err != nil || len(codes) == 0 {
		t.Skipf("拿不到评分范围：%v", err)
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

	// ---- A：真实路径（含一次性冷取数）----
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, end, codeCount, true)
	realElapsed := time.Since(began)
	if err != nil || bf.Scored == 0 {
		t.Skipf("回填失败：%v / %v", err, bf.Skipped)
	}
	t.Logf("A 真实路径 RefreshBackfill：%d 天 × %d 只 = %s",
		bf.Days, len(codes), realElapsed.Round(time.Millisecond))

	// ---- B：热态单日 ----
	cache := svc.backfillCacheFor(provider, start, end)
	if cache == nil {
		t.Skip("拿不到区间缓存")
	}
	scoring := NewScoringService(svc.stock)
	scoring.override = cache

	var engineTotal, saveTotal time.Duration
	rows := 0
	for _, date := range dates {
		began = time.Now()
		result, err := scoring.ScoreStocks(codes, date, 0, score.StrategyTrendGrowth)
		engineTotal += time.Since(began)
		if err != nil || result == nil {
			continue
		}
		rows += len(result.Rows)

		began = time.Now()
		if err := svc.saveScoringResult(store, result, score.StrategyTrendGrowth, false); err != nil {
			t.Fatalf("%d 落库失败：%v", date, err)
		}
		saveTotal += time.Since(began)
	}
	days := time.Duration(len(dates))
	enginePerDay := engineTotal / days
	savePerDay := saveTotal / days
	warmPerDay := enginePerDay + savePerDay

	t.Logf("B 热态单日：引擎 %s + 落库 %s = %s/天（%d 行/天）",
		enginePerDay.Round(time.Millisecond), savePerDay.Round(time.Millisecond),
		warmPerDay.Round(time.Millisecond), rows/len(dates))
	t.Logf("→ 240 天回填的热态成本：%s（冷取数另计一次）",
		(warmPerDay * 240).Round(time.Second))
}
