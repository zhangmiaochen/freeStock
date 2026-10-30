"""压力窗口对比：基准 vs 提高熊市仓位/止损。"""
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


CONFIGS = [
    ("基准 bear0.5", mk()),
    ("bear0.7+stop25", mk(bear_exposure=0.7, stop_loss=0.25)),
    ("bear0.8", mk(bear_exposure=0.8)),
    ("bear1.0", mk(bear_exposure=1.0)),
]

WINDOWS = [
    ("样本外 2010-09~2012-12", "2010-09-01", "2012-12-31"),
    ("2015 股灾 2015-06~2016-12", "2015-06-01", "2016-12-31"),
    ("阴跌 2021-01~2023-12", "2021-01-01", "2023-12-31"),
]


def main():
    rows = []
    for wname, a, b in WINDOWS:
        print(f"\n=== {wname} ===", flush=True)
        for name, p in CONFIGS:
            try:
                r = bt.run(p, a, b)
                m = bt.metrics(r["equity"])
                rows.append(dict(window=wname, name=name, total=m["total"], cagr=m["cagr"],
                                 dd=m["maxdd"], sharpe=m["sharpe"]))
                print(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% sharpe={m['sharpe']:5.2f}", flush=True)
            except Exception as exc:  # noqa: BLE001
                print(name, "ERR", exc, flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "stress_opt.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
