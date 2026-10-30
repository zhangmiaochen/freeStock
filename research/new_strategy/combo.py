"""多策略组合：加入再平衡成本，并输出分年对比。"""
import sqlite3

import numpy as np
import pandas as pd

DB = r"D:\workspace\go\stockSoft\freeStock\bin\portfolio.db"
QG_RUN = "btr_1793345933983655800_0640"
LB_RUN = "btr_1793264024848991500_0001"
OUT = r"D:\workspace\go\stockSoft\freeStock\reports\fund_strategy"
COST = 0.001  # 单边 0.1%（切换成本）


def load(con, run):
    df = pd.read_sql_query(
        "SELECT trade_date, total_value FROM portfolio_backtest_daily WHERE run_id=? ORDER BY trade_date",
        con, params=(run,))
    df["date"] = pd.to_datetime(df["trade_date"], format="%Y%m%d")
    return df.set_index("date")["total_value"]


def metrics(eq):
    r = eq.pct_change().dropna()
    total = eq.iloc[-1] / eq.iloc[0] - 1
    years = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (1 + total) ** (1 / years) - 1
    dd = (eq / eq.cummax() - 1).min()
    gaps = np.diff(eq.index.values).astype("timedelta64[D]").astype(int)
    ppy = 252 if np.median(gaps) <= 3 else 12
    sharpe = (r.mean() / r.std()) * np.sqrt(ppy) if r.std() > 0 else 0
    return dict(total=total * 100, cagr=cagr * 100, dd=dd * 100, sharpe=sharpe,
                calmar=cagr / abs(dd) if dd else 0, years=years)


def combo_monthly(eq1, eq2, w, cost=COST):
    m1 = eq1.resample("ME").last()
    m2 = eq2.resample("ME").last()
    idx = m1.index.intersection(m2.index)
    m1, m2 = m1.loc[idx], m2.loc[idx]
    r1 = m1.pct_change().fillna(0).values
    r2 = m2.pct_change().fillna(0).values
    v1, v2 = w, 1 - w
    out = [1.0]
    for i in range(1, len(idx)):
        v1 *= 1 + r1[i]
        v2 *= 1 + r2[i]
        total = v1 + v2
        target = w * total
        turnover = abs(target - v1)
        total -= turnover * cost
        v1 = w * total
        v2 = total - v1
        out.append(total)
    return pd.Series(out, index=idx)


def main():
    con = sqlite3.connect(DB)
    qg = load(con, QG_RUN)
    lb = load(con, LB_RUN)
    con.close()
    idx = qg.index.intersection(lb.index)
    qg, lb = qg.loc[idx] / qg.loc[idx].iloc[0], lb.loc[idx] / lb.loc[idx].iloc[0]
    m1 = qg.resample("ME").last().pct_change().dropna()
    m2 = lb.resample("ME").last().pct_change().dropna()
    print(f"窗口 {idx[0].date()} ~ {idx[-1].date()}，月度相关 {m1.corr(m2):.2f}")

    rows = []
    curves = {}
    def add(name, eq):
        m = metrics(eq)
        rows.append(dict(name=name, **m))
        curves[name] = eq
        print(f"{name:16s} total={m['total']:8.2f}% cagr={m['cagr']:6.2f}% dd={m['dd']:7.2f}% "
              f"sharpe={m['sharpe']:5.2f} calmar={m['calmar']:5.3f}")

    add("100% 质量成长", qg)
    add("100% 趋势低吸", lb)
    for w in (0.3, 0.4, 0.5, 0.6, 0.7):
        add(f"{int(w*100)}/{int((1-w)*100)} 再平衡", combo_monthly(qg, lb, w))

    # 分年：50/50 vs QG
    print("\n分年收益（%）：")
    yq = (qg.resample("YE").last() / qg.resample("YE").last().shift(1) - 1) * 100
    for name in ("50/50 再平衡", "40/60 再平衡"):
        y = (curves[name].resample("YE").last() / curves[name].resample("YE").last().shift(1) - 1) * 100
        print(f"  {name}: " + " ".join(f"{k.year}:{v:+.1f}" for k, v in y.dropna().items()))
    print("  100% 质量成长: " + " ".join(f"{k.year}:{v:+.1f}" for k, v in yq.dropna().items()))

    pd.DataFrame(rows).to_csv(OUT + r"\combo_sweep.csv", index=False, encoding="utf-8-sig")


if __name__ == "__main__":
    main()
