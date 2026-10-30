"""基本面 + 趋势/震荡 策略回测。

只用「调仓日之前已经公告」的财报（announce_date <= 调仓日），
调仓日收盘出信号、**次日开盘**成交，避免未来函数。

用法：
  python bt.py --preset core --start 2013-01-01 --end 2026-10-28
"""
import argparse
import json
import os

import numpy as np
import pandas as pd

HERE = os.path.dirname(os.path.abspath(__file__))
CACHE = os.path.join(HERE, "cache")


# ---------------------------------------------------------------- 数据
_DATA = {}


def load_all():
    if _DATA:
        return _DATA["close"], _DATA["open"], _DATA["amount"], _DATA["fin"]
    z = np.load(os.path.join(CACHE, "px.npz"), allow_pickle=True)
    dates = pd.to_datetime(z["dates"].astype(str), format="%Y%m%d")
    symbols = pd.Index(z["symbols"])
    close = pd.DataFrame(z["close"], index=dates, columns=symbols)
    open_ = pd.DataFrame(z["open"], index=dates, columns=symbols)
    amount = pd.DataFrame(z["amount"], index=dates, columns=symbols)

    # 拆股/送转：A 股单日跌幅不可能超过 -22%（除权除息除外），用它识别并做后向复权。
    r = close.pct_change(fill_method=None)
    jump = (r < -0.22).to_numpy()
    mult = np.where(jump, (1 + r).to_numpy(), 1.0)
    mult = np.nan_to_num(mult, nan=1.0)
    rev = np.cumprod(mult[::-1], axis=0)[::-1]
    factor = np.ones_like(close.to_numpy())
    factor[:-1] = rev[1:]
    close = close * factor
    open_ = open_ * factor

    fin = pd.read_parquet(os.path.join(CACHE, "fin.parquet"))
    fin["announce_date"] = pd.to_datetime(fin["announce_date"])
    fin["q"] = fin["report_period"].str[-1].astype(int)
    fin = fin.sort_values("announce_date")
    _DATA.update(close=close, open=open_, amount=amount, fin=fin)
    return close, open_, amount, fin


# ---------------------------------------------------------------- 评分
_FIN_ASOF = {}


def fin_asof(fin, date):
    key = (id(fin), date)
    if key in _FIN_ASOF:
        return _FIN_ASOF[key]
    sub = fin[fin["announce_date"] <= date]
    if sub.empty:
        _FIN_ASOF[key] = None
        return None
    # 取"最新报告期"，而不是"最新公告"：否则一条 2024 年发布的 2010 年报更正
    # 会盖掉 2023 年报。报告期字符串（2024Q1..Q4）可直接按字典序排。
    sub = sub.sort_values(["symbol", "report_period", "announce_date"])
    sub = sub.drop_duplicates("symbol", keep="last")
    v = sub.set_index("symbol")
    _FIN_ASOF[key] = v
    return v


def rank_pct(s):
    return s.rank(pct=True)


def board_of(sym):
    """按代码前缀判断板块：main / gem / star。"""
    p3 = sym[:3]
    if p3 in ("688", "689"):
        return "star"
    if p3 in ("300", "301", "302"):
        return "gem"
    if p3 in ("600", "601", "603", "605", "000", "001", "002", "003"):
        return "main"
    return "other"


