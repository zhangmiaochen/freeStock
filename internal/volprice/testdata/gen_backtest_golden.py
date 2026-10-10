# -*- coding: utf-8 -*-
"""生成 `backtest_golden_test.go`：用**原版 volume_price_backtest** 跑出黄金值。

这是信号生产者的端到端比对：metrics / trades / **daily_signals** / diagnostics
四块全部逐项比。`daily_signals` 就是 `signal_history` 的来源，所以这一层不过，
后面的信号表就无从谈起。

用 `market_state_mode="manual"` 固定市场状态，让每一天都进入评估分支（不受
状态闸门随机影响），并与模型各自要求的市场状态对齐，保证真的会跑出信号。

用法：

    python internal/volprice/testdata/gen_backtest_golden.py \
        --original "D:\\workspace\\python\\stock\\tushare-stock-tonyz\\pythonProject\\pythonProject" \
        --out internal/volprice/backtest_golden_test.go
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import math
import os
import shutil
import subprocess
import sys

MODEL_STATE = {
    "trend_pullback": "bull",
    "box_range": "range",
    "oversold_rebound": "bear",
}

DIAG_INT_KEYS = (
    "visible_days", "market_match_days", "market_mismatch_days", "model_context_used_days",
    "entry_prefilter_pass_days", "entry_prefilter_miss_days", "model_evaluated_days",
    "model_no_signal_days", "signal_count", "buy_signal_count", "sell_signal_count",
    "candidate_count", "candidate_buy_count", "loose_candidate_pass_count",
    "loose_candidate_miss_count", "loose_candidate_threshold_miss_count",
    "loose_candidate_quality_block_count", "loose_candidate_buy_signal_count",
    "loose_candidate_pending_count", "loose_candidate_trigger_confirmed_count",
    "loose_candidate_trigger_volume_miss_count", "loose_candidate_triggered_count",
    "loose_candidate_expired_count", "loose_candidate_invalidated_count",
    "loose_candidate_unresolved_count", "loose_candidate_trigger_reject_count",
    "loose_candidate_entry_risk_reject_count", "loose_candidate_intraday_stop_count",
    "trend_intraday_stop_ignored_count", "trend_close_stop_pending_count",
    "trend_sell_signal_ignored_count", "trend_sell_signal_confirmed_count",
    "trend_first_target_reached_count", "trend_first_target_trailing_exit_count",
    "trend_trailing_take_profit_pending_count", "trend_confirm_failure_exit_pending_count",
    "trend_continuation_failure_exit_pending_count", "box_failure_exit_count",
    "oversold_failure_exit_count", "model_switch_days", "auto_switch_quality_gate_block_count",
    "trade_count",
)

DIAG_MAP_KEYS = (
    "market_state_counts", "loose_candidate_quality_block_reasons",
    "loose_candidate_entry_risk_reject_reasons", "model_route_counts",
    "model_usage_counts",
    # ⚠ 键名必须是 `_block_reasons`（`_empty_diagnostics` 里就是这个名字）。
    # 我第一版写成 `auto_switch_quality_gate_reasons`，取到 None → 生成成 nil，
    # 于是黄金值里这个分桶永远是空的，测试却报"Go 侧多记了" —— 是生成器错，不是实现错。
    "auto_switch_quality_gate_block_reasons",
)


def go_str(value) -> str:
    return json.dumps(str(value), ensure_ascii=False)


def go_float(value) -> str:
    if value is None:
        return "nan()"
    try:
        f = float(value)
    except Exception:
        return "nan()"
    if math.isnan(f):
        return "nan()"
    if math.isinf(f):
        return "math.Inf(1)" if f > 0 else "math.Inf(-1)"
    return repr(f)


def go_int_map(values) -> str:
    if not values:
        return "nil"
    items = ", ".join("%s: %d" % (go_str(k), int(v)) for k, v in sorted(values.items()))
    return "map[string]int{" + items + "}"


def load_series(here):
    spec = importlib.util.spec_from_file_location(
        "gen_eval_confirmed_golden", os.path.join(here, "gen_eval_confirmed_golden.py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.load_series(here)


def load_seq_date(here):
    spec = importlib.util.spec_from_file_location(
        "gen_prefilter_golden", os.path.join(here, "gen_prefilter_golden.py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module._seq_date


def with_trailing(pd, frame, seq_date, count: int = 4):
    """在形态后面补几根"平静"的尾巴。

    ⚠ 这一步是必需的：`run_volume_price_backtest` 把第 i 根当成"今天"、
    第 i+1 根当成"明天"，循环到 `len-2` 就结束 —— **最后一根永远不会是信号日**。
    而这些形态都把"启动/突破"放在最后一根，不补尾巴的话信号永远出不来
    （实测：30 个用例只出 132 个 sell、0 个 buy、0 笔成交）。

    尾巴不影响信号日：信号日的可见数据是 `bars[:i+1]`，未来那几根不参与判定。
    """
    last = frame.iloc[-1]
    close = float(last["close"])
    volume = float(last["volume"])
    rows = []
    for k in range(count):
        base = close * (1.0 + 0.002 * (k + 1))
        rows.append(
            {
                "trade_date": seq_date(200 + k),
                "open": base * 0.999, "high": base * 1.004,
                "low": base * 0.996, "close": base, "volume": volume,
            }
        )
    return pd.concat([frame, pd.DataFrame(rows)], ignore_index=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--original", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    if args.original not in sys.path:
        sys.path.insert(0, args.original)

    import pandas as pd  # noqa: E402
    import volume_price_backtest as vpb  # noqa: E402

    here = os.path.dirname(os.path.abspath(__file__))
    series = load_series(here)
    seq_date = load_seq_date(here)

    lines = []
    lines.append("// Code generated by testdata/gen_backtest_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 黄金值由**原版 volume_price_backtest.run_volume_price_backtest** 跑出。")
    lines.append("package volprice")
    lines.append("")
    import_index = len(lines)

    lines.append("type goldenBacktestMetrics struct {")
    lines.append("\tTotalTrades int")
    lines.append("\tWinRate float64")
    lines.append("\tAvgProfitPct float64")
    lines.append("\tProfitLossRatio float64")
    lines.append("\tMaxDrawdownPct float64")
    lines.append("\tMaxConsecutiveLosses int")
    lines.append("\tAvgHoldingDays float64")
    lines.append("}")
    lines.append("")
    lines.append("type goldenTrade struct {")
    lines.append("\tModelID string")
    lines.append("\tSignalType string")
    lines.append("\tSignalLevel string")
    lines.append("\tSignalSubtype string")
    lines.append("\tEntrySignalSource string")
    lines.append("\tEntryDate string")
    lines.append("\tExitDate string")
    lines.append("\tSellReason string")
    lines.append("\tEntryPrice float64")
    lines.append("\tExitPrice float64")
    lines.append("\tStopPrice float64")
    lines.append("\tTargetPrice float64")
    lines.append("\tProfitPct float64")
    lines.append("\tHoldingDays int")
    lines.append("\tScore float64")
    lines.append("}")
    lines.append("")
    lines.append("type goldenSignal struct {")
    lines.append("\tSignalDate string")
    lines.append("\tModelID string")
    lines.append("\tSignalType string")
    lines.append("\tSignalLevel string")
    lines.append("\tSignalSubtype string")
    lines.append("\tScore float64")
    lines.append("\tEntryPrice float64")
    lines.append("\tStopPrice float64")
    lines.append("\tTargetPrice float64")
    lines.append("}")
    lines.append("")
    lines.append("type goldenBacktestDiagnostics struct {")
    for key in DIAG_INT_KEYS:
        lines.append("\t%s int" % "".join(part.capitalize() for part in key.split("_")))
    for key in DIAG_MAP_KEYS:
        lines.append("\t%s map[string]int" % "".join(part.capitalize() for part in key.split("_")))
    lines.append("}")
    lines.append("")
    lines.append("type goldenBacktestCase struct {")
    lines.append("\tName string")
    lines.append("\tModel string")
    lines.append("\tManualState string")
    lines.append("\tAutoSwitch bool")
    lines.append("\tBars []goldenBar")
    lines.append("\tOK bool")
    lines.append("\tMessage string")
    lines.append("\tMetrics goldenBacktestMetrics")
    lines.append("\tTrades []goldenTrade")
    lines.append("\tSignals []goldenSignal")
    lines.append("\tDiagnostics goldenBacktestDiagnostics")
    lines.append("}")
    lines.append("")
    lines.append("var goldenBacktestCases = []goldenBacktestCase{")
    lines.append("")

    def emit_case(name, model, state, frame, result, auto_switch):
        signals = result.get("daily_signals") or []
        trades = result.get("trades") or []
        metrics = result.get("metrics") or {}
        diagnostics = result.get("diagnostics") or {}
        lines.append("\t{")
        lines.append("\t\tName: %s," % go_str(name))
        lines.append("\t\tModel: %s," % go_str(model))
        lines.append("\t\tManualState: %s," % go_str(state))
        lines.append("\t\tAutoSwitch: %s," % ("true" if auto_switch else "false"))
        lines.append("\t\tBars: []goldenBar{")
        for _, row in frame.iterrows():
            lines.append(
                "\t\t\t{%s, %s, %s, %s, %s, %s},"
                % (
                    go_str(row["trade_date"]), go_float(row["open"]), go_float(row["high"]),
                    go_float(row["low"]), go_float(row["close"]), go_float(row["volume"]),
                )
            )
        lines.append("\t\t},")
        lines.append("\t\tOK: true,")
        lines.append("\t\tMessage: %s," % go_str(result.get("message", "")))
        lines.append("\t\tMetrics: goldenBacktestMetrics{")
        lines.append("\t\t\tTotalTrades: %d," % int(metrics.get("total_trades") or 0))
        lines.append("\t\t\tWinRate: %s," % go_float(metrics.get("win_rate")))
        lines.append("\t\t\tAvgProfitPct: %s," % go_float(metrics.get("avg_profit_pct")))
        lines.append("\t\t\tProfitLossRatio: %s," % go_float(metrics.get("profit_loss_ratio")))
        lines.append("\t\t\tMaxDrawdownPct: %s," % go_float(metrics.get("max_drawdown_pct")))
        lines.append("\t\t\tMaxConsecutiveLosses: %d," % int(metrics.get("max_consecutive_losses") or 0))
        lines.append("\t\t\tAvgHoldingDays: %s," % go_float(metrics.get("avg_holding_days")))
        lines.append("\t\t},")
        lines.append("\t\tTrades: []goldenTrade{")
        for trade in trades:
            lines.append("\t\t\t{")
            lines.append("\t\t\t\tModelID: %s," % go_str(trade.get("model_id", "")))
            lines.append("\t\t\t\tSignalType: %s," % go_str(trade.get("signal_type", "")))
            lines.append("\t\t\t\tSignalLevel: %s," % go_str(trade.get("signal_level", "")))
            lines.append("\t\t\t\tSignalSubtype: %s," % go_str(trade.get("signal_subtype", "")))
            lines.append("\t\t\t\tEntrySignalSource: %s," % go_str(trade.get("entry_signal_source", "")))
            lines.append("\t\t\t\tEntryDate: %s," % go_str(trade.get("entry_date", "")))
            lines.append("\t\t\t\tExitDate: %s," % go_str(trade.get("exit_date", "")))
            lines.append("\t\t\t\tSellReason: %s," % go_str(trade.get("sell_reason", "")))
            lines.append("\t\t\t\tEntryPrice: %s," % go_float(trade.get("entry_price")))
            lines.append("\t\t\t\tExitPrice: %s," % go_float(trade.get("exit_price")))
            lines.append("\t\t\t\tStopPrice: %s," % go_float(trade.get("stop_price")))
            lines.append("\t\t\t\tTargetPrice: %s," % go_float(trade.get("target_price")))
            lines.append("\t\t\t\tProfitPct: %s," % go_float(trade.get("profit_pct")))
            lines.append("\t\t\t\tHoldingDays: %d," % int(trade.get("holding_days") or 0))
            lines.append("\t\t\t\tScore: %s," % go_float(trade.get("score")))
            lines.append("\t\t\t},")
        lines.append("\t\t},")
        lines.append("\t\tSignals: []goldenSignal{")
        for signal in signals:
            lines.append("\t\t\t{")
            lines.append("\t\t\t\tSignalDate: %s," % go_str(signal.get("signal_date", "")))
            lines.append("\t\t\t\tModelID: %s," % go_str(signal.get("model_id", "")))
            lines.append("\t\t\t\tSignalType: %s," % go_str(signal.get("signal_type", "")))
            lines.append("\t\t\t\tSignalLevel: %s," % go_str(signal.get("signal_level", "")))
            lines.append("\t\t\t\tSignalSubtype: %s," % go_str(signal.get("signal_subtype", "")))
            lines.append("\t\t\t\tScore: %s," % go_float(signal.get("score")))
            lines.append("\t\t\t\tEntryPrice: %s," % go_float(signal.get("entry_price")))
            lines.append("\t\t\t\tStopPrice: %s," % go_float(signal.get("stop_price")))
            lines.append("\t\t\t\tTargetPrice: %s," % go_float(signal.get("target_price")))
            lines.append("\t\t\t},")
        lines.append("\t\t},")
        lines.append("\t\tDiagnostics: goldenBacktestDiagnostics{")
        for key in DIAG_INT_KEYS:
            field = "".join(part.capitalize() for part in key.split("_"))
            lines.append("\t\t\t%s: %d," % (field, int(diagnostics.get(key) or 0)))
        for key in DIAG_MAP_KEYS:
            field = "".join(part.capitalize() for part in key.split("_"))
            lines.append("\t\t\t%s: %s," % (field, go_int_map(diagnostics.get(key))))
        lines.append("\t\t},")
        lines.append("\t},")
        return len(signals), len(trades)

    total_signals, total_trades = 0, 0
    for name, builder in series:
        frame = with_trailing(pd, builder(pd), seq_date)
        for model, state in MODEL_STATE.items():
            result = vpb.run_volume_price_backtest(
                frame, model, market_state_mode="manual", manual_market_state=state,
                required_market_state=state,
            )
            if not result.get("ok"):
                continue
            signals = result.get("daily_signals") or []
            trades = result.get("trades") or []
            total_signals += len(signals)
            total_trades += len(trades)
            metrics = result.get("metrics") or {}
            diagnostics = result.get("diagnostics") or {}

            lines.append("\t{")
            lines.append("\t\tName: %s," % go_str(name))
            lines.append("\t\tModel: %s," % go_str(model))
            lines.append("\t\tManualState: %s," % go_str(state))
            lines.append("\t\tBars: []goldenBar{")
            for _, row in frame.iterrows():
                lines.append(
                    "\t\t\t{%s, %s, %s, %s, %s, %s},"
                    % (
                        go_str(row["trade_date"]), go_float(row["open"]), go_float(row["high"]),
                        go_float(row["low"]), go_float(row["close"]), go_float(row["volume"]),
                    )
                )
            lines.append("\t\t},")
            lines.append("\t\tOK: true,")
            lines.append("\t\tMessage: %s," % go_str(result.get("message", "")))
            lines.append("\t\tMetrics: goldenBacktestMetrics{")
            lines.append("\t\t\tTotalTrades: %d," % int(metrics.get("total_trades") or 0))
            lines.append("\t\t\tWinRate: %s," % go_float(metrics.get("win_rate")))
            lines.append("\t\t\tAvgProfitPct: %s," % go_float(metrics.get("avg_profit_pct")))
            lines.append("\t\t\tProfitLossRatio: %s," % go_float(metrics.get("profit_loss_ratio")))
            lines.append("\t\t\tMaxDrawdownPct: %s," % go_float(metrics.get("max_drawdown_pct")))
            lines.append("\t\t\tMaxConsecutiveLosses: %d," % int(metrics.get("max_consecutive_losses") or 0))
            lines.append("\t\t\tAvgHoldingDays: %s," % go_float(metrics.get("avg_holding_days")))
            lines.append("\t\t},")
            lines.append("\t\tTrades: []goldenTrade{")
            for trade in trades:
                lines.append("\t\t\t{")
                lines.append("\t\t\t\tModelID: %s," % go_str(trade.get("model_id", "")))
                lines.append("\t\t\t\tSignalType: %s," % go_str(trade.get("signal_type", "")))
                lines.append("\t\t\t\tSignalLevel: %s," % go_str(trade.get("signal_level", "")))
                lines.append("\t\t\t\tSignalSubtype: %s," % go_str(trade.get("signal_subtype", "")))
                lines.append("\t\t\t\tEntrySignalSource: %s," % go_str(trade.get("entry_signal_source", "")))
                lines.append("\t\t\t\tEntryDate: %s," % go_str(trade.get("entry_date", "")))
                lines.append("\t\t\t\tExitDate: %s," % go_str(trade.get("exit_date", "")))
                lines.append("\t\t\t\tSellReason: %s," % go_str(trade.get("sell_reason", "")))
                lines.append("\t\t\t\tEntryPrice: %s," % go_float(trade.get("entry_price")))
                lines.append("\t\t\t\tExitPrice: %s," % go_float(trade.get("exit_price")))
                lines.append("\t\t\t\tStopPrice: %s," % go_float(trade.get("stop_price")))
                lines.append("\t\t\t\tTargetPrice: %s," % go_float(trade.get("target_price")))
                lines.append("\t\t\t\tProfitPct: %s," % go_float(trade.get("profit_pct")))
                lines.append("\t\t\t\tHoldingDays: %d," % int(trade.get("holding_days") or 0))
                lines.append("\t\t\t\tScore: %s," % go_float(trade.get("score")))
                lines.append("\t\t\t},")
            lines.append("\t\t},")
            lines.append("\t\tSignals: []goldenSignal{")
            for signal in signals:
                lines.append("\t\t\t{")
                lines.append("\t\t\t\tSignalDate: %s," % go_str(signal.get("signal_date", "")))
                lines.append("\t\t\t\tModelID: %s," % go_str(signal.get("model_id", "")))
                lines.append("\t\t\t\tSignalType: %s," % go_str(signal.get("signal_type", "")))
                lines.append("\t\t\t\tSignalLevel: %s," % go_str(signal.get("signal_level", "")))
                lines.append("\t\t\t\tSignalSubtype: %s," % go_str(signal.get("signal_subtype", "")))
                lines.append("\t\t\t\tScore: %s," % go_float(signal.get("score")))
                lines.append("\t\t\t\tEntryPrice: %s," % go_float(signal.get("entry_price")))
                lines.append("\t\t\t\tStopPrice: %s," % go_float(signal.get("stop_price")))
                lines.append("\t\t\t\tTargetPrice: %s," % go_float(signal.get("target_price")))
                lines.append("\t\t\t},")
            lines.append("\t\t},")
            lines.append("\t\tDiagnostics: goldenBacktestDiagnostics{")
            for key in DIAG_INT_KEYS:
                field = "".join(part.capitalize() for part in key.split("_"))
                lines.append("\t\t\t%s: %d," % (field, int(diagnostics.get(key) or 0)))
            for key in DIAG_MAP_KEYS:
                field = "".join(part.capitalize() for part in key.split("_"))
                lines.append("\t\t\t%s: %s," % (field, go_int_map(diagnostics.get(key))))
            lines.append("\t\t},")
            lines.append("\t},")

    lines.append("}")
    lines.append("")

    # ---- 第二组：`auto_model_switch=True` ----
    #
    # 生产者从不开启它，但它是 `volume_price_backtest.py` 的一部分，
    # 且 `volume_price_service.py` 会用 —— 所以单独出一组黄金值。
    lines.append("// goldenAutoSwitchCases 与上面同形态，但开启 auto_model_switch。")
    lines.append("var goldenAutoSwitchCases = []goldenBacktestCase{")
    lines.append("")
    auto_signals, auto_trades = 0, 0
    for name, builder in series:
        frame = with_trailing(pd, builder(pd), seq_date)
        for state in ("bull", "range", "bear"):
            result = vpb.run_volume_price_backtest(
                frame, "trend_pullback", market_state_mode="manual",
                manual_market_state=state, auto_model_switch=True,
            )
            if not result.get("ok"):
                continue
            signal_count, trade_count = emit_case(name, "trend_pullback", state, frame, result, True)
            auto_signals += signal_count
            auto_trades += trade_count
    lines.append("}")
    lines.append("")

    if "math.Inf" in "\n".join(lines):
        lines[import_index:import_index] = ["import \"math\"", ""]

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines))
    gofmt = shutil.which("gofmt")
    if gofmt:
        subprocess.run([gofmt, "-w", args.out], check=True)

    print(f"wrote {args.out}: {len(series)*len(MODEL_STATE)} cases, "
          f"{total_signals} daily_signals, {total_trades} trades; "
          f"auto-switch 组 {auto_signals} daily_signals, {auto_trades} trades")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
