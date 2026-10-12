package service

import (
	"sort"
	"strconv"
	"time"

	"freestock/internal/backtest"
	"freestock/internal/portfolio"
	"freestock/internal/score"
)

// 把 `signal_history` 里的信号按原实现的 merge_asof 语义贴到评分行上。
//
// 对应 `portfolio_service._attach_signal_candidates`（:1490-1608）在**单日**
// 上的用法（:2469）：
//
//	score_frame = self._attach_signal_candidates(
//	    self._scope_filter(self._strategy_strength_slice(latest_scores, portfolio), portfolio),
//	    trade_date, trade_date)
//
// 几个必须照抄的细节：
//   - 信号回看窗口的起点是 `_buffer_start_trade_date(trade_date, 30)`，
//     即**往前 30 个交易日**（不是 30 个自然日）；
//   - 日历只用 `[signal_start, trade_date]` 这一段，`trade_idx` 是**段内序号**；
//   - tolerance 是 `30-1 = 29`（见 `score.SignalCandidateLookbackTradeDays`）；
//   - attach **可能让行数变少**（交易日不在日历里的行被丢弃），所以调用方
//     必须用返回值，不能假设长度不变。

// signalLookbackTradeDays 对应 `SIGNAL_CANDIDATE_LOOKBACK_TRADE_DAYS`。
const signalLookbackTradeDays = 30

// signalRoughLookbackDays 对应 `_buffer_start_trade_date` 里
// `timedelta(days=lookback_open_days * 3)` 的那个 ×3 自然日粗估。
const signalRoughLookbackDays = 3

// signalBufferStart 对应 `_buffer_start_trade_date(start, 30)`。
//
// 取 [start - 90 自然日, start] 的开市日，返回倒数第 `max(30, lookback) + 1` 个。
// 取不到日历时退回"粗估的自然日"（与原实现的 `return rough_start` 一致）。
func (s *PortfolioService) signalBufferStart(tradeDate int, lookback int) int {
	if tradeDate <= 0 {
		return tradeDate
	}
	if lookback <= 0 {
		lookback = signalLookbackTradeDays
	}
	step := lookback
	if step < signalLookbackTradeDays {
		step = signalLookbackTradeDays
	}
	anchor, err := time.Parse("20060102", strconv.Itoa(tradeDate))
	if err != nil {
		return tradeDate
	}
	rough := anchor.AddDate(0, 0, -step*signalRoughLookbackDays)
	roughDate, convErr := strconv.Atoi(rough.Format("20060102"))
	if convErr != nil || roughDate <= 0 {
		return tradeDate
	}
	days, err := s.tradingDates(roughDate, tradeDate)
	if err != nil || len(days) == 0 {
		return roughDate
	}
	position := len(days) - 1 - step
	if position < 0 {
		position = 0
	}
	return days[position]
}

