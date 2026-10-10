package volprice

import (
	"reflect"
	"testing"
)

// **回测（= `signal_history` 的生产者）与原版 Python 的端到端比对。**
//
// 黄金值由 `testdata/gen_backtest_golden.py` 生成（直接 import 原版
// `volume_price_backtest.run_volume_price_backtest`）。10 种形态 × 3 个模型
// × 各自要求的市场状态 = 30 个用例，比四块：
//
//	metrics       —— 收益/回撤/连胜连亏/持仓天数
//	trades        —— 每笔成交的进出场、原因、盈亏、持仓天数
//	daily_signals —— **信号表来源**：日期/模型/类型/级别/子类型/分数/三价
//	diagnostics   —— 43 个计数器 + 6 个分桶
//
// 这一层不过，后面的信号表就无从谈起。
func TestBacktestParityWithOriginalPython(t *testing.T) {
	if len(goldenBacktestCases) == 0 {
		t.Fatal("黄金值文件为空 —— 请先运行 testdata/gen_backtest_golden.py")
	}
	totalSignals, totalTrades := 0, 0
	for _, tc := range goldenBacktestCases {
		tc := tc
		t.Run(tc.Name+"/"+tc.Model, func(t *testing.T) {
			bars := barsFromGolden(tc.Bars)
			got := RunVolumePriceBacktest(bars, tc.Model, BacktestOptions{
				MarketStateMode:     "manual",
				ManualMarketState:   tc.ManualState,
				RequiredMarketState: tc.ManualState,
			})
			if got.OK != tc.OK {
				t.Fatalf("ok：go %v，python %v（%s）", got.OK, tc.OK, got.Message)
			}
			if !tc.OK {
				return
			}
			if got.Message != tc.Message {
				t.Errorf("message：go %q，python %q", got.Message, tc.Message)
			}
			totalSignals += len(got.DailySignals)
			totalTrades += len(got.Trades)

			compareBacktestMetrics(t, got.Metrics, tc.Metrics)
			compareBacktestTrades(t, got.Trades, tc.Trades)
			compareBacktestSignals(t, got.DailySignals, tc.Signals)
			compareBacktestDiagnostics(t, got.Diagnostics, tc.Diagnostics)
		})
	}
	t.Logf("比对到 %d 笔成交、%d 条信号", totalTrades, totalSignals)
	if totalSignals == 0 {
		t.Fatal("没有任何信号 —— 黄金值没有覆盖到信号生产路径")
	}
}

// **`auto_model_switch=True` 的比对。**
//
// 生产者（`research_service._collect_signal_rows`）从不开启它，但它属于
// `volume_price_backtest.py`，`volume_price_service.py` 会用。
// 开启后：每日先自选模型（三个模型各算一次，按
// `(rank, confirmed_bonus, candidate_bonus, score, -priority_idx, default_bonus)`
// 取最大），自选质量闸门不过则当天不出信号，且 `required_state` 被清空。
//
// 诊断里的 `model_route_counts` / `model_switch_days` /
// `auto_switch_quality_gate_block_*` 是这一族的主证据。
func TestBacktestAutoSwitchParityWithOriginalPython(t *testing.T) {
	if len(goldenAutoSwitchCases) == 0 {
		t.Fatal("黄金值文件里没有 auto_model_switch 组 —— 请重新运行生成器")
	}
	signals, trades := 0, 0
	for _, tc := range goldenAutoSwitchCases {
		tc := tc
		t.Run(tc.Name+"/"+tc.ManualState, func(t *testing.T) {
			got := RunVolumePriceBacktest(barsFromGolden(tc.Bars), tc.Model, BacktestOptions{
				MarketStateMode:     "manual",
				ManualMarketState:   tc.ManualState,
				RequiredMarketState: tc.ManualState,
				AutoModelSwitch:     true,
			})
			if got.OK != tc.OK {
				t.Fatalf("ok：go %v，python %v（%s）", got.OK, tc.OK, got.Message)
			}
			signals += len(got.DailySignals)
			trades += len(got.Trades)
			compareBacktestMetrics(t, got.Metrics, tc.Metrics)
			compareBacktestTrades(t, got.Trades, tc.Trades)
			compareBacktestSignals(t, got.DailySignals, tc.Signals)
			compareBacktestDiagnostics(t, got.Diagnostics, tc.Diagnostics)
		})
	}
	t.Logf("auto-switch 组比对到 %d 笔成交、%d 条信号", trades, signals)
	if signals == 0 {
		t.Fatal("auto-switch 组没有任何信号 —— 黄金值没有覆盖到自选路径")
	}
}

