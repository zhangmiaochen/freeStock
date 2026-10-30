"""第七批：把单票上限放宽到 25%/30%，看收益与风险的交换比。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=10, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score", max_weight=1.0)


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("v2_score8_cap15", mk(n=8, max_weight=0.15)),
    ("score8_cap20", mk(n=8, max_weight=0.20)),
    ("score8_cap25", mk(n=8, max_weight=0.25)),
    ("score8_cap30", mk(n=8, max_weight=0.30)),
    ("s2_8_cap20", mk(n=8, weight_mode="score2", max_weight=0.20)),
    ("s2_8_cap25", mk(n=8, weight_mode="score2", max_weight=0.25)),
    ("s2_8_cap30", mk(n=8, weight_mode="score2", max_weight=0.30)),
    ("s2_6_cap25", mk(n=6, weight_mode="score2", max_weight=0.25)),
    ("s2_6_cap30", mk(n=6, weight_mode="score2", max_weight=0.30)),
]


def yearly(eq):
    return (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100


SEGS = [("2013-01-01", "2018-12-31"), ("2019-01-01", "2021-12-31"),
        ("2022-01-01", "2023-12-31"), ("2024-01-01", "2026-10-28")]


def main():
    start, end = "2013-01-01", "2026-10-28"
    rows, curves = [], {}
    for name, p in VARIANTS:
        res = bt.run(p, start, end)
        eq = res["equity"]
        m = bt.metrics(eq)
        m.update(trades=res["trades"], name=name)
        seg = []
        for a, b in SEGS:
            s = eq.loc[a:b]
            seg.append(round((s.iloc[-1] / s.iloc[0] - 1) * 100, 1) if len(s) else None)
        m.update(zip(["s1", "s2", "s3", "s4"], seg))
        y = yearly(eq).dropna()
        m["worst_year"] = round(y.min(), 1)
        m["best_year"] = round(y.max(), 1)
        m["neg_years"] = int((y < 0).sum())
        rows.append(m)
        curves[name] = eq
        print(f"{name:18s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} worstYr={m['worst_year']:6.1f} segs={seg}", flush=True)

    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "reports", "fund_strategy")
    pd.DataFrame(rows).to_csv(os.path.join(out, "sweep7.csv"), index=False, encoding="utf-8-sig")
    pd.DataFrame(curves).to_csv(os.path.join(out, "equity7.csv"), encoding="utf-8-sig")
    print("\n年度收益（%）")
    print(pd.DataFrame({k: yearly(v) for k, v in curves.items()}).round(1).to_string())


if __name__ == "__main__":
    main()
