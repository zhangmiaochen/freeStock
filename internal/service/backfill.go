package service

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 历史回填：批量给一个日期区间内的**每个交易日**打分并落库。
//
// 原实现 `portfolio_service.py:690-740` 的 `refresh_backfill(start_date, end_date)`。
// 原版的 `refresh_latest()` 其实就是 `refresh_backfill(latest, latest)`。
//
// 这是**回测的前提** —— 回测要在历史每个交易日重放决策，
// 就必须先有那一天的评分表。

// BackfillResult 是一次历史回填的结果。
type BackfillResult struct {
	StrategyID string `json:"strategyId"`
	StartDate  int    `json:"startDate"`
	EndDate    int    `json:"endDate"`
	// Days 是区间内的交易日数，Scored 是实际成功的天数
	Days   int   `json:"days"`
	Scored int   `json:"scored"`
	Dates  []int `json:"dates"`
	// Skipped 是跳过的日期及原因（数据源没有那天、或该天无有效行情）
	Skipped []string `json:"skipped"`
	// PerDay 是每天的评分条数（按日期升序）
	PerDay  map[string]int `json:"perDay"`
	Elapsed string         `json:"elapsed"`
	// Reused 是本次**复用已有评分、没有重算**的 (交易日 × 代码) 数
	Reused int `json:"reused"`
	// ScoredFromCache 是完全没动、直接跳过的交易日数
	ScoredFromCache int `json:"scoredFromCache"`
	// Total 是本次实际需要计算的 (交易日 × 代码) 数
	Total int `json:"total"`
}

