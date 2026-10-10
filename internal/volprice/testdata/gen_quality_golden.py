# -*- coding: utf-8 -*-
"""生成 `quality_golden_test.go`：用**原版模型模块**算行为质量/箱体/超跌修复的黄金值。

覆盖点：
  - `_behavior_quality` 的 6 个枚举 + 分数 + 死亡缩量标记
  - `_behavior_condition_text` / `_behavior_quality_ok` / `_behavior_trade_block_reasons`
  - `_behavior_score_adjust` 的 clamp 与减 8 分支
  - `_box_window_metrics`（numpy 切片 + nonzero 位置）与 `_box_structure_quality_fast`
  - `_oversold_rebound_quality` 多组输入
  - `_candidate_trade_block_reasons` 的**全部分支**（硬原因/软原因为空/进度不足/软原因过多/强承接不足/通过）

用法：

    python internal/volprice/testdata/gen_quality_golden.py \
        --original "D:\\workspace\\python\\stock\\tushare-stock-tonyz\\pythonProject\\pythonProject" \
        --out internal/volprice/quality_golden_test.go
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


def go_string_slice(values) -> str:
    if not values:
        return "nil"
    return "[]string{" + ", ".join(go_str(v) for v in values) + "}"


def series_panic(pd):
    """连续急跌 + 恐慌放量：压 panic_volume / no_acceptance / death_volume。"""
    rows = []
    for day in range(60):
        if day < 45:
            base = 40.0 - day * 0.05
            volume = 700_000.0
        else:
            base = 38.0 - (day - 44) * 0.9
            volume = 700_000.0 * (1.0 + (day - 44) * 0.55)
        open_p = base + 0.35
        high = base + 0.5
        low = base - 0.55
        close = base - 0.4
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": open_p, "high": high, "low": low, "close": close, "volume": volume}
        )
    return pd.DataFrame(rows)


def series_dying(pd):
    """连续缩量 + 破前低 + 收在低位：压 death_volume（衰竭缩量）分支。

    这一支之前 6 种形态一条都没命中，于是 `_behavior_quality_ok` 的
    "死亡缩量直接不合格"、`_behavior_score_adjust` 的减 8、以及箱体里的
    "存在死亡缩量风险" 都**没有被验证过**。
    """
    rows = []
    for day in range(44):
        base = 20.0 + (day % 5) * 0.04
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": base, "high": base + 0.08, "low": base - 0.08,
             "close": base, "volume": 1_000_000.0}
        )
    prev_close = rows[-1]["close"]
    prev_low = rows[-1]["low"]
    volumes = [900_000.0, 800_000.0, 700_000.0, 600_000.0, 500_000.0, 400_000.0]
    for k, volume in enumerate(volumes):
        day = 44 + k
        open_p = prev_close * 0.995
        high = open_p * 1.002
        low = open_p * 0.985
        close = low + 0.15 * (high - low)   # 收在低位 → close_pos ≈ 0.15
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": open_p, "high": high, "low": low, "close": close, "volume": volume}
        )
        prev_close, prev_low = close, low
    return pd.DataFrame(rows)


def series_distribution(pd):
    """高位放量长上影：压 distribution_volume / selling_pressure。"""
    rows = []
    for day in range(60):
        base = 20.0 + day * 0.08
        rows.append(
            {"trade_date": f"2026{(day // 28) + 1:02d}{(day % 28) + 1:02d}",
             "open": base - 0.1, "high": base + 0.12, "low": base - 0.14,
             "close": base, "volume": 600_000.0}
        )
    for k in range(4, 0, -1):
        day = 60 - k
        open_p = rows[day]["close"]
        rows[day] = {
            "trade_date": rows[day]["trade_date"],
            "open": open_p, "high": open_p * 1.06, "low": open_p * 0.995,
            "close": open_p * 1.005, "volume": 600_000.0 * 2.6,
        }
    return pd.DataFrame(rows)


def load_metrics_series(here):
    """复用 gen_metrics_golden 里的四种形态，避免两处各写一份。"""
    spec = importlib.util.spec_from_file_location(
        "gen_metrics_golden", os.path.join(here, "gen_metrics_golden.py")
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return list(module.SERIES)


REBOUND_INPUTS = [
    (-15.0, True, True),
    (-8.0, False, False),
    (-3.0, True, False),
    (-22.0, True, True),
]

BLOCK_INPUTS = [
    # (hits, misses, soft, hard, min_score, require_strong, min_progress, max_soft)
    (["c1", "c2", "c3"], [], [], [], 45.0, True, 85.0, 1),
    (["c1"], ["m1", "m2", "m3"], [], [], 45.0, True, 85.0, 1),
    (["c1", "c2"], ["m1"], ["s1", "s2"], [], 45.0, True, 85.0, 1),
    (["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"], ["m1"], ["s1"], [], 45.0, True, 85.0, 1),
    (["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"], ["m1"], ["s1", "s2"], [], 45.0, True, 85.0, 1),
    (["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"], ["m1"], ["s1"], [], 45.0, False, 85.0, 1),
    (["c1"], ["m1"], ["s1"], ["硬原因A"], 45.0, True, 85.0, 1),
    (["c1"], ["m1"], ["s1"], [], 95.0, True, 85.0, 1),
]


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

    here = os.path.dirname(os.path.abspath(__file__))
    series = load_metrics_series(here) + [
        ("panic", series_panic),
        ("distribution", series_distribution),
        ("dying", series_dying),
    ]

    lines = []
    lines.append("// Code generated by testdata/gen_quality_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 黄金值由**原版 volume_price_models** 的行为质量/箱体/超跌修复函数跑出。")
    lines.append("package volprice")
    lines.append("")
    import_index = len(lines)

    lines.append("type goldenReboundCase struct {")
    lines.append("\tFall5 float64")
    lines.append("\tPanic bool")
    lines.append("\tRebound bool")
    lines.append("\tValues map[string]float64")
    lines.append("\tReasons string")
    lines.append("}")
    lines.append("")
    lines.append("type goldenBlockCase struct {")
    lines.append("\tHits []string")
    lines.append("\tMisses []string")
    lines.append("\tSoft []string")
    lines.append("\tHard []string")
    lines.append("\tMinScore float64")
    lines.append("\tRequireStrong bool")
    lines.append("\tMinProgress float64")
    lines.append("\tMaxSoft int")
    lines.append("\tWant string")
    lines.append("}")
    lines.append("")
    lines.append("type goldenQualityCase struct {")
    lines.append("\tName string")
    lines.append("\tBars []goldenBar")
    lines.append("\tBehaviorScore float64")
    lines.append("\tBehaviorDeath bool")
    lines.append("\tVolumeBehavior string")
    lines.append("\tAcceptanceQuality string")
    lines.append("\tUpperShadowBehavior string")
    lines.append("\tBehaviorText string")
    lines.append("\tScoreAdjust float64")
    lines.append("\tQualityOK45 bool")
    lines.append("\tBlockReasons45 string")
    lines.append("\tBoxWindow map[string]float64")
    lines.append("\tBoxWindowOK bool")
    lines.append("\tBoxQuality map[string]float64")
    lines.append("\tBoxReasons string")
    lines.append("\tRebounds []goldenReboundCase")
    lines.append("\tBlocks []goldenBlockCase")
    lines.append("}")
    lines.append("")
    lines.append("var goldenQualityCases = []goldenQualityCase{")

    for name, builder in series:
        frame = builder(pd)
        prepared = vpf.prepare_bars(frame)
        feature = vpf.calculate_feature_snapshot_prepared(prepared)

        behavior = vpm._behavior_quality(feature)
        box_window = vpm._box_window_metrics(prepared)
        box_quality = vpm._box_structure_quality_fast(prepared, feature)

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
        lines.append("\t\tBehaviorScore: %s," % go_float(behavior.get("score")))
        lines.append("\t\tBehaviorDeath: %s," % ("true" if behavior.get("death_volume_risk") else "false"))
        lines.append("\t\tVolumeBehavior: %s," % go_str(behavior.get("volume_behavior", "")))
        lines.append("\t\tAcceptanceQuality: %s," % go_str(behavior.get("acceptance_quality", "")))
        lines.append("\t\tUpperShadowBehavior: %s," % go_str(behavior.get("upper_shadow_behavior", "")))
        lines.append("\t\tBehaviorText: %s," % go_str(vpm._behavior_condition_text(feature, quality=behavior)))
        lines.append("\t\tScoreAdjust: %s," % go_float(vpm._behavior_score_adjust(feature, quality=behavior)))
        lines.append("\t\tQualityOK45: %s," % ("true" if vpm._behavior_quality_ok(feature, min_score=45.0, quality=behavior) else "false"))
        lines.append("\t\tBlockReasons45: %s," % go_str("|".join(vpm._behavior_trade_block_reasons(feature, min_score=45.0, quality=behavior))))

        lines.append("\t\tBoxWindow: map[string]float64{")
        for key in sorted(box_window or {}):
            lines.append("\t\t\t%s: %s," % (go_str(key), go_float(box_window[key])))
        lines.append("\t\t},")
        lines.append("\t\tBoxWindowOK: %s," % ("true" if box_window else "false"))

        lines.append("\t\tBoxQuality: map[string]float64{")
        for key in sorted(box_quality or {}):
            value = box_quality[key]
            if key == "block_reasons":
                continue
            if isinstance(value, bool):
                lines.append("\t\t\t%s: %s," % (go_str(key), go_float(1.0 if value else 0.0)))
            elif isinstance(value, (int, float)):
                lines.append("\t\t\t%s: %s," % (go_str(key), go_float(value)))
        lines.append("\t\t},")
        lines.append("\t\tBoxReasons: %s," % go_str("|".join(box_quality.get("block_reasons") or [])))

        lines.append("\t\tRebounds: []goldenReboundCase{")
        for fall5, panic, rebound in REBOUND_INPUTS:
            result = vpm._oversold_rebound_quality(prepared, feature, fall5, panic, rebound)
            lines.append("\t\t\t{")
            lines.append("\t\t\t\tFall5: %s, Panic: %s, Rebound: %s,"
                         % (go_float(fall5), "true" if panic else "false", "true" if rebound else "false"))
            lines.append("\t\t\t\tValues: map[string]float64{")
            for key in sorted(result or {}):
                value = result[key]
                if key == "block_reasons":
                    continue
                if isinstance(value, bool):
                    lines.append("\t\t\t\t\t%s: %s," % (go_str(key), go_float(1.0 if value else 0.0)))
                elif isinstance(value, (int, float)):
                    lines.append("\t\t\t\t\t%s: %s," % (go_str(key), go_float(value)))
            lines.append("\t\t\t\t},")
            lines.append("\t\t\t\tReasons: %s," % go_str("|".join(result.get("block_reasons") or [])))
            lines.append("\t\t\t},")
        lines.append("\t\t},")

        lines.append("\t\tBlocks: []goldenBlockCase{")
        for hits, misses, soft, hard, min_score, strong, progress, max_soft in BLOCK_INPUTS:
            want = vpm._candidate_trade_block_reasons(
                feature,
                min_score=min_score,
                confirmation_hits=hits,
                confirmation_misses=misses,
                hard_reasons=hard,
                soft_reasons=soft,
                min_progress=progress,
                max_soft_misses=max_soft,
                require_strong_acceptance=strong,
                quality=behavior,
            )
            lines.append("\t\t\t{")
            lines.append("\t\t\t\tHits: %s, Misses: %s, Soft: %s, Hard: %s,"
                         % (go_string_slice(hits), go_string_slice(misses),
                            go_string_slice(soft), go_string_slice(hard)))
            lines.append("\t\t\t\tMinScore: %s, RequireStrong: %s, MinProgress: %s, MaxSoft: %d,"
                         % (go_float(min_score), "true" if strong else "false", go_float(progress), max_soft))
            lines.append("\t\t\t\tWant: %s," % go_str("|".join(want)))
            lines.append("\t\t\t},")
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

    print(f"wrote {args.out}: {len(series)} cases, "
          f"{len(REBOUND_INPUTS)} rebound inputs, {len(BLOCK_INPUTS)} block inputs each")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
