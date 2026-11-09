"""打分稳定性：财务指标不常变，分数/名单到底多常变？"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

P = dict(n=8, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
         min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
         min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
         throttle=None, weight_mode="score2", max_weight=0.15, min_trade=0.01,
         hold_mode="rebalance", exec_lag=1, fin_lag=0, bench_ma=200, stop_loss=0.0)

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"


def main():
    close, op, amt, fin = bt.load_all()
    dates = close.index
    col_pos = {d: i for i, d in enumerate(dates)}
    ctx = {"date_pos": col_pos}
    ctx["ma"] = close.rolling(200, min_periods=160).mean()
    ctx["hi20"] = close.rolling(20, min_periods=15).max()
    ctx["amt20"] = amt.rolling(20, min_periods=15).mean()
    ctx["vol"] = close.pct_change(fill_method=None).rolling(60, min_periods=40).std()
    first = close.apply(lambda s: s.first_valid_index())
    ctx["first_pos"] = pd.Series({c: col_pos[v] for c, v in first.items() if v is not None and not pd.isna(v)})

    # 2019-2026 的月末
    idx = close.loc["2019-01-01":"2026-10-28"].index
    ym = pd.Series(idx, index=idx).dt.to_period("M")
    month_ends = list(pd.to_datetime(pd.Series(idx, index=idx).groupby(ym).max().values))

    prev_all, prev_pool, prev_target = None, None, None
    rows = []
    for d in month_ends:
        t = bt.build_table(close, amt, fin, d, P, ctx, apply_pullback=False)  # 全池（含未回调的）
        scores = t["score"]
        entry = t.index[t["pullback_ok"].to_numpy()]
        target = list(t.loc[entry].index[:8])
        if prev_all is not None:
            common = scores.index.intersection(prev_all.index)
            a, b = prev_all.reindex(common), scores.reindex(common)
            rho = a.corr(b, method="spearman")
            mad = float((b - a).abs().mean() * 100)
            same = float(((b - a).abs() < 1e-9).mean() * 100)
            overlap_all = len(set(scores.index) & set(prev_all.index)) / max(len(scores), 1) * 100
            overlap_target = len(set(target) & set(prev_target)) if prev_target is not None else 0
            overlap_pool = len(set(entry) & set(prev_pool)) / max(len(entry), 1) * 100 if prev_pool is not None else 0
            rows.append(dict(date=d.date(), n=len(scores), rho=rho, mad_pp=mad, same_pct=same,
                             overlap_all_pct=overlap_all, target_overlap=overlap_target,
                             pool_overlap_pct=overlap_pool, n_entry=len(entry)))
        prev_all, prev_pool, prev_target = scores, entry, target

    df = pd.DataFrame(rows)
    print("月份数", len(df))
    print("相邻月份：形态相关 rho 中位 %.3f（最小 %.3f）" % (df.rho.median(), df.rho.min()))
    print("相邻月份：分数平均变动 %.2f 分（满分 100）" % df.mad_pp.mean())
    print("相邻月份：分数完全没变的股票占比 %.1f%%" % df.same_pct.mean())
    print("相邻月份：候选池重合率（回调池）%.1f%%" % df.pool_overlap_pct.mean())
    print("相邻月份：目标前 8 重合数 %.2f / 8" % df.target_overlap.mean())
    print("目标前 8 完全不变的比例：%.1f%%" % (df.target_overlap == 8).mean())
    print("目标前 8 换了 ≥4 只的比例：%.1f%%" % (df.target_overlap <= 4).mean())
    df.to_csv(os.path.join(OUT, "score_stability.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