def build_table(close, amount, fin, date, p, ctx, apply_pullback=True):
    """返回当日可买/可持有清单：index=code，列 = score / pullback_ok。

    基本面、趋势、流动性、上市时间都在这里过滤；
    **pullback 只作为一个布尔标记**，是否要求它由调用方决定
    （入场要求，持有时不要求——否则涨上去就被踢出名单，换手极高）。
    """
    asof = date - pd.Timedelta(days=int(p.get("fin_lag", 0)))
    f = fin_asof(fin, asof)
    if f is None or f.empty:
        return pd.DataFrame(columns=["score", "pullback_ok"])

    roe = f["roe"] * (4.0 / f["q"])          # 年化（Q1*4 / Q2*2 / Q3*4/3 / Q4*1）
    rev = f["revenue_yoy"]
    gnp = f["net_profit_yoy"]
    gm = f["gross_margin"]
    debt = (f["total_liabilities"] / f["total_assets"] * 100).replace([np.inf, -np.inf], np.nan)
    ocf = (f["net_cash_operate"] / f["net_profit"]).replace([np.inf, -np.inf], np.nan)
    ocf = ocf.where(f["net_profit"] > 0)

    df = pd.DataFrame({"roe": roe, "rev_yoy": rev, "np_yoy": gnp, "gm": gm, "debt": debt, "ocf": ocf})
    df = df.reindex(close.columns)
    df["close"] = close.loc[date]
    df = df[df["close"] > 0]

    boards = p.get("boards")
    if boards:
        keep = [s for s in df.index if board_of(s) in boards]
        df = df.loc[keep]

    df = df[df["roe"] >= p["min_roe"]]
    df = df[df["rev_yoy"] >= p["min_rev_yoy"]]
    df = df[df["np_yoy"] >= p["min_np_yoy"]]
    df = df[df["gm"] >= p["min_gm"]]
    df = df[df["debt"] <= p["max_debt"]]
    df = df[df["ocf"] >= p["min_ocf"]]
    if df.empty:
        return pd.DataFrame(columns=["score", "pullback_ok"])

    if p["trend_ma"]:
        ma = ctx["ma"].loc[date]
        df = df[df["close"] > ma.reindex(df.index)]
    if p["pullback"]:
        hi20 = ctx["hi20"].loc[date]
        dd = (df["close"] / hi20.reindex(df.index) - 1) * 100
        df["pullback_ok"] = (dd <= p["dd_hi"]) & (dd >= p["dd_lo"])
        if apply_pullback:
            # 与改造前一致：先在"回调到位"的池子里算分位，再排序
            df = df[df["pullback_ok"]]
            df["pullback_ok"] = True
    else:
        df["pullback_ok"] = True

    amt = ctx["amt20"].loc[date].reindex(df.index)
    df = df[amt >= p["min_amount"]]

    if p["min_listed"]:
        pos_now = ctx["date_pos"][date]
        first_pos = ctx["first_pos"].reindex(df.index)
        df = df[(pos_now - first_pos) >= p["min_listed"]]
    if df.empty:
        return pd.DataFrame(columns=["score", "pullback_ok"])

    q = 0.6 * rank_pct(df["roe"]) + 0.4 * rank_pct(df["gm"])
    g = 0.5 * rank_pct(df["rev_yoy"]) + 0.5 * rank_pct(df["np_yoy"])
    s = 0.5 * rank_pct(-df["debt"]) + 0.5 * rank_pct(df["ocf"])
    score = 0.4 * q + 0.4 * g + 0.2 * s
    if p.get("value"):
        val = load_val_asof(date)
        if val is not None:
            pe = val["pe_ttm"].reindex(df.index)
            pb = val["pb"].reindex(df.index)
            valid = pe.notna() & (pe > 0)
            vrank = pd.Series(0.5, index=df.index)
            if valid.sum() > 5:
                vrank[valid] = 0.6 * (1 - rank_pct(pe[valid])) + 0.4 * (1 - rank_pct(pb[valid]))
            score = 0.35 * q + 0.35 * g + 0.15 * s + 0.15 * vrank
    df["score"] = score
    return df[["score", "pullback_ok"]].sort_values("score", ascending=False)


_CTX = {}
_VAL = {}


def compute_weights(scores, selected, ctx, date, p):
    """动态权重：按分数/波动加权，单票不超过 max_weight。

    等权时就是 1/N；cap 只在"动态权重"下有意义。
    """
    mode = p.get("weight_mode", "equal")
    cap = p.get("max_weight", 1.0)
    if not selected:
        return {}
    if mode == "score":
        s = scores.reindex(selected).fillna(scores.min())
        raw = 0.5 + s.rank(pct=True)          # 0.5 ~ 1.5，头尾 3 倍差
    elif mode == "score2":
        s = scores.reindex(selected).fillna(scores.min())
        r = s.rank(pct=True)
        raw = 0.05 + r ** 2                    # 更陡：头部权重明显更大，cap 会真正生效
    elif mode == "invvol":
        vol = ctx["vol"].loc[date].reindex(selected).replace(0, np.nan)
        vol = vol.fillna(vol.median())
        raw = 1.0 / vol
    elif mode == "score_invvol":
        s = scores.reindex(selected).fillna(scores.min())
        vol = ctx["vol"].loc[date].reindex(selected).replace(0, np.nan)
        vol = vol.fillna(vol.median())
        raw = (0.5 + s.rank(pct=True)) / vol
    else:
        raw = pd.Series(1.0, index=selected)
    w = raw / raw.sum()

    if 0 < cap < 1:
        for _ in range(20):
            over = w > cap + 1e-12
            if not over.any():
                break
            excess = (w[over] - cap).sum()
            w[over] = cap
            rest = ~over
            base = w[rest].sum()
            if base <= 0:
                break
            w[rest] = w[rest] + excess * w[rest] / base
    return w.to_dict()