func compareBacktestMetrics(t *testing.T, got BacktestMetrics, want goldenBacktestMetrics) {
	t.Helper()
	if got.TotalTrades != want.TotalTrades {
		t.Errorf("metrics.total_trades：go %d，python %d", got.TotalTrades, want.TotalTrades)
	}
	if got.MaxConsecutiveLoses != want.MaxConsecutiveLosses {
		t.Errorf("metrics.max_consecutive_losses：go %d，python %d",
			got.MaxConsecutiveLoses, want.MaxConsecutiveLosses)
	}
	compareGoldenFloat(t, "metrics.win_rate", got.WinRate, want.WinRate)
	compareGoldenFloat(t, "metrics.avg_profit_pct", got.AvgProfitPct, want.AvgProfitPct)
	compareGoldenFloat(t, "metrics.profit_loss_ratio", got.ProfitLossRatio, want.ProfitLossRatio)
	compareGoldenFloat(t, "metrics.max_drawdown_pct", got.MaxDrawdownPct, want.MaxDrawdownPct)
	compareGoldenFloat(t, "metrics.avg_holding_days", got.AvgHoldingDays, want.AvgHoldingDays)
}

func compareBacktestTrades(t *testing.T, got []Trade, want []goldenTrade) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("成交笔数：go %d，python %d", len(got), len(want))
		for i, trade := range got {
			t.Logf("  go[%d] %s -> %s (%s) %.2f%%",
				i, trade.EntryDate, trade.ExitDate, trade.SellReason, trade.ProfitPct)
		}
		for i, trade := range want {
			t.Logf("  py[%d] %s -> %s (%s) %.2f%%",
				i, trade.EntryDate, trade.ExitDate, trade.SellReason, trade.ProfitPct)
		}
		return
	}
	for i := range want {
		prefix := "trades[" + itoa(i) + "]"
		mine, expected := got[i], want[i]
		checkStr(t, prefix+".model_id", mine.ModelID, expected.ModelID)
		checkStr(t, prefix+".signal_type", mine.SignalType, expected.SignalType)
		checkStr(t, prefix+".signal_level", mine.SignalLevel, expected.SignalLevel)
		checkStr(t, prefix+".signal_subtype", mine.SignalSubtype, expected.SignalSubtype)
		checkStr(t, prefix+".entry_signal_source", mine.EntrySignalSource, expected.EntrySignalSource)
		checkStr(t, prefix+".entry_date", mine.EntryDate, expected.EntryDate)
		checkStr(t, prefix+".exit_date", mine.ExitDate, expected.ExitDate)
		checkStr(t, prefix+".sell_reason", mine.SellReason, expected.SellReason)
		compareGoldenFloat(t, prefix+".entry_price", mine.EntryPrice, expected.EntryPrice)
		compareGoldenFloat(t, prefix+".exit_price", mine.ExitPrice, expected.ExitPrice)
		compareGoldenFloat(t, prefix+".stop_price", mine.StopPrice, expected.StopPrice)
		compareGoldenFloat(t, prefix+".target_price", mine.TargetPrice, expected.TargetPrice)
		compareGoldenFloat(t, prefix+".profit_pct", mine.ProfitPct, expected.ProfitPct)
		compareGoldenFloat(t, prefix+".score", mine.Score, expected.Score)
		if mine.HoldingDays != expected.HoldingDays {
			t.Errorf("%s.holding_days：go %d，python %d", prefix, mine.HoldingDays, expected.HoldingDays)
		}
	}
}

