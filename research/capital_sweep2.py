import sys
sys.path.insert(0, r"research\new_strategy")
import bt

BASE = dict(trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            max_weight=0.15, bench_ma=200, exec_lag=1, hold_mode="rebalance")

def one(name, start="2020-01-01", **kw):
    p = dict(BASE); p.update(kw)
    r = bt.run(p, start, "2026-10-28")
    m = bt.metrics(r["equity"])
    print("%-30s total=%8.2f%% cagr=%6.2f%% dd=%7.2f%% sharpe=%5.2f trades=%d" % (
        name, m["total"], m["cagr"], m["maxdd"], m["sharpe"], r["trades"]), flush=True)

one("n=8 equal every=2", every=2, n=8, weight_mode="equal")
one("n=8 score2 every=2", every=2, n=8, weight_mode="score2")
one("n=8 equal 2013-2026", start="2013-01-01", n=8, weight_mode="equal")
one("n=8 score2 2013-2026", start="2013-01-01", n=8, weight_mode="score2")
one("n=12 equal", n=12, weight_mode="equal")
one("n=8 equal noBear", n=8, weight_mode="equal", bear_exposure=1.0)
