"""v3 最终规格：1% 免打扰带 + 打印一次真实权重分布 + 保存指标。"""
import json
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

P = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
         min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
         min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
         throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
         hold_mode="rebalance", exec_lag=1, fin_lag=0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"

# 打印一个真实调仓日的权重，确认公式
close, open_, amount, fin = bt.load_all()
dates = close.index
d = dates[dates.searchsorted(pd.Timestamp("2025-06-30"))]
dates2 = close.index
col_pos = {x: i for i, x in enumerate(dates2)}
ctx = {"date_pos": col_pos}
ctx["ma"] = close.rolling(200, min_periods=160).mean()
ctx["hi20"] = close.rolling(20, min_periods=15).max()
ctx["amt20"] = amount.rolling(20, min_periods=15).mean()
ctx["vol"] = close.pct_change(fill_method=None).rolling(60, min_periods=40).std()
first = close.apply(lambda s: s.first_valid_index())
ctx["first_pos"] = pd.Series({c: col_pos[v] for c, v in first.items() if v is not None and not pd.isna(v)})

t = bt.build_table(close, amount, fin, d, P, ctx)
entry = t[t["pullback_ok"]]
top = list(entry.index[:P["n"]])
w = bt.compute_weights(t["score"], top, ctx, d, P)
print("调仓日", d.date(), "候选", len(entry), "权重：")
for i, (k, v) in enumerate(sorted(w.items(), key=lambda x: -x[1]), 1):
    print(f"  {i}. {k}  score={t.loc[k,'score']:.3f}  weight={v*100:.1f}%")

res = {}
for name, (a, b) in {"2013-2026": ("2013-01-01", "2026-10-28"), "2010-2012": ("2010-09-01", "2012-12-31")}.items():
    r = bt.run(P, a, b)
    m = bt.metrics(r["equity"])
    m["trades"] = r["trades"]
    res[name] = m
    print(name, json.dumps(m, ensure_ascii=False), flush=True)
    if name == "2013-2026":
        eq = r["equity"]
        eq.to_csv(os.path.join(OUT, "equity_final.csv"), encoding="utf-8-sig")
        y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100
        y.index = y.index.year
        y.to_csv(os.path.join(OUT, "final_yearly.csv"), encoding="utf-8-sig")
        segs = {}
        for a2, b2 in [("2013-01-01", "2018-12-31"), ("2019-01-01", "2021-12-31"),
                       ("2022-01-01", "2023-12-31"), ("2024-01-01", "2026-10-28")]:
            s = eq.loc[a2:b2]
            segs[f"{a2[:4]}-{b2[:4]}"] = round((s.iloc[-1] / s.iloc[0] - 1) * 100, 1)
        res["segs"] = segs
        print("segs", segs)
        print("yearly", {int(k): round(v, 1) for k, v in y.dropna().items()})

json.dump({"params": P, "results": res}, open(os.path.join(OUT, "spec_summary.json"), "w", encoding="utf-8"),
          ensure_ascii=False, indent=1)
