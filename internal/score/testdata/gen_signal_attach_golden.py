# -*- coding: utf-8 -*-
"""生成 `signal_attach_golden_test.go`：用 **pandas 的 merge_asof** 当基准。

`_attach_signal_candidates` 的核心就是一句
`pd.merge_asof(..., by="ts_code", direction="backward", tolerance=29)`。
这里把原实现那一段**逐句照抄**（过滤正则 → 计数 → 排序去重 → merge_asof），
让 pandas 本身当基准，Go 侧只需比对"贴完之后的 9 个 active_signal_* 列"。

刻意覆盖的边界：
  - 信号正好在 **29** 个交易日前 → 命中
  - 信号正好在 **30** 个交易日前 → **不命中**（tolerance 是 29 不是 30）
  - 同一 `(trade_date, ts_code)` 同时有 buy 与 candidate_buy → buy 胜出
  - 非 A 股代码（688 之外的北交所/指数）→ 被正则剔除
  - 左表里存在**不在交易日历内**的日期 → 这些行会被丢掉
  - 完全没有信号的票 → 全默认值（type=""、age=999、count=0）

用法：

    python internal/score/testdata/gen_signal_attach_golden.py --out internal/score/signal_attach_golden_test.go
"""

from __future__ import annotations

import argparse
import json
import math
import os
import shutil
import subprocess
import sys

LOOKBACK = 30  # 原实现 SIGNAL_CANDIDATE_LOOKBACK_TRADE_DAYS


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


