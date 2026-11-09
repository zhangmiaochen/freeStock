"""V4 原型：日线/周频动量类策略的快速回测（向量化信号 + 逐日撮合）。

只做"规则固定、不拟合"的对照实验：
  A. 周频动量前 N（20 日涨幅）+ MA60 趋势过滤 + 指数择时 + 止损
  B. 20 日新高突破（Donchian）+ 跌破 MA20 卖出
  C. 短期反转（5 日跌幅最深、但仍在 MA60 上方）
"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "new_strategy"))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\v4"
os.makedirs(OUT, exist_ok=True)


def load():
    close, op, amt, fin = bt.load_all()
    bench = bt.load_benchmark()
    return close, op, amt, bench


def run(close, op, amt, bench, *, mode="mom", n=10, rebal=5, stop=0.0, regime=True,
        min_amt=5e7, max_pos=None, cost=0.0023, start="2013-01-01", end="2026-10-28",
        exit_rank=0):
    dates = close.index
    cols = close.columns
    i0 = dates.searchsorted(pd.Timestamp(start))
    i1 = dates.searchsorted(pd.Timestamp(end))
    ma60 = close.rolling(60, min_periods=40).mean()
    ma20 = close.rolling(20, min_periods=15).mean()
    amt20 = amt.rolling(20, min_periods=15).mean()
    hi20 = close.rolling(20, min_periods=15).max()
    ret20 = close / close.shift(20) - 1
    ret5 = close / close.shift(5) - 1
    bench_ma = bench.rolling(200, min_periods=160).mean()

    cash = 1.0
    pos = {}          # code -> shares
    entry = {}        # code -> price
    equity = []
    trades = 0
    hold_days = 0
    closed = 0

    for i in range(i0, i1 + 1):
        d = dates[i]

        # 每日：止损检查（用收盘信号，次日开盘执行）
        if stop > 0 and pos:
            for code in list(pos):
                c = close.at[d, code]
                if np.isfinite(c) and c <= entry[code] * (1 - stop):
                    nxt = dates[i + 1] if i + 1 < len(dates) else d
                    px = op.at[nxt, code]
                    if not np.isfinite(px) or px <= 0:
                        px = c
                    cash += pos.pop(code) * px * (1 - cost)
                    entry.pop(code, None)
                    trades += 1
                    closed += 1

        if i % rebal == 0 and i + 1 < len(dates):
            nxt = dates[i + 1]
            px_open = op.loc[nxt]

            # 信号
            trend = close.loc[d] > ma60.loc[d]
            liquid = amt20.loc[d] > min_amt
            eligible = trend & liquid & np.isfinite(close.loc[d])
            if regime:
                try:
                    if bench.loc[d] < bench_ma.loc[d]:
                        eligible[:] = False
                except KeyError:
                    pass
            if mode == "mom":
                sig = ret20.loc[d].where(eligible)
            elif mode == "breakout":
                sig = (close.loc[d] >= hi20.loc[d] * 0.999).astype(float).where(eligible)
                sig = sig * ret20.loc[d]   # 同分时按动量排
            else:  # reversal
                sig = (-ret5.loc[d]).where(eligible)
            top = list(sig.dropna().nlargest(n).index) if eligible.any() else []
            # 缓冲带：持仓只要还在前 exit_rank 名就不卖（降低换手）
            rank = sig.rank(ascending=False)
            keep_band = set(rank[rank <= exit_rank].dropna().index) if exit_rank else set(top)

            # 卖出不在保留区的
            for code in list(pos):
                if code in keep_band:
                    continue
                px = px_open.get(code)
                if not np.isfinite(px) or px <= 0:
                    px = close.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    continue
                cash += pos.pop(code) * px * (1 - cost)
                entry.pop(code, None)
                trades += 1
                closed += 1

            # 买入：只有空出的仓位才买（不要每周把钱全砸进去加仓）
            slots = max(0, n - len(pos))
            new_codes = [c for c in top if c not in pos][:slots]
            if new_codes:
                budget = cash / len(new_codes)
                for code in new_codes:
                    px = px_open.get(code)
                    if not np.isfinite(px) or px <= 0:
                        continue
                    spend = min(budget, cash)
                    if spend <= 0:
                        break
                    shares = spend / px
                    pos[code] = pos.get(code, 0) + shares
                    entry[code] = px
                    cash -= spend * (1 + cost)
                    trades += 1

        # 估值
        val = cash
        for code, sh in pos.items():
            c = close.at[d, code]
            if not np.isfinite(c):
                s = close.loc[:d, code].dropna()
                c = s.iloc[-1] if len(s) else 0
            val += sh * c
        equity.append((d, val))

    eq = pd.Series(dict(equity)).sort_index()
    return eq, trades


def metrics(eq):
    r = eq.pct_change().dropna()
    total = eq.iloc[-1] / eq.iloc[0] - 1
    years = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (1 + total) ** (1 / years) - 1
    dd = (eq / eq.cummax() - 1).min()
    sharpe = (r.mean() / r.std()) * np.sqrt(252) if r.std() > 0 else 0
    return dict(total=total * 100, cagr=cagr * 100, dd=dd * 100, sharpe=sharpe,
                calmar=cagr / abs(dd) if dd else 0, years=years)


def main():
    close, op, amt, bench = load()
    variants = [
        ("A 周频动量 top10 缓冲30 + 止损8%", dict(mode="mom", n=10, rebal=5, stop=0.08, regime=True, exit_rank=30)),
        ("A2 周频动量 top10 缓冲50", dict(mode="mom", n=10, rebal=5, stop=0.10, regime=True, exit_rank=50)),
        ("A3 周频动量 top10 缓冲100", dict(mode="mom", n=10, rebal=5, stop=0.10, regime=True, exit_rank=100)),
        ("B 20日新高突破 缓冲30 + 止损8%", dict(mode="breakout", n=10, rebal=5, stop=0.08, regime=True, exit_rank=30)),
        ("C 5日反转 缓冲30", dict(mode="reversal", n=10, rebal=5, stop=0.0, regime=True, exit_rank=30)),
    ]
    rows = []
    for name, kw in variants:
        eq, trades = run(close, op, amt, bench, **kw)
        m = metrics(eq)
        y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1).dropna() * 100
        row = dict(name=name, **m, worst=round(y.min(), 1), trades=trades)
        rows.append(row)
        print(f"{name:34s} total={m['total']:10.2f}% cagr={m['cagr']:7.2f}% dd={m['dd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} worst={row['worst']:6.1f} trades={trades}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "v4_proto.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
