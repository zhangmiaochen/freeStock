"""板块组合对比：主板 / 创业板 / 科创板 及其组合，跑 v3-final。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, boards=None)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"

COMBOS = [
    ("主板", {"main"}),
    ("创业板", {"gem"}),
    ("科创板", {"star"}),
    ("主板+创业板", {"main", "gem"}),
    ("创业板+科创板", {"gem", "star"}),
    ("主板+科创板", {"main", "star"}),
    ("全部", {"main", "gem", "star"}),
]

WINDOWS = [("2020-2026", "2020-01-01", "2026-10-28"),
           ("2013-2026", "2013-01-01", "2026-10-28")]


def main():
    rows = []
    for wname, a, b in WINDOWS:
        print(f"\n=== 窗口 {wname} ===", flush=True)
        for label, boards in COMBOS:
            if wname == "2013-2026" and "star" in boards:
                continue  # 科创板 2019-07 才有，长窗口不适用
            p = dict(BASE)
            p["boards"] = boards
            r = bt.run(p, a, b)
            m = bt.metrics(r["equity"])
            y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
            row = dict(window=wname, combo=label, total=m["total"], cagr=m["cagr"],
                       dd=m["maxdd"], sharpe=m["sharpe"], trades=r["trades"],
                       worst_year=round(y.min(), 1) if len(y) else None)
            rows.append(row)
            print(f"{label:12s} total={m['total']:9.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:8.2f}% "
                  f"sharpe={m['sharpe']:5.2f} trades={r['trades']:5d} worstYr={row['worst_year']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "board_combo.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