def build_calendar():
    """50 个交易日的伪日历（YYYYMMDD，逐日递增、跨月正确）。"""
    dates = []
    month, day = 1, 1
    for _ in range(50):
        dates.append(int("%04d%02d%02d" % (2026, month, day)))
        day += 1
        if day > 28:
            day = 1
            month += 1
    return dates


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    import numpy as np
    import pandas as pd

    calendar = build_calendar()
    # ⚠ 键必须是**字符串**：原实现的 `trade_index_map` 来自 `_calendar_dates()`，
    # 而 frame 里的 trade_date 是字符串。用 int 键会让 map 全部落空、
    # 左表被整片丢掉（我第一次就踩了这个：rows_out=0）。
    index_of = {str(date): idx for idx, date in enumerate(calendar)}

    # ---- 左表：评分行（含两个不在日历里的日期，用来验证"丢行"行为）
    left_codes, left_dates = [], []
    for code in ("600000.SH", "000001.SZ", "300750.SZ", "430047.BJ"):
        for idx in range(0, 50, 3):
            left_codes.append(code)
            left_dates.append(calendar[idx])
    left_codes.append("600000.SH")
    left_dates.append(20269999)   # 不在日历里
    left_codes.append("000001.SZ")
    left_dates.append(20261231)   # 不在日历里

    work = pd.DataFrame({"ts_code": left_codes, "trade_date": [str(d) for d in left_dates]})
    work["_signal_row_no"] = np.arange(len(work), dtype="int64")

    # ---- 右表：信号（含重复、非 A 股、乱序）
    signal_specs = [
        # (date_idx, code, model_id, model_name, signal_type, signal_level, level_label, score)
        (0, "600000.SH", "trend_pullback", "趋势缩量回踩", "buy", "confirmed", "确认", 88.0),
        (0, "600000.SH", "box_range", "箱体低吸高抛", "candidate_buy", "candidate", "候选", 91.0),
        (10, "600000.SH", "box_range", "箱体低吸高抛", "candidate_buy", "candidate", "候选", 83.5),
        (20, "600000.SH", "trend_pullback", "趋势缩量回踩", "buy", "confirmed", "确认", 79.0),
        (25, "000001.SZ", "trend_pullback", "趋势缩量回踩", "buy", "confirmed", "确认", 86.0),
        (30, "000001.SZ", "oversold_rebound", "超跌反抽", "candidate_buy", "candidate", "候选", 77.25),
        (44, "300750.SZ", "box_range", "箱体低吸高抛", "buy", "confirmed", "确认", 92.0),
        (45, "300750.SZ", "box_range", "箱体低吸高抛", "candidate_buy", "candidate", "候选", 90.0),
        (5, "430047.BJ", "trend_pullback", "趋势缩量回踩", "buy", "confirmed", "确认", 80.0),
        (12, "688981.SH", "box_range", "箱体低吸高抛", "sell", "confirmed", "确认", 70.0),
        (12, "688981.SH", "box_range", "箱体低吸高抛", "buy", "confirmed", "确认", 71.0),
    ]
    raw = pd.DataFrame(
        [
            {
                "trade_date": calendar[idx],
                "ts_code": code,
                "model_id": model_id,
                "model_name": model_name,
                "market_state": "bull",
                "market_state_label": "牛市",
                "signal_type": signal_type,
                "signal_level": level,
                "signal_level_label": level_label,
                "score": score,
            }
            for (idx, code, model_id, model_name, signal_type, level, level_label, score) in signal_specs
        ]
    )

    # ---- 以下是 `_load_signal_candidate_frame` 的逐句复刻（只差 SQL 取数）
    frame = raw.copy()
    frame["trade_date"] = frame["trade_date"].astype(str).str.slice(0, 8)
    frame["ts_code"] = frame["ts_code"].astype(str).str.strip().str.upper()
    frame = frame[
        frame["ts_code"].str.match(r"^(?:600|601|603|605|688|689|000|001|002|003|300|301)\d{3}\.(?:SH|SZ)$", na=False)
    ].copy()
    frame["signal_type"] = frame["signal_type"].astype(str).str.strip().str.lower()
    frame["score"] = pd.to_numeric(frame.get("score"), errors="coerce").fillna(0.0)
    frame["signal_priority"] = frame["signal_type"].map({"buy": 0, "candidate_buy": 1}).fillna(9)
    frame["signal_count"] = frame.groupby(["trade_date", "ts_code"])["ts_code"].transform("size")
    frame = frame.sort_values(
        ["trade_date", "ts_code", "signal_priority", "score", "model_id"],
        ascending=[True, True, True, False, True],
    ).drop_duplicates(["trade_date", "ts_code"], keep="first")
    frame = frame[
        ["trade_date", "ts_code", "model_id", "model_name", "market_state",
         "market_state_label", "signal_type", "signal_level", "signal_level_label",
         "score", "signal_count"]
    ].copy()

    signal_df = frame.copy()
    signal_df["signal_trade_date"] = signal_df["trade_date"].astype(str).str.slice(0, 8)
    signal_df["signal_trade_idx"] = signal_df["signal_trade_date"].map(index_of)

    work["trade_date"] = work["trade_date"].astype(str).str.slice(0, 8)
    work["trade_idx"] = work["trade_date"].map(index_of)
    work = work[work["trade_idx"].notna()].copy()
    signal_df = signal_df[signal_df["signal_trade_idx"].notna()].copy()
    work["trade_idx"] = work["trade_idx"].astype("int64")
    signal_df["signal_trade_idx"] = signal_df["signal_trade_idx"].astype("int64")

    signal_df = signal_df.rename(
        columns={
            "model_id": "active_signal_model_id",
            "model_name": "active_signal_model_name",
            "signal_type": "active_signal_type",
            "signal_level": "active_signal_level",
            "signal_level_label": "active_signal_level_label",
            "score": "active_signal_score",
        }
    )
    signal_df = signal_df[
        ["ts_code", "signal_trade_date", "signal_trade_idx", "active_signal_model_id",
         "active_signal_model_name", "active_signal_type", "active_signal_level",
         "active_signal_level_label", "active_signal_score", "signal_count"]
    ].copy()

    left = work.sort_values(["trade_idx", "ts_code", "_signal_row_no"]).reset_index(drop=True)
    right = signal_df.sort_values(
        ["signal_trade_idx", "ts_code", "active_signal_type", "active_signal_score",
         "active_signal_model_id"],
        ascending=[True, True, True, False, True],
    ).reset_index(drop=True)
    merged = pd.merge_asof(
        left, right, left_on="trade_idx", right_on="signal_trade_idx",
        by="ts_code", direction="backward", tolerance=max(0, LOOKBACK - 1),
    )
    merged["signal_age"] = (
        pd.to_numeric(merged.get("trade_idx"), errors="coerce")
        - pd.to_numeric(merged.get("signal_trade_idx"), errors="coerce")
    )
    for col, default_value in (
        ("active_signal_type", ""), ("active_signal_score", 0.0),
        ("active_signal_model_id", ""), ("active_signal_model_name", ""),
        ("active_signal_level", ""), ("active_signal_level_label", ""),
        ("signal_trade_date", ""), ("signal_age", 999), ("signal_count", 0),
    ):
        if col not in merged.columns:
            merged[col] = default_value
    merged["active_signal_type"] = merged["active_signal_type"].fillna("").astype(str).str.strip().str.lower()
    merged["active_signal_score"] = pd.to_numeric(merged.get("active_signal_score"), errors="coerce").fillna(0.0)
    merged["active_signal_model_id"] = merged["active_signal_model_id"].fillna("").astype(str).str.strip()
    merged["active_signal_model_name"] = merged["active_signal_model_name"].fillna("").astype(str).str.strip()
    merged["active_signal_level"] = merged["active_signal_level"].fillna("").astype(str).str.strip()
    merged["active_signal_level_label"] = merged["active_signal_level_label"].fillna("").astype(str).str.strip()
    merged["signal_trade_date"] = merged["signal_trade_date"].fillna("").astype(str).str.slice(0, 8)
    merged["signal_age"] = pd.to_numeric(merged.get("signal_age"), errors="coerce").fillna(999).astype(int)
    merged["signal_count"] = pd.to_numeric(merged.get("signal_count"), errors="coerce").fillna(0).astype(int)
    merged = merged.sort_values("_signal_row_no").drop(
        columns=["_signal_row_no", "trade_idx", "signal_trade_idx"], errors="ignore"
    )

    # ---- 生成 Go
    lines = []
    lines.append("// Code generated by testdata/gen_signal_attach_golden.py; DO NOT EDIT.")
    lines.append("//")
    lines.append("// 基准是 **pandas 的 merge_asof**：原实现 `_attach_signal_candidates`")
    lines.append("// 的核心就是它，这里让 pandas 本身当 oracle。")
    lines.append("package score")
    lines.append("")
    lines.append("var goldenSignalAttachCalendar = []int64{")
    lines.append("\t" + ", ".join(str(d) for d in calendar) + ",")
    lines.append("}")
    lines.append("")
    lines.append("// goldenSignalAttachRawSignals 是**未过滤、未去重**的原始信号。")
    lines.append("var goldenSignalAttachRawSignals = []SignalCandidateRow{")
    for item in signal_specs:
        idx, code, model_id, model_name, signal_type, level, level_label, score = item
        lines.append(
            "\t{TradeDate: %d, Code: %s, ModelID: %s, ModelName: %s, SignalType: %s, SignalLevel: %s, SignalLevelLabel: %s, Score: %s},"
            % (calendar[idx], go_str(code), go_str(model_id), go_str(model_name),
               go_str(signal_type), go_str(level), go_str(level_label), go_float(score))
        )
    lines.append("}")
    lines.append("")
    lines.append("// goldenSignalAttachDeduped 是原实现过滤+去重之后的候选（Go 侧直接用这个）。")
    lines.append("var goldenSignalAttachDeduped = []SignalCandidateRow{")
    for _, row in frame.iterrows():
        lines.append(
            "\t{TradeDate: %s, Code: %s, ModelID: %s, ModelName: %s, MarketState: %s, MarketStateLabel: %s, SignalType: %s, SignalLevel: %s, SignalLevelLabel: %s, Score: %s, SignalCount: %d},"
            % (str(int(row["trade_date"])), go_str(row["ts_code"]), go_str(row["model_id"]),
               go_str(row["model_name"]), go_str(row["market_state"]),
               go_str(row["market_state_label"]), go_str(row["signal_type"]),
               go_str(row["signal_level"]), go_str(row["signal_level_label"]),
               go_float(row["score"]), int(row["signal_count"]))
        )
    lines.append("}")
    lines.append("")
    lines.append("var goldenSignalAttachRowCodes = []string{")
    lines.append("\t" + ", ".join(go_str(c) for c in left_codes) + ",")
    lines.append("}")
    lines.append("var goldenSignalAttachRowDates = []int64{")
    lines.append("\t" + ", ".join(str(d) for d in left_dates) + ",")
    lines.append("}")
    lines.append("")
    lines.append("type goldenSignalAttachWant struct {")
    lines.append("\tCode string")
    lines.append("\tTradeDate int64")
    lines.append("\tType string")
    lines.append("\tAge int")
    lines.append("\tScore float64")
    lines.append("\tModelID string")
    lines.append("\tModelName string")
    lines.append("\tLevel string")
    lines.append("\tLevelLabel string")
    lines.append("\tSignalTradeDate int64")
    lines.append("\tCount int")
    lines.append("}")
    lines.append("")
    lines.append("var goldenSignalAttachRows = []goldenSignalAttachWant{")
    for _, row in merged.iterrows():
        signal_date = str(row["signal_trade_date"]).strip()
        lines.append("\t{")
        lines.append("\t\tCode: %s," % go_str(row["ts_code"]))
        lines.append("\t\tTradeDate: %s," % str(int(row["trade_date"])))
        lines.append("\t\tType: %s," % go_str(row["active_signal_type"]))
        lines.append("\t\tAge: %d," % int(row["signal_age"]))
        lines.append("\t\tScore: %s," % go_float(row["active_signal_score"]))
        lines.append("\t\tModelID: %s," % go_str(row["active_signal_model_id"]))
        lines.append("\t\tModelName: %s," % go_str(row["active_signal_model_name"]))
        lines.append("\t\tLevel: %s," % go_str(row["active_signal_level"]))
        lines.append("\t\tLevelLabel: %s," % go_str(row["active_signal_level_label"]))
        lines.append("\t\tSignalTradeDate: %s," % (signal_date if signal_date and signal_date != "nan" else "0"))
        lines.append("\t\tCount: %d," % int(row["signal_count"]))
        lines.append("\t},")
    lines.append("}")
    lines.append("")

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines))
    gofmt = shutil.which("gofmt")
    if gofmt:
        subprocess.run([gofmt, "-w", args.out], check=True)

    matched = int((merged["signal_age"] != 999).sum())
    print(f"wrote {args.out}: calendar={len(calendar)} rows_in={len(left_codes)} "
          f"rows_out={len(merged)} matched={matched} candidates={len(frame)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
