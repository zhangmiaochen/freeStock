# -*- coding: utf-8 -*-
"""生成 `prefilter_golden_test.go`：用**原版模型模块**算入场前置过滤的黄金值。

覆盖：
  - `_recent_breakout`（窗口 `[max(20,n-60), n-2]` 内找最后一根放量新高）
  - `trend_continuation_prefilter` / `trend_continuation_candidate_prefilter`
    / `trend_pullback_candidate_prefilter`
  - `_passes_entry_prefilter` / `_passes_candidate_prefilter` 对三个模型的分派

用法：

    python internal/volprice/testdata/gen_prefilter_golden.py \
        --original "D:\\workspace\\python\\stock\\tushare-stock-tonyz\\pythonProject\\pythonProject" \
        --out internal/volprice/prefilter_golden_test.go
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

MODELS = ("trend_pullback", "box_range", "oversold_rebound")


def go_str(value) -> str:
    return json.dumps(str(value), ensure_ascii=False)


def go_float(value) -> str:
    if value is None:
        return "nan()"
    f = float(value)
    if math.isnan(f):
        return "nan()"
    if math.isinf(f):
        return "math.Inf(1)" if f > 0 else "math.Inf(-1)"
    return repr(f)


def _seq_date(day: int) -> str:
    """严格递增、互不重复的伪交易日。

    ⚠ 不能用 `2026{(day//28)+1:02d}{(day%28)+1:02d}` 里硬编码一个补录日期 ——
    `prepare_bars` 会**按 trade_date 排序并去重**，一旦新加的"最后一根"日期
    与前面某根撞上，它会被排回中间，"末端突破"就凭空消失了
    （我踩过：day=84 也映射成 20260401）。
    """
    return f"2026{1 + day // 28:02d}{day % 28 + 1:02d}"


def series_trend_start(pd):
    """窄幅平台 + 末端放量突破：命中 `trend_continuation_prefilter` 的 early_ok 分支。

    这一支（以及 standard_ok / platform_ok）在原来的 6 种形态里**一次都没命中**，
    所以 `tc` 恒为 false —— 三个 `or` 的**真分支**都没被验证过。
    """
    rows = []
    for day in range(89):
        # 30 日振幅约 8%（落在 platform_ok 的 6%~28% 区间）
        base = 15.0 + math.sin(day / 3.0) * 0.35
        rows.append(
            {"trade_date": _seq_date(day),
             "open": base - 0.05, "high": base + 0.12, "low": base - 0.12,
             "close": base, "volume": 500_000.0 + (day % 5) * 4_000.0}
        )
    prior_high = max(row["high"] for row in rows[-30:])
    last_open = prior_high * 0.97
    last_close = prior_high * 1.045
    rows.append(
        {"trade_date": _seq_date(89),
         "open": last_open, "high": last_close * 1.004, "low": last_open * 0.996,
         "close": last_close, "volume": 1_500_000.0}
    )
    return pd.DataFrame(rows)


def series_pullback_reclaim(pd):
    """上升途中回踩 MA10 ≥3% 后放量收复：命中 standard_ok 分支。"""
    rows = []
    for day in range(58):
        base = 10.0 + day * 0.052
        rows.append(
            {"trade_date": _seq_date(day),
             "open": base - 0.04, "high": base + 0.1, "low": base - 0.1,
             "close": base, "volume": 700_000.0 + (day % 6) * 5_000.0}
        )
    peak = rows[-1]["close"]
    # 六根回踩：累计约 -4%，最低触及 MA10 附近
    for k in range(1, 7):
        price = peak - k * (peak * 0.007)
        rows.append(
            {"trade_date": _seq_date(57 + k),
             "open": price + 0.03, "high": price + 0.06, "low": price - 0.09,
             "close": price - 0.02, "volume": 620_000.0 - k * 15_000.0}
        )
    prev_close = rows[-1]["close"]
    reclaim = prev_close * 1.035
    rows.append(
        {"trade_date": _seq_date(64),
         "open": prev_close * 1.002, "high": reclaim * 1.006, "low": prev_close * 0.998,
         "close": reclaim, "volume": 900_000.0}
    )
    return pd.DataFrame(rows)


def series_box_buy(pd):
    """震荡箱体下沿缩量承接：命中 `_eval_box_low_prepared` 的**买入**路径。

    （原来只有 `uptrend` 在 range 下命中了箱体的**卖出**路径，买入路径没被验证。）

    构造要点：
      - 箱体振幅落 8%~25%，下沿被摸到 ≥2 次、上沿 ≥1 次；
      - 末端三根**连续缩量**触 `volume_decay_score=100`；
      - 收盘在箱底 3% 内、带承接、且**不下破前低**（否则承接只能算"弱"）；
      - ⚠ 最后 30 根必须走成 **先降后升的 V**：`downtrend_channel_risk` 要求
        `close < close10*0.97 且 close10 < close20*0.97`，若这 30 根是单边下行，
        `close10`（第 -11 根）会明显低于 `close20`（第 -21 根），第二个条件就成立，
        箱体直接被判"仍在下降通道"而不可交易。
    """
    highs = []
    rows = []
    for day in range(30):
        rows.append(
            {"trade_date": _seq_date(day),
             "open": 23.0, "high": 23.12, "low": 22.9, "close": 23.0, "volume": 600_000.0}
        )
    # 第 -21 根（index 39）在箱底，第 -11 根（index 49）在箱顶 → 破掉"下降通道"
    for offset in range(30):
        idx = 30 + offset
        if offset <= 9:            # index 30..39：24.0 降到 21.0
            base = 24.0 - offset * (3.0 / 9.0)
        elif offset <= 19:         # index 40..49：21.0 升回 23.6
            base = 21.0 + (offset - 9) * (2.6 / 10.0)
        else:                      # index 50..59：再回到箱底附近
            base = 23.6 - (offset - 19) * (2.2 / 10.0)
        rows.append(
            {"trade_date": _seq_date(idx),
             "open": base - 0.05, "high": base + 0.12, "low": base - 0.10,
             "close": base, "volume": 600_000.0}
        )
        highs.append((idx, base))
    # 末端四根：连续缩量，贴近箱底
    prev_low = rows[-1]["low"]
    closes = [21.30, 21.20, 21.25, 21.45]
    volumes = [700_000.0, 640_000.0, 580_000.0, 520_000.0]
    for k, (close_p, volume) in enumerate(zip(closes, volumes)):
        open_p = close_p + 0.12
        low = prev_low * 0.998 if k == len(closes) - 1 else close_p - 0.08
        rows.append(
            {"trade_date": _seq_date(60 + k),
             "open": open_p, "high": close_p + 0.06, "low": low,
             "close": close_p, "volume": volume}
        )
        prev_low = low
    return pd.DataFrame(rows)


def series_oversold_buy(pd):
    """急跌恐慌释放后首次修复：命中 `_eval_oversold_rebound_prepared` 的**买入**路径。

    构造要点：
      - 5 日跌幅 ≥20%（用 -8%/日 × 5 根）；
      - 近 7 日出现 ≥3 倍均量的恐慌放量；
      - `volume_stop`（第 -2 根量小于第 -3 根）为真 —— 不能只靠 `reversal_flag`，
        因为修复日要"收盘越过前一日高点"，而恐慌日收盘即当日低位，
        这要求修复日涨幅**大于前一根的跌幅**；
      - ⚠ 前 20 根必须缓升（或持平），否则 `close10 < close20*0.96` 会成立，
        被判"仍有下跌中继风险"而不可交易。
    """
    rows = []
    for day in range(51):
        base = 30.0 + (day - 50) * 0.001   # 几乎持平，避免构成下降通道
        rows.append(
            {"trade_date": _seq_date(day),
             "open": base, "high": base + 0.01, "low": base - 0.01,
             "close": base, "volume": 600_000.0}
        )
    panic_volumes = [900_000.0, 1_400_000.0, 1_900_000.0, 2_200_000.0, 1_800_000.0]
    price = rows[-1]["close"]
    for k, volume in enumerate(panic_volumes):
        day = 51 + k
        close_p = price * 0.92
        rows.append(
            {"trade_date": _seq_date(day),
             "open": price, "high": price, "low": close_p * 0.995,
             "close": close_p, "volume": volume}
        )
        price = close_p
    prev = rows[-1]
    close_p = prev["close"] * 1.095          # 涨幅需大于前一根跌幅，才能越过前高
    rows.append(
        {"trade_date": _seq_date(56),
         "open": prev["close"] * 1.004, "high": close_p * 1.002,
         "low": prev["low"] * 0.996, "close": close_p, "volume": 2_800_000.0}
    )
    return pd.DataFrame(rows)


def series_crash(pd):
    """连续急跌（5 日跌幅远超 12%）：命中超跌反抽的入场前置过滤。"""
    rows = []
    for day in range(60):
        if day < 52:
            base = 30.0 + (day % 4) * 0.05
            volume = 600_000.0
        else:
            base = 30.0 - (day - 51) * 1.35
            volume = 600_000.0 * (1.0 + (day - 51) * 0.4)
        open_p = base + 0.25
        high = base + 0.4
        low = base - 0.5
        close = base - 0.3
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": open_p, "high": high, "low": low, "close": close, "volume": volume}
        )
    return pd.DataFrame(rows)


def series_box(pd):
    """宽幅震荡、末端贴近下沿：命中箱体的入场前置过滤。"""
    rows = []
    for day in range(80):
        cycle = day % 20
        if cycle < 10:
            base = 24.0 - cycle * 0.42
        else:
            base = 24.0 - (19 - cycle) * 0.42
        base = max(base, 20.0)
        open_p = base - 0.1
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": open_p, "high": base + 0.3, "low": base - 0.35,
             "close": base, "volume": 550_000.0 + (day % 5) * 7_000.0}
        )
    # 末端回到下沿附近并缩量
    for k in range(4, 0, -1):
        day = 80 - k
        rows[day]["close"] = 20.05 + k * 0.02
        rows[day]["open"] = rows[day]["close"] + 0.05
        rows[day]["low"] = 19.95
        rows[day]["high"] = rows[day]["close"] + 0.1
        rows[day]["volume"] = 420_000.0
    return pd.DataFrame(rows)


def load_metrics_series(here):
    spec = importlib.util.spec_from_file_location(
        "gen_metrics_golden", os.path.join(here, "gen_metrics_golden.py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return list(module.SERIES)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--original", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    if args.original not in sys.path:
        sys.path.insert(0, args.original)

    import pandas as pd  # noqa: E402
    import volume_price_features as vpf  # noqa: E402
    import volume_price_models as vpm  # noqa: E402
    import volume_price_backtest as vpb  # noqa: E402

    here = os.path.dirname(os.path.abspath(__file__))
    series = load_metrics_series(here) + [
        ("trend_start", series_trend_start),
        ("pullback_reclaim", series_pullback_reclaim),
        ("crash", series_crash),
        ("box", series_box),
    ]

    lines = []
    lines.append("// Code generated by testdata/gen_prefilter_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 黄金值由**原版 volume_price_models / volume_price_backtest** 在同一批 K 线上跑出。")
    lines.append("package volprice")
    lines.append("")
    import_index = len(lines)

    lines.append("type goldenPrefilterCase struct {")
    lines.append("\tName string")
    lines.append("\tBars []goldenBar")
    lines.append("\tRecentBreakoutOK bool")
    lines.append("\tRecentBreakoutIdx int")
    lines.append("\tRecentBreakoutDate string")
    lines.append("\tRecentBreakout map[string]float64")
    lines.append("\tTrendContinuation bool")
    lines.append("\tTrendContinuationCandidate bool")
    lines.append("\tTrendPullbackCandidate bool")
    lines.append("\tEntryByModel map[string]bool")
    lines.append("\tCandidateByModel map[string]bool")
    lines.append("}")
    lines.append("")
    lines.append("var goldenPrefilterCases = []goldenPrefilterCase{")
    lines.append("")

    summary = []
    for name, builder in series:
        frame = builder(pd)
        prepared = vpf.prepare_bars(frame)

        breakout = vpm._recent_breakout(prepared)
        tc = bool(vpm.trend_continuation_prefilter(prepared))
        tcc = bool(vpm.trend_continuation_candidate_prefilter(prepared))
        tpc = bool(vpm.trend_pullback_candidate_prefilter(prepared))
        entry = {m: bool(vpb._passes_entry_prefilter(m, prepared)) for m in MODELS}
        candidate = {m: bool(vpb._passes_candidate_prefilter(m, prepared)) for m in MODELS}
        summary.append(
            "%s(breakout=%s,tc=%s,tcc=%s,tpc=%s,entry=%s,cand=%s)"
            % (name, bool(breakout), tc, tcc, tpc,
               "".join("1" if entry[m] else "0" for m in MODELS),
               "".join("1" if candidate[m] else "0" for m in MODELS))
        )

        lines.append("\t{")
        lines.append("\t\tName: %s," % go_str(name))
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
        lines.append("\t\tRecentBreakoutOK: %s," % ("true" if breakout else "false"))
        breakout_idx = int(breakout["idx"]) if breakout else 0
        breakout_date = breakout["trade_date"] if breakout else ""
        lines.append("\t\tRecentBreakoutIdx: %d," % breakout_idx)
        lines.append("\t\tRecentBreakoutDate: %s," % go_str(breakout_date))
        lines.append("\t\tRecentBreakout: map[string]float64{")
        if breakout:
            for key in ("close", "high", "low", "volume", "platform_high", "platform_low", "volume_ratio"):
                lines.append("\t\t\t%s: %s," % (go_str(key), go_float(breakout[key])))
        lines.append("\t\t},")
        lines.append("\t\tTrendContinuation: %s," % ("true" if tc else "false"))
        lines.append("\t\tTrendContinuationCandidate: %s," % ("true" if tcc else "false"))
        lines.append("\t\tTrendPullbackCandidate: %s," % ("true" if tpc else "false"))
        lines.append("\t\tEntryByModel: map[string]bool{")
        for model in MODELS:
            lines.append("\t\t\t%s: %s," % (go_str(model), "true" if entry[model] else "false"))
        lines.append("\t\t},")
        lines.append("\t\tCandidateByModel: map[string]bool{")
        for model in MODELS:
            lines.append("\t\t\t%s: %s," % (go_str(model), "true" if candidate[model] else "false"))
        lines.append("\t\t},")
        lines.append("\t},")

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

    print(f"wrote {args.out}: {len(series)} cases")
    print("  " + "\n  ".join(summary))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
