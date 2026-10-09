package backtest

import (
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"freestock/internal/score"
)

// trace 打一条逐日耗时日志（供诊断"回测卡在哪一天"）。
//
// 只在耗时明显时打，避免正常回测刷屏。
func (e *Engine) trace(stage, date string, elapsed time.Duration, extra int) {
	if elapsed < 300*time.Millisecond {
		return
	}
	slog.Info("回测逐日耗时", "stage", stage, "date", date,
		"elapsed", elapsed.Round(time.Millisecond), "n", extra)
}

// PlanBuilder 由调用方注入：给定"当日持仓行"与"当日评分截面"，
// 产出一份调仓计划。
//
// 做成回调是因为**候选池的构建要用到 service 层的评分表读法与行业/概念成员表**，
// 放进 backtest 包会造成循环依赖。
type PlanBuilder func(held []score.Row, day StrengthDay) score.RotationPlan

// Run 跑一次回测。对应原实现 `PortfolioBacktestEngine.run`（:396-621）。
func (e *Engine) Run(buildPlan PlanBuilder) (*Result, error) {
	// 交易日 = 有价格数据的日期（原实现 :407 用的是 price_df 的日期）
	dates := make([]string, 0, len(e.priceMap))
	for date := range e.priceMap {
		if date >= e.cfg.StartDate && date <= e.cfg.EndDate {
			dates = append(dates, date)
		}
	}
	sort.Strings(dates)
	if len(dates) == 0 {
		return &Result{}, nil
	}
	// 交易日序号表：`_build_held_rows` 的 holding_trade_days 要用
	e.dateIndex = make(map[string]int, len(dates))
	for i, date := range dates {
		e.dateIndex[date] = i
	}

	benchmarkBase := e.benchmark[dates[0]]
	positions := map[string]*Position{}
	cash := e.cfg.InitialCapital
	totalValue := e.cfg.InitialCapital

	var pendingPlan *score.RotationPlan
	pendingSignalDate := ""

	for _, tradeDate := range dates {
		day, hasStrength := e.strengthByDate[tradeDate]
		tradeCountBefore := len(e.trades)
		executionNAV := totalValue

		if pendingPlan != nil {
			execBegan := time.Now()
			e.executePlan(positions, &cash, *pendingPlan, tradeDate, executionNAV, pendingSignalDate)
			e.trace("执行计划", tradeDate, time.Since(execBegan), len(e.trades)-tradeCountBefore)
		}

		// 盯市：用当日 close
		positionValue := 0.0
		for _, pos := range positions {
			if bar, ok := e.priceMap[tradeDate][pos.Code]; ok && bar.Close > 0 {
				pos.CurrentPrice = bar.Close
				if bar.Close > pos.HighestPrice {
					pos.HighestPrice = bar.Close
				}
			}
			mark := pos.CurrentPrice
			if mark <= 0 {
				mark = pos.CostPrice
			}
			positionValue += float64(pos.Shares) * mark
		}
		totalValue = cash + positionValue

		// 用**当日收盘后**的数据生成下一交易日的计划
		if hasStrength && buildPlan != nil {
			planBegan := time.Now()
			held := e.heldRows(positions, day)
			plan := buildPlan(held, day)
			pendingPlan = &plan
			pendingSignalDate = tradeDate
			e.trace("生成计划", tradeDate, time.Since(planBegan), len(plan.Rows))
		} else {
			pendingPlan = nil
			pendingSignalDate = ""
		}

		prevTotal := e.cfg.InitialCapital
		if len(e.snapshots) > 0 {
			prevTotal = e.snapshots[len(e.snapshots)-1].TotalValue
		}
		dailyReturn := 0.0
		if prevTotal > 0 {
			dailyReturn = (totalValue/prevTotal - 1) * 100
		}
		cumulativeReturn := 0.0
		if e.cfg.InitialCapital > 0 {
			cumulativeReturn = (totalValue/e.cfg.InitialCapital - 1) * 100
		}
		benchmarkValue, benchmarkReturn := 0.0, 0.0
		if close := e.benchmark[tradeDate]; benchmarkBase > 0 && close > 0 {
			benchmarkValue = e.cfg.InitialCapital * (close / benchmarkBase)
			benchmarkReturn = (close/benchmarkBase - 1) * 100
		}

		e.snapshots = append(e.snapshots, Snapshot{
			Date: tradeDate, TotalValue: totalValue, Cash: cash, PositionValue: positionValue,
			DailyReturn: dailyReturn, CumulativeReturn: cumulativeReturn,
			PositionCount: len(positions), TradeCount: max0(len(e.trades) - tradeCountBefore),
			BenchmarkValue: benchmarkValue, BenchmarkReturn: benchmarkReturn,
		})
	}

	return &Result{Snapshots: e.snapshots, Trades: e.trades}, nil
}

