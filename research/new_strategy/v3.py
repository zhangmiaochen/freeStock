"""v3：score2 + 8 只 + cap15%，输出分年/分段并保存净值。"""
import json
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

P = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
         min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
         min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
         weight_mode="score2", max_weight=0.15)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"

r = bt.run(P, "2013-01-01", "2026-10-28")
m = bt.metrics(r["equity"])
eq = r["equity"]
y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100
seg = [("2013-01-01", "2018-12-31"), ("2019-01-01", "2021-12-31"),
       ("2022-01-01", "2023-12-31"), ("2024-01-01", "2026-10-28")]
segs = {}
for a, b in seg:
    s = eq.loc[a:b]
    segs[f"{a[:4]}-{b[:4]}"] = round((s.iloc[-1] / s.iloc[0] - 1) * 100, 1)
print(json.dumps(m, ensure_ascii=False))
print("segs", segs)
print(" ".join("%d:%+.1f" % (k.year, v) for k, v in y.dropna().items()))
eq.to_csv(os.path.join(OUT, "equity_v3.csv"), encoding="utf-8-sig")

r2 = bt.run(P, "2010-09-01", "2012-12-31")
print("OOS", json.dumps(bt.metrics(r2["equity"]), ensure_ascii=False))
json.dump({"params": P, "metrics": m, "segs": segs, "oos": bt.metrics(r2["equity"])},
          open(os.path.join(OUT, "v3_summary.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
