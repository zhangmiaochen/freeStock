"""构建基本面回测的数据缓存。

数据全部来自本地数据湖（parquet），只做读取与对齐，不改数据。
输出到 research/new_strategy/cache/：
  px.npz      日线：close/open/amount 宽表（float32）+ 日期 + 代码
  fin.parquet 财报指标（按 announce_date 排序，PIT 用）
  val.parquet 估值（PE/PB，2016+，按月采样）
  ind.parquet 行业归属（2020+，按月采样，申万）
"""
import glob
import os

import numpy as np
import pandas as pd
import pyarrow.parquet as pq

LAKE = r"D:\workspace\stockTool\CNEquity\data\cnequity\curated"
OUT = os.path.join(os.path.dirname(__file__), "cache")
os.makedirs(OUT, exist_ok=True)


def build_prices(start_year=2010, end_year=2026):
    """按年读取 daily_bars，拼成宽表。"""
    out = {}
    dates_all = []
    for year in range(start_year, end_year + 1):
        files = sorted(glob.glob(os.path.join(LAKE, "daily_bars", f"trade_date={year}-*", "*.parquet")))
        if not files:
            continue
        frames = []
        for f in files:
            t = pq.read_table(f, columns=["symbol", "trade_date", "close", "open", "amount"])
            frames.append(t.to_pandas())
        df = pd.concat(frames, ignore_index=True)
        df = df[df["symbol"].str.endswith((".SH", ".SZ"))]
        df["trade_date"] = pd.to_datetime(df["trade_date"])
        for col in ["close", "open", "amount"]:
            w = df.pivot_table(index="trade_date", columns="symbol", values=col, aggfunc="last")
            out.setdefault(col, []).append(w.astype("float32"))
        dates_all.append(df["trade_date"].drop_duplicates())
        print(f"  {year}: rows={len(df)} symbols={df['symbol'].nunique()}", flush=True)
    res = {}
    for col, frames in out.items():
        res[col] = pd.concat(frames, axis=0).sort_index()
        res[col] = res[col][~res[col].index.duplicated(keep="last")]
    # 统一列
    cols = res["close"].columns
    for col in res:
        res[col] = res[col].reindex(columns=cols)
    dates = res["close"].index
    np.savez_compressed(
        os.path.join(OUT, "px.npz"),
        dates=dates.strftime("%Y%m%d").values.astype("int64"),
        symbols=np.array(cols),
        close=res["close"].values,
        open=res["open"].values,
        amount=res["amount"].values,
    )
    print("prices:", res["close"].shape, "->", os.path.join(OUT, "px.npz"), flush=True)


ITEMS = [
    "roe", "revenue_yoy", "net_profit_yoy", "gross_margin",
    "net_profit", "revenue", "total_equity", "total_assets",
    "total_liabilities", "net_cash_operate",
]


def build_fin():
    files = sorted(glob.glob(os.path.join(LAKE, "financial_statement_items", "report_period=*", "*.parquet")))
    frames = []
    for f in files:
        t = pq.read_table(f, columns=["symbol", "report_period", "item_code", "item_value", "announce_date"])
        df = t.to_pandas()
        df = df[df["item_code"].isin(ITEMS) & df["announce_date"].notna()]
        if len(df):
            frames.append(df)
        if len(frames) % 20 == 0:
            print(f"  fin {len(frames)} files, rows={sum(len(x) for x in frames)}", flush=True)
    df = pd.concat(frames, ignore_index=True)
    df = df[df["symbol"].str.endswith((".SH", ".SZ"))]
    # 同一 (symbol, report_period, item) 可能有多条公告（更正/重述）：保留最后公告的一条
    df = df.sort_values("announce_date").drop_duplicates(
        subset=["symbol", "report_period", "item_code"], keep="last")
    wide = df.pivot_table(
        index=["symbol", "report_period", "announce_date"],
        columns="item_code", values="item_value", aggfunc="last").reset_index()
    wide.columns.name = None
    wide["announce_date"] = pd.to_datetime(wide["announce_date"])
    # 同一报告期可能因"部分科目后补公告"被拆成多行（其中一行大量 NaN）：
    # 按列取最后一个非空值合并，避免最后公告的残行盖掉完整行。
    ann = wide.groupby(["symbol", "report_period"])["announce_date"].max()
    vals = wide.drop(columns=["announce_date"]).groupby(["symbol", "report_period"]).last()
    wide = vals.join(ann).reset_index()
    wide = wide.sort_values(["symbol", "report_period"]).reset_index(drop=True)
    # 衍生因子：ROE 同比趋势、增速加速度（供因子实验用）
    g = wide.groupby("symbol")
    wide["roe_prev4"] = g["roe"].shift(4)
    wide["gm_prev4"] = g["gross_margin"].shift(4)
    wide["rev_prev"] = g["revenue_yoy"].shift(1)
    wide["np_prev"] = g["net_profit_yoy"].shift(1)
    wide = wide.sort_values(["announce_date", "symbol"]).reset_index(drop=True)
    wide.to_parquet(os.path.join(OUT, "fin.parquet"), index=False)
    print("fin:", wide.shape, "ann", wide["announce_date"].min(), wide["announce_date"].max(), flush=True)


def build_val():
    files = sorted(glob.glob(os.path.join(LAKE, "valuation_metrics", "trade_date=*", "*.parquet")))
    frames = []
    for f in files:
        t = pq.read_table(f, columns=["symbol", "trade_date", "pe_ttm", "pb", "total_mv"])
        frames.append(t.to_pandas())
    df = pd.concat(frames, ignore_index=True)
    df = df[df["symbol"].str.endswith((".SH", ".SZ"))]
    df["trade_date"] = pd.to_datetime(df["trade_date"])
    # 只保留每月最后一个交易日的快照，够月度调仓用
    df["ym"] = df["trade_date"].dt.to_period("M")
    last = df.groupby("ym")["trade_date"].transform("max")
    df = df[df["trade_date"] == last]
    df.to_parquet(os.path.join(OUT, "val.parquet"), index=False)
    print("val:", df.shape, df["trade_date"].min(), df["trade_date"].max(), flush=True)


def build_ind():
    files = sorted(glob.glob(os.path.join(LAKE, "industry_members", "as_of_date=*", "*.parquet")))
    frames = []
    for f in files:
        t = pq.read_table(f, columns=["symbol", "as_of_date", "classification_system", "l1_code", "l1_name"])
        df = t.to_pandas()
        df = df[df["symbol"].str.endswith((".SH", ".SZ"))]
        frames.append(df)
    df = pd.concat(frames, ignore_index=True)
    df["as_of_date"] = pd.to_datetime(df["as_of_date"])
    df.to_parquet(os.path.join(OUT, "ind.parquet"), index=False)
    print("ind:", df.shape, df["as_of_date"].min(), df["as_of_date"].max(),
          df["classification_system"].value_counts().to_dict(), flush=True)


if __name__ == "__main__":
    print("building prices...", flush=True)
    build_prices()
    print("building fin...", flush=True)
    build_fin()
    print("building val...", flush=True)
    build_val()
    print("building ind...", flush=True)
    build_ind()
    print("done")
