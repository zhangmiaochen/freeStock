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

正确读法（§4）：**短开一个只读 DuckDB 连接直读 cne 的库，查完立刻关闭** ——
单次 0.10–0.14s，而且**永远读到最新的视图定义**，不需要复制、不需要维护副本。
起子进程 9–12s vs 直读 0.1s，差两个数量级。

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

### ✅ 方案 A（首选）：短开只读连接，直读 cne 的文件

**不复制、不重建**：每次（或每批）查询时 `duckdb.connect(cne_db, read_only=True)`，查完立刻关闭。
cne 的 `.duckdb` 里已经持久化了全部视图，只读打开就能直接用。

实测（同一个财务查询）：

| 方式 | 单次耗时 |
|---|---|
| **每次短开只读连接** | **0.10 – 0.14s**（打开 0.018s + 查询 ~0.1s） |
| 长连接（自己建好层） | 0.07 – 0.10s |

**两者只差约 0.03 秒，但方案 A 换来两个关键好处**：

1. **永远是最新定义** —— cne 升级改了视图 SQL、或数据发布了新 revision，你下次读到的就是新的，
   **不存在"要不要更新副本"的问题**（详见 §5.4）；
2. **零维护** —— 不需要复制、不需要检测失效、不需要重建。

> ⚠️ **唯一的代价是"读者与写者互斥"**：DuckDB 不允许同一文件的只读连接与读写连接并存。
> 实测：持有一个只读连接后，再开读写连接会得到
> `ConnectionException: Can't open a connection to same database file with a different configuration than existing connections`。
> 也就是说**你正在读的那一瞬间，cne 无法重建它的视图**（反之亦然）。
>
> 规避方式：**把连接开得尽量短**（打开→查询→关闭，约 0.1 秒），并对
> `duckdb.connect` 的失败做一次重试。只读连接彼此可以并发（实测多个只读连接共存无冲突），
> 所以高并发时用连接池，不要用长连接独占。

### ⚠️ 方案 B：复制视图定义到自己库里（快，但**必须自己处理失效**）

把 73 个视图 + 2 个宏的定义导出，在自己的 DuckDB（或 `:memory:`）里重建，然后长期复用。
好处是**完全不受 cne 写入影响**（不碰 cne 的文件）；代价是**你必须自己检测上游变化**，
否则会安静地读旧数据、用旧语义。

实测：导出 73 视图 + 2 宏，在自己的库里 **2 轮收敛、一次性约 4.4 秒**；
随后 `财务 单股 0.17s`、`日线 单股 0.68s`、`daily_bars count 1.24s`。

**配方与失效检测见 §5。选这条路之前，先读 §5.4。**

### ❌ 方案 C（要避开）：请求路径里起 `cne query` 子进程

单次 9–12s，且随机器负载波动（同一条 SQL 实测 8.86s 与 11.84s）。
只适合人工排查 / 低频脚本，**不适合放进服务端请求路径**。

---

## 5. 可直接实现的配方（已实测跑通）

> **先读 §5.4。** 本节是「方案 B：复制视图层」的实现与失效处理；
> 如果你能用 §4 的方案 A（短连接直读），**根本不需要这一节** —— 也就没有"要不要更新"的问题。
> 选方案 B 意味着你接手了「跟随上游变化」这件事，下节给出可自动化的做法。

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

### 5.4 ⚠️ cne 升级 / 数据更新后，这份副本要不要更新？——**要，而且要自动检测**

这是方案 B 的**唯一硬伤**。副本是「某一时刻的定义快照」，上游一变它就不对了。
会变的有三类，**危险程度递增**：

| 触发 | 表现 | 危险度 |
|---|---|---|
| ① 数据发布新 revision | 视图 SQL 里固化的 `meta/revisions/data/<数据集>/<hash>/` 路径变了 | 中：**安静地读到旧数据**，不报错 |
| ② cne 升级改了视图 SQL | 例如复权锚点、去重 `QUALIFY` 的排序键、OTC 过滤规则变了 | **高：语义漂移**。你会继续用旧算法算出**看起来正常但和 Python 对不上**的数字 |
| ③ 数据集增删 | 视图从 73 个变成 75 个 | 中：新数据集查不到（会报错，反而安全）；删掉的不报错但已无意义 |

②对本项目尤其致命 —— 你们做的正是「复刻一致性」，而这类不一致**不会抛异常**，
只会让 `复刻一致性审计` 里辛苦对齐的公式悄悄偏掉。

#### 好消息：三类触发可以共用一个信号

实测：**revision 路径就嵌在视图 SQL 里**（如 `.../meta/revisions/data/daily_bars/f59679f0`），
所以「数据更新」和「定义升级」都会改变视图 SQL 本身。
于是只要对**全部视图 + 宏的定义**取一个哈希，就能一次覆盖三种情况：

```python
import hashlib, duckdb

def definitions_fingerprint(cne_db: str) -> tuple[str, int, int]:
    """只读打开 -> 导出 -> 哈希 -> 立刻关闭（实测 0.05–0.09s）"""
    con = duckdb.connect(cne_db, read_only=True)
    try:
        views = con.execute(
            "select view_name, sql from duckdb_views() where schema_name='main' order by view_name"
        ).fetchall()
        macros = con.execute(
            "select function_name, parameters, macro_definition from duckdb_functions() "
            "where function_type='table_macro' and internal=false order by function_name"
        ).fetchall()
    finally:
        con.close()
    blob = repr(views) + repr(macros)
    return hashlib.sha256(blob.encode()).hexdigest()[:16], len(views), len(macros)
```

