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
    ("n8_bear50", mk(n=8, bear_exposure=0.5)),
    ("n8_thr", mk(n=8, throttle=[(0.15, 0.6), (0.25, 0.5)])),
    ("n8_bear50_thr", mk(n=8, bear_exposure=0.5, throttle=[(0.15, 0.6), (0.25, 0.5)])),
    ("n8_ma250", mk(n=8, trend_ma=250)),
    ("n8_dd20", mk(n=8, dd_lo=-20, dd_hi=-2)),
    ("n8_roe12", mk(n=8, min_roe=12)),
    ("n8_q", mk(n=8, every=3)),
    ("n8_bear50_dd20", mk(n=8, bear_exposure=0.5, dd_lo=-20, dd_hi=-2)),
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
    pd.DataFrame(rows).to_csv(os.path.join(out, "sweep3.csv"), index=False, encoding="utf-8-sig")
    pd.DataFrame({k: v for k, v in curves.items()}).to_csv(os.path.join(out, "equity3.csv"), encoding="utf-8-sig")
    print("\n年度收益（%）")
    print(pd.DataFrame({k: yearly(v) for k, v in curves.items()}).assign(**{}).round(1).to_string())


if __name__ == "__main__":
    main()
