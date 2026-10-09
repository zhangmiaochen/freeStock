package service

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

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
}

// RefreshBackfill 批量给 [startDate, endDate] 区间内每个交易日打分。
//
// limit 与 RefreshStrength 同义（>0 时只评股票列表前 limit 只，保护数据源）。
// **回测时建议传足够大的 limit**（如 1000+），否则候选池太小、
// 回测出来的组合和实盘建议不是一回事。
func (s *PortfolioService) RefreshBackfill(
	strategyID string, startDate, endDate, limit int,
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
	if len(dates) == 0 {
		return nil, fmt.Errorf("区间内没有交易日（%d ~ %d）", startDate, endDate)
	}

	// 代码集合每天相同：一次算好，避免逐日重复取列表
	codes, heldCount, err := s.scoringUniverse(store, limit)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("没有可评分的股票")
	}
	slog.Info("历史回填开始",
		"strategy", strategyID, "from", startDate, "to", endDate,
		"tradingDays", len(dates), "codes", len(codes), "heldIncluded", heldCount)

	scoring := NewScoringService(s.stock)
	out := &BackfillResult{
		StrategyID: strategyID, StartDate: startDate, EndDate: endDate,
		Days: len(dates), Dates: dates, PerDay: map[string]int{},
	}

	for _, date := range dates {
		// 逐日调用：ScoreStocks 会按 tradeDate 取"截止该日"的窗口
		result, err := scoring.ScoreStocks(codes, date, 0, strategyID)
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
		if err := s.saveScoringResult(store, result, strategyID); err != nil {
			out.Skipped = append(out.Skipped,
				fmt.Sprintf("%d：落库失败 %v", date, err))
			continue
		}
		out.Scored++
		out.PerDay[fmt.Sprintf("%d", date)] = len(result.Rows)
	}

	out.Elapsed = time.Since(began).Round(time.Millisecond).String()
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
