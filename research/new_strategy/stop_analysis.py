"""25% 止损是否"割在最低点"：止损明细 + 后续走势 + 分年影响 + 参数敏感性。"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200, stop_loss=0.0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("基准（无止损）", mk()),
    ("止损 20%", mk(stop_loss=0.20)),
    ("止损 25%", mk(stop_loss=0.25)),
    ("止损 30%", mk(stop_loss=0.30)),
    ("止损 35%", mk(stop_loss=0.35)),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    close, _, _, _ = bt.load_all()
    rows = []
    eqs = {}
    stop_log = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        eqs[name] = r["equity"]
        rows.append(dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"],
                         sharpe=m["sharpe"], worst_year=round(y.min(), 1), stops=len(r["stop_log"])))
        if name == "止损 25%":
            stop_log = r["stop_log"]
        print(f"{name:14s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} worstYr={y.min():6.1f} stops={len(r['stop_log'])}", flush=True)

    pd.DataFrame(rows).to_csv(os.path.join(OUT, "stop_sensitivity.csv"), index=False, encoding="utf-8-sig")

    # 分年影响：止损25 vs 基准
    yb = (eqs["基准（无止损）"].resample("YE").last() / eqs["基准（无止损）"].resample("YE").last().shift(1) - 1) * 100
    ys = (eqs["止损 25%"].resample("YE").last() / eqs["止损 25%"].resample("YE").last().shift(1) - 1) * 100
    print("\n年度对比（止损25 - 基准，pp）：")
    for k in yb.dropna().index:
        y = k.year
        print(f"  {y}: 基准 {yb.loc[k]:+6.1f}%  止损25 {ys.loc[k]:+6.1f}%  差 {ys.loc[k]-yb.loc[k]:+5.1f}")

    # 止损后走势：卖出后 20/60/120 个交易日的收益，以及是否"卖飞"
    print("\n止损明细分析（25%）：")
    fwd = {20: [], 60: [], 120: []}
    rebound = 0
    for ev in stop_log:
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
    n = len(stop_log)
    print(f"  止损次数 {n}")
    for k, v in fwd.items():
        if v:
            print(f"  止损后 {k:3d} 日：均值 {np.mean(v)*100:+6.2f}%  中位 {np.median(v)*100:+6.2f}%  "
                  f"继续跌的比例 {np.mean(np.array(v)<0)*100:4.1f}%")
    print(f"  卖出后 60 日内反弹超 5% 的比例：{rebound/max(n,1)*100:.1f}%")


if __name__ == "__main__":
    main()
