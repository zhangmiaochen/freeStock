"""批量跑基本面策略的参数组合，输出对比表。"""
import json
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=15, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=1.0)


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("fund", mk(trend_ma=0, pullback=False)),
    ("trend", mk(pullback=False)),
    ("core", mk()),
    ("core_q", mk(every=3)),
    ("core_bear50", mk(bear_exposure=0.5)),
    ("core_bear50_q", mk(bear_exposure=0.5, every=3)),
    ("core_n10", mk(n=10)),
    ("core_n20", mk(n=20)),
    ("core_ma120", mk(trend_ma=120)),
    ("core_strict", mk(min_roe=12, min_np_yoy=10)),
]


def yearly(eq):
    return (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100


def main():
    start, end = "2013-01-01", "2026-10-28"
    bench = bt.load_benchmark()
    b = bench.loc[start:end]
    bm = bt.metrics((b / b.iloc[0]))
    rows = []
    curves = {}
    for name, p in VARIANTS:
        res = bt.run(p, start, end)
        eq = res["equity"]
        m = bt.metrics(eq)
        m["trades"] = res["trades"]
        m["name"] = name
        rows.append(m)
        curves[name] = eq
        print(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} trades={res['trades']:5d}", flush=True)
    print(f"{'CSI300':16s} total={bm['total']:8.2f}% cagr={bm['cagr']:6.2f}% dd={bm['maxdd']:7.2f}% "
          f"sharpe={bm['sharpe']:5.2f}", flush=True)

    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "reports", "fund_strategy")
    os.makedirs(out, exist_ok=True)
    pd.DataFrame(rows).to_csv(os.path.join(out, "sweep.csv"), index=False, encoding="utf-8-sig")

    ydf = pd.DataFrame({name: yearly(eq) for name, eq in curves.items()})
    ydf["CSI300"] = yearly(b)
    ydf.index = ydf.index.year
    ydf.to_csv(os.path.join(out, "yearly.csv"), encoding="utf-8-sig")
    print("\n年度收益（%）")
    print(ydf.round(1).to_string())

    # 保存所有净值
    eqdf = pd.DataFrame({name: eq for name, eq in curves.items()})
    eqdf["CSI300"] = b / b.iloc[0]
    eqdf.to_csv(os.path.join(out, "equity.csv"), encoding="utf-8-sig")


if __name__ == "__main__":
    main()
