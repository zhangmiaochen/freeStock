"""核对成交笔数：拆分成 新买/加仓/减仓/清仓，并测试最小交易阈值。"""
import os
import sys

import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

BASE = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"

rows = []
for band in (0.0, 0.005, 0.01, 0.02):
    p = dict(BASE)
    p["min_trade"] = band
    r = bt.run(p, "2013-01-01", "2026-10-28")
    m = bt.metrics(r["equity"])
    tt = r["trade_types"]
    months = len(r["states"]) or 1
    avg_eq = r["equity"].mean()
    row = dict(band=band, total=m["total"], cagr=m["cagr"], dd=m["maxdd"], sharpe=m["sharpe"],
               trades=r["trades"], per_month=round(r["trades"] / months, 1),
               turnover_x=round(r["traded_value"] / avg_eq, 1),
               new=tt.get("new", 0), add=tt.get("add", 0), trim=tt.get("trim", 0),
               exit=tt.get("exit", 0), months=months)
    rows.append(row)
    print(f"band={band:.3f} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['maxdd']:7.2f}% "
          f"trades={r['trades']:5d} ({row['per_month']:4.1f}/月) 换手={row['turnover_x']:5.1f}x "
          f"new={row['new']:4d} add={row['add']:4d} trim={row['trim']:4d} exit={row['exit']:4d}", flush=True)

pd.DataFrame(rows).to_csv(os.path.join(OUT, "trade_count_check.csv"), index=False, encoding="utf-8-sig")
