"""PE 分位过滤的稳健性：阈值平台 + 分年。"""
import os
import sys

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


def main():
    start, end = "2016-01-01", "2026-10-28"
    rows, eqs = [], {}
    variants = [("基准", mk())] + [(f"PE分位≤{int(t*100)}%", mk(pe_pct_max=t)) for t in (0.5, 0.6, 0.7, 0.8, 0.9)]
    for name, p in variants:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        eqs[name] = r["equity"]
        row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                   calmar=round(m["cagr"] / abs(m["maxdd"]), 3), worst_year=round(y.min(), 1), trades=r["trades"])
        rows.append(row)
        print(f"{name:14s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} calmar={row['calmar']:5.3f} worstYr={row['worst_year']:6.1f}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "pe_sweep.csv"), index=False, encoding="utf-8-sig")

    print("\n分年对比（PE分位≤70% vs 基准）：")
    yb = (eqs["基准"].resample("YE").last() / eqs["基准"].resample("YE").last().shift(1) - 1) * 100
    yp = (eqs["PE分位≤70%"].resample("YE").last() / eqs["PE分位≤70%"].resample("YE").last().shift(1) - 1) * 100
    for k in yb.dropna().index:
        print(f"  {k.year}: 基准 {yb.loc[k]:+6.1f}%  过滤 {yp.loc[k]:+6.1f}%  差 {yp.loc[k]-yb.loc[k]:+5.1f}pp")


if __name__ == "__main__":
    main()
