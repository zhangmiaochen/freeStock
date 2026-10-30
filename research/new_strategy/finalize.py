"""最终候选策略：跑全区间 + 样本外区间，保存净值与指标。"""
import json
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

P = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
         min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
         min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
         weight_mode="score", max_weight=0.15)

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "reports", "fund_strategy")
os.makedirs(OUT, exist_ok=True)

runs = {
    "2013-2026": ("2013-01-01", "2026-10-28"),
    "2010-2012(OOS)": ("2010-09-01", "2012-12-31"),
}
res = {}
for name, (a, b) in runs.items():
    r = bt.run(P, a, b)
    m = bt.metrics(r["equity"])
    m["trades"] = r["trades"]
    res[name] = m
    print(name, json.dumps(m, ensure_ascii=False), flush=True)
    r["equity"].to_csv(os.path.join(OUT, f"equity_{name.split('-')[0]}.csv"), encoding="utf-8-sig")

json.dump({"params": P, "results": res}, open(os.path.join(OUT, "final_summary.json"), "w", encoding="utf-8"),
          ensure_ascii=False, indent=1, default=str)

# 年度收益
eq = pd.read_csv(os.path.join(OUT, "equity_2013.csv"), index_col=0, parse_dates=True).iloc[:, 0]
y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100
y.index = y.index.year
y.to_csv(os.path.join(OUT, "final_yearly.csv"), encoding="utf-8-sig")
print(y.round(1).to_string())
