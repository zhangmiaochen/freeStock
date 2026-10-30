"""持有期测试：最短持有锁定 / 最长持有强制换仓。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200, stop_loss=0.0,
            min_hold_months=0, max_hold_months=0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("基准（自然持有 ~1.7月）", mk()),
    ("最短持有 2 月", mk(min_hold_months=2)),
    ("最短持有 3 月", mk(min_hold_months=3)),
    ("最短持有 4 月", mk(min_hold_months=4)),
    ("最长持有 3 月", mk(max_hold_months=3)),
    ("最长持有 6 月", mk(max_hold_months=6)),
    ("最长持有 12 月", mk(max_hold_months=12)),
    ("最短3 + 最长6", mk(min_hold_months=3, max_hold_months=6)),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    rows = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                   calmar=round(m["cagr"] / abs(m["maxdd"]), 3), worst_year=round(y.min(), 1),
                   trades=r["trades"], avg_hold=round(r["avg_hold_months"], 2))
        rows.append(row)
        print(f"{name:22s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} calmar={row['calmar']:5.3f} 平均持有={row['avg_hold']:.2f}月", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "hold_sweep.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
