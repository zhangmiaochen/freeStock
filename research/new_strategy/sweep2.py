"""第二批：在 core 基础上做风控与参数邻域测试。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=15, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=1.0,
            throttle=None)


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("n10_bear50", mk(n=10, bear_exposure=0.5)),
    ("n10_throttle", mk(n=10, throttle=[(0.15, 0.6), (0.25, 0.5)])),
    ("n10_bear50_thr", mk(n=10, bear_exposure=0.5, throttle=[(0.15, 0.6), (0.25, 0.5)])),
    ("n10_q", mk(n=10, every=3)),
    ("n10_q_bear50", mk(n=10, every=3, bear_exposure=0.5)),
    ("n8", mk(n=8)),
    ("n12", mk(n=12)),
    ("n10_val", mk(n=10, value=True)),
    ("n10_val_bear50", mk(n=10, value=True, bear_exposure=0.5)),
    ("n10_strict", mk(n=10, min_roe=12, min_np_yoy=10)),
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
        rows.append(m)
        curves[name] = eq
        print(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} trades={res['trades']:5d} segs={seg}", flush=True)

    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "reports", "fund_strategy")
    os.makedirs(out, exist_ok=True)
    pd.DataFrame(rows).to_csv(os.path.join(out, "sweep2.csv"), index=False, encoding="utf-8-sig")
    pd.DataFrame({k: v for k, v in curves.items()}).to_csv(os.path.join(out, "equity2.csv"), encoding="utf-8-sig")
    print("\n年度收益（%）")
    print(pd.DataFrame({k: yearly(v) for k, v in curves.items()}).assign(**{}).round(1).to_string())


if __name__ == "__main__":
    main()
