"""数据与口径的静态审计：财报公告日、复权、股票池。"""
import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bt  # noqa: E402

CACHE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "cache")
fin = pd.read_parquet(os.path.join(CACHE, "fin.parquet"))
fin["announce_date"] = pd.to_datetime(fin["announce_date"])

print("=== 财报公告日审计 ===")
print("rows", len(fin), "symbols", fin["symbol"].nunique())
print("announce range", fin["announce_date"].min().date(), fin["announce_date"].max().date())

# 报告期 -> 期末日期
def period_end(p):
    y, q = int(p[:4]), int(p[-1])
    return pd.Timestamp(year=y, month=q * 3, day=1) + pd.offsets.MonthEnd(0)


fin["period_end"] = fin["report_period"].map(period_end)
fin["lag_days"] = (fin["announce_date"] - fin["period_end"]).dt.days
print("公告滞后天数：中位 %.0f，P90 %.0f，最大 %d" % (
    fin["lag_days"].median(), fin["lag_days"].quantile(0.9), fin["lag_days"].max()))
print("announce < period_end 的行数（不该有）:", int((fin["lag_days"] < 0).sum()))
print("announce 早于期末超过 0 天的样本:", fin.loc[fin["lag_days"] < 0, ["symbol", "report_period", "announce_date"]].head(3).to_dict("records"))

# 同一 (symbol, period) 多条公告 = 更正/重述
dup = fin.groupby(["symbol", "report_period"]).size()
print("每个 (symbol, period) 平均公告条数 %.2f，最多 %d" % (dup.mean(), dup.max()))

# 各年 Q4 的中位公告日，确认是真实公告节奏
for y in (2013, 2016, 2019, 2022, 2025):
    sub = fin[fin["report_period"] == f"{y}Q4"]
    if len(sub):
        print(f"  {y}Q4: n={len(sub)}, 公告中位日 {sub['announce_date'].median().date()}, 滞后中位 {sub['lag_days'].median():.0f} 天")

print("\n=== 价格复权审计 ===")
close, open_, amount, _ = bt.load_all()
print("价格矩阵", close.shape, "日期", close.index[0].date(), "~", close.index[-1].date())
# 检测到的拆股跳空（跌幅 < -22%）数量
r = close.pct_change(fill_method=None)
jumps = (r < -0.22).sum().sum()
print("检测到的拆股/送转跳空（用于后向复权）:", int(jumps))
print("说明：复权是把历史价格乘一个常数，不改变任何一天的收益率与均线比值；"
      "信号只用到当日及以前的收盘价。")

print("\n=== 股票池审计 ===")
first = close.apply(lambda s: s.first_valid_index())
print("有价格的股票数", first.notna().sum())
print("最早有效价格日期分布（前 5）:", first.dropna().sort_values().head(5).dt.date.tolist())
