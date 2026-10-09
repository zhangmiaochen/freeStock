package service

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// shiftDays 把 YYYYMMDD 往前/后挪若干自然日（用 time 算，避免非法日期）。
func shiftDays(t *testing.T, date, days int) int {
	t.Helper()
	parsed, err := time.Parse("20060102", fmt.Sprintf("%d", date))
	if err != nil {
		t.Fatalf("解析日期 %d 失败：%v", date, err)
	}
	out, err := strconv.Atoi(parsed.AddDate(0, 0, days).Format("20060102"))
	if err != nil {
		t.Fatalf("格式化日期失败：%v", err)
	}
	return out
}

// mustStore 拿组合库（测试用）。
func mustStore(t *testing.T, svc *PortfolioService) *portfolio.Store {
	t.Helper()
	store, err := svc.db()
	if err != nil {
		t.Fatalf("取组合库失败：%v", err)
	}
	return store
}

// ⚠ 这是本优化**最重要的一个测试**。
//
// 回填提速靠的是"同一只股票只取一次日线、再按区间切片"。
// 一旦切片结果与真实请求有任何差异，历史评分就会变 —— 那是不可接受的。
//
// 所以这里用真实数据做**逐条比对**：
//
//	直接逐日请求（旧路径） vs 走区间缓存（新路径）
//
// 评分行必须完全一致。
func TestRangeCacheScoresIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源，逐条比对）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}

	// 取少量代码与几个交易日，够验证等价性又不至于太慢
	const codes = 8
	codesList, _, err := svc.scoringUniverse(mustStore(t, svc), codes)
	if err != nil || len(codesList) == 0 {
		t.Skipf("拿不到代码列表：%v", err)
	}
	latest, err := provider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}

	// ⚠ 日期算术不能在 YYYYMMDD 上直接减天数（20261008-30=20260978 是非法日期）
	from := shiftDays(t, latest, -30)
	dates, err := svc.tradingDates(from, latest)
	if err != nil || len(dates) < 3 {
		t.Skipf("交易日不足：%v（%d ~ %d）", err, from, latest)
	}
	// 取最后 3 个交易日做比对
	dates = dates[len(dates)-3:]

	// ① 旧路径：不带缓存，逐日直接请求
	plain := NewScoringService(svc.stock)
	// ② 新路径：带区间缓存
	cached := NewScoringService(svc.stock)
	cached.override = newRangeCachingProvider(
		provider, scoreWindowStart(dates[0], scoreLookbackTradingDays), latest)

	// ③ **对照组**：同一路径（都不带缓存）跑两次。
	//
	// 没有这个对照就没法判断"不一致"是我引入的还是本来就有 ——
	// 实测 `IndustryReturn20D` 这类字段存在浮点末位抖动，
	// 只有跟"自己 vs 自己"比过才知道基线在哪。
	repeat := NewScoringService(svc.stock)

	for _, date := range dates {
		before, err := plain.ScoreStocks(codesList, date, 0, score.StrategyTrendGrowth)
		if err != nil {
			t.Fatalf("%d 旧路径失败：%v", date, err)
		}
		after, err := cached.ScoreStocks(codesList, date, 0, score.StrategyTrendGrowth)
		if err != nil {
			t.Fatalf("%d 新路径失败：%v", date, err)
		}

		if len(before.Rows) != len(after.Rows) {
			t.Fatalf("%d 评分行数不同：旧 %d，新 %d",
				date, len(before.Rows), len(after.Rows))
		}
		// ⚠ **按代码比对，不能按下标。**
		//
		// `loadScoreInputs` 是并发 append 的，行顺序取决于 goroutine 调度 ——
		// 实测同一份代码连跑两次，首行代码都不一样（000008/000011 → 000010/000002）。
		// 按下标比会把"顺序抖动"误报成"结果不一致"。
		beforeByCode := map[string]score.Row{}
		for _, row := range before.Rows {
			beforeByCode[row.Code] = row
		}
		for _, b := range after.Rows {
			a, ok := beforeByCode[b.Code]
			if !ok {
				t.Errorf("%d 新路径多出代码 %s", date, b.Code)
				continue
			}
			if diff := compareScoreRow(a, b); diff != "" {
				t.Errorf("%d %s 评分不一致：%s", date, b.Code, diff)
			}
		}
		t.Logf("%d：%d 行，旧/新逐字段一致", date, len(before.Rows))

		// 对照：同一路径再跑一次，看基线抖动有多大
		again, err := repeat.ScoreStocks(codesList, date, 0, score.StrategyTrendGrowth)
		if err != nil {
			t.Fatalf("%d 对照失败：%v", date, err)
		}
		againByCode := map[string]score.Row{}
		for _, row := range again.Rows {
			againByCode[row.Code] = row
		}
		baseDiff := 0
		for _, a := range before.Rows {
			if c, ok := againByCode[a.Code]; ok && compareScoreRow(a, c) != "" {
				baseDiff++
			}
		}
		t.Logf("  对照组（旧 vs 旧再跑一次）：%d/%d 行有差异", baseDiff, len(before.Rows))
	}
}

