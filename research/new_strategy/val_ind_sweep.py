"""行业/估值数据引入测试。"""
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


VAL = [
    ("估值-基准", mk()),
    ("PE≤60", mk(pe_max=60)),
    ("PE≤80", mk(pe_max=80)),
    ("PE分位≤70%", mk(pe_pct_max=0.7)),
    ("估值融合 w=0.1", mk(val_weight=0.1)),
    ("估值融合 w=0.2", mk(val_weight=0.2)),
]

IND = [
    ("行业-基准", mk()),
    ("单行业≤2只", mk(ind_cap=2)),
    ("行业动量前50%", mk(ind_mom_top=0.5)),
    ("行业≤2只+动量前50%", mk(ind_cap=2, ind_mom_top=0.5)),
]


def main():
    rows = []
    for wname, start, end, variants in [
        ("2016-2026（估值）", "2016-01-01", "2026-10-28", VAL),
        ("2020-2026（行业）", "2020-01-01", "2026-10-28", IND),
    ]:
        print(f"\n=== {wname} ===", flush=True)
        for name, p in variants:
            r = bt.run(p, start, end)
            m = bt.metrics(r["equity"])
            y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
            row = dict(window=wname, name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"],
                       sharpe=m["sharpe"], calmar=round(m["cagr"] / abs(m["maxdd"]), 3),
                       worst_year=round(y.min(), 1), trades=r["trades"])
            rows.append(row)
            print(f"{name:20s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
                  f"sharpe={m['sharpe']:5.2f} calmar={row['calmar']:5.3f} worstYr={row['worst_year']:6.1f}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "val_ind_sweep.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
