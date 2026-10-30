"""补测：陡评分 + 15% 上限，看能否兼顾收益与 2019-2021 的稳定性。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score", max_weight=0.15)


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("v2_score8_cap15", mk()),
    ("s2_8_cap15", mk(weight_mode="score2", max_weight=0.15)),
    ("s2_10_cap15", mk(n=10, weight_mode="score2", max_weight=0.15)),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "reports", "fund_strategy")
    rows = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        eq = r["equity"]
        m = bt.metrics(eq)
        m.update(name=name, trades=r["trades"])
        y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1).dropna() * 100
        seg = eq.loc["2019-01-01":"2021-12-31"]
        m["y1921"] = round((seg.iloc[-1] / seg.iloc[0] - 1) * 100, 1)
        m["worst_year"] = round(y.min(), 1)
        rows.append(m)
        print(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} 2019-21={m['y1921']:6.1f}% worstYr={m['worst_year']:6.1f}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(out, "sweep8.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
