# cne 查询慢的原因与接入方案（实测）

> 本文是 `freeStock` 在接入 CNEquity（`cne`）数据湖时，对「为什么 `cne query` 慢」的**实测记录**，
> 用于说明本项目该用什么方式读这个湖、以及为什么要避开某几种看似自然的写法。
> 全部数据于 2026-10-08 在同一台机器实测（Windows / Python 3.11.3 / cnequity 0.16.0）。
> 上游：CNEquity 数据湖，命令名 `cne`。

---

## 0. 一句话结论

**慢的不是查询，是每条 `cne query` 都要在启动阶段无条件重建整个 DuckDB 视图层。**

一次「查某只股票的全部财务报表」实测 **9.34 秒**，其中：

| 组成 | 耗时 | 占比 |
|---|---|---|
| 命令启动（Python + Click + 配置） | ~2.5s | 27% |
| **重建全部视图/宏（96 条 SQL）** | **~6.0s** | **65%** |
| 真正的查询 | **0.06s** | **0.6%** |

所以：**不要在每个 HTTP 请求里起一个 `cne query` 子进程。** 这是本文唯一必须记住的一条。

---

## 1. 实测：端到端耗时

| 命令 | 耗时 |
|---|---|
| `cne --version`（完全不碰湖） | 2.50s |
| `cne status --datasets` | 7.25 – 9.02s |
| `cne query --sql "select 1"`（不碰任何数据） | **8.86 – 11.84s** |
| `cne query --sql "select count(*) from daily_bars"` | 11.87s |
| `cne query --sql "select count(*) from financial_statement_items"` | 11.66 – 12.72s |

同一个财务 count 连跑 3 次：**10.80 / 11.52 / 10.88s** —— 固定开销一次都省不掉。

**空查询和扫 888 万行几乎一样慢**，这一条就足以说明瓶颈与数据量无关。

---

## 2. 实测：把开销拆开

### 2.1 `cne query` 的实现（读源码得出，非推测）

```python
def query(config_path, sql, dataset, symbol, refresh):
    cfg = _cfg(config_path)                          # 0.00s
    db_path = ensure_duckdb_views(cfg)               # 6.00–6.80s   ← 罪魁
    con = duckdb.connect(str(db_path), read_only=True)   # 0.02s
    df = con.execute(sql).pl()                       # select 1 → 0.00s
    con.close()                                      # 0.09s
```

关键：`ensure_duckdb_views` **没有缓存、没有跳过判断**，每次调用都把全部视图与宏重建一遍，
并且写进一个**文件型** DuckDB（`data/cnequity/duckdb/cnequity.duckdb`，约 524 KB）。

### 2.2 进程内逐阶段

| 阶段 | 耗时 |
|---|---|
| `load_config` | 0.00s |
| **`ensure_duckdb_views`** | **6.00 – 6.80s** |
| `duckdb.connect(read_only)` | 0.02s |
| `execute('select 1')` | 0.00s |
| `execute('select count(*) from daily_bars').pl()` | 1.46s |
| `con.close()` | 0.09s |

### 2.3 那 6 秒具体花在哪（拦截至 96 条语句）

| 语句 | 耗时 |
|---|---|
| `CREATE VIEW daily_bars_adj`（多 CTE + 两次 LEFT JOIN） | **1.79s** |
| `CREATE VIEW daily_bars`（OTC 过滤，JOIN instruments） | 0.62s |
| `CREATE VIEW adj_factors`（OTC 过滤） | 0.58s |
| `CREATE MACRO daily_bars_qfq(...)` | 0.56s |
| `DESCRIBE read_parquet(...)` ×2（推断 schema） | 1.03s |
| `CREATE VIEW daily_bars_including_otc` | 0.52s |
| `CREATE MACRO daily_bars_total_return(...)` | 0.51s |
| 其余约 80 条视图/宏 | 合计约 1s |

### 2.4 为什么这几条特别慢：视图指向 1.6 万个 parquet

视图路径不是 `curated/`，而是 **`meta/revisions/data/<数据集>/<revision-hash>/`**：

```
合计 16,129 个 parquet / 864 MB
  daily_bars                   10,321 个文件   236.6 MB
  adj_factors                   5,223 个文件   100.7 MB
  financial_statement_items       233 个文件   498.5 MB
  trading_status                  111 个文件     0.6 MB
```

`CREATE VIEW ... AS read_parquet('<glob>', union_by_name=true)` 时，DuckDB 必须**展开 glob 并推断/合并 schema**，
也就是去读这些文件的页脚。而 `daily_bars` / `adj_factors` 在一次构建里被**引用多次**
（原视图、`_including_otc`、`daily_bars_adj`、两个 TABLE MACRO），于是同一批页脚被反复解析。

---

## 3. 实测：查询本身有多快

### 3.1 财务数据（`financial_statement_items`，8,881,727 行 / 233 文件 / 498.5 MB）

视图就绪后，纯查询耗时：

