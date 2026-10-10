# -*- coding: utf-8 -*-
"""生成 `metrics_golden_test.go`：用**原版模型模块**算四个结构度量的黄金值。

覆盖四种形态（各自压不同的分支）：

  uptrend   温和上行 + 回踩 MA10   → trend_continuation 的 reentry/digestion 分支
  breakout  放量突破 + 2~8 日整理   → power_breakout_reentry 全字段
  platform  长期平台 + 突破         → early_maintrend / trend_platform
  down      单边下行                → 多为"度量不可用"（原实现 return {}）

用法：

    python internal/volprice/testdata/gen_metrics_golden.py \
        --original "D:\\workspace\\python\\stock\\tushare-stock-tonyz\\pythonProject\\pythonProject" \
        --out internal/volprice/metrics_golden_test.go
"""

from __future__ import annotations

import argparse
import json
import math
import os
import shutil
import subprocess
import sys


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


def _date(day: int) -> str:
    return f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}"


def series_uptrend(pd):
    rows = []
    for day in range(90):
        base = 10.0 + day * 0.09 + (day % 5) * 0.12
        if day >= 78:  # 最近的回踩：贴近 MA10
            base -= (day - 77) * 0.06
        rows.append(
            {
                "trade_date": _date(day),
                "open": base - 0.08,
                "high": base + 0.25,
                "low": base - 0.22,
                "close": base,
                "volume": 800_000.0 + (day % 9) * 12_000.0,
            }
        )
    return pd.DataFrame(rows)


def series_breakout(pd):
    """强突破 + 整理 + 再次转强：命中 power_breakout_reentry 的全部条件。"""
    n = 70
    rows = []
    for day in range(n):
        base = 12.0 + day * 0.05 + (day % 4) * 0.06
        open_p = base - 0.05
        high = base + 0.18
        low = base - 0.16
        close = base
        volume = 600_000.0 + (day % 6) * 8_000.0
        rows.append(
            {"trade_date": _date(day), "open": open_p, "high": high, "low": low,
             "close": close, "volume": volume}
        )
    # 突破日放在 n-4：后面正好 3 根（1 根收尾 + 2 根整理）
    b = n - 4
    pre = rows[b - 1]["close"]
    rows[b] = {
        "trade_date": _date(b),
        "open": pre * 1.02,
        "high": pre * 1.16,
        "low": pre * 1.015,
        "close": pre * 1.155,      # 收在高位（close_pos 接近 1）
        "volume": rows[b - 1]["volume"] * 3.0,
    }
    mid = (rows[b]["high"] + rows[b]["low"]) / 2.0
    for k in (1, 2):
        rows[b + k] = {
            "trade_date": _date(b + k),
            "open": mid * 1.01,
            "high": rows[b]["high"] * (0.995 + 0.004 * k),
            "low": mid * 1.005,
            "close": mid * 1.02,
            "volume": rows[b]["volume"] * 0.45,
        }
    # 最后一根：再次放量转强
    rows[b + 3] = {
        "trade_date": _date(b + 3),
        "open": mid * 1.02,
        "high": rows[b]["high"] * 1.035,
        "low": mid * 1.01,
        "close": rows[b]["high"] * 1.03,
        "volume": rows[b + 1]["volume"] * 2.2,
    }
    return pd.DataFrame(rows)


def series_platform(pd):
    """长期窄幅平台 + 末端放量突破：命中 early_maintrend / trend_platform。"""
    rows = []
    for day in range(90):
        base = 15.0 + (day % 12) * 0.03
        rows.append(
            {
                "trade_date": _date(day),
                "open": base - 0.04,
                "high": base + 0.12,
                "low": base - 0.12,
                "close": base,
                "volume": 500_000.0 + (day % 5) * 6_000.0,
            }
        )
    for k in range(1, 6):
        day = 90 - k - 1
        rows[day]["close"] = rows[day]["close"] - 0.05
        rows[day]["low"] = rows[day]["low"] - 0.04
        rows[day]["volume"] = 460_000.0
    last = rows[-1]
    prior_high = max(r["high"] for r in rows[-30:-1])
    last["open"] = prior_high * 0.99
    last["high"] = prior_high * 1.09
    last["low"] = prior_high * 0.985
    last["close"] = prior_high * 1.075
    last["volume"] = 1_600_000.0
    return pd.DataFrame(rows)


