import sys
sys.path.insert(0, r"research\new_strategy")
import bt

BASE = dict(trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            max_weight=0.15, bench_ma=200, exec_lag=1, hold_mode="rebalance")

def one(name, **kw):
    p = dict(BASE); p.update(kw)
    r = bt.run(p, "2020-01-01", "2026-10-28")
    m = bt.metrics(r["equity"])
    print("%-26s total=%8.2f%% cagr=%6.2f%% dd=%7.2f%% sharpe=%5.2f trades=%d" % (
        name, m["total"], m["cagr"], m["maxdd"], m["sharpe"], r["trades"]), flush=True)

for n in (5, 6, 8, 10):
    one(f"n={n} score2", n=n, weight_mode="score2")
one("n=8 equal", n=8, weight_mode="equal")
one("n=5 score2 cap20", n=5, weight_mode="score2", max_weight=0.20)
one("n=6 score2 cap20", n=6, weight_mode="score2", max_weight=0.20)
