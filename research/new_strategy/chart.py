import os

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import pandas as pd

plt.rcParams["font.sans-serif"] = ["Microsoft YaHei", "SimHei", "Arial"]
plt.rcParams["axes.unicode_minus"] = False

OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
eq = pd.read_csv(os.path.join(OUT, "equity_v3.csv"), index_col=0, parse_dates=True).iloc[:, 0]
bench = pd.read_csv(os.path.join(OUT, "equity.csv"), index_col=0, parse_dates=True)["CSI300"]
bench = bench.reindex(eq.index).ffill()

y = (eq.resample("YE").last() / eq.resample("YE").last().shift(1) - 1) * 100
yb = (bench.resample("YE").last() / bench.resample("YE").last().shift(1) - 1) * 100
y.index = y.index.year
yb.index = yb.index.year

fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(15, 6))
ax1.plot(eq.index, eq.values, color="#2e86ab", linewidth=1.8, label="新策略（质量成长+趋势回调）")
ax1.plot(bench.index, bench.values, color="#999999", linewidth=1.2, label="沪深300")
ax1.set_yscale("log")
ax1.set_title("2013-2026 累计净值（对数轴，起点 1.0）")
ax1.grid(alpha=0.25, which="both")
ax1.legend(fontsize=9)

years = [i for i in y.index if 2014 <= i <= 2026]
x = range(len(years))
w = 0.4
ax2.bar([i - w / 2 for i in x], [y.get(i, 0) for i in years], w, label="新策略", color="#2e86ab")
ax2.bar([i + w / 2 for i in x], [yb.get(i, 0) for i in years], w, label="沪深300", color="#bbbbbb")
ax2.set_xticks(list(x))
ax2.set_xticklabels(years, rotation=45)
ax2.axhline(0, color="black", linewidth=0.8)
ax2.set_ylabel("年度收益 %")
ax2.set_title("年度收益对比")
ax2.grid(axis="y", alpha=0.25)
ax2.legend(fontsize=9)

fig.tight_layout()
out = os.path.join(OUT, "新策略_净值与年度收益.png")
fig.savefig(out, dpi=130)
print("saved", out)
