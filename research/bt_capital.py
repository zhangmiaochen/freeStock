"""资金约束下的 v3 引擎（研究用）：100 股整手 + 实际本金 + 现金再利用。

与 Go 回测口径尽量对齐：
  月频（每月最后一个交易日收盘算、次日开盘成交）、8 只、单票上限 15%、
  熊市（沪深300 < MA200）×0.5、1% 层数免打扰。
成本：买入 0.13%（佣金+滑点）、卖出 0.23%（佣金+印花税+滑点），最低佣金 5 元。
"""
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, r"research\new_strategy")
import bt  # noqa: E402

BASE = dict(trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
            min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-2, dd_lo=-20, min_amount=3e7,
            min_listed=250, value=False, cost=0.0023, every=1, bear_exposure=0.5,
            max_weight=0.15, bench_ma=200, exec_lag=1, hold_mode="rebalance")

MIN_COMM = 5.0
BUY_FEE = 0.0013
SELL_FEE = 0.0023


def norm_cap(raw, cap):
    """归一化并把超限项压到 cap（迭代配平）。"""
    w = np.array(raw, dtype=float)
    if w.sum() <= 0:
        return np.zeros_like(w)
    w = w / w.sum()
    for _ in range(50):
        over = w > cap + 1e-12
        if not over.any():
            break
        excess = (w[over] - cap).sum()
        w[over] = cap
        rest = ~over
        if not rest.any():
            break
        w[rest] += excess * w[rest] / w[rest].sum()
    return w


def weights_for(scores, mode, cap):
    k = len(scores)
    if k == 0:
        return np.array([])
    if mode == "equal":
        raw = np.ones(k)
    else:
        r = pd.Series(scores).rank(pct=True).to_numpy()
        raw = 0.05 + r ** 2
    return norm_cap(raw, cap)