// RefreshBackfill 批量给 [startDate, endDate] 区间内每个交易日打分。
//
// ⚠ **与原实现有两处结构性差异（都是我加的，原版没有）**：
//
//  1. **`limit` 参数原版不存在**。原实现是
//     `refresh_backfill(start_date, end_date)`（portfolio_service.py:685）——
//     **永远全市场**，没有"回填股票数"这种东西。
//
//     这个参数是本次移植时为了"保护慢数据源"加的，**但它是用户那个
//     「回测零成交」问题的直接成因**：广度闸门判的是"全市场评分≥70 的只数"，
//     只回填 1000 只会让这个数被系统性低估，闸门恒判"广度不足"并删光买入信号。
//     UI 已把默认值改成 0（全市场）并对覆盖不足 50% 的情况显式告警。
//
//  2. **原版一次刷所有"活跃策略"**（`refresh_latest` → `_active_strategy_ids()`：
//     取所有**非 archived** 组合用到的策略去重），我这里是一次一个策略。
//     对多个组合用不同策略的场景，原版一次就能全部刷新，我需要逐个调用。
//
// 保留 `limit` 的理由：写测试/快速验证时确实需要小样本。
// 但**生产路径应当传 0**。
//
// limit 语义：
//
//	limit <= 0 → **全市场**（所有有行情的股票）
//	limit >  0 → 只评股票列表前 limit 只
//
// **回测必须用全市场**：只用前 1000 只的话，候选池与实盘建议不是一回事，
// 回测出来的组合也不代表策略的真实表现。
//
// force=false 时**增量**：已经算过的 (交易日, 代码) 不再重算 ——
// 重跑一个已经填过的区间应当在几秒内结束，而不是又花几分钟。
func (s *PortfolioService) RefreshBackfill(
	strategyID string, startDate, endDate, limit int, force bool,
) (*BackfillResult, error) {
	store, err := s.db()
	if err != nil {
		return nil, err
	}
	if strategyID == "" {
		strategyID = score.StrategyTrendGrowth
	}
	if startDate <= 0 || endDate <= 0 || startDate > endDate {
		return nil, fmt.Errorf("回测区间格式错误，应为 YYYYMMDD 且开始日不能晚于结束日")
	}

	began := time.Now()
	dates, err := s.tradingDates(startDate, endDate)
	if err != nil {
		return nil, err
	}
	datesElapsed := time.Since(began)
	if len(dates) == 0 {
		return nil, fmt.Errorf("区间内没有交易日（%d ~ %d）", startDate, endDate)
	}

	// 代码集合每天相同：一次算好，避免逐日重复取列表
	universeBegan := time.Now()
	codes, heldCount, err := s.scoringUniverse(store, limit)
	if err != nil {
		return nil, err
	}
	universeElapsed := time.Since(universeBegan)
	if len(codes) == 0 {
		return nil, fmt.Errorf("没有可评分的股票")
	}
	slog.Info("历史回填开始",
		"strategy", strategyID, "from", startDate, "to", endDate,
		"tradingDays", len(dates), "codes", len(codes), "heldIncluded", heldCount)
	slog.Info("回填阶段耗时（取数前）",
		"tradingDates", datesElapsed.Round(time.Millisecond),
		"scoringUniverse", universeElapsed.Round(time.Millisecond))

	// **同一只股票只取一次日线。**
	//
	// 原本每天都要重新拉一遍 320 个交易日的窗口，于是
	// 回填 N 天 = N × 股票数 次请求（同一只股票的历史被重复拉 N 次）。
	// 这里把每只股票在 [最早的评分窗口起点, 区间终点] 的日线缓存起来，
	// 逐日评分时从缓存按区间切片 —— 结果与真实请求一致（见 provider_cache.go）。
	scoring := NewScoringService(s.stock)
	var cacheHits, cacheMisses, cacheFetches int
	var cacheRef *rangeCachingProvider
	if provider, _ := s.stock.snapshot(); provider != nil && startDate > 0 {
		windowStart := scoreWindowStart(startDate, scoreLookbackTradingDays)
		cache := newRangeCachingProvider(provider, windowStart, endDate)
		scoring.override = cache
		// 留给紧接着的回测复用 —— 回测要的价格是这个窗口的子集
		s.setBackfillCache(provider, cache)
		cacheRef = cache
		slog.Info("回填启用日线缓存",
			"windowStart", windowStart, "windowEnd", endDate,
			"before", fmt.Sprintf("%d 次/只", len(dates)), "after", "1 次/只")
	}

	out := &BackfillResult{
		StrategyID: strategyID, StartDate: startDate, EndDate: endDate,
		Days: len(dates), Dates: dates, PerDay: map[string]int{},
	}
	var scoreTotal, saveTotal time.Duration

	for _, date := range dates {
		// **增量**：已经算过的 (交易日, 代码) 不再重算。
		//
		// 和单日刷新同一个道理 —— 评分只取决于 (代码, 交易日, 策略)，
		// 重算值完全相同。重跑一个已经填过的区间应当是**近乎瞬间**的事，
		// 而不是又花几分钟。
		todo := codes
		if !force {
			existing, err := store.StrengthCodes(int64(date), strategyID)
			if err != nil {
				// 读不到不该让回填失败 —— 退化为该日全量重算
				slog.Warn("读取已有评分失败，该日全量重算", "date", date, "err", err)
			} else if len(existing) > 0 {
				missing := make([]string, 0, len(codes))
				for _, code := range codes {
					if existing[portfolio.BareCode(code)] {
						continue
					}
					missing = append(missing, code)
				}
				out.Reused += len(codes) - len(missing)
				todo = missing
			}
		}
		if len(todo) == 0 {
			// 该日已全部算过 —— 一次行情都不拉
			out.ScoredFromCache++
			continue
		}

		// 逐日调用：ScoreStocks 会按 tradeDate 取"截止该日"的窗口
		scoreBegan := time.Now()
		result, err := scoring.ScoreStocks(todo, date, 0, strategyID)
		scoreTotal += time.Since(scoreBegan)
		if err != nil {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%d：%v", date, err))
			continue
		}
		if len(result.Rows) == 0 {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%d：该日无有效行情", date))
			continue
		}
		saveBegan := time.Now()
		if err := s.saveScoringResult(store, result, strategyID); err != nil {
			saveTotal += time.Since(saveBegan)
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%d：落库失败 %v", date, err))
			continue
		}
		saveTotal += time.Since(saveBegan)
		out.Scored++
		out.PerDay[fmt.Sprintf("%d", date)] = len(result.Rows)
	}

	out.Elapsed = time.Since(began).Round(time.Millisecond).String()
	if cacheRef != nil {
		cacheHits, cacheMisses, cacheFetches = cacheRef.stats()
	}
	slog.Info("历史回填阶段耗时",
		"tradingDates", datesElapsed.Round(time.Millisecond),
		"scoringUniverse", universeElapsed.Round(time.Millisecond),
		"评分(取数+指标)", scoreTotal.Round(time.Millisecond),
		"缓存命中", cacheHits, "未命中", cacheMisses, "真实取数", cacheFetches,
		"落库", saveTotal.Round(time.Millisecond),
		"合计", out.Elapsed)
	slog.Info("历史回填完成",
		"strategy", strategyID, "scored", out.Scored,
		"tradingDays", out.Days, "skipped", len(out.Skipped), "elapsed", out.Elapsed)
	return out, nil
}

// tradingDates 取区间内的交易日（YYYYMMDD 升序）。
//
// 用**沪深300指数日线**的日期作为交易日历 —— 指数在每个 A 股交易日都有数据，
// 与数据湖 `trading_calendar` 等价，而这条路径（loadBenchmark）已经验证可用。
func (s *PortfolioService) tradingDates(startDate, endDate int) ([]int, error) {
	if s.stock == nil {
		return nil, fmt.Errorf("缺少股票服务依赖")
	}
	closes, err := s.stock.loadBenchmark(score.LowBuyIndexBenchmarkCode, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("取交易日历失败：%w", err)
	}
	out := make([]int, 0, len(closes))
	for date := range closes {
		if date >= int64(startDate) && date <= int64(endDate) {
			out = append(out, int(date))
		}
	}
	sort.Ints(out)
	return out, nil
}