func compareBacktestSignals(t *testing.T, got []Signal, want []goldenSignal) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("信号条数：go %d，python %d", len(got), len(want))
		limit := len(got)
		if len(want) < limit {
			limit = len(want)
		}
		for i := 0; i < limit; i++ {
			t.Logf("  go[%d] %s %s %s %.2f | py[%d] %s %s %s %.2f",
				i, got[i].SignalDate, got[i].ModelID, got[i].SignalType, got[i].Score,
				i, want[i].SignalDate, want[i].ModelID, want[i].SignalType, want[i].Score)
		}
		return
	}
	for i := range want {
		prefix := "signals[" + itoa(i) + "]"
		mine, expected := got[i], want[i]
		checkStr(t, prefix+".signal_date", mine.SignalDate, expected.SignalDate)
		checkStr(t, prefix+".model_id", mine.ModelID, expected.ModelID)
		checkStr(t, prefix+".signal_type", mine.SignalType, expected.SignalType)
		checkStr(t, prefix+".signal_level", mine.SignalLevel, expected.SignalLevel)
		checkStr(t, prefix+".signal_subtype", mine.SignalSubtype, expected.SignalSubtype)
		compareGoldenFloat(t, prefix+".score", mine.Score, expected.Score)
		compareGoldenFloat(t, prefix+".entry_price", derefOrNaN(mine.EntryPrice), expected.EntryPrice)
		compareGoldenFloat(t, prefix+".stop_price", derefOrNaN(mine.StopPrice), expected.StopPrice)
		compareGoldenFloat(t, prefix+".target_price", derefOrNaN(mine.TargetPrice), expected.TargetPrice)
	}
}

