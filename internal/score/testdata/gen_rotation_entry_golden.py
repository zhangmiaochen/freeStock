# -*- coding: utf-8 -*-
"""生成 `rotation_entry_golden_test.go`：用**原版** `initial_entry_layers`
（portfolio_rotation_logic.py:1438-1540）跑出建仓层数的黄金值。

为什么要专门做这个：那个函数里有 **4 组信号条件、二十多个阈值**
（`bull_ready` / `range_ready` / `bull_fill_ready` / `range_fill_ready`），
外加 `capacity_pressure` 的比例判定。Go 侧是照着读代码抄的 ——
`1.002` 抄成 `1.02`、`>=` 抄成 `>`、`-16` 抄成 `-6`，**读一遍根本看不出来**，
而后果是"牛市首次建仓 1 层还是 2 层"，直接差一半仓位。

所以这里的做法是：对**每一个阈值都取"刚好不到 / 刚好等于 / 刚好超过"三点**，
再让原版当裁判。任何一位抄错都会在这些点上立刻暴露。

策略构造是确定性的：`build_rotation_policy(state, target, *,
total_layers=..., max_layers_per_stock=...)`，其中 `desired_total_layers`
在 bear 下是 `max(max_layers, (layers+1)//2)`、其余是 `layers`
（portfolio_rotation_logic.py:204/221/237）—— 这个值也一并写进黄金值，
用来顺带校验 Go 的 `BuildStrategyConfig` 算得对不对。

用法：

    python internal/score/testdata/gen_rotation_entry_golden.py \
        --original "D:\\workspace\\python\\stock\\tushare-stock-tonyz\\pythonProject\\pythonProject" \
        --out internal/score/rotation_entry_golden_test.go
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys


def go_str(value) -> str:
    return json.dumps(str(value), ensure_ascii=False)


def go_float(value) -> str:
    f = float(value)
    return repr(f)


# 各行字段的"中性"底稿：默认**不满足**任何一组信号条件（无信号、低分）。
BASE_ROW = {
    "strategy_id": "trend_growth",
    "total_score": 70.0,
    "trend_score": 20.0,
    "structure_score": 10.0,
    "rs_score": 8.0,
    "active_signal_type": "",
    "signal_age": 999,
    "active_signal_score": 0.0,
    "close_price": 100.0,
    "ma20_price": 100.0,
    "ma30_price": 100.0,
    "drawdown_from_high_120": 0.0,
}

# 命中 `bull_ready` 的一组（原版 :1467-1482）：
#   bull / buy / age<=2 / 80,22,12,10 / signal_score>=68 / close>0 /
#   close>=ma20*0.998 / close>=ma30*1.002 / -16<=dd120<=4
BULL_READY = {
    "active_signal_type": "buy", "signal_age": 2, "active_signal_score": 68.0,
    "total_score": 80.0, "trend_score": 22.0, "structure_score": 12.0, "rs_score": 10.0,
    "close_price": 100.0, "ma20_price": 100.0, "ma30_price": 95.0,
    "drawdown_from_high_120": 0.0,
}

# 命中 `bull_fill_ready`（原版 :1504-1519）：门槛更低、年龄更宽
BULL_FILL = {
    "active_signal_type": "buy", "signal_age": 4, "active_signal_score": 66.0,
    "total_score": 77.0, "trend_score": 21.0, "structure_score": 11.0, "rs_score": 9.0,
    "close_price": 100.0, "ma20_price": 100.0, "ma30_price": 95.0,
    "drawdown_from_high_120": 0.0,
}

# 命中 `range_ready`（原版 :1483-1498）
RANGE_READY = {
    "active_signal_type": "buy", "signal_age": 2, "active_signal_score": 74.0,
    "total_score": 84.0, "trend_score": 23.0, "structure_score": 13.0, "rs_score": 11.0,
    "close_price": 100.0, "ma20_price": 100.0, "ma30_price": 95.0,
    "drawdown_from_high_120": -5.0,
}

# 命中 `range_fill_ready`（原版 :1520-1535）
RANGE_FILL = {
    "active_signal_type": "buy", "signal_age": 2, "active_signal_score": 70.0,
    "total_score": 81.0, "trend_score": 21.0, "structure_score": 11.0, "rs_score": 9.0,
    "close_price": 100.0, "ma20_price": 100.0, "ma30_price": 95.0,
    "drawdown_from_high_120": -5.0,
}


def build_cases():
    """返回 (描述, 行覆盖, 策略参数, free_layers, remaining_slot_need, target)。"""
    cases = []

    def add(desc, row_over, state, *, total_layers=10, max_layers=3, free=0, need=0, target=5):
        cases.append((desc, row_over, state, total_layers, max_layers, free, need, target))

    # ---- 1. 策略分流（低吸/高手/超跌/趋势成长 × 三种状态 × 层数上限）----
    #
    # ⚠ **低吸的 `max_layers_per_stock` 在生产里恒等于 `total_layers`**：
    # 原版调用处是 `max_layers_per_stock = max(1, self._legacy_total_layers())
    # if strategy_id == STRATEGY_TREND_PULLBACK_LOW_BUY else 3`
    #（portfolio_service.py:1032 / :2309），Go 的 `BuildStrategyConfig` 也强制覆盖。
    # 所以这里必须**按调用方的口径**给值 —— 让两者自由组合会造出不可达用例，
    # 我第一版就是这么错的（max_layers=1 配 total_layers=10），
    # 那个"分歧"在生产里永远不会出现。
    for strategy in ("trend_pullback_low_buy", "expert_simulation", "oversold_rebound", "trend_growth"):
        for state in ("bull", "range", "bear"):
            for max_layers in (1, 2, 3):
                for total_layers in (1, 2, 10, 20):
                    effective_max = max(1, total_layers) if strategy == "trend_pullback_low_buy" else max_layers
                    add(f"分流 {strategy}/{state}/max{effective_max}/total{total_layers}",
                        {"strategy_id": strategy}, state,
                        total_layers=total_layers, max_layers=effective_max, free=10, need=0)
    # 超跌的分数边界（>=70 → 2 层）
    for score in (69.9, 70.0, 70.1):
        add(f"超跌 score={score}", {"strategy_id": "oversold_rebound", "total_score": score},
            "range", free=10)

    # ---- 2. 四组条件的阈值边界（±0.1）----
    # 每组给出 (条件名, 状态, 基准行, {字段: [低于, 等于, 高于]})
    groups = [
        # bull_ready
        ("bull_ready", "bull", BULL_READY, {
            "total_score": [79.9, 80.0, 80.1],
            "trend_score": [21.9, 22.0, 22.1],
            "structure_score": [11.9, 12.0, 12.1],
            "rs_score": [9.9, 10.0, 10.1],
            "active_signal_score": [67.9, 68.0, 68.1],
            "signal_age": [2, 3],
            "drawdown_from_high_120": [4.0, 4.1, -16.0, -16.1],
        }),
        # range_ready
        ("range_ready", "range", RANGE_READY, {
            "total_score": [83.9, 84.0, 84.1],
            "trend_score": [22.9, 23.0, 23.1],
            "structure_score": [12.9, 13.0, 13.1],
            "rs_score": [10.9, 11.0, 11.1],
            "active_signal_score": [73.9, 74.0, 74.1],
            "signal_age": [2, 3],
            "drawdown_from_high_120": [-1.0, -0.9, -18.0, -18.1],
        }),
        # bull_fill_ready
        ("bull_fill_ready", "bull", BULL_FILL, {
            "total_score": [76.9, 77.0, 77.1],
            "trend_score": [20.9, 21.0, 21.1],
            "structure_score": [10.9, 11.0, 11.1],
            "rs_score": [8.9, 9.0, 9.1],
            "active_signal_score": [65.9, 66.0, 66.1],
            "signal_age": [4, 5],
            "drawdown_from_high_120": [4.0, 4.1, -20.0, -20.1],
        }),
        # range_fill_ready
        ("range_fill_ready", "range", RANGE_FILL, {
            "total_score": [80.9, 81.0, 81.1],
            "trend_score": [20.9, 21.0, 21.1],
            "structure_score": [10.9, 11.0, 11.1],
            "rs_score": [8.9, 9.0, 9.1],
            "active_signal_score": [69.9, 70.0, 70.1],
            "drawdown_from_high_120": [-1.0, -0.9, -20.0, -20.1],
        }),
    ]
    for name, state, base, dims in groups:
        for field, values in dims.items():
            for value in values:
                row = dict(base)
                row[field] = value
                add(f"{name} {field}={value}", row, state, free=10, need=0)

    # ---- 3. 均线守卫（close 相对 ma 的比例，逐个压边界）----
    #   bull_ready : close >= ma20*0.998 → ma20=100 时 close>=99.8
    #                close >= ma30*1.002 → ma30=100 时 close>=100.2
    for close in (99.7, 99.8, 99.9, 100.0, 100.1, 100.2, 100.3):
        for ma20, ma30 in ((100.0, 100.0), (0.0, 0.0), (0.0, 100.0), (100.0, 0.0)):
            row = dict(BULL_READY)
            row.update({"close_price": close, "ma20_price": ma20, "ma30_price": ma30})
            add(f"bull_ready 均线 close={close} ma20={ma20} ma30={ma30}", row, "bull", free=10)
    for close in (98.5, 99.0, 99.5, 99.9, 100.0):
        row = dict(RANGE_READY)
        row.update({"close_price": close, "ma20_price": 100.0, "ma30_price": 100.0})
        add(f"range_ready 均线 close={close} ma20=ma30=100", row, "range", free=10)
    # close<=0 → 四组全部不成立
    for name, state, base in (("bull_ready", "bull", BULL_READY), ("range_ready", "range", RANGE_READY),
                              ("bull_fill_ready", "bull", BULL_FILL), ("range_fill_ready", "range", RANGE_FILL)):
        row = dict(base)
        row["close_price"] = 0.0
        add(f"{name} close=0", row, state, free=10)

    # ---- 4. 信号类型与状态不匹配 ----
    for signal_type in ("", "buy", "candidate_buy", "sell", "BUY"):
        for state, base in (("bull", BULL_READY), ("range", RANGE_READY)):
            row = dict(base)
            row["active_signal_type"] = signal_type
            add(f"信号类型 {signal_type!r} @{state}", row, state, free=10)

    # ---- 5. capacity_pressure（free_layers / remaining_slot_need / 比例门槛）----
    #   free>0 且 free>=max(2, need+1) 且 free/desired_total_layers>=0.2
    #   total_layers=10、bull → desired_total_layers=10 → 比例门槛 = 2
    for free in (0, 1, 2, 3, 10):
        for need in (0, 1, 2, 5):
            for state, base in (("bull", BULL_FILL), ("range", RANGE_FILL)):
                add(f"capacity free={free} need={need} @{state}", base, state, free=free, need=need)
    # 比例边界：total_layers=20 → 门槛 4；total_layers=5 → 门槛 1
    for total_layers in (5, 10, 20):
        for free in (0, 1, 2, 3, 4, 5):
            add(f"capacity 比例 total={total_layers} free={free}", BULL_FILL, "bull",
                total_layers=total_layers, free=free, need=0)
    # bear 下 desired_total_layers = max(max_layers, (layers+1)//2)
    for total_layers in (10, 20):
        for max_layers in (2, 3):
            add(f"bear desired total={total_layers} max={max_layers}",
                dict(BULL_FILL, strategy_id="trend_growth"), "bear",
                total_layers=total_layers, max_layers=max_layers, free=10)
    # 有信号但 free=0 → capacity_pressure 不成立（只有 ready 两支能命中）
    for state, base in (("bull", BULL_FILL), ("range", RANGE_FILL)):
        add(f"capacity free=0 @{state}", base, state, free=0, need=0)

    return cases


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--original", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    if args.original not in sys.path:
        sys.path.insert(0, args.original)

    import portfolio_rotation_logic as rot  # noqa: E402
    import portfolio_strategy as strat  # noqa: E402

    def policy_of(strategy_id, state, target, total_layers, max_layers):
        """按**生产路径**构造 policy。

        ⚠ 原版有两个 policy 构造器，bear 分支公式**不一样**：

            build_rotation_policy        : desired_total_layers = max(max_layers, (layers+1)//2)
            policy_from_strategy_config  : desired_total_layers = max(1, (layers+1)//2)

        而 `build_rotation_plan`（:2012）是
        `policy_from_strategy_config(strategy_config) if strategy_config is not None
         else build_rotation_policy(...)`，生产**总是**传 strategy_config
        → 走的是**后者**。

        我第一版用了前者，于是报出一批"Go 少减了一半层数"的假分歧 ——
        实际是 Go 对的、我的 oracle 错了。这条弯路值得留在注释里：
        做 oracle 时，"生产走哪条路"必须先确认，否则会把对的改错。
        """
        config = strat.build_strategy_config(
            strategy_id, state,
            target_position_count=target, total_layers=total_layers,
            max_layers_per_stock=max_layers, market_scope=None,
        )
        return rot.policy_from_strategy_config(config)

    lines = []
    lines.append("// Code generated by testdata/gen_rotation_entry_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 黄金值由**原版 initial_entry_layers**（portfolio_rotation_logic.py:1438-1540）跑出。")
    lines.append("package score")
    lines.append("")
    lines.append("type goldenEntryLayerCase struct {")
    lines.append("\tDesc string")
    lines.append("\tStrategyID string")
    lines.append("\tMarketState string")
    lines.append("\tTargetPositionCount int")
    lines.append("\tTotalLayers int")
    lines.append("\tMaxLayersPerStock int")
    lines.append("\tDesiredTotalLayers int")
    lines.append("\tFreeLayers int")
    lines.append("\tRemainingSlotNeed int")
    lines.append("\tTotalScore float64")
    lines.append("\tTrendScore float64")
    lines.append("\tStructureScore float64")
    lines.append("\tRSScore float64")
    lines.append("\tSignalType string")
    lines.append("\tSignalAge int")
    lines.append("\tSignalScore float64")
    lines.append("\tClose float64")
    lines.append("\tMa20 float64")
    lines.append("\tMa30 float64")
    lines.append("\tDrawdown120 float64")
    lines.append("\tWant int")
    lines.append("}")
    lines.append("")
    lines.append("var goldenEntryLayerCases = []goldenEntryLayerCase{")

    count = 0
    for desc, row_over, state, total_layers, max_layers, free, need, target in build_cases():
        row = dict(BASE_ROW)
        row.update(row_over)
        policy = policy_of(row["strategy_id"], state, target, total_layers, max_layers)
        want = int(rot.initial_entry_layers(row, policy, free_layers=free, remaining_slot_need=need))
        # 用 policy 的实际值（而不是入参）：`build_strategy_config` 可能覆盖
        lines.append("\t{")
        lines.append("\t\tDesc: %s," % go_str(desc))
        lines.append("\t\tStrategyID: %s, MarketState: %s," % (
            go_str(row["strategy_id"]), go_str(policy.market_state)))
        lines.append("\t\tTargetPositionCount: %d, TotalLayers: %d, MaxLayersPerStock: %d," % (
            target, total_layers, int(policy.max_layers_per_stock)))
        lines.append("\t\tDesiredTotalLayers: %d, FreeLayers: %d, RemainingSlotNeed: %d," % (
            int(policy.desired_total_layers), free, need))
        lines.append("\t\tTotalScore: %s, TrendScore: %s, StructureScore: %s, RSScore: %s," % (
            go_float(row["total_score"]), go_float(row["trend_score"]),
            go_float(row["structure_score"]), go_float(row["rs_score"])))
        lines.append("\t\tSignalType: %s, SignalAge: %d, SignalScore: %s," % (
            go_str(row["active_signal_type"]), int(row["signal_age"]),
            go_float(row["active_signal_score"])))
        lines.append("\t\tClose: %s, Ma20: %s, Ma30: %s, Drawdown120: %s," % (
            go_float(row["close_price"]), go_float(row["ma20_price"]),
            go_float(row["ma30_price"]), go_float(row["drawdown_from_high_120"])))
        lines.append("\t\tWant: %d," % want)
        lines.append("\t},")
        count += 1
    lines.append("}")
    lines.append("")

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines))
    gofmt = shutil.which("gofmt")
    if gofmt:
        subprocess.run([gofmt, "-w", args.out], check=True)
    print(f"wrote {args.out}: {count} cases")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
