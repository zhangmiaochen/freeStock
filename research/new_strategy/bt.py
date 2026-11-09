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


# ---- 行业（申万一级，2020+） ----
_IND = {}
_IND_SNAP = {}


def load_ind():
    if "df" not in _IND:
        df = pd.read_parquet(os.path.join(CACHE, "ind.parquet"))
        df = df[df["classification_system"] == "sw"]
        df = df.sort_values("as_of_date").drop_duplicates(["symbol", "as_of_date"], keep="last")
        _IND["df"] = df
    return _IND["df"]


def ind_snapshot(date):
    if date in _IND_SNAP:
        return _IND_SNAP[date]
    df = load_ind()
    sub = df[df["as_of_date"] <= date]
    if sub.empty:
        snap = pd.Series(dtype=object)
    else:
        last = sub["as_of_date"].max()
        snap = sub[sub["as_of_date"] == last].set_index("symbol")["l1_code"]
    _IND_SNAP[date] = snap
    return snap


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

    # 衍生因子（fn 里已按报告期预计算）：ROE 同比趋势、增速加速度
    if "roe_prev4" in f.columns:
        df["roe_trend"] = (f["roe"] - f["roe_prev4"]).reindex(df.index)
        df["accel"] = (f["revenue_yoy"] - f["rev_prev"]).reindex(df.index)

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
    if p.get("min_roe_trend") is not None and "roe_trend" in df:
        df = df[df["roe_trend"] >= p["min_roe_trend"]]
    if p.get("min_accel") is not None and "accel" in df:
        df = df[df["accel"] >= p["min_accel"]]
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
    # 因子融合：把"ROE 趋势 / 增速加速度"按权重混进总分
    fw = float(p.get("factor_weight", 0) or 0)
    if fw > 0 and "roe_trend" in df:
        mix = p.get("factor_mix", "both")
        if mix == "trend":
            extra = rank_pct(df["roe_trend"])
        elif mix == "accel":
            extra = rank_pct(df["accel"])
        else:
            extra = 0.5 * rank_pct(df["roe_trend"]) + 0.5 * rank_pct(df["accel"])
        score = (1 - fw) * score + fw * extra
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
    # 估值过滤（2016+）：只保留 PE 为正且不过高的
    if p.get("pe_max") is not None or p.get("pe_pct_max") is not None:
        val = load_val_asof(date)
        if val is not None:
            pe = val["pe_ttm"].reindex(df.index)
            if p.get("pe_max") is not None:
                mask = (pe > 0) & (pe <= p["pe_max"])
                df = df[mask.fillna(False)]
            if p.get("pe_pct_max") is not None:
                pe_v = pe.reindex(df.index)
                pos = pe_v[pe_v > 0]
                if len(pos) > 5:
                    thr = pos.rank(pct=True)
                    keep = set(thr[thr <= p["pe_pct_max"]].index)
                    df = df[df.index.isin(keep)]
    # 估值融合：把"便宜程度"按权重混进分数
    vw = float(p.get("val_weight", 0) or 0)
    if vw > 0:
        val = load_val_asof(date)
        if val is not None:
            pe = val["pe_ttm"].reindex(df.index)
            pb = val["pb"].reindex(df.index)
            valid = pe.notna() & (pe > 0)
            if valid.sum() > 5:
                vr = pd.Series(0.5, index=df.index)
                vr[valid] = 0.6 * (1 - rank_pct(pe[valid])) + 0.4 * (1 - rank_pct(pb[valid]))
                df["score"] = (1 - vw) * df["score"] + vw * vr
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
    bench_win = int(p.get("bench_ma", 200))
    bench_ma = bench.rolling(bench_win, min_periods=min(160, max(20, int(bench_win * 0.8)))).mean()
    bench_dd = bench / bench.cummax() - 1
    if verbose:
        print(f"rebalance dates: {len(bt_dates)}", flush=True)

    cash = 1.0
    pos = {}
    states = []      # (exec_date, cash, pos_copy)
    trades = 0
    traded_value = 0.0
    trade_types = {}
    peak_value = 1.0
    entry_px = {}     # 建仓价（用于"亏损暂缓卖出"判断）
    hold_start = {}   # 建仓时的调仓序号
    month_idx = 0
    loser_holds = 0   # 因亏损被暂缓卖出的次数
    sold_at_loss = 0  # 最终仍以亏损卖出/清仓的次数
    stop_log = []     # 止损明细：(日期, 代码, 成本, 卖出价)
    exit_log = []     # 所有卖出：原因 / 卖出时是否仍在 MA200 上方 / 持有收益
    peak_px = {}      # 持有期最高收盘价（用于移动止盈）
    hold_sum = 0      # 已平仓持仓的持有月数合计（用于算平均持有期）
    closed = 0

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
            # 行业过滤/上限（2020+ 才有申万行业）
            snap = None
            if p.get("ind_cap") or p.get("ind_mom_top"):
                snap = ind_snapshot(d)
                if len(snap) and p.get("ind_mom_top"):
                    ret60 = (close.loc[d] / close.shift(60).loc[d] - 1).reindex(pool.index)
                    ind_of = snap.reindex(pool.index)
                    ind_ret = ret60.groupby(ind_of).mean().dropna()
                    if len(ind_ret) > 3:
                        keep_n = max(1, int(np.ceil(len(ind_ret) * p["ind_mom_top"])))
                        top_ind = set(ind_ret.sort_values(ascending=False).index[:keep_n])
                        pool = pool[ind_of.isin(top_ind)]
            target = []
            cap = p.get("ind_cap") or 0
            counts = {}
            for code in pool.index:
                if cap and snap is not None:
                    ind = snap.get(code)
                    if ind is not None and counts.get(ind, 0) >= cap:
                        continue
                    if ind is not None:
                        counts[ind] = counts.get(ind, 0) + 1
                target.append(code)
                if len(target) >= p["n"]:
                    break
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

        # 更新持有期最高价（用收盘价）
        if pos:
            for sym in list(pos):
                c_peak = close.at[d, sym] if sym in close.columns else np.nan
                if np.isfinite(c_peak):
                    peak_px[sym] = max(peak_px.get(sym, 0.0), float(c_peak))

        # 先卖不在目标里的（hold_losers：亏损的可以暂缓，等回本/部分回本/超时/破趋势再卖）
        # stop_loss：持仓浮亏达到阈值，即使在目标名单里也先卖出（本月不再买回）
        kept = set()
        stop = float(p.get("stop_loss", 0) or 0)
        for sym in list(pos):
            pr0 = price_of(sym)
            if pr0 is None:
                pos.pop(sym, None)
                entry_px.pop(sym, None)
                hold_start.pop(sym, None)
                continue
            months = month_idx - hold_start.get(sym, month_idx)
            # 最长持有：到期强制卖出（即使在目标名单里）
            if p.get("max_hold_months") and months >= p["max_hold_months"]:
                if sym in target:
                    target = [t for t in target if t != sym]
                cash += pos[sym] * pr0 * (1 - p["cost"])
                traded_value += pos[sym] * pr0
                pos.pop(sym, None)
                entry_px.pop(sym, None)
                hold_start.pop(sym, None)
                trades += 1
                trade_types["max_hold"] = trade_types.get("max_hold", 0) + 1
                hold_sum += months
                closed += 1
                continue
            if stop > 0:
                ep0 = entry_px.get(sym, pr0)
                if ep0 > 0 and pr0 / ep0 - 1 <= -stop:
                    if sym in target:
                        target = [t for t in target if t != sym]
                    cash += pos[sym] * pr0 * (1 - p["cost"])
                    traded_value += pos[sym] * pr0
                    pos.pop(sym, None)
                    entry_px.pop(sym, None)
                    hold_start.pop(sym, None)
                    trades += 1
                    trade_types["stop"] = trade_types.get("stop", 0) + 1
                    sold_at_loss += 1
                    stop_log.append({"date": int(d.strftime("%Y%m%d")), "code": sym, "entry": ep0, "exit": pr0})
                    hold_sum += months
                    closed += 1
                    continue
            if sym in target:
                continue
            # 移动止盈：掉出名单但仍在 MA200 上方、且没从最高点回撤超过阈值 → 继续持有
            if p.get("trail_exit"):
                c_now = close.at[d, sym] if sym in close.columns else np.nan
                ma_s = ctx.get("ma")
                ma_v = ma_s.loc[d].get(sym) if ma_s is not None else None
                above = ma_v is not None and np.isfinite(ma_v) and np.isfinite(c_now) and c_now > ma_v
                pk = peak_px.get(sym, c_now)
                ret_now = c_now / entry_px.get(sym, c_now) - 1 if entry_px.get(sym, c_now) > 0 else 0.0
                if above and np.isfinite(c_now) and c_now >= pk * (1 - p["trail_exit"]) and (
                    not p.get("trail_winners_only") or ret_now > 0
                ):
                    kept.add(sym)
                    continue
            # 最短持有：没到期就不卖（占用仓位，新买入用剩余预算）
            if p.get("min_hold_months") and months < p["min_hold_months"]:
                kept.add(sym)
                continue
            pr = price_of(sym)
            if pr is None:
                pos.pop(sym, None)
                entry_px.pop(sym, None)
                hold_start.pop(sym, None)
                continue
            ep = entry_px.get(sym, pr)
            ret = pr / ep - 1 if ep > 0 else 0.0
            if p.get("hold_losers") and ret < 0:
                months = month_idx - hold_start.get(sym, month_idx)
                ok_ma = True
                if p.get("loser_ma"):
                    ma_series = ctx.get("ma")
                    if ma_series is not None:
                        try:
                            ma_v = ma_series.loc[d].get(sym)
                        except KeyError:
                            ma_v = None
                        ok_ma = ma_v is not None and np.isfinite(ma_v) and pr >= ma_v
                    else:
                        ok_ma = False
                ok_time = p.get("loser_max_months", 0) <= 0 or months < p["loser_max_months"]
                recovered = ret >= p.get("recover_to", 0.0)
                if not recovered and ok_time and ok_ma:
                    kept.add(sym)
                    loser_holds += 1
                    continue
                sold_at_loss += 1
            elif ret < 0:
                sold_at_loss += 1
            cash += pos[sym] * pr * (1 - p["cost"])
            traded_value += pos[sym] * pr
            pos.pop(sym)
            entry_px.pop(sym, None)
            hold_start.pop(sym, None)
            peak_px.pop(sym, None)
            trades += 1
            trade_types["exit"] = trade_types.get("exit", 0) + 1
            ma_now = ctx["ma"].loc[d].get(sym) if ctx.get("ma") is not None else None
            above_ma = bool(ma_now is not None and np.isfinite(ma_now) and pr > ma_now)
            exit_log.append({"date": int(d.strftime("%Y%m%d")), "code": sym, "price": pr,
                             "ret": ret, "above_ma": above_ma})
            hold_sum += months
            closed += 1

        # 目标权重（动态：按分数/波动加权，单票有上限）
        if target:
            prices = {s: price_of(s) for s in target}
            prices = {s: v for s, v in prices.items() if v}
            if prices:
                value = cash + sum(pos[s] * prices.get(s, price_of(s) or 0) for s in pos)
                keep_value = sum(pos[s] * (price_of(s) or 0) for s in sorted(kept))
                base = max(value - keep_value, 0.0)
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
                # 指数自身回撤再加一层减仓（比"指数<MA200"更灵敏）
                if p.get("bench_dd_throttle"):
                    try:
                        idx_dd = float(bench_dd.loc[d])
                    except KeyError:
                        idx_dd = 0.0
                    for level, factor in p["bench_dd_throttle"]:
                        if idx_dd <= -level:
                            expo *= factor
                            break
                weights = compute_weights(scores, list(prices), ctx, d, p)
                band = p.get("min_trade", 0.0)
                for sym, pr in prices.items():
                    w = base * expo * weights.get(sym, 0.0)
                    cur = pos.get(sym, 0.0) * pr
                    delta = w - cur
                    if abs(delta) < band * value:
                        continue          # 权重偏离太小，不动（减少无谓换手）
                    held_before = sym in pos
                    if delta > 1e-9:
                        cash -= delta * (1 + p["cost"])
                        pos[sym] = pos.get(sym, 0.0) + delta / pr
                        if not held_before:
                            entry_px[sym] = pr
                            hold_start[sym] = month_idx
                            peak_px[sym] = pr
                        else:
                            # 加仓：按金额加权更新成本价
                            old_sh = pos[sym] - delta / pr
                            entry_px[sym] = (entry_px.get(sym, pr) * old_sh + pr * (delta / pr)) / pos[sym]
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
        month_idx += 1
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
            "loser_holds": loser_holds, "sold_at_loss": sold_at_loss,
            "stop_log": stop_log,
            "exit_log": exit_log,
            "avg_hold_months": (hold_sum / closed) if closed else 0.0,
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