def load_val_asof(date):
    if "df" not in _VAL:
        v = pd.read_parquet(os.path.join(CACHE, "val.parquet"))
        v["trade_date"] = pd.to_datetime(v["trade_date"])
        _VAL["df"] = v
    v = _VAL["df"]
    sub = v[v["trade_date"] <= date]
    if sub.empty:
        return None
    return sub[sub["trade_date"] == sub["trade_date"].max()].set_index("symbol")


def load_benchmark():
    import glob
    files = sorted(glob.glob(os.path.join(os.path.dirname(CACHE), "..", "..", "..", "..",
        "stockTool", "CNEquity", "data", "cnequity", "curated", "index_bars", "trade_date=*", "*.parquet")))
    # 上面的相对路径不可靠，直接写死数据湖根目录
    root = r"D:\workspace\stockTool\CNEquity\data\cnequity\curated\index_bars"
    rows = []
    for f in sorted(glob.glob(os.path.join(root, "trade_date=*", "*.parquet"))):
        t = pd.read_parquet(f, columns=["symbol", "trade_date", "close"])
        rows.append(t[t["symbol"] == "000300.SH"])
    d = pd.concat(rows)
    d["trade_date"] = pd.to_datetime(d["trade_date"])
    return d.set_index("trade_date")["close"].sort_index()


# ---------------------------------------------------------------- 回测
def rebalance_dates(close, start, end, every=1):
    idx = close.loc[start:end].index
    ym = pd.Series(idx, index=idx).dt.to_period("M")
    last = pd.Series(idx, index=idx).groupby(ym).max()
    vals = list(pd.to_datetime(last.values))
    if every > 1:
        vals = vals[every - 1::every]
    return vals