def series_down(pd):
    rows = []
    for day in range(70):
        base = 30.0 - day * 0.12 - (day % 4) * 0.05
        rows.append(
            {
                "trade_date": _date(day),
                "open": base + 0.05,
                "high": base + 0.2,
                "low": base - 0.25,
                "close": base,
                "volume": 700_000.0 + (day % 7) * 9_000.0,
            }
        )
    return pd.DataFrame(rows)


SERIES = [
    ("uptrend", series_uptrend),
    ("breakout", series_breakout),
    ("platform", series_platform),
    ("down", series_down),
]


def numeric_map(metrics):
    """把原版返回的 dict 拆成 数值/布尔 映射 与 字符串映射。"""
    numbers, texts = {}, {}
    for key, value in (metrics or {}).items():
        if isinstance(value, bool):
            numbers[key] = 1.0 if value else 0.0
        elif isinstance(value, (int, float)) and not isinstance(value, bool):
            numbers[key] = float(value)
        else:
            texts[key] = str(value)
    return numbers, texts


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

    lines = []
    lines.append("// Code generated by testdata/gen_metrics_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 黄金值由**原版 volume_price_models** 的四个结构度量函数在同一份 K 线上跑出。")
    lines.append("package volprice")
    lines.append("")
    import_index = len(lines)

    lines.append("type goldenMetricsCase struct {")
    lines.append("\tName string")
    lines.append("\tBars []goldenBar")
    lines.append("\tTrendContinuation map[string]float64")
    lines.append("\tTrendContinuationOK bool")
    lines.append("\tPowerBreakout map[string]float64")
    lines.append("\tPowerBreakoutOK bool")
    lines.append("\tPowerBreakoutDate string")
    lines.append("\tEarlyMainTrend map[string]float64")
    lines.append("\tEarlyMainTrendOK bool")
    lines.append("\tTrendPlatform map[string]float64")
    lines.append("\tTrendPlatformOK bool")
    lines.append("}")
    lines.append("")
    lines.append("var goldenMetricsCases = []goldenMetricsCase{")

    for name, builder in SERIES:
        frame = builder(pd)
        prepared = vpf.prepare_bars(frame)

        tc = vpm._trend_continuation_metrics(prepared)
        pb = vpm._power_breakout_reentry_metrics(prepared)
        em = vpm._early_maintrend_start_metrics(prepared)
        tp = vpm._trend_platform_reentry_metrics(prepared)

        tc_numbers, _ = numeric_map(tc)
        pb_numbers, pb_text = numeric_map(pb)
        em_numbers, _ = numeric_map(em)
        tp_numbers, _ = numeric_map(tp)

        lines.append("\t{")
        lines.append("\t\tName: %s," % go_str(name))
        lines.append("\t\tBars: []goldenBar{")
        for _, row in frame.iterrows():
            lines.append(
                "\t\t\t{%s, %s, %s, %s, %s, %s},"
                % (
                    go_str(row["trade_date"]),
                    go_float(row["open"]),
                    go_float(row["high"]),
                    go_float(row["low"]),
                    go_float(row["close"]),
                    go_float(row["volume"]),
                )
            )
        lines.append("\t\t},")

        def emit(field, ok_field, numbers, ok, date_field=None, texts=None):
            lines.append("\t\t%s: map[string]float64{" % field)
            for key in sorted(numbers):
                lines.append("\t\t\t%s: %s," % (go_str(key), go_float(numbers[key])))
            lines.append("\t\t},")
            lines.append("\t\t%s: %s," % (ok_field, "true" if ok else "false"))
            if date_field is not None:
                value = (texts or {}).get("breakout_date", "")
                lines.append("\t\t%s: %s," % (date_field, go_str(value)))

        emit("TrendContinuation", "TrendContinuationOK", tc_numbers, bool(tc))
        emit("PowerBreakout", "PowerBreakoutOK", pb_numbers, bool(pb),
             date_field="PowerBreakoutDate", texts=pb_text)
        emit("EarlyMainTrend", "EarlyMainTrendOK", em_numbers, bool(em))
        emit("TrendPlatform", "TrendPlatformOK", tp_numbers, bool(tp))
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

    summary = []
    for name, builder in SERIES:
        prepared = vpf.prepare_bars(builder(pd))
        summary.append(
            "%s(tc=%s,pb=%s,em=%s,tp=%s)"
            % (
                name,
                bool(vpm._trend_continuation_metrics(prepared)),
                bool(vpm._power_breakout_reentry_metrics(prepared)),
                bool(vpm._early_maintrend_start_metrics(prepared)),
                bool(vpm._trend_platform_reentry_metrics(prepared)),
            )
        )
    print(f"wrote {args.out}: " + " ".join(summary))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