// 评分必须是**可复现的**：同一交易日跑两次，行顺序与数值都要一致。
//
// 修这个是因为 `loadScoreInputs` 并发 append，顺序随调度变化；
// 而行业收益靠累加得出（浮点加法不满足结合律），
// 且候选池要做 `head()` 截断 —— 顺序抖动会让调仓建议在边界上换人。
func TestScoringIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	codesList, _, err := svc.scoringUniverse(mustStore(t, svc), 60)
	if err != nil || len(codesList) == 0 {
		t.Skipf("拿不到代码：%v", err)
	}
	latest, err := provider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}

	scoring := NewScoringService(svc.stock)
	first, err := scoring.ScoreStocks(codesList, latest, 0, score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("首次评分失败：%v", err)
	}
	second, err := scoring.ScoreStocks(codesList, latest, 0, score.StrategyTrendGrowth)
	if err != nil {
		t.Fatalf("二次评分失败：%v", err)
	}
	if len(first.Rows) != len(second.Rows) {
		t.Fatalf("两次行数不同：%d vs %d", len(first.Rows), len(second.Rows))
	}
	for i := range first.Rows {
		a, b := first.Rows[i], second.Rows[i]
		if a.Code != b.Code {
			t.Fatalf("第 %d 行代码不同（顺序不稳定）：%s vs %s", i, a.Code, b.Code)
		}
		if diff := compareScoreRow(a, b); diff != "" {
			t.Errorf("第 %d 行 %s 数值不稳定：%s", i, a.Code, diff)
		}
	}
	t.Logf("跑两次：%d 行，顺序与数值完全一致", len(first.Rows))
}

// compareScoreRow 逐字段比对两行评分，返回第一处差异（空串表示一致）。
func compareScoreRow(a, b score.Row) string {
	type field struct {
		name string
		x, y float64
	}
	fields := []field{
		{"TotalScore", a.TotalScore, b.TotalScore},
		{"TrendScore", a.TrendScore, b.TrendScore},
		{"StructureScore", a.StructureScore, b.StructureScore},
		{"RSScore", a.RSScore, b.RSScore},
		{"CapitalScore", a.CapitalScore, b.CapitalScore},
		{"RepairScore", a.RepairScore, b.RepairScore},
		{"Close", a.Close, b.Close},
		{"High", a.High, b.High},
		{"Low", a.Low, b.Low},
		{"Open", a.Open, b.Open},
		{"Ma5", a.Ma5, b.Ma5},
		{"Ma20", a.Ma20, b.Ma20},
		{"Ma30", a.Ma30, b.Ma30},
		{"Ma60", a.Ma60, b.Ma60},
		{"Ma120", a.Ma120, b.Ma120},
		{"Ma250", a.Ma250, b.Ma250},
		{"Ma5Prev", a.Ma5Prev, b.Ma5Prev},
		{"Ma250Prev", a.Ma250Prev, b.Ma250Prev},
		{"VolumeRatio520", a.VolumeRatio520, b.VolumeRatio520},
		{"DrawdownFromHigh20", a.DrawdownFromHigh20, b.DrawdownFromHigh20},
		{"DrawdownFromHigh120", a.DrawdownFromHigh120, b.DrawdownFromHigh120},
		{"RelativeStrength20D", a.RelativeStrength20D, b.RelativeStrength20D},
		{"StockReturn20D", a.StockReturn20D, b.StockReturn20D},
		{"IndustryReturn20D", a.IndustryReturn20D, b.IndustryReturn20D},
		{"BenchmarkReturn20D", a.BenchmarkReturn20D, b.BenchmarkReturn20D},
		{"Amount", a.Amount, b.Amount},
	}
	for _, f := range fields {
		if f.x != f.y {
			return fmt.Sprintf("%s 旧=%.10f 新=%.10f", f.name, f.x, f.y)
		}
	}
	if a.Market != b.Market {
		return fmt.Sprintf("Market 旧=%s 新=%s", a.Market, b.Market)
	}
	return ""
}

// 区间切片必须与真实请求返回**同一批行**（含边界、保序）。
func TestRangeCacheMatchesDirectFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	provider, _ := svc.stock.snapshot()
	if provider == nil {
		t.Skip("数据源未就绪")
	}
	codesList, _, err := svc.scoringUniverse(mustStore(t, svc), 3)
	if err != nil || len(codesList) == 0 {
		t.Skipf("拿不到代码：%v", err)
	}
	latest, err := provider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}

	from := scoreWindowStart(latest-60, scoreLookbackTradingDays)
	cache := newRangeCachingProvider(provider, from, latest)

	for _, code := range codesList {
		// 先用缓存（会触发一次整窗口取数）
		_, err := cache.DailyHistory(context.Background(), code, from, latest)
		if err != nil {
			t.Fatalf("%s 缓存取数失败：%v", code, err)
		}
		// 再用两个不同的窄区间，分别与真实请求比对
		for _, win := range [][2]int{{latest - 20, latest}, {latest - 45, latest - 5}} {
			direct, err := provider.DailyHistory(context.Background(), code, win[0], win[1])
			if err != nil {
				t.Fatalf("%s 真实请求失败：%v", code, err)
			}
			viaCache, err := cache.DailyHistory(context.Background(), code, win[0], win[1])
			if err != nil {
				t.Fatalf("%s 缓存请求失败：%v", code, err)
			}
			if len(direct) != len(viaCache) {
				t.Fatalf("%s [%d,%d] 行数不同：真实 %d，缓存 %d",
					code, win[0], win[1], len(direct), len(viaCache))
			}
			for i := range direct {
				if direct[i].Date != viaCache[i].Date || direct[i].Close != viaCache[i].Close ||
					direct[i].Open != viaCache[i].Open || direct[i].High != viaCache[i].High ||
					direct[i].Low != viaCache[i].Low || direct[i].Volume != viaCache[i].Volume {
					t.Errorf("%s [%d,%d] 第 %d 行不同：真实 %+v，缓存 %+v",
						code, win[0], win[1], i, direct[i], viaCache[i])
				}
			}
		}
		t.Logf("%s：缓存与真实请求在两个区间上逐行一致（缓存 %d 只）",
			code, cache.cachedCodes())
	}
}