def run(p, start="2020-01-01", end="2026-10-28", capital=100000.0, n=8,
        weight_mode="score2", afford="actual", bear=0.5, redeploy=False,
        cap=0.15, verbose=False):
    close, open_, amount, fin = bt.load_all()
    close, open_, amount = close.loc[:end], open_.loc[:end], amount.loc[:end]
    dates = close.index
    col_pos = {d: i for i, d in enumerate(dates)}
    ma = close.rolling(p["trend_ma"], min_periods=160).mean()
    hi20 = close.rolling(20, min_periods=15).max()
    amt20 = amount.rolling(20, min_periods=15).mean()
    first = close.apply(lambda s: s.first_valid_index())
    first_pos = pd.Series({c: col_pos[v] for c, v in first.items()
                           if v is not None and not pd.isna(v)})
    ctx = {"date_pos": col_pos, "ma": ma, "hi20": hi20, "amt20": amt20,
           "vol": close.pct_change(fill_method=None).rolling(60, min_periods=40).std(),
           "first_pos": first_pos}

    bench = bt.load_benchmark()
    bma = bench.rolling(p["bench_ma"], min_periods=160).mean()

    months = pd.Series(dates).dt.to_period("M")
    last_by_month = pd.Series(dates).groupby(months).last()
    rb = [d for d in last_by_month if pd.Timestamp(start) <= d <= pd.Timestamp(end)]

    cash = capital
    pos = {}  # code -> shares
    snapshots = []  # (生效日, cash, pos 副本)
    trades = 0
    cash_ratio = []

    for d in rb:
        i = dates.searchsorted(d)
        nxt = dates[i + 1] if i + 1 < len(dates) else None
        if nxt is None:
            break
        # 当日权益（收盘）
        eq_val = cash
        for c, sh in pos.items():
            px = close.at[d, c]
            if np.isfinite(px):
                eq_val += sh * px
        expo = 1.0 if (bench[d] >= bma[d] if np.isfinite(bma.get(d, np.nan)) else True) else bear

        table = bt.build_table(close, amount, fin, d, p, ctx, apply_pullback=True)
        pool = table[table["pullback_ok"]] if len(table) else table
        cands = list(pool.index[: n * 4])
        scores = pool["score"] if len(pool) else pd.Series(dtype=float)

        # ---- 选股（可负担过滤）----
        def price_at(c):
            px = open_.at[nxt, c] if c in open_.columns else np.nan
            if not np.isfinite(px) or px <= 0:
                px = close.at[d, c]
            return float(px) if np.isfinite(px) else 0.0

        def select(mode):
            sel = list(cands[:n])
            for _ in range(6):
                w = weights_for(scores.reindex(sel).fillna(0).to_numpy(), weight_mode, cap) * expo
                bad = [c for c, wi in zip(sel, w) if price_at(c) * 100 > eq_val * wi + 1e-9]
                if not bad:
                    break
                keep = [c for c in sel if c not in bad]
                extra = [c for c in cands if c not in keep][: n - len(keep)]
                if not extra:
                    sel = keep
                    break
                sel = keep + extra
            return sel

        if afford == "none":
            sel = list(cands[:n])
        elif afford == "maxweight":
            budget = eq_val * cap * expo
            sel = [c for c in cands if price_at(c) * 100 <= budget][:n]
        else:  # actual
            sel = select("actual")

        w = weights_for(scores.reindex(sel).fillna(0).to_numpy(), weight_mode, cap) * expo
        target = dict(zip(sel, w))

        px_o = open_.loc[nxt]

        def px_of(c):
            v = px_o.get(c, np.nan)
            if not np.isfinite(v) or v <= 0:
                v = close.at[d, c]
            return float(v) if np.isfinite(v) else 0.0

        # ---- 卖出不在名单的 ----
        for c in list(pos):
            if c in target:
                continue
            px = px_of(c)
            if px <= 0:
                continue
            cash += pos.pop(c) * px * (1 - SELL_FEE)
            trades += 1

        # ---- 买入 ----
        buys = []
        for c in sel:
            px = px_of(c)
            if px <= 0:
                continue
            budget = eq_val * target[c] - (pos.get(c, 0) * 0)  # 目标市值
            cur_val = pos.get(c, 0) * px
            need = budget - cur_val
            if need <= 0:
                continue
            lots = int(need / px / 100)
            if lots <= 0:
                continue
            sh = lots * 100
            amt = sh * px
            fee = max(amt * BUY_FEE, MIN_COMM)
            if amt + fee > cash:
                lots = int(cash / (px * 100)) - 0
                while lots > 0 and lots * 100 * px * (1 + BUY_FEE) + MIN_COMM > cash:
                    lots -= 1
                if lots <= 0:
                    continue
                sh = lots * 100
                amt = sh * px
                fee = max(amt * BUY_FEE, MIN_COMM)
            pos[c] = pos.get(c, 0) + sh
            cash -= amt + fee
            trades += 1
            buys.append(c)

        # ---- 现金再利用：按排名加仓到未到上限的票 ----
        if redeploy and cash > 0:
            for c in sel:
                if c not in pos:
                    continue
                px = px_of(c)
                if px <= 0:
                    continue
                cap_val = eq_val * cap * expo
                cur_val = pos[c] * px
                room = cap_val - cur_val
                if room < px * 100:
                    continue
                lots = int(min(room, cash) / px / 100)
                while lots > 0 and lots * 100 * px * (1 + BUY_FEE) + MIN_COMM > cash:
                    lots -= 1
                if lots <= 0:
                    continue
                sh = lots * 100
                amt = sh * px
                fee = max(amt * BUY_FEE, MIN_COMM)
                pos[c] += sh
                cash -= amt + fee
                trades += 1

        # 记录这一期的持仓状态（用于按日估值）
        snapshots.append((nxt, cash, dict(pos)))
        if eq_val > 0 and target:
            cash_ratio.append(max(0.0, cash / eq_val))

    if not snapshots:
        return pd.Series(dtype=float), {"total": 0, "cagr": 0, "maxdd": 0, "sharpe": 0, "trades": 0, "exposure": 0}

    # 按日估值：用最近一期快照的持仓 × 当日收盘
    daily = []
    start_ts = pd.Timestamp(start)
    for d in dates[dates >= start_ts]:
        snap = None
        for s in snapshots:
            if s[0] <= d:
                snap = s
            else:
                break
        if snap is None:
            continue
        _, c0, pos0 = snap
        v = c0
        for code, sh in pos0.items():
            px = close.at[d, code]
            if np.isfinite(px):
                v += sh * px
        daily.append((d, v))
    e = pd.Series(dict(daily)).sort_index()
    m = bt.metrics(e)
    m["trades"] = trades
    m["exposure"] = float(np.mean(cash_ratio)) * 100 if cash_ratio else 0
    return e, m


def main():
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument("--capital", type=float, default=100000)
    ap.add_argument("--start", default="2020-01-01")
    ap.add_argument("--end", default="2026-10-28")
    args = ap.parse_args()
    rows = []
    variants = [
        ("no-afford", dict(afford="none")),
        ("afford=maxweight(现状)", dict(afford="maxweight")),
        ("afford=actual", dict(afford="actual")),
        ("actual+equal", dict(afford="actual", weight_mode="equal")),
        ("actual+redeploy", dict(afford="actual", redeploy=True)),
        ("actual+equal+redeploy", dict(afford="actual", weight_mode="equal", redeploy=True)),
        ("actual+equal+redeploy+bear1", dict(afford="actual", weight_mode="equal", redeploy=True, bear=1.0)),
    ]
    for name, kw in variants:
        _, m = run(BASE, args.start, args.end, capital=args.capital, **kw)
        print("%-30s total=%8.2f%% cagr=%6.2f%% dd=%7.2f%% sharpe=%5.2f cash=%5.1f%% trades=%d" % (
            name, m["total"], m["cagr"], m["maxdd"], m["sharpe"], m["exposure"], m["trades"]), flush=True)
        rows.append((name, m))
    return rows


if __name__ == "__main__":
    main()