def run(p, start="2013-01-01", end="2026-10-28", verbose=False):
    every = p.get("every", 1)
    bear_exposure = p.get("bear_exposure", 1.0)
    close, open_, amount, fin = load_all()
    close = close.loc[:end]
    open_ = open_.loc[:end]
    amount = amount.loc[:end]

    dates = close.index
    col_pos = {d: i for i, d in enumerate(dates)}
    key = (p["trend_ma"], p["pullback"], len(dates))
    if key in _CTX:
        ctx = _CTX[key]
    else:
        ctx = {"date_pos": col_pos}
        if p["trend_ma"]:
            ctx["ma"] = close.rolling(p["trend_ma"], min_periods=int(p["trend_ma"] * 0.8)).mean()
        if p["pullback"]:
            ctx["hi20"] = close.rolling(20, min_periods=15).max()
        ctx["amt20"] = amount.rolling(20, min_periods=15).mean()
        ctx["vol"] = close.pct_change(fill_method=None).rolling(60, min_periods=40).std()
        first = close.apply(lambda s: s.first_valid_index())
        ctx["first_pos"] = pd.Series({c: col_pos[v] for c, v in first.items() if v is not None and not pd.isna(v)})
        _CTX[key] = ctx
    bt_dates = rebalance_dates(close, start, end, every)
    bench = load_benchmark()
    bench_ma = bench.rolling(200, min_periods=160).mean()
    if verbose:
        print(f"rebalance dates: {len(bt_dates)}", flush=True)

    cash = 1.0
    pos = {}
    states = []      # (exec_date, cash, pos_copy)
    trades = 0
    traded_value = 0.0
    trade_types = {}
    peak_value = 1.0

    lag = int(p.get("exec_lag", 1))
    for d in bt_dates:
        i = dates.searchsorted(d)
        if i + lag >= len(dates):
            break
        nxt = dates[i + lag]
        table = build_table(close, amount, fin, d, p, ctx, apply_pullback=p.get("hold_mode") != "trend")
        if len(table):
            entry_index = table.index[table["pullback_ok"].to_numpy()]
            if p.get("hold_mode") == "trend" and pos:
                keep = table.index.intersection(pd.Index(list(pos)))
                pool = table.loc[table.index.isin(entry_index) | table.index.isin(keep)]
            else:
                pool = table.loc[entry_index]
            target = list(pool.index[: p["n"]])
            scores = table["score"]
        else:
            target = []
            scores = pd.Series(dtype=float)
        px = open_.loc[nxt]

        def price_of(sym):
            v = px.get(sym)
            if np.isfinite(v) and v > 0:
                return v
            s = close.loc[:d, sym].dropna()
            return s.iloc[-1] if len(s) else None

        # 先卖不在目标里的
        for sym in list(pos):
            if sym in target:
                continue
            pr = price_of(sym)
            if pr is None:
                pos.pop(sym, None)
                continue
            cash += pos[sym] * pr * (1 - p["cost"])
            traded_value += pos[sym] * pr
            pos.pop(sym)
            trades += 1
            trade_types["exit"] = trade_types.get("exit", 0) + 1

        # 目标权重（动态：按分数/波动加权，单票有上限）
        if target:
            prices = {s: price_of(s) for s in target}
            prices = {s: v for s, v in prices.items() if v}
            if prices:
                value = cash + sum(pos[s] * prices.get(s, price_of(s) or 0) for s in pos)
                peak_value = max(peak_value, value)
                expo = 1.0
                try:
                    if bench.loc[d] < bench_ma.loc[d]:
                        expo = bear_exposure
                except KeyError:
                    expo = 1.0
                dd_now = value / peak_value - 1
                for level, factor in p.get("throttle") or []:
                    if dd_now <= -level:
                        expo *= factor
                        break
                weights = compute_weights(scores, list(prices), ctx, d, p)
                band = p.get("min_trade", 0.0)
                for sym, pr in prices.items():
                    w = value * expo * weights.get(sym, 0.0)
                    cur = pos.get(sym, 0.0) * pr
                    delta = w - cur
                    if abs(delta) < band * value:
                        continue          # 权重偏离太小，不动（减少无谓换手）
                    held_before = sym in pos
                    if delta > 1e-9:
                        cash -= delta * (1 + p["cost"])
                        pos[sym] = pos.get(sym, 0.0) + delta / pr
                        traded_value += delta
                        trades += 1
                        trade_types["new" if not held_before else "add"] = trade_types.get(
                            "new" if not held_before else "add", 0) + 1
                    elif delta < -1e-9:
                        cash -= delta * (1 - p["cost"])
                        pos[sym] = pos.get(sym, 0.0) + delta / pr
                        traded_value += -delta
                        trades += 1
                        trade_types["trim"] = trade_types.get("trim", 0) + 1
        states.append((nxt, cash, dict(pos)))

    # 逐日估值
    eq_dates = [d for d in dates if d >= (states[0][0] if states else start)]
    eq = []
    si = 0
    cur_cash, cur_pos = (states[0][1], states[0][2]) if states else (1.0, {})
    for d in eq_dates:
        while si + 1 < len(states) and states[si + 1][0] <= d:
            si += 1
            cur_cash, cur_pos = states[si][1], states[si][2]
        val = cur_cash
        for s, sh in cur_pos.items():
            c = close.at[d, s]
            if not np.isfinite(c):
                c = close.loc[:d, s].dropna().iloc[-1] if close.loc[:d, s].notna().any() else 0
            val += sh * c
        eq.append((d, val))

    return {"equity": pd.Series(dict(eq)).sort_index(), "trades": trades,
            "trade_types": trade_types, "traded_value": traded_value,
            "states": states}


def metrics(eq, rf=0.02):
    ret = eq.pct_change().dropna()
    total = eq.iloc[-1] / eq.iloc[0] - 1
    years = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (1 + total) ** (1 / years) - 1
    dd = (eq / eq.cummax() - 1).min()
    vol = ret.std() * np.sqrt(252)
    sharpe = (ret.mean() * 252 - rf) / vol if vol > 0 else 0
    return dict(total=round(total * 100, 2), cagr=round(cagr * 100, 2),
                maxdd=round(dd * 100, 2), vol=round(vol * 100, 2), sharpe=round(sharpe, 2),
                years=round(years, 2))


PRESETS = {
    "fund": dict(n=15, trend_ma=0, pullback=False, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
                 min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
                 min_listed=250, value=False, cost=0.0023),
    "trend": dict(n=15, trend_ma=200, pullback=False, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
                  min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
                  min_listed=250, value=False, cost=0.0023),
    "core": dict(n=15, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
                 min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
                 min_listed=250, value=False, cost=0.0023),
    "core_value": dict(n=15, trend_ma=200, pullback=True, min_roe=8, min_rev_yoy=0, min_np_yoy=0,
                       min_gm=15, max_debt=70, min_ocf=0.3, dd_hi=-1, dd_lo=-15, min_amount=3e7,
                       min_listed=250, value=True, cost=0.0023),
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--preset", default="core")
    ap.add_argument("--start", default="2013-01-01")
    ap.add_argument("--end", default="2026-10-28")
    args = ap.parse_args()
    p = PRESETS[args.preset]
    res = run(p, args.start, args.end, verbose=True)
    m = metrics(res["equity"])
    print(json.dumps({**m, "trades": res["trades"]}, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
