"""25% 止损的细节：分年影响 + 止损后走势（写 UTF-8 报告）。"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200, stop_loss=0.0)


def yearly(eq):
    s = eq.resample("YE").last()
    return (s / s.shift(1) - 1) * 100


def main():
    close, _, _, _ = bt.load_all()
    lines = []
    p0 = dict(BASE)
    r0 = bt.run(p0, "2013-01-01", "2026-10-28")
    p1 = dict(BASE); p1["stop_loss"] = 0.25
    r1 = bt.run(p1, "2013-01-01", "2026-10-28")
    m0, m1 = bt.metrics(r0["equity"]), bt.metrics(r1["equity"])
    y0, y1 = yearly(r0["equity"]), yearly(r1["equity"])
    lines.append("基准: total=%.2f cagr=%.2f dd=%.2f sharpe=%.2f" % (m0["total"], m0["cagr"], m0["maxdd"], m0["sharpe"]))
    lines.append("止损25: total=%.2f cagr=%.2f dd=%.2f sharpe=%.2f stops=%d" % (m1["total"], m1["cagr"], m1["maxdd"], m1["sharpe"], len(r1["stop_log"])))
    lines.append("")
    lines.append("年度对比（止损25 - 基准）：")
    for k in y0.dropna().index:
        lines.append("  %d: 基准 %+6.1f%%  止损25 %+6.1f%%  差 %+5.1fpp" % (k.year, y0.loc[k], y1.loc[k], y1.loc[k] - y0.loc[k]))

    log = r1["stop_log"]
    lines.append("")
    lines.append("止损明细（%d 笔）：" % len(log))
    fwd = {20: [], 60: [], 120: []}
    rebound = 0
    for ev in log:
        code = ev["code"]
        if code not in close.columns:
            continue
        s = close[code].dropna()
        idx = s.index.searchsorted(pd.Timestamp(str(ev["date"])))
        if idx >= len(s):
            continue
        base_px = s.iloc[idx]
        for n in fwd:
            if idx + n < len(s):
                fwd[n].append(s.iloc[idx + n] / base_px - 1)
        if idx + 60 < len(s) and s.iloc[idx + 60] / base_px - 1 > 0.05:
            rebound += 1
        lines.append("  %d %s 成本 %.2f 卖出 %.2f 亏 %.1f%%" % (
            ev["date"], code, ev["entry"], ev["exit"], (ev["exit"] / ev["entry"] - 1) * 100))
    lines.append("")
    for k, v in fwd.items():
        if v:
            lines.append("止损后 %3d 日：均值 %+6.2f%%  中位 %+6.2f%%  继续跌比例 %.1f%%" % (
                k, np.mean(v) * 100, np.median(v) * 100, np.mean(np.array(v) < 0) * 100))
    lines.append("卖出后 60 日内反弹 >5%% 的比例：%.1f%%" % (rebound / max(len(log), 1) * 100))
    open(os.path.join(OUT, "stop_analysis.txt"), "w", encoding="utf-8").write("\n".join(lines))


if __name__ == "__main__":
    main()
