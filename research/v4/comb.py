"""V4：动能 + 布林 + 角度（均线斜率）组合测试。

规则（收盘信号、次日开盘成交、单边 0.23%）：
  A 布林反弹+动能+角度 : 前一日收盘在下轨下方、今日收回下轨上；20 日动能>0；MA20 斜率>0
                          卖出：涨破上轨 / MA20 斜率转负
  B 角度趋势           : MA20 斜率>0 且收盘上穿 MA20
                          卖出：收盘跌破 MA20
  C 动能+中轨          : 20 日动能>5% 且收盘上穿布林中轨
                          卖出：收盘跌破中轨
  D 布林收口突破       : 带宽处于低位后收盘突破上轨，且 MA20 斜率>0
                          卖出：跌破中轨
"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "new_strategy"))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\v4"


def build(close, mode):
    ma20 = close.rolling(20, min_periods=15).mean()
    ma60 = close.rolling(60, min_periods=50).mean()
    mid = close.rolling(20, min_periods=15).mean()
    sd = close.rolling(20, min_periods=15).std()
    upper, lower = mid + 2 * sd, mid - 2 * sd
    roc20 = close / close.shift(20) - 1
    slope20 = ma20 / ma20.shift(10) - 1          # MA20 的 10 日斜率（角度）
    slope5 = ma20 / ma20.shift(5) - 1
    bw = (upper - lower) / mid.replace(0, np.nan)
    bw_low = bw.rolling(60, min_periods=40).quantile(0.2)

    buy = pd.DataFrame(False, index=close.index, columns=close.columns)
    sell = pd.DataFrame(False, index=close.index, columns=close.columns)

    if mode == "A":
        buy = (close.shift(1) <= lower.shift(1)) & (close > lower) & (roc20 > 0) & (slope20 > 0)
        sell = (close > upper) | (slope20 < 0)
    elif mode == "B":
        buy = (close > ma20) & (close.shift(1) <= ma20.shift(1)) & (slope20 > 0)
        sell = close < ma20
    elif mode == "C":
        buy = (roc20 > 0.05) & (close > mid) & (close.shift(1) <= mid.shift(1))
        sell = close < mid
    elif mode == "D":
        buy = (bw < bw_low) & (close > upper) & (slope20 > 0) & (close > ma60)
        sell = close < mid
    return buy.fillna(False), sell.fillna(False), ma20


def run(close, op, amt, buy, sell, *, n=10, cost=0.0023, start="2013-01-01", end="2026-10-28",
        min_amt=5e7, min_hold=3):
    dates = close.index
    i0 = dates.searchsorted(pd.Timestamp(start))
    i1 = dates.searchsorted(pd.Timestamp(end))
    amt20 = amt.rolling(20, min_periods=15).mean()
    buy_prev = buy.shift(1).fillna(False)

    cash, pos, entry, buy_idx = 1.0, {}, {}, {}
    eq, trades, closed, wins, hold_sum = [], 0, 0, 0, 0

    for i in range(i0, i1 + 1):
        d = dates[i]
        for code in list(pos):
            if pos[code].get("sell") and (i - buy_idx[code]) >= min_hold:
                px = op.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    px = close.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    pos.pop(code); entry.pop(code, None); buy_idx.pop(code, None); continue
                cash += pos.pop(code)["sh"] * px * (1 - cost)
                closed += 1; trades += 1
                hold_sum += i - buy_idx.pop(code)
                if px > entry.pop(code, px):
                    wins += 1

        if len(pos) < n:
            ev = cash
            for c2, p2 in pos.items():
                c2v = close.at[d, c2]
                ev += p2["sh"] * (c2v if np.isfinite(c2v) else entry.get(c2, 0.0))
            cands = []
            for code in close.columns:
                if code in pos or not buy_prev.at[d, code]:
                    continue
                a = amt20.at[d, code]
                if not (np.isfinite(a) and a >= min_amt):
                    continue
                cands.append(code)
                if len(cands) >= n - len(pos):
                    break
            for code in cands:
                px = op.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    continue
                budget = min(cash, ev / n)
                if budget <= 0:
                    break
                pos[code] = dict(sh=budget / px, sell=False)
                entry[code] = px
                buy_idx[code] = i
                cash -= budget * (1 + cost)
                trades += 1

        for code in pos:
            if sell.at[d, code]:
                pos[code]["sell"] = True

        v = cash
        for code, p in pos.items():
            c = close.at[d, code]
            if not np.isfinite(c):
                s = close.loc[:d, code].dropna()
                c = s.iloc[-1] if len(s) else 0
            v += p["sh"] * c
        eq.append((d, v))

    e = pd.Series(dict(eq)).sort_index()
    r = e.pct_change().dropna()
    total = e.iloc[-1] / e.iloc[0] - 1
    years = (e.index[-1] - e.index[0]).days / 365.25
    cagr = (1 + total) ** (1 / years) - 1
    dd = (e / e.cummax() - 1).min()
    sharpe = (r.mean() / r.std()) * np.sqrt(252) if r.std() > 0 else 0
    return dict(total=total * 100, cagr=cagr * 100, dd=dd * 100, sharpe=sharpe,
                calmar=cagr / abs(dd) if dd else 0, trades=trades,
                win=(wins / closed * 100) if closed else 0, hold=(hold_sum / closed) if closed else 0)


def main():
    close, op, amt, fin = bt.load_all()
    rows = []
    for mode, name in [("A", "布林反弹+动能+角度"), ("B", "角度趋势(上穿MA20)"),
                       ("C", "动能+中轨"), ("D", "布林收口突破")]:
        buy, sell, _ = build(close, mode)
        m = run(close, op, amt, buy, sell)
        rows.append(dict(name=name, **m))
        print(f"{name:18s} total={m['total']:9.2f}% cagr={m['cagr']:6.2f}% dd={m['dd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} win={m['win']:5.1f}% hold={m['hold']:5.1f}d trades={m['trades']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "comb.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
