"""对比：入场看回调、持有只看趋势（hold_mode=trend） vs 现状（每次重新选 top8）。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.0,
            hold_mode="rebalance")

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
rows = []
for name, hold_mode in [("rebalance（现状）", "rebalance"), ("hold_trend（持有只看趋势）", "trend")]:
    p = dict(BASE)
    p["hold_mode"] = hold_mode
    r = bt.run(p, "2013-01-01", "2026-10-28")
    m = bt.metrics(r["equity"])
    tt = r["trade_types"]
    months = len(r["states"]) or 1
    avg_eq = r["equity"].mean()
    row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
               trades=r["trades"], per_month=round(r["trades"] / months, 1),
               turnover=round(r["traded_value"] / avg_eq, 1),
               new=tt.get("new", 0), add=tt.get("add", 0), trim=tt.get("trim", 0), exit=tt.get("exit", 0))
    rows.append(row)
    y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
    print(f"{name:26s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
          f"sharpe={m['sharpe']:5.2f} trades={r['trades']:5d} ({row['per_month']}/月) turnover={row['turnover']}x "
          f"new={row['new']} exit={row['exit']} worstYr={y.min():.1f}", flush=True)

pd.DataFrame(rows).to_csv(os.path.join(OUT, "hold_mode_check.csv"), index=False, encoding="utf-8-sig")