| 查询 | 耗时 | 结果 |
|---|---|---|
| `count(*)` 全表 | 2.01s | 8,881,727 |
| `where symbol = '600519.SH'` 取数 | **0.38s** | 2,801 行 |
| `where symbol = '600519.SH'` count | **0.07s** | 2,801 |
| `where report_period = '2025Q3'` | **0.10s** | 154,466 |
| `symbol + report_period` | **0.06s** | 28 行 |
| 某科目全市场时间序列（跨 102 期聚合） | 2.56s | 102 行 |

### 3.2 行情数据（`daily_bars`，4,253,142 行 / 10,321 文件）

| 查询 | 耗时 |
|---|---|
| `count(*)` | 1.51s |
| `where symbol = '600519.SH'` | 0.78s |

**结论：带过滤条件的查询 0.06–0.38 秒，全表聚合 2–2.6 秒。数据层性能完全没问题。**

---

## 4. 本项目可用的三种读法（按推荐度）

### ✅ 方案 A（推荐）：导出视图定义，在自己库里建一次，长期持有连接

DuckDB 的视图是**持久化对象**，而且 `duckdb_views()` / `duckdb_functions()` 能把定义整体读出来。
所以本项目可以：**从 cne 已建好的库里把定义导出来，在自己的 DuckDB（或 `:memory:`）里建一次，
然后长期复用同一个连接。**

**完整配方（含参考代码）见 §5，已实测跑通**：导出 73 个视图 + 2 个宏，在自己库里 2 轮收敛、
一次性约 4.4 秒；随后 `财务 单股 0.17s`、`日线 单股 0.68s`、`daily_bars count 1.24s`。

实测收益：

| 方式 | 单次查询耗时 |
|---|---|
| 每次起 `cne query` 子进程 | **9–12s** |
| 自建一次后复用连接 | **0.00–2.6s**（多数 0.2–1.3s） |

> ⚠️ **不要直接长期持有 cne 的那个 `.duckdb` 文件**：DuckDB 不允许「只读连接 + 读写连接」同时打开同一文件。
> 实测：持有一个只读连接后，再开读写连接会得到
> `ConnectionException: Can't open a connection to same database file with a different configuration than existing connections`。
> 也就是说**你持有只读连接期间，`cne query` / 任何写视图的命令都会失败**。
>
> 所以方案 A 要求把定义**复制到自己的库里**，而不是共用 cne 的文件。

### ⚠️ 方案 B：复制 cne 已建好的 `.duckdb`（快，但会静默读到旧数据）

实测：直接打开 cne 建好的文件，全新只读连接里已有 **73 个 VIEW**，无需重建：

| 查询 | 耗时 |
|---|---|
| `count(*) from daily_bars` | 1.36s |
| `financial_statement_items where symbol='600519.SH'` | 0.18s |

**但有一个必须知道的陷阱**：视图的 SQL 里**固化了当时的 revision 路径**
（`meta/revisions/data/daily_bars/<hash>/`）。数据湖一旦发布新版本，指针会变，
而**你手上这份副本仍指向旧 revision —— 不会报错，只会安静地给你旧数据**。
若用这种方式，必须在湖更新后重新取（例如比较 `meta/revisions` 下的指针文件时间）。

### ❌ 方案 C（要避开）：请求路径里起 `cne query` 子进程

单次 9–12s，且随机器负载波动（同一条 SQL 实测 8.86s 与 11.84s）。
只适合人工排查 / 低频脚本，**不适合放进服务端请求路径**。

---

## 5. 可直接实现的配方（已实测跑通）

思路：**导出定义 → 在自己的库里按「重试到收敛」重放 → 之后长期复用连接。**

顺序不能自己排：视图之间、宏与视图之间都有依赖
（`adj_factors` 依赖 `adj_factors_including_otc`；宏 `daily_bars_qfq` 依赖视图 `daily_bars`），
实测按名字排序直接建会失败，**分轮重试 2 轮即全部收敛**。

### 5.1 参考实现（Python，Go 侧照此移植）

```python
import duckdb

CNE_DB = r"D:\workspace\stockTool\CNEquity\data\cnequity\duckdb\cnequity.duckdb"

# ① 只读打开 cne 的库，导出定义（只读连接之间可并发，不会影响别的只读方）
src = duckdb.connect(CNE_DB, read_only=True)
views = src.execute(
    "select view_name, sql from duckdb_views() where schema_name = 'main'"
).fetchall()
macros = src.execute(
    "select function_name, parameters, macro_definition from duckdb_functions() "
    "where function_type = 'table_macro' and internal = false"   # internal=false 才是 cne 自己的
).fetchall()
src.close()

stmts = [(name, sql) for name, sql in views]
for name, params, definition in macros:
    # parameters 是列表（如 ['start_date','end_date']），必须 join；直接插值会语法错
    stmts.append((name,
        f"CREATE OR REPLACE MACRO {name}({', '.join(params)}) AS TABLE {definition}"))

# ② 在自己的库里重放；分轮重试，直到某一轮完全没有进展
target = duckdb.connect()               # 或换成自己的文件路径
pending, rounds, done = list(stmts), 0, 0
while pending:
    rounds += 1
    still = []
    for name, sql in pending:
        try:
            target.execute(sql); done += 1
        except Exception:
            still.append((name, sql))   # 依赖还没建好，下一轮再来
    if len(still) == len(pending):      # 一轮毫无进展 -> 真的失败
        raise RuntimeError([n for n, _ in still])
    pending = still

# ③ 之后所有查询都走这一个连接
print(target.execute(
    "select count(*) from financial_statement_items where symbol='600519.SH'").fetchall())
```

