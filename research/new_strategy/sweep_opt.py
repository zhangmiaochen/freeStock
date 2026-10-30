"""优化方向筛选：择时/止损/调仓频率/回撤节流。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200,
            stop_loss=0.0, bench_dd_throttle=None)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("基准（bear0.5/月频）", mk()),
    ("熊市空仓（bear0.0）", mk(bear_exposure=0.0)),
    ("熊市0.3", mk(bear_exposure=0.3)),
    ("熊市0.7", mk(bear_exposure=0.7)),
    ("指数MA120", mk(bench_ma=120)),
    ("指数MA250", mk(bench_ma=250)),
    ("指数回撤15%→×0.7", mk(bench_dd_throttle=[(0.15, 0.7)])),
    ("指数回撤20%→×0.5", mk(bench_dd_throttle=[(0.20, 0.5)])),
    ("个股止损15%", mk(stop_loss=0.15)),
    ("个股止损20%", mk(stop_loss=0.20)),
    ("个股止损25%", mk(stop_loss=0.25)),
    ("双月调仓", mk(every=2)),
    ("季度调仓", mk(every=3)),
    ("组合回撤15%→×0.5", mk(throttle=[(0.15, 0.5)])),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    rows = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        calmar = m["total"] / abs(m["maxdd"]) if m["maxdd"] else 0
        row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                   calmar=round(calmar, 1), worst_year=round(y.min(), 1), trades=r["trades"])
        rows.append(row)
        print(f"{name:22s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} calmar={calmar:6.1f} worstYr={row['worst_year']:6.1f} trades={r['trades']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "sweep_opt.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