// executePlan 按"先卖、再减、后买"三段执行（原实现 :448-528）。
func (e *Engine) executePlan(
	positions map[string]*Position, cash *float64,
	plan score.RotationPlan, tradeDate string, executionNAV float64, signalDate string,
) {
	rows := plan.Rows

	// ① 清仓与换出（先卖，腾出资金）
	for _, row := range rows {
		action := row.Action
		if action != score.ActionReplaceOut && action != score.ActionClear {
			continue
		}
		reason := row.Reason
		if action == score.ActionReplaceOut {
			reason = "调仓换出|" + row.Reason
		}
		delta := row.LayersDelta
		if delta < 0 {
			delta = -delta
		}
		// ⚠ 参数是 `shares=0, layerDelta=...`（原实现传的是 `layer_delta=`）——
		// 传反了会只卖掉 1 股而不是整仓/整层。
		*cash = e.sellPosition(positions, row.Code, tradeDate, *cash,
			0, max1(delta), reason, signalDate, "open", row2Score(row))
	}

	// ② 减仓
	for _, row := range rows {
		if row.Action != score.ActionReduce {
			continue
		}
		pos := positions[row.Code]
		if pos == nil || pos.Shares <= 1 || pos.Layers <= 0 {
			continue
		}
		delta := row.LayersDelta
		if delta < 0 {
			delta = -delta
		}
		*cash = e.sellPosition(positions, row.Code, tradeDate, *cash,
			0, max1(delta), "调仓减仓", signalDate, "open", row2Score(row))
	}

	// ③ 买入（换入 / 建仓 / 加层），按 rank_no、评分降序、代码升序
	buys := make([]score.RotationRow, 0, len(rows))
	for _, row := range rows {
		switch row.Action {
		case score.ActionReplaceIn, score.ActionAdd, score.ActionAddLayer:
			buys = append(buys, row)
		}
	}
	sort.SliceStable(buys, func(i, j int) bool {
		if buys[i].RankNo != buys[j].RankNo {
			return buys[i].RankNo < buys[j].RankNo
		}
		if buys[i].Score != buys[j].Score {
			return buys[i].Score > buys[j].Score
		}
		return buys[i].Code < buys[j].Code
	})

	for _, row := range buys {
		if *cash <= 0 {
			break
		}
		target := max1(row.TargetLayers)
		current := 0
		if pos := positions[row.Code]; pos != nil {
			current = pos.Layers
		}
		layerDelta := max0(target - current)
		if layerDelta <= 0 {
			continue
		}
		if positions[row.Code] == nil && len(positions) >= e.cfg.MaxStockCount {
			continue
		}
		budget := e.layerBudget(executionNAV, layerDelta)
		reason := "调仓买入|评分" + formatScore(row.Score) + "|" + row.Reason
		*cash = e.buyPosition(positions, row2Score(row), tradeDate, *cash,
			budget, reason, target, signalDate, "open")
	}
}

// heldRows 生成"当日持仓行"给计划构建器用：持仓代码取当日评分，
// 再把成本/层数/最高价等持仓状态覆盖上去（原实现 `_build_held_rows` :192-232）。
func (e *Engine) heldRows(positions map[string]*Position, day StrengthDay) []score.Row {
	if len(positions) == 0 {
		return nil
	}
	byCode := e.scoreByDate[day.Date]
	out := make([]score.Row, 0, len(positions))
	for code, pos := range positions {
		row, ok := byCode[code]
		if !ok {
			// 当日没有该股评分：仍要产出（否则会被当成"没建议"而漏掉），
			// 评分记 0 —— 与原实现的左连接落空一致
			row = score.Row{Code: code, Name: pos.Name}
		}
		row.PositionLayers = pos.Layers
		row.EntryPrice = pos.CostPrice
		row.CurrentPrice = pos.CurrentPrice
		row.HighestPrice = pos.HighestPrice
		// 持有**交易日**数（原实现 :215）：
		// `max(0, current_index - buy_index + 1)` —— 当天买算 1 天。
		// 两个序号都取不到时记 0（原实现同样回落 0）。
		if current, okCur := e.dateIndex[day.Date]; okCur {
			if bought, okBuy := e.dateIndex[pos.BuyDate]; okBuy {
				held := current - bought + 1
				if held > 0 {
					row.HoldingTradeDays = held
				}
			}
		}
		if pos.CostPrice > 0 && pos.CurrentPrice > 0 {
			row.ProfitLossPct = (pos.CurrentPrice/pos.CostPrice - 1) * 100
		}
		out = append(out, row)
	}
	return out
}

// row2Score 把计划行转成一个"评分片段"，用于流水里记 score 与快照。
func row2Score(row score.RotationRow) score.Row {
	return score.Row{Code: row.Code, Name: row.Name, TotalScore: row.Score}
}

// formatScore 对应原实现的 `f"{score:.2f}"`。
func formatScore(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
}

// daysBetween 算两个 YYYYMMDD 之间的自然日数（原实现 :139-143）。
func daysBetween(from, to string) int {
	if from == "" || to == "" || len(from) != 8 || len(to) != 8 {
		return 0
	}
	parse := func(v string) (time.Time, bool) {
		t, err := time.Parse("20060102", v)
		return t, err == nil
	}
	a, okA := parse(from)
	b, okB := parse(to)
	if !okA || !okB {
		return 0
	}
	days := int(b.Sub(a).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}
