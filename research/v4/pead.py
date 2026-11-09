"""V4：财报超预期漂移（PEAD）事件驱动回测。

规则（事先固定，不拟合）：
  事件：财报公告日（PIT）
  条件：净利同比 >= 30%、营收同比 >= 20%、增速比上季加速、ROE > 5、
        资产负债率 < 80%、经营现金流 > 0
  过滤：信号日收盘 > MA120、20 日均额 >= 5000 万、股价 > 2 元
  买入：信号日（公告后第一个交易日）的次日开盘
  持有：最多 N 个交易日，或收盘跌破 MA20 卖出
  组合：最多 10 只、等权 10%、单边成本 0.23%
"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "new_strategy"))
import bt  # noqa: E402

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\v4"


def build_events(fin):
    f = fin.copy()
    f["debt"] = (f["total_liabilities"] / f["total_assets"] * 100).replace([np.inf, -np.inf], np.nan)
    f["ocf"] = (f["net_cash_operate"] / f["net_profit"]).replace([np.inf, -np.inf], np.nan)
    f["accel"] = f["revenue_yoy"] - f["rev_prev"]
    ok = (
        (f["net_profit_yoy"] >= 30) & (f["revenue_yoy"] >= 20) & (f["accel"] > 0)
        & (f["roe"] > 5) & (f["debt"] < 80) & (f["ocf"] > 0)
        & f["announce_date"].notna()
    )
    ev = f[ok].copy()
    ev["score"] = 0.5 * ev["net_profit_yoy"] + 0.5 * ev["revenue_yoy"] + 5 * ev["accel"]
    ev = ev.sort_values(["announce_date", "score"], ascending=[True, False])
    return ev


def run(close, op, amt, bench, events, *, n=10, hold=40, regime=False,
        min_amt=5e7, min_price=2.0, cost=0.0023, start="2013-01-01", end="2026-10-28",
        exit_ma=20, stop=0.0, pb=None):
    dates = close.index
    i0 = dates.searchsorted(pd.Timestamp(start))
    i1 = dates.searchsorted(pd.Timestamp(end))
    ma120 = close.rolling(120, min_periods=100).mean()
    ma20 = close.rolling(20, min_periods=15).mean()
    ma60 = close.rolling(60, min_periods=50).mean()
    amt20 = amt.rolling(20, min_periods=15).mean()
    hi20 = close.rolling(20, min_periods=15).max()
    bench_ma = bench.rolling(200, min_periods=160).mean()
    pos_of = {d: i for i, d in enumerate(dates)}

    # 事件 -> 信号日/买入日
    plans = []  # (buy_idx, code, score)
    for _, e in events.iterrows():
        d = pd.Timestamp(e["announce_date"])
        sig_idx = dates.searchsorted(d, side="right")  # 公告后第一个交易日
        if sig_idx >= len(dates) - 1:
            continue
        buy_idx = sig_idx + 1
        if buy_idx < i0 or buy_idx > i1:
            continue
        plans.append((buy_idx, e["symbol"], float(e["score"])))
    plans.sort(key=lambda x: (x[0], -x[2]))
    by_day = {}
    for item in plans:
        by_day.setdefault(item[0], []).append(item)

    cash = 1.0
    pos = {}  # code -> dict(shares, entry, deadline, buy_idx)
    equity = []
    trades = 0
    wins = 0
    hold_days_sum = 0
    closed = 0
    invested_days = 0

    for i in range(i0, i1 + 1):
        d = dates[i]

        # 1) 开盘卖出（前一日收盘触发的）
        for code in list(pos):
            p = pos[code]
            if p.get("sell"):
                px = op.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    px = close.at[d, code]
                if not np.isfinite(px) or px <= 0:
                    pos.pop(code)
                    continue
                cash += p["shares"] * px * (1 - cost)
                trades += 1
                closed += 1
                hold_days_sum += i - p["buy_idx"]
                if px > p["entry"]:
                    wins += 1
                pos.pop(code)

        # 2) 开盘买入
        if i in by_day and len(pos) < n:
            px_open = op.loc[d]
            # 组合总权益（用于等权 1/n 仓位）
            equity_now = cash
            for c2, p2 in pos.items():
                c2v = close.at[d, c2]
                if not np.isfinite(c2v):
                    c2v = p2["entry"]
                equity_now += p2["shares"] * c2v
            for _, code, score in by_day[i]:
                if len(pos) >= n:
                    break
                if not isinstance(code, str):
                    continue
                if code in pos:
                    continue
                px = px_open.get(code)
                if px is None:
                    continue
                px = float(px)
                if not np.isfinite(px) or px <= 0 or px < min_price:
                    continue
                # 信号日校验（信号日 = 买入日前一个交易日）
                sig = dates[i - 1]
                c = close.at[sig, code]
                m = ma120.at[sig, code]
                m20 = ma20.at[sig, code]
                a = amt20.at[sig, code]
                if not (np.isfinite(c) and np.isfinite(m) and np.isfinite(a)):
                    continue
                if c <= m or a < min_amt:
                    continue
                # 只在 MA20 上方买（否则买入次日就会被 MA20 规则卖掉）
                if not (np.isfinite(m20) and c > m20):
                    continue
                # 可选：只在回调时买（业绩超预期 + 回调买点）
                if pb is not None:
                    hi = hi20.at[sig, code]
                    if not (np.isfinite(hi) and hi > 0):
                        continue
                    dd = (c / hi - 1) * 100
                    if not (pb[0] <= dd <= pb[1]):
                        continue
                if regime:
                    try:
                        if bench.loc[sig] < bench_ma.loc[sig]:
                            continue
                    except KeyError:
                        pass
                budget = min(cash, equity_now / n)
                if budget <= 0:
                    break
                sh = budget / px
                pos[code] = dict(shares=sh, entry=px, deadline=i + hold, buy_idx=i)
                cash -= budget * (1 + cost)
                trades += 1

        # 3) 收盘检查卖出条件
        for code, p in pos.items():
            c = close.at[d, code]
            if not np.isfinite(c):
                continue
            hit = i >= p["deadline"]
            if not hit and exit_ma:
                m = (ma20 if exit_ma == 20 else ma60).at[d, code]
                hit = (i - p["buy_idx"]) >= 3 and np.isfinite(m) and c < m
            if not hit and stop > 0:
                hit = c <= p["entry"] * (1 - stop)
            if hit:
                p["sell"] = True

        # 估值
        val = cash
        for code, p in pos.items():
            c = close.at[d, code]
            if not np.isfinite(c):
                s = close.loc[:d, code].dropna()
                c = s.iloc[-1] if len(s) else 0
            val += p["shares"] * c
        equity.append((d, val))
        if pos:
            invested_days += 1

    eq = pd.Series(dict(equity)).sort_index()
    r = eq.pct_change().dropna()
    total = eq.iloc[-1] / eq.iloc[0] - 1
    years = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (1 + total) ** (1 / years) - 1
    dd = (eq / eq.cummax() - 1).min()
    sharpe = (r.mean() / r.std()) * np.sqrt(252) if r.std() > 0 else 0
    return dict(total=total * 100, cagr=cagr * 100, dd=dd * 100, sharpe=sharpe,
                calmar=cagr / abs(dd) if dd else 0, trades=trades,
                win_rate=(wins / closed * 100) if closed else 0,
                avg_hold=(hold_days_sum / closed) if closed else 0,
                exposure=invested_days / max(1, len(equity)) * 100)


def main():
    close, op, amt, fin = bt.load_all()
    bench = bt.load_benchmark()
    ev = build_events(fin)
    print("符合条件的事件数:", len(ev), flush=True)
    rows = []
    for name, kw in [
        ("hold40 MA60退出+择时", dict(hold=40, exit_ma=60, regime=True)),
        ("超预期+回调买点(MA60退出+择时)", dict(hold=40, exit_ma=60, regime=True, pb=(-15, -3))),
        ("超预期+回调买点(无均线+止损15%)", dict(hold=40, exit_ma=0, stop=0.15, pb=(-15, -3))),
    ]:
        m = run(close, op, amt, bench, ev, **kw)
        rows.append(dict(name=name, **m))
        print(f"{name:20s} total={m['total']:9.2f}% cagr={m['cagr']:6.2f}% dd={m['dd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} win={m['win_rate']:5.1f}% hold={m['avg_hold']:4.1f}d "
              f"exp={m['exposure']:5.1f}% trades={m['trades']}", flush=True)
    pd.DataFrame(rows).to_csv(os.path.join(OUT, "pead.csv"), index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