// attachSignalsToRows 对应单日用法下的 `_attach_signal_candidates`。
//
// `stored` 是 `signal_history` 的读取器（`*portfolio.Store`）；`rows` 与 `dates`
// 一一对应。返回的是**存活下来的行**（可能比入参少）。
func (s *PortfolioService) attachSignalsToRows(
	stored *portfolio.Store, rows []score.Row, dates []int64,
) []score.Row {
	if stored == nil || len(rows) == 0 || len(dates) != len(rows) {
		return rows
	}
	// 单日用法：起止都取该行的交易日。多日（回测）用法下取最小/最大值，
	// 与原实现 `min_trade_date / max_trade_date` 一致。
	minDate, maxDate := dates[0], dates[0]
	for _, date := range dates {
		if date < minDate {
			minDate = date
		}
		if date > maxDate {
			maxDate = date
		}
	}
	if minDate <= 0 || maxDate <= 0 {
		return rows
	}
	signalStart := s.signalBufferStart(int(minDate), signalLookbackTradeDays)
	if signalStart <= 0 {
		return rows
	}
	stored2, err := stored.LoadSignalCandidates(int64(signalStart), maxDate)
	if err != nil {
		// 读不到信号不该让整个计划失败：原实现此时会走"全部默认值"的分支
		//（`signal_df.empty` → active_signal_* 全空、signal_age=999）。
		return rows
	}
	days, err := s.tradingDates(signalStart, int(maxDate))
	if err != nil || len(days) == 0 {
		// 没有日历就无法算 trade_idx；原实现在这里会退回"并集排序"，
		// 那需要帧里的日期集合，这里直接用行日期去重排序等价。
		days = calendarFromDates(dates)
		if len(days) == 0 {
			return rows
		}
	}
	calendar := make([]int64, 0, len(days))
	for _, day := range days {
		calendar = append(calendar, int64(day))
	}
	candidates := make([]score.SignalCandidateRow, 0, len(stored2))
	for _, item := range stored2 {
		candidates = append(candidates, score.SignalCandidateRow{
			TradeDate: item.TradeDate, Code: item.Code,
			ModelID: item.ModelID, ModelName: item.ModelName,
			MarketState: item.MarketState, MarketStateLabel: item.MarketStateLabel,
			SignalType: item.SignalType, SignalLevel: item.SignalLevel,
			SignalLevelLabel: item.SignalLevelLabel, Score: item.Score,
			SignalCount: item.SignalCount,
		})
	}
	attached, _ := score.AttachSignalCandidates(rows, dates, candidates, calendar, score.SignalAttachOptions{})
	return attached
}

// attachSignalsToDays 按**整个区间**给回测的评分日贴一次信号。
//
// 对应原版回测路径上的 `_attach_signal_candidates(scope_history, start, end)`
// （portfolio_service.py:1004）—— 它是对**整段帧**做一次 merge_asof，
// 而不是逐日做。这里同样只查两次库（候选 + 日历）：
// `attachSignalsToRows` 内部按 min/max 日期一次性取候选，逐行匹配。
//
// 行会按日重新分组（attach 可能丢弃不在交易日历里的行），所以返回新的切片，
// 且**可能比入参少行** —— 调用方必须用返回值。
func (s *PortfolioService) attachSignalsToDays(
	stored *portfolio.Store, days []backtest.StrengthDay,
) []backtest.StrengthDay {
	if stored == nil || len(days) == 0 {
		return days
	}
	total := 0
	for _, day := range days {
		total += len(day.Rows)
	}
	if total == 0 {
		return days
	}
	rows := make([]score.Row, 0, total)
	dates := make([]int64, 0, total)
	for _, day := range days {
		for _, row := range day.Rows {
			rows = append(rows, row)
			dates = append(dates, row.TradeDate)
		}
	}
	attached := s.attachSignalsToRows(stored, rows, dates)

	// 按日回填。用日期索引而不是位置索引 —— attach 之后行序与行数都可能变，
	// 按位置切会**把行错配到别的交易日**（那是静默的错误信号年龄）。
	byDate := make(map[int64][]score.Row, len(days))
	for _, row := range attached {
		byDate[row.TradeDate] = append(byDate[row.TradeDate], row)
	}
	out := make([]backtest.StrengthDay, 0, len(days))
	for _, day := range days {
		day.Rows = byDate[int64(atoiDate(day.Date))]
		out = append(out, day)
	}
	return out
}

// calendarFromDates 是 `_calendar_dates` 取不到日历时的退路：
// 用帧里出现过的日期去重排序（对应原实现
// `sorted(set(work.trade_date) | set(signal_df.trade_date))` 的等价形式）。
func calendarFromDates(dates []int64) []int {
	seen := map[int64]bool{}
	out := make([]int, 0, len(dates))
	for _, date := range dates {
		if date <= 0 || seen[date] {
			continue
		}
		seen[date] = true
		out = append(out, int(date))
	}
	sort.Ints(out)
	return out
}