func compareBacktestDiagnostics(t *testing.T, got *Diagnostics, want goldenBacktestDiagnostics) {
	t.Helper()
	intPairs := []struct {
		name     string
		mine     int
		expected int
	}{
		{"visible_days", got.VisibleDays, want.VisibleDays},
		{"market_match_days", got.MarketMatchDays, want.MarketMatchDays},
		{"market_mismatch_days", got.MarketMismatchDays, want.MarketMismatchDays},
		{"model_context_used_days", got.ModelContextUsedDays, want.ModelContextUsedDays},
		{"entry_prefilter_pass_days", got.EntryPrefilterPassDays, want.EntryPrefilterPassDays},
		{"entry_prefilter_miss_days", got.EntryPrefilterMissDays, want.EntryPrefilterMissDays},
		{"model_evaluated_days", got.ModelEvaluatedDays, want.ModelEvaluatedDays},
		{"model_no_signal_days", got.ModelNoSignalDays, want.ModelNoSignalDays},
		{"signal_count", got.SignalCount, want.SignalCount},
		{"buy_signal_count", got.BuySignalCount, want.BuySignalCount},
		{"sell_signal_count", got.SellSignalCount, want.SellSignalCount},
		{"candidate_count", got.CandidateCount, want.CandidateCount},
		{"candidate_buy_count", got.CandidateBuyCount, want.CandidateBuyCount},
		{"loose_candidate_pass_count", got.LooseCandidatePassCount, want.LooseCandidatePassCount},
		{"loose_candidate_miss_count", got.LooseCandidateMissCount, want.LooseCandidateMissCount},
		{"loose_candidate_threshold_miss_count", got.LooseCandidateThreshMiss, want.LooseCandidateThresholdMissCount},
		{"loose_candidate_quality_block_count", got.LooseQualityBlockCount, want.LooseCandidateQualityBlockCount},
		{"loose_candidate_buy_signal_count", got.LooseBuySignalCount, want.LooseCandidateBuySignalCount},
		{"loose_candidate_pending_count", got.LoosePendingCount, want.LooseCandidatePendingCount},
		{"loose_candidate_trigger_confirmed_count", got.LooseTriggerConfirmed, want.LooseCandidateTriggerConfirmedCount},
		{"loose_candidate_trigger_volume_miss_count", got.LooseTriggerVolumeMiss, want.LooseCandidateTriggerVolumeMissCount},
		{"loose_candidate_triggered_count", got.LooseTriggeredCount, want.LooseCandidateTriggeredCount},
		{"loose_candidate_expired_count", got.LooseExpiredCount, want.LooseCandidateExpiredCount},
		{"loose_candidate_invalidated_count", got.LooseInvalidatedCount, want.LooseCandidateInvalidatedCount},
		{"loose_candidate_unresolved_count", got.LooseUnresolvedCount, want.LooseCandidateUnresolvedCount},
		{"loose_candidate_trigger_reject_count", got.LooseTriggerRejectCount, want.LooseCandidateTriggerRejectCount},
		{"loose_candidate_entry_risk_reject_count", got.LooseEntryRiskReject, want.LooseCandidateEntryRiskRejectCount},
		{"loose_candidate_intraday_stop_count", got.LooseIntradayStopCount, want.LooseCandidateIntradayStopCount},
		{"trend_intraday_stop_ignored_count", got.TrendIntradayStopIgnored, want.TrendIntradayStopIgnoredCount},
		{"trend_close_stop_pending_count", got.TrendCloseStopPending, want.TrendCloseStopPendingCount},
		{"trend_sell_signal_ignored_count", got.TrendSellSignalIgnored, want.TrendSellSignalIgnoredCount},
		{"trend_sell_signal_confirmed_count", got.TrendSellSignalConfirmed, want.TrendSellSignalConfirmedCount},
		{"trend_first_target_reached_count", got.TrendFirstTargetReached, want.TrendFirstTargetReachedCount},
		{"trend_first_target_trailing_exit_count", got.TrendFirstTargetTrailing, want.TrendFirstTargetTrailingExitCount},
		{"trend_trailing_take_profit_pending_count", got.TrendTrailingPending, want.TrendTrailingTakeProfitPendingCount},
		{"trend_confirm_failure_exit_pending_count", got.TrendConfirmFailurePending, want.TrendConfirmFailureExitPendingCount},
		{"trend_continuation_failure_exit_pending_count", got.TrendContinuationFailurePending, want.TrendContinuationFailureExitPendingCount},
		{"box_failure_exit_count", got.BoxFailureExitCount, want.BoxFailureExitCount},
		{"oversold_failure_exit_count", got.OversoldFailureExitCount, want.OversoldFailureExitCount},
		{"model_switch_days", got.ModelSwitchDays, want.ModelSwitchDays},
		{"auto_switch_quality_gate_block_count", got.AutoSwitchGateBlock, want.AutoSwitchQualityGateBlockCount},
		{"trade_count", got.TradeCount, want.TradeCount},
	}
	for _, pair := range intPairs {
		if pair.mine != pair.expected {
			t.Errorf("diagnostics.%s：go %d，python %d", pair.name, pair.mine, pair.expected)
		}
	}
	mapPairs := []struct {
		name     string
		mine     map[string]int
		expected map[string]int
	}{
		{"market_state_counts", got.MarketStateCounts, want.MarketStateCounts},
		{"loose_candidate_quality_block_reasons", got.LooseQualityBlockReasons, want.LooseCandidateQualityBlockReasons},
		{"loose_candidate_entry_risk_reject_reasons", got.LooseEntryRiskReasons, want.LooseCandidateEntryRiskRejectReasons},
		{"model_route_counts", got.ModelRouteCounts, want.ModelRouteCounts},
		{"model_usage_counts", got.ModelUsageCounts, want.ModelUsageCounts},
		{"auto_switch_quality_gate_block_reasons", got.AutoSwitchGateReasons, want.AutoSwitchQualityGateBlockReasons},
	}
	for _, pair := range mapPairs {
		if !intMapEqual(pair.mine, pair.expected) {
			t.Errorf("diagnostics.%s：\n  go     %v\n  python %v", pair.name, pair.mine, pair.expected)
		}
	}
}

func intMapEqual(a, b map[string]int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func checkStr(t *testing.T, label, mine, expected string) {
	t.Helper()
	if mine != expected {
		t.Errorf("%s：\n  go     %q\n  python %q", label, mine, expected)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