### 5.2 实测结果

| 步骤 | 结果 |
|---|---|
| 导出 | 视图 **73** 个 / 宏 **2** 个 |
| 重放 | 第 1 轮成功 69，第 2 轮成功 6 → 共 **75 个对象，一次性约 4.4s** |
| 建好后 `select 1` | 0.00s |
| 建好后 财务 单股 | **0.17s** |
| 建好后 财务 单期（154,466 行） | 0.20s |
| 建好后 日线 单股 | 0.68s |
| 建好后 `count(*) from daily_bars`（425 万行） | 1.24s |
| 建好后 `count(*) from financial_statement_items`（888 万行） | 2.0 – 5.4s（随缓存冷热波动） |
| 建好后宏 `daily_bars_qfq(起, 止)`（98.8 万行复权计算） | 5.47s（**真实计算**，不是固定开销） |

### 5.3 落地时必须注意的四件事

1. **revision 会变**：视图 SQL 固化的是当时的 `meta/revisions/data/<数据集>/<hash>/` 路径。
   湖更新后必须**重新导出 + 重建**，否则会安静地读旧数据。建议以「revision 指针变化」为触发条件，
   而不是定时无脑重建。
2. **持有只读连接时不要让 cne 去写视图**（见 §4 的警告）。用方案 A 的独立库可完全避开。
3. **别把 `parameters` 直接拼进 SQL**：DuckDB 返回列表，实测直接插值报
   `Parser Error: syntax error at or near "["`。
4. **`duckdb_functions()` 里混着 DuckDB 自带宏**（`histogram`、`duckdb_logs_parsed` 等，
   `internal=true`、`db=system`），必须用 `internal = false` 过滤。


---

## 6. 复现本文的测量

```python
# 1) 端到端
import subprocess, time
for sql in ("select 1", "select count(*) from financial_statement_items"):
    t0 = time.time()
    subprocess.run(["cne", "query", "--sql", sql, "--config", "configs/cnequity.toml"])
    print(sql, round(time.time() - t0, 2), "s")

# 2) 拆阶段（进程内）
from cnequity.config import load_config
from cnequity.query.views import ensure_duckdb_views
import duckdb, time
cfg = load_config("configs/cnequity.toml")
t0 = time.time(); db = ensure_duckdb_views(cfg); print("views", round(time.time()-t0, 2))
con = duckdb.connect(str(db), read_only=True)
t0 = time.time(); con.execute("select 1").fetchall(); print("select 1", round(time.time()-t0, 4))

# 3) 逐条语句定位（把 views.duckdb 换成带计时的垫片即可，见本文 §2.3）
```

统计文件规模：

```powershell
Get-ChildItem D:\workspace\stockTool\CNEquity\data\cnequity\meta\revisions\data -Recurse -Filter *.parquet |
  Group-Object { $_.Directory.Parent.Name } |
  Sort-Object Count -Descending | Select-Object -First 10 Name, Count
```

---

## 7. 给上游（CNEquity）的改进建议

按收益排序：

1. **让 `ensure_duckdb_views` 可跳过**：若 DuckDB 文件里视图已存在、且各数据集的 committed revision 指针未变，
   就没有必要重建。这一条能直接消掉 6 秒里的大部分。
2. **减少文件数（治本）**：`daily_bars` 10,321 个文件是主要来源；合并小文件能让 glob + schema 推断快一个量级。
3. **给 `cne query` 一个常驻模式**（`--daemon` 或 `cne mcp` 复用连接）：把 6 秒从「每次」变成「每进程一次」。
4. `cne --version` 就要 2.5 秒，说明 CLI 冷启动本身偏重；如可延后导入重模块会有普适收益。

---

## 8. 附：财务表怎么查（列名极易猜错）

`financial_statement_items` 是 **EAV 长表**，不是宽表。实测列名：

```
symbol, report_period, statement_type, item_code, item_value, announce_date,
source, data_version, fetched_at, available_at, source_published_at, observed_at, revision_id
```

`statement_type` 取值分布（实测）：

| statement_type | 行数 |
|---|---|
| `income` | 3,158,569 |
| `indicator` | 2,319,318 |
| `balance` | 2,143,575 |
| `cashflow` | 1,260,265 |

**能用的写法**（实测 0.06s）：

```sql
select statement_type, item_code, item_value
from financial_statement_items
where symbol = '600519.SH' and report_period = '2025Q3'
order by statement_type, item_code;
```

> ⚠️ `item_name` / `value` / `revenue` 这类名字**都不存在**，会直接报
> `BinderException: Referenced column ... not found`。取科目值要用 `item_code` + `item_value`。

其它数据集的视图名与 `cne` 的数据集 ID 一致（如 `daily_bars`、`instruments`、`earnings_disclosure_schedule`），
可直接当表名查询。