实测结果：

| 项 | 值 |
|---|---|
| 指纹（当前湖） | `4156d52c6b2e61ed` |
| 视图 / 宏 | 73 / 2 |
| 检查耗时 | **0.05 – 0.09s**（可放心在启动时与定时任务里跑） |
| 内容未变时 | 指纹稳定（连测两次一致） |

#### ❌ 不要用文件 mtime 当信号

实测：跑一条**无关的** `cne query`（`select 1`），`.duckdb` 的 mtime 就从 `10:52:01` 变成 `10:55:50`，
而**定义指纹完全没变** —— cne 每次命令都会重写这个文件。
所以 mtime 只能当「值得去算一次指纹」的廉价提示，**不能作为判断依据**。

#### 落地写法

```python
CACHED = load_cached_fingerprint()          # 持久化，随进程/服务重启保留

def ensure_layer():
    fp, nviews, nmacros = definitions_fingerprint(CNE_DB)
    if fp == CACHED:
        return                                  # 未变化，什么都不做（这才是常态）
    build_own_layer()                            # 变了才重建（约 4.4s）
    save_cached_fingerprint(fp)
    log(f"视图层已重建: {fp} views={nviews} macros={nmacros}")
```

四条必须遵守的纪律：

1. **只在指纹变化时重建**，不要每次启动都重建（否则白付 4.4 秒）。
2. **读 cne 的文件必须「短开短关」**：只读连接与 cne 的写入互斥（见 §4 警告），
   拿完定义立刻 `close()`，不要长期持有。
3. **打开失败要重试并保留现有层**：cne 正在写时会 `ConnectionException`。
   这时**继续用旧层**并打好 `stale` 标记与告警，**不要清空、也不要静默当作最新**。
4. **升级 cne 后必须先跑一次任意 cne 命令**（例如 `cne status --datasets`）：
   因为视图是 cne 命令**运行时**重建的，升级后如果一次 cne 命令都没跑过，
   它的 `.duckdb` 里仍然是旧定义，你的指纹自然也读不到变化。
   可以把 `cnequity` 版本一起记录：版本变了就提示"请先跑一次 cne 命令"。

#### 如果不想接手这些：回到方案 A

短连接直读（§4 方案 A）每查一次多花约 **0.03 秒**，换来**永远不需要判断"要不要更新"**。
对绝大多数场景这是更划算的交易 —— 只有当查询 QPS 很高、或绝对不能容忍与 cne 的读写互斥时，
才值得选方案 B 并承担上面的失效检测。



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

---

## 9. 本项目的实测优化记录（Go 侧，2026-10）

> 本节是 `freeStock` 自己的改动记录，与上文（上游 `cne` 的实测）分开。

### 9.1 先说结论：本项目从来没有起过 `cne` 子进程

`internal/cnlake` 一直是**用 `parquet-go` 直读 curated parquet**，
也就是 §4 方案 A 的思路（只不过连 DuckDB 都不经过）。所以 §4 方案 C 那个坑没踩。

### 9.2 实测：单股财报查询 16.9s → 1.6s（10.4 倍）

数据集 `financial_statement_items`：**880 万行 / 498 MB / 102 个季度分区**。

| | 耗时 |
|---|---|
| 优化前（动态 map 路径） | **16.875s** |
| 优化后（列裁剪 + 读取时过滤 + 并发） | **1.628s** |
| 上文 DuckDB（视图就绪，§3.1） | 0.38s |

**根因**（三条叠加）：
1. **没有列裁剪** —— 动态 `map[string]any` 路径把 13 列全读了
2. **每行建一个 map** —— 880 万次 map 构造 + 字符串 key 哈希
3. **串行读 102 个分区**

**改法**：
- 定义 `financeStatementRow` 投影结构体（只 7 列），走 `readTyped[financeStatementRow]`
  —— `parquet-go` 的 `GenericReader[T]` 只读结构体里出现的字段，这就是列裁剪
- `keep` 回调在**解出一行时就判 symbol**，不再"全部物化完再筛"
- `readTyped` 改成**有界工作池**（上限 8：磁盘 IO 密集，再多只会互相抢 IO），
  **按文件顺序合并**保证结果与串行逐字节一致

> ⚠ 分区键是 `report_period`（按季度），**不是 symbol** ——
> 单股查询仍然要扫全部分区文件，这一层削减不掉。
> 剩下的 1.6s vs DuckDB 的 0.38s，差额主要是 **row group 统计裁剪**
> （DuckDB 会用 symbol 列的 min/max 跳过整个 row group）。
> 本项目没做，因为单个季度文件里 symbol 是乱序的，收益不确定 ——
> 要做的话先实测每个 row group 的 symbol 分布。

### 9.3 ⚠ 已知的**乘性成本**：历史回填会重复取数（未修）

`RefreshBackfill` 对区间内**每个交易日**都调一次 `ScoreStocks`，
而 `ScoreStocks` 每天都要重新 `DailyHistory(code, 窗口起点, 该日)`
取一遍完整窗口（约 250 个交易日）。

于是回填 N 天的成本是 **N × 股票数 × 一次完整窗口请求** ——
**同一只股票的历史被重复拉了 N 次**。

实测：回填 25 天 × 60 只 ≈ 1500 次请求，远超合理的量级。

**可行改法**（未做）：一次性取每只股票在 `[区间起点 - 窗口, 区间终点]` 的完整日线，
然后**在内存里按日切片**算指标，而不是每天重新拉。

后续如果回测/回填要常态化使用，这一条应该是首要优化项。
