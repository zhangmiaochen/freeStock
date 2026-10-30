"""因子实验：ROE 同比趋势 / 增速加速度（过滤 vs 打分融合）。"""
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
            stop_loss=0.0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("基准", mk()),
    ("过滤 ROE趋势≥0", mk(min_roe_trend=0.0)),
    ("过滤 加速度≥0", mk(min_accel=0.0)),
    ("过滤 两者≥0", mk(min_roe_trend=0.0, min_accel=0.0)),
    ("过滤 ROE趋势≥-2", mk(min_roe_trend=-2.0)),
    ("过滤 加速度≥-5", mk(min_accel=-5.0)),
    ("融合 w=0.2 两者", mk(factor_weight=0.2)),
    ("融合 w=0.3 两者", mk(factor_weight=0.3)),
    ("融合 w=0.2 ROE趋势", mk(factor_weight=0.2, factor_mix="trend")),
    ("融合 w=0.2 加速度", mk(factor_weight=0.2, factor_mix="accel")),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    rows = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        calmar = m["cagr"] / abs(m["maxdd"]) if m["maxdd"] else 0
        row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                   calmar=round(calmar, 3), worst_year=round(y.min(), 1), trades=r["trades"])
        rows.append(row)
        print(f"{name:20s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} calmar={calmar:5.3f} worstYr={row['worst_year']:6.1f}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "factor_sweep.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
