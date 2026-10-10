package service

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 回填逐日成本拆解 —— 回答一个具体问题：
//
//	「逐日重算指标」（day2 的 320 天窗口里有 319 天与 day1 重叠）到底占多少时间？
//
// 背景：上一步已经省掉了**重复物化**（每只股票整窗口只取一次日线、按区间零拷贝切片，
// 见 provider_cache.go）。剩下的可合并项理论上只有"指标窗口重叠"这一块：
//
//	days × window  ->  window + days
//
// 但那只是**指标计算**的上限，不是回填总耗时的上限。这个用例把逐日成本
// 按阶段拆开量一遍，才能判断值不值得为它引入增量指标（那是高风险改动）。
//
// 用 FREESTOCK_PROFILE=1 开启；规模用环境变量控制：
//
//	FREESTOCK_PROFILE_CODES=2000   参与评分的股票数
//	FREESTOCK_PROFILE_DAYS=8       回填的交易日数
//
// ⚠ **第一天与其余天必须分开看**：第一天要把整窗口的日线真取一遍
// （阶段 0，一次性），其余天全部命中缓存。240 天的回填里只有 1 天是冷的，
// 所以**稳定态（warm）的单日成本才决定总耗时**。
func TestBackfillStageProfile(t *testing.T) {
	if os.Getenv("FREESTOCK_PROFILE") != "1" {
		t.Skip("阶段拆解（会打数据源，几十秒）：设 FREESTOCK_PROFILE=1 才跑")
	}
	codeCount := envIntOr("FREESTOCK_PROFILE_CODES", 2000)
	dayCount := envIntOr("FREESTOCK_PROFILE_DAYS", 8)

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
	latest, err := provider.LatestTradeDate(context.Background())
	if err != nil || latest <= 0 {
		t.Skipf("拿不到最新交易日：%v", err)
	}
	// 多取一些自然日，保证够 dayCount 个交易日（含周末与节假日）
	dates, err := svc.tradingDates(shiftDays(t, latest, -dayCount*3-10), latest)
	if err != nil || len(dates) < 2 {
		t.Skipf("交易日不足：%v", err)
	}
	if len(dates) > dayCount {
		dates = dates[len(dates)-dayCount:]
	}
	start, end := dates[0], dates[len(dates)-1]

	t.Logf("规模：%d 只 × %d 个交易日（%d ~ %d）", len(codes), len(dates), start, end)

	// ---------------------------------------------------------------
	// ⓪ 真实路径：直接跑 RefreshBackfill，看它自己报的合计
	// ---------------------------------------------------------------
	began := time.Now()
	bf, err := svc.RefreshBackfill(portfolio.StrategyTrendGrowth, start, end, codeCount, true)
	realElapsed := time.Since(began)
	if err != nil {
		t.Skipf("真实回填失败：%v", err)
	}
	if bf.Scored == 0 {
		t.Skipf("真实回填没出数据：%v", bf.Skipped)
	}
	t.Logf("⓪ RefreshBackfill（真实路径，%d 天 × %d 只）：%s（%.2f ms/只·天）",
		bf.Days, len(codes), realElapsed.Round(time.Millisecond),
		float64(realElapsed.Milliseconds())/float64(bf.Days*len(codes)))

	// 复用回填刚建立的区间缓存 —— 后面所有阶段都在**热缓存**上量
	cache := svc.backfillCacheFor(provider, start, end)
	if cache == nil {
		t.Skip("拿不到回填建立的区间缓存")
	}
	if os.Getenv("FREESTOCK_PROFILE_STAGES") == "0" {
		// 只量真实路径（阶段拆解本身会让指标/分位多跑一遍，
		// 做 CPU profile 时要关掉，否则那些函数的占比被放大一倍）
		return
	}

	// ---------------------------------------------------------------
	// 逐日：各阶段单独计时（每一天各阶段各量一遍）
	// ---------------------------------------------------------------
	type dayRow struct {
		date                                                       int
		slice, ind, prev, repair, cross, state, bench, board, full time.Duration
	}
	rows := make([]dayRow, 0, len(dates))
	saveTotal := time.Duration(0)
	ctx := context.Background()

	scoring := NewScoringService(svc.stock)
	scoring.override = cache

	for _, date := range dates {
		win := scoreWindowStart(date, scoreLookbackTradingDays)
		var row dayRow
		row.date = date

		// ① 按区间切片（合并物化之后，这一步应当几乎不花钱）
		type item struct {
			code string
			bars []score.Bar
		}
		items := make([]item, 0, len(codes))
		began = time.Now()
		for _, code := range codes {
			bars, err := cache.DailyBars(ctx, code, win, date)
			// 与 loadScoreInputs 同样的门槛（少于 30 根算不出有意义的指标）
			if err != nil || len(bars) < 30 {
				continue
			}
			items = append(items, item{code: code, bars: bars})
		}
		row.slice = time.Since(began)

		// ② 指标（"逐日重算"的主体）
		began = time.Now()
		inds := make([]score.Indicators, len(items))
		for i, it := range items {
			if ind, ok := score.ComputeIndicators(it.bars); ok {
				inds[i] = ind
			}
		}
		row.ind = time.Since(began)

		// ③ 前 60 日最高价（同样每天重算整窗口）
		began = time.Now()
		prevs := make([]float64, len(items))
		for i, it := range items {
			prevs[i] = score.PrevHigh60(it.bars)
		}
		row.prev = time.Since(began)

		// ④ 修复能力（O(n²)：每个位置都 rollingMax(120)）
		began = time.Now()
		repairs := make([]score.RepairFeatures, len(items))
		for i, it := range items {
			repairs[i] = score.BuildRepairFeatures(it.bars)
		}
		row.repair = time.Since(began)

		// ⑤ 横截面分位 + 评分公式（与 ScoreStocks 里那段等价；分位用查询器）
		began = time.Now()
		amounts := make([]float64, 0, len(items))
		for i := range items {
			amounts = append(amounts, items[i].bars[len(items[i].bars)-1].Amount)
		}
		amountRanks := score.NewRankLookup(amounts)
		turnRanks := score.NewRankLookup(amounts)
		industryRanks := score.NewRankLookup(amounts)
		for i := range items {
			ind := inds[i]
			ind.AmountRankPct = amountRanks.Pct(amounts[i])
			ind.TurnRankPct = turnRanks.Pct(amounts[i])
			ind.IndustryStrengthPct = industryRanks.Pct(amounts[i])
			_ = score.ComputeStrategyScores(score.StrategyTrendGrowth, score.StrategyScoreInput{
				Indicators: ind, Repair: repairs[i],
				NearRepair: score.NearTermRepairScore(ind, prevs[i]),
				PrevHigh60: prevs[i],
			})
		}
		row.cross = time.Since(began)

		// ⑥ 市场状态（**每天固定抽样 400 只 × 85 天**，与评分只数无关）
		began = time.Now()
		_ = scoring.marketState(cache, date)
		row.state = time.Since(began)

		// ⑦ 基准 20 日收益
		began = time.Now()
		_ = scoring.benchmarkReturn20D(cache, date)
		row.bench = time.Since(began)

		// ⑧ 板块归属（遍历全部板块成分）
		began = time.Now()
		_, _ = svc.stock.boardsForCodes(codes)
		row.board = time.Since(began)

		// ⑨ 完整 ScoreStocks —— 真实路径（②~⑦ 都在里面）
		began = time.Now()
		result, err := scoring.ScoreStocks(codes, date, 0, score.StrategyTrendGrowth)
		row.full = time.Since(began)
		if err != nil || result == nil {
			rows = append(rows, row)
			continue
		}

		// ⑩ 落库（先删后插，一天一个事务）
		began = time.Now()
		_ = svc.saveScoringResult(store, result, score.StrategyTrendGrowth)
		saveTotal += time.Since(began)

		rows = append(rows, row)
	}

	// ---------------------------------------------------------------
	// 报表：逐日 + 冷/热均值
	// ---------------------------------------------------------------
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	t.Logf("%-10s %7s %7s %7s %7s %7s %8s %8s %7s %8s %8s",
		"日期", "切片", "指标", "前高", "修复", "横截面", "市场状态", "基准", "板块", "落库", "Score合计")
	for _, r := range rows {
		t.Logf("%-10d %7s %7s %7s %7s %7s %8s %8s %7s %8s %8s",
			r.date, ms(r.slice), ms(r.ind), ms(r.prev), ms(r.repair), ms(r.cross),
			ms(r.state), ms(r.bench), ms(r.board), ms(saveTotal/time.Duration(len(rows))), ms(r.full))
	}

	avg := func(pick func(dayRow) time.Duration, skipFirst bool) time.Duration {
		from := 0
		if skipFirst && len(rows) > 1 {
			from = 1
		}
		if from >= len(rows) {
			return 0
		}
		var sum time.Duration
		for _, r := range rows[from:] {
			sum += pick(r)
		}
		return sum / time.Duration(len(rows)-from)
	}

	warm := func(name string, pick func(dayRow) time.Duration) {
		t.Logf("  %-22s 冷 %8s   热 %8s", name,
			avg(pick, false).Round(time.Millisecond), avg(pick, true).Round(time.Millisecond))
	}
	t.Logf("各阶段单日耗时（冷=含首日取数，热=其余天）：")
	warm("① 区间切片", func(r dayRow) time.Duration { return r.slice })
	warm("② 指标 ComputeIndicators", func(r dayRow) time.Duration { return r.ind })
	warm("③ 前高 PrevHigh60", func(r dayRow) time.Duration { return r.prev })
	warm("④ 修复 BuildRepairFeatures", func(r dayRow) time.Duration { return r.repair })
	warm("⑤ 横截面 + 评分公式", func(r dayRow) time.Duration { return r.cross })
	warm("⑥ 市场状态（400 只抽样）", func(r dayRow) time.Duration { return r.state })
	warm("⑦ 基准 20 日收益", func(r dayRow) time.Duration { return r.bench })
	warm("⑧ 板块归属", func(r dayRow) time.Duration { return r.board })
	warm("⑨ ScoreStocks 合计", func(r dayRow) time.Duration { return r.full })
	t.Logf("  %-22s 热 %8s（⑩ 落库，天/事务）", "⑩ saveScoringResult",
		(saveTotal / time.Duration(len(rows))).Round(time.Millisecond))

	// 「逐日重算指标」= ②+③+④ —— 增量指标最多只能省掉这一块
	recompute := func(r dayRow) time.Duration { return r.ind + r.prev + r.repair }
	total := func(r dayRow) time.Duration {
		return r.full + saveTotal/time.Duration(len(rows))
	}
	recomputeWarm := avg(recompute, true)
	totalWarm := avg(total, true)
	t.Logf("「逐日重算指标」②+③+④ 热态合计 %s/天；占 ScoreStocks 热态 %s 的 %.1f%%，占单日合计 %s 的 %.1f%%",
		recomputeWarm.Round(time.Millisecond), avg(func(r dayRow) time.Duration { return r.full }, true).Round(time.Millisecond),
		ratio(recomputeWarm, avg(func(r dayRow) time.Duration { return r.full }, true)),
		totalWarm.Round(time.Millisecond), ratio(recomputeWarm, totalWarm))
	t.Logf("→ 把「逐日重算指标」**完全消灭**（不可能），稳定态单日最多降 %.1f%%",
		ratio(recomputeWarm, totalWarm))
}

// envIntOr 读一个整型环境变量，缺失或非法时用默认值。
func envIntOr(key string, def int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return def
}

func ratio(part, whole time.Duration) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
