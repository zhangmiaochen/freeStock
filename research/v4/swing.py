"""V4 备选：多级别波段（底部/顶部识别）——只在收盘信号、次日开盘成交。

变体（规则事先固定）：
  RSI  : 周线趋势向上 + 日线 RSI6 从超卖回升买入；RSI6 超买/跌破 MA20/周线转弱卖出
  BOLL : 收盘跌破布林下轨后拐头买入；回到中轨或跌破 MA200 卖出
  MACD : MACD 在零轴下方金叉买入；死叉或跌破 MA200 卖出
  ZIG  : 20 日低点反弹 5% 买入；从持仓最高点回撤 5% 或跌破 MA200 卖出
"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "new_strategy"))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\v4"


def indicators(close):
    ma200 = close.rolling(200, min_periods=160).mean()
    ma20 = close.rolling(20, min_periods=15).mean()
    delta = close.diff()
    gain = delta.clip(lower=0).rolling(6, min_periods=4).mean()
    loss = (-delta.clip(upper=0)).rolling(6, min_periods=4).mean()
    rs = gain / loss.replace(0, np.nan)
    rsi6 = 100 - 100 / (1 + rs)
    mid = close.rolling(20, min_periods=15).mean()
    sd = close.rolling(20, min_periods=15).std()
    lower, upper = mid - 2 * sd, mid + 2 * sd
    ema12 = close.ewm(span=12, adjust=False).mean()
    ema26 = close.ewm(span=26, adjust=False).mean()
    macd = ema12 - ema26
    sig = macd.ewm(span=9, adjust=False).mean()
    # 周线趋势：用上一周收盘价的 MA20（避免用本周未完成数据）
    wk = close.resample("W-FRI").last()
    wk_ma = wk.rolling(20, min_periods=15).mean()
    wk_up = (wk > wk_ma).shift(1)                    # 用上周的值
    wk_up_daily = wk_up.reindex(close.index, method="ffill")
    return dict(ma200=ma200, ma20=ma20, rsi6=rsi6, lower=lower, mid=mid,
                macd=macd, sig=sig, wk_up=wk_up_daily)


def signals(close, ind, mode):
    buy = pd.DataFrame(False, index=close.index, columns=close.columns)
    sell = pd.DataFrame(False, index=close.index, columns=close.columns)
    if mode == "rsi":
        buy = (ind["wk_up"] == True) & (ind["rsi6"] < 20) & (ind["rsi6"] > ind["rsi6"].shift(1))
        sell = (ind["rsi6"] > 80) | (close < ind["ma20"]) | (ind["wk_up"] != True)
    elif mode == "boll":
        buy = (close < ind["lower"]) & (close > ind["ma200"]) & (close > close.shift(1))
        sell = (close > ind["mid"]) | (close < ind["ma200"])
    elif mode == "macd":
        cross_up = (ind["macd"] > ind["sig"]) & (ind["macd"].shift(1) <= ind["sig"].shift(1))
        cross_dn = (ind["macd"] < ind["sig"]) & (ind["macd"].shift(1) >= ind["sig"].shift(1))
        buy = cross_up & (ind["macd"] < 0) & (close > ind["ma200"])
        sell = cross_dn | (close < ind["ma200"])
    return buy.fillna(False), sell.fillna(False)


def run(close, op, buy, sell, *, n=10, cost=0.0023, start="2013-01-01", end="2026-10-28",
        zig=False, min_amt=None, amt=None):
    dates = close.index
    i0 = dates.searchsorted(pd.Timestamp(start))
    i1 = dates.searchsorted(pd.Timestamp(end))
    ma200 = close.rolling(200, min_periods=160).mean()
    low20 = close.rolling(20, min_periods=15).min()
    amt20 = amt.rolling(20, min_periods=15).mean() if amt is not None else None
    # 信号必须用**前一日收盘**（避免当日收盘信号在当日开盘成交的未来函数）
    buy_prev = buy.shift(1).fillna(False)
    sell_prev = sell.shift(1).fillna(False)

    cash = 1.0
    pos = {}
    peak = {}
    entry = {}
    eq = []
    trades = 0
    wins = 0
    closed = 0
    hold_sum = 0
    buy_idx = {}

    for i in range(i0, i1 + 1):
        d = dates[i]
        # 开盘卖出
        for code in list(pos):
            if pos[code].get("sell"):
                px = op.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    px = close.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    pos.pop(code); peak.pop(code, None); buy_idx.pop(code, None); continue
                cash += pos[code]["sh"] * px * (1 - cost)
                closed += 1
                trades += 1
                hold_sum += i - buy_idx.get(code, i)
                if px > entry.get(code, px):
                    wins += 1
                pos.pop(code); peak.pop(code, None); entry.pop(code, None); buy_idx.pop(code, None)
        # 开盘买入
        if len(pos) < n:
            # 计算权益
            ev = cash
            for c2, p2 in pos.items():
                c2v = close.at[d, c2]
                ev += p2["sh"] * (c2v if np.isfinite(c2v) else p2["entry"])
            candidates = []
            for code in close.columns:
                if code in pos:
                    continue
                if not buy_prev.at[d, code]:
                    continue
                if zig:
                    c = close.at[d, code]
                    lo = low20.at[d, code]
                    if not (np.isfinite(c) and np.isfinite(lo)) or c < lo * 1.05:
                        continue
                if amt20 is not None and min_amt is not None:
                    a = amt20.at[d, code]
                    if not (np.isfinite(a) and a >= min_amt):
                        continue
                candidates.append(code)
                if len(candidates) >= n - len(pos):
                    break
            for code in candidates:
                px = op.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    continue
                budget = min(cash, ev / n)
                if budget <= 0:
                    break
                pos[code] = dict(sh=budget / px, sell=False, entry=px)
                peak[code] = px
                entry[code] = px
                buy_idx[code] = i
                cash -= budget * (1 + cost)
                trades += 1
        # 收盘信号
        for code in pos:
            c = close.at[d, code]
            if not np.isfinite(c):
                continue
            if c > peak.get(code, c):
                peak[code] = c
            if sell.at[d, code]:
                pos[code]["sell"] = True
            if zig and c <= peak.get(code, c) * 0.95:
                pos[code]["sell"] = True
            if zig and np.isfinite(ma200.at[d, code]) and c < ma200.at[d, code]:
                pos[code]["sell"] = True
        # 估值
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
                win=(wins / closed * 100) if closed else 0,
                hold=(hold_sum / closed) if closed else 0)


def main():
    close, op, amt, fin = bt.load_all()
    ind = indicators(close)
    rows = []
    for mode in ("rsi", "boll", "macd"):
        buy, sell = signals(close, ind, mode)
        m = run(close, op, buy, sell, amt=amt, min_amt=5e7)
        rows.append(dict(name=mode.upper(), **m))
        print(f"{mode.upper():6s} total={m['total']:9.2f}% cagr={m['cagr']:6.2f}% dd={m['dd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} win={m['win']:5.1f}% hold={m['hold']:5.1f}d trades={m['trades']}", flush=True)
    # ZIG：20日低点反弹5%买入
    ma200 = close.rolling(200, min_periods=160).mean()
    low20 = close.rolling(20, min_periods=15).min()
    zig_buy = (close >= low20 * 1.05) & (close > ma200)
    empty = pd.DataFrame(False, index=close.index, columns=close.columns)
    m = run(close, op, zig_buy, empty, zig=True, amt=amt, min_amt=5e7)
    rows.append(dict(name="ZIG5", **m))
    print(f"ZIG5   total={m['total']:9.2f}% cagr={m['cagr']:6.2f}% dd={m['dd']:7.2f}% "
          f"sharpe={m['sharpe']:5.2f} win={m['win']:5.1f}% hold={m['hold']:5.1f}d trades={m['trades']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "swing.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
