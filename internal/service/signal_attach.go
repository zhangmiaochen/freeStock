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

// attachSignalsToDays 按区间给回测的评分日贴信号。
//
// 对应原版回测路径上的 `_attach_signal_candidates(scope_history, start, end)`
// （portfolio_service.py:1004）—— 语义上是对**整段帧**做一次 merge_asof。
//
// ⚠ **实现上必须逐日贴，不能先把整个区间的行摊平。**
//
// 我第一版就是摊平的：把 970 天 × 7000 行 ≈ **680 万行** `score.Row` 复制进
// 一个新切片（`score.Row` 很大，这一份就是 GB 级），再 `byDate` 分组回填、
// 期间 attach 又返回一份新切片 —— 内存直接翻几倍，表现是**回测卡住不动**。
//
// 为什么逐日贴与整段贴**结果完全相同**：`signal_age` 是
// `trade_idx(行) - trade_idx(信号)` —— 两个下标都取自**同一份交易日历**。
// 这里仍然一次性取来整段区间的候选与日历，只是每天单独调
// `AttachSignalCandidates`，所以下标（以及它们的差）与整段贴一字不差；
// 变的只是"同时驻留的行数"，从 680 万降到一天的量级。
//
// 只查两次库（候选 + 日历），不会退化成逐日查库。
func (s *PortfolioService) attachSignalsToDays(
	stored *portfolio.Store, days []backtest.StrengthDay,
) []backtest.StrengthDay {
	if stored == nil || len(days) == 0 {
		return days
	}
	minDate, maxDate := atoiDate(days[0].Date), atoiDate(days[len(days)-1].Date)
	if minDate > maxDate {
		minDate, maxDate = maxDate, minDate
	}
	if minDate <= 0 || maxDate <= 0 {
		return days
	}
	signalStart := s.signalBufferStart(minDate, signalLookbackTradeDays)
	if signalStart <= 0 {
		return days
	}
	storedRows, err := stored.LoadSignalCandidates(int64(signalStart), int64(maxDate))
	if err != nil {
		// 读不到信号不该让整个回测失败：原实现此时走"全部默认值"的分支
		//（`signal_df.empty` → active_signal_* 全空、signal_age=999）。
		return days
	}
	rawDays, err := s.tradingDates(signalStart, maxDate)
	calendar := make([]int64, 0, len(rawDays))
	if err == nil {
		for _, day := range rawDays {
			calendar = append(calendar, int64(day))
		}
	}
	if len(calendar) == 0 {
		// 没有日历就算不出 trade_idx；原实现在这里退回"并集排序"，
		// 等价形式是拿行日期去重排序。
		dates := make([]int64, 0, len(days))
		for _, day := range days {
			dates = append(dates, int64(atoiDate(day.Date)))
		}
		fallback := calendarFromDates(dates)
		for _, day := range fallback {
			calendar = append(calendar, int64(day))
		}
		if len(calendar) == 0 {
			return days
		}
	}
	candidates := make([]score.SignalCandidateRow, 0, len(storedRows))
	for _, item := range storedRows {
		candidates = append(candidates, score.SignalCandidateRow{
			TradeDate: item.TradeDate, Code: item.Code,
			ModelID: item.ModelID, ModelName: item.ModelName,
			MarketState: item.MarketState, MarketStateLabel: item.MarketStateLabel,
			SignalType: item.SignalType, SignalLevel: item.SignalLevel,
			SignalLevelLabel: item.SignalLevelLabel, Score: item.Score,
			SignalCount: item.SignalCount,
		})
	}

	opts := score.SignalAttachOptions{StartDate: int64(signalStart), EndDate: int64(maxDate)}

	// **按天分块贴**，而不是整段摊平、也不是逐日。
	//
	// 两个极端都试过，都不行：
	//   - **整段摊平**（第一版）：970 天 × 7000 行 ≈ 680 万行 `score.Row`
	//     复制一份就是 GB 级，外加 `byDate` 回填与 attach 的输出切片 ——
	//     内存翻几倍，回测**卡住不动**；
	//   - **逐日**（第二版）：`AttachSignalCandidates` 每次调用都会从完整候选集
	//     **重建并排序 `byCode`**（score/signal_attach.go:103-126）。逐日就是
	//     把这份工作做 970 次 —— 内存是下来了，CPU 与分配反而爆掉。
	//
	// 分块取两者之间：每块 `attachChunkDays` 天，内存按块封顶，
	// 候选分组只做 `天数/块大小` 次。
	for start := 0; start < len(days); start += attachChunkDays {
		stop := start + attachChunkDays
		if stop > len(days) {
			stop = len(days)
		}
		chunk := days[start:stop]
		rows := make([]score.Row, 0, 4096)
		dates := make([]int64, 0, 4096)
		for _, day := range chunk {
			date := int64(atoiDate(day.Date))
			for _, row := range day.Rows {
				rows = append(rows, row)
				dates = append(dates, date)
			}
		}
		if len(rows) == 0 {
			continue
		}
		attached, _ := score.AttachSignalCandidates(rows, dates, candidates, calendar, opts)

		// 用日期索引回填 —— attach 之后行数与行序都可能变
		//（交易日不在日历里的行会被丢弃），按位置切会把行错配到别的交易日。
		byDate := make(map[int64][]score.Row, len(chunk))
		for _, row := range attached {
			byDate[row.TradeDate] = append(byDate[row.TradeDate], row)
		}
		for i := range chunk {
			chunk[i].Rows = byDate[int64(atoiDate(chunk[i].Date))]
		}
	}
	return days
}

// attachChunkDays 是回测贴信号的**分块天数**。
//
// 取值权衡见 `attachSignalsToDays`：块越大内存越高，越小则候选分组重复次数越多。
// 20 天 ≈ 14 万行，一天的量级内；970 天的区间只需 49 次分组。
const attachChunkDays = 20

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
