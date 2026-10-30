"""测试：不在名单的亏损持仓，是否该等回本/部分回本再卖。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0,
            hold_losers=False, recover_to=0.0, loser_max_months=0, loser_ma=0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


VARIANTS = [
    ("基准：立即卖", mk()),
    ("等回本（不限时）", mk(hold_losers=True, recover_to=0.0, loser_max_months=0)),
    ("等回本（最多6月）", mk(hold_losers=True, recover_to=0.0, loser_max_months=6)),
    ("等回本（最多12月）", mk(hold_losers=True, recover_to=0.0, loser_max_months=12)),
    ("回到 -5% 就卖（6月）", mk(hold_losers=True, recover_to=-0.05, loser_max_months=6)),
    ("回到 -10% 就卖（6月）", mk(hold_losers=True, recover_to=-0.10, loser_max_months=6)),
    ("趋势没破就拿着（MA200）", mk(hold_losers=True, recover_to=0.0, loser_max_months=0, loser_ma=200)),
    ("站上MA60就卖", mk(hold_losers=True, recover_to=0.0, loser_max_months=0, loser_ma=60)),
]


def main():
    start, end = "2013-01-01", "2026-10-28"
    rows = []
    for name, p in VARIANTS:
        r = bt.run(p, start, end)
        m = bt.metrics(r["equity"])
        y = (r["equity"].resample("YE").last() / r["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        row = dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                   trades=r["trades"], loser_holds=r["loser_holds"], sold_at_loss=r["sold_at_loss"],
                   worst_year=round(y.min(), 1))
        rows.append(row)
        print(f"{name:26s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} 暂缓={r['loser_holds']:4d} 亏卖={r['sold_at_loss']:4d} "
              f"trades={r['trades']:5d} worstYr={row['worst_year']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "hold_losers.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
