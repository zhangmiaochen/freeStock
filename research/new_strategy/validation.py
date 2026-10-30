"""多窗口验证 + 未来函数检查。

1) 未来函数：把成交日再推后 1 天、把财报再滞后 30/60 天，看结果是否崩。
2) 过拟合：8 组参数逐年 walk-forward——只用过去 3 年挑参数，下一年验证。
"""
import json
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
V3 = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
          min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
          min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
          throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
          hold_mode="rebalance", exec_lag=1, fin_lag=0)

START, END = "2013-01-01", "2026-10-28"


def run(p):
    r = bt.run(p, START, END)
    m = bt.metrics(r["equity"])
    m["trades"] = r["trades"]
    return m, r["equity"]


def yearly(eq):
    s = eq.resample("YE").last()
    return (s / s.shift(1) - 1) * 100


print("=== 1) 未来函数检查 ===", flush=True)
look = []
for name, kw in [
    ("base（T+1 开盘，财报当日可用）", {}),
    ("成交推迟到 T+2", {"exec_lag": 2}),
    ("财报再滞后 30 天", {"fin_lag": 30}),
    ("财报再滞后 60 天", {"fin_lag": 60}),
]:
    p = dict(V3)
    p.update(kw)
    m, _ = run(p)
    look.append({"case": name, **{k: m[k] for k in ("total", "cagr", "maxdd", "sharpe")}})
    print(f"{name:32s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% sharpe={m['sharpe']:5.2f}", flush=True)

print("\n=== 2) 参数网格 + 逐年 walk-forward ===", flush=True)
grid = []
for n in (8, 10):
    for wm in ("score", "score2"):
        for dl in (-15, -20):
            p = dict(V3)
            p.update(n=n, weight_mode=wm, dd_lo=dl)
            grid.append((f"n{n}_{wm}_dd{abs(dl)}", p))

curves, rows = {}, []
for name, p in grid:
    m, eq = run(p)
    curves[name] = eq
    y = yearly(eq).dropna()
    rows.append({"name": name, "total": m["total"], "cagr": m["cagr"], "dd": m["maxdd"],
                 "sharpe": m["sharpe"], **{f"y{int(k.year)}": round(v, 1) for k, v in y.items()}})
    print(f"{name:18s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% sharpe={m['sharpe']:5.2f}", flush=True)

df = pd.DataFrame(rows)
df.to_csv(os.path.join(OUT, "grid_yearly.csv"), index=False, encoding="utf-8-sig")

# walk-forward：每年用过去 3 年（按 Calmar = 复合/最大回撤）挑，下一年验证
years = list(range(2017, 2027))
wf_rows = []
chain = 1.0
for y in years:
    train = [y - 3, y - 2, y - 1]
    best, best_score = None, -1e9
    for name, eq in curves.items():
        seg = eq.loc[f"{train[0]}-01-01": f"{train[-1]}-12-31"]
        if len(seg) < 100:
            continue
        tot = seg.iloc[-1] / seg.iloc[0] - 1
        dd = (seg / seg.cummax() - 1).min()
        score = tot / abs(dd) if dd < 0 else tot
        if score > best_score:
            best, best_score = name, score
    ret = yearly(curves[best]).get(pd.Timestamp(f"{y}-12-31"), np.nan)
    if np.isnan(ret):
        continue
    chain *= 1 + ret / 100
    wf_rows.append({"year": y, "picked": best, "train_score": round(best_score, 2),
                    "test_return": round(ret, 1), "chain": round((chain - 1) * 100, 1)})
    print(f"{y} 训练{train} -> 选 {best:16s} 验证收益 {ret:+6.1f}%  累计 {((chain-1)*100):+7.1f}%", flush=True)

pd.DataFrame(wf_rows).to_csv(os.path.join(OUT, "walkforward.csv"), index=False, encoding="utf-8-sig")
v3_chain = 1.0
for y in years:
    v = yearly(curves["n8_score2_dd20"]).get(pd.Timestamp(f"{y}-12-31"), np.nan)
    if not np.isnan(v):
        v3_chain *= 1 + v / 100
print(f"\nwalk-forward 2017-2026 累计: {((chain-1)*100):+.1f}%")
print(f"固定 v3（n8_score2_dd20）同期: {((v3_chain-1)*100):+.1f}%")
print(f"网格里最好的一年: {df['total'].max():.2f}% / 最差: {df['total'].min():.2f}%")
