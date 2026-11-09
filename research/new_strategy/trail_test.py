"""测试：涨出回调带就卖，卖飞的代价 + 移动止盈能不能救。"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
            hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200, stop_loss=0.0,
            trail_exit=0.0, trail_winners_only=False)


def mk(**kw):
    p = dict(BASE)
    p.update(kw)
    return p


def metrics(eq):
    return bt.metrics(eq)


def main():
    start, end = "2013-01-01", "2026-10-28"
    close, _, _, _ = bt.load_all()
    lines = []

    # ---- 1) 卖出后的走势：区分"涨出去"和"跌出去" ----
    r = bt.run(mk(), start, end)
    log = r["exit_log"]
    lines.append(f"基准卖出次数 {len(log)}")
    for label, subset in [("仍在MA200上方（多为涨出回调带）", [e for e in log if e["above_ma"]]),
                          ("已跌破MA200（趋势/基本面变差）", [e for e in log if not e["above_ma"]])]:
        fwd = {20: [], 60: [], 120: []}
        fly = 0
        for ev in subset:
            code = ev["code"]
            if code not in close.columns:
                continue
            s = close[code].dropna()
            idx = s.index.searchsorted(pd.Timestamp(str(ev["date"])))
            if idx >= len(s):
                continue
            base_px = s.iloc[idx]
            for n in fwd:
                if idx + n < len(s):
                    fwd[n].append(s.iloc[idx + n] / base_px - 1)
            if idx + 60 < len(s) and s.iloc[idx + 60] / base_px - 1 > 0.10:
                fly += 1
        lines.append(f"\n{label}：{len(subset)} 笔")
        for n, v in fwd.items():
            if v:
                lines.append(f"  卖出后 {n:3d} 日：均值 {np.mean(v)*100:+6.2f}%  中位 {np.median(v)*100:+6.2f}%  "
                             f"上涨比例 {np.mean(np.array(v)>0)*100:4.1f}%")
        lines.append(f"  卖出后 60 日涨超 10% 的比例（卖飞率）：{fly/max(len(subset),1)*100:.1f}%")

    # ---- 2) 移动止盈 ----
    lines.append("\n=== 移动止盈（掉出名单但趋势未破 → 继续持有，回撤超阈值再卖）===")
    rows = []
    for name, p in [("基准", mk()),
                    ("移动止盈 10%", mk(trail_exit=0.10)),
                    ("移动止盈 15%", mk(trail_exit=0.15)),
                    ("移动止盈 20%", mk(trail_exit=0.20)),
                    ("移动止盈 25%", mk(trail_exit=0.25)),
                    ("只留盈利的·15%", mk(trail_exit=0.15, trail_winners_only=True))]:
        rr = bt.run(p, start, end)
        m = metrics(rr["equity"])
        y = (rr["equity"].resample("YE").last() / rr["equity"].resample("YE").last().shift(1) - 1).dropna() * 100
        rows.append(dict(name=name, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
                         calmar=round(m["cagr"] / abs(m["maxdd"]), 3), worst_year=round(y.min(), 1),
                         hold=round(rr["avg_hold_months"], 2)))
        lines.append(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
                     f"sharpe={m['sharpe']:5.2f} calmar={rows[-1]['calmar']:5.3f} worstYr={rows[-1]['worst_year']:6.1f} "
                     f"hold={rows[-1]['hold']:.2f}月")
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "trail_sweep.csv"), index=False, encoding="utf-8-sig")
    open(os.path.join(OUT, "trail_analysis.txt"), "w", encoding="utf-8").write("\n".join(lines))


if __name__ == "__main__":
    main()
