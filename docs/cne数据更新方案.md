# cne 数据更新：手动更新 + 自动更新（实现方案）

> 本文是 `freeStock` 增加「手动更新 / 自动更新 cne 数据」的**实现方案**，
> 目标是把每一步落到本项目的具体文件与接口上，而不是泛泛而谈。
> 实测环境：Windows / Go 1.26 / 桌面端（Wails v3）/ cnequity 0.16.0，
> 数据湖 `D:\workspace\stockTool\CNEquity`，配置 `configs\cnequity.toml`。
>
> **不修改 cne 源码。** 更新一律通过 cne 自己的官方命令 `cne run daily` 完成。
>
> 相关文档：查询性能与接入方式见《cne查询性能问题与接入方案.md》——
> **那条路径（短开只读 DuckDB）只管读，和本文的"更新"是两件事**。

---

## 0. 一句话方案

**手动更新**：应用起一个后台 `cne run daily` 子进程，UI 轮询状态与进度，可取消；
**自动更新**：由 **Windows 计划任务**兜底（应用关着也会跑），应用启动时再做一次**漏日补跑**，
并负责计划任务的创建/修改/展示。

**不要用 cne 自带的调度器** —— 实测它在这台 Windows 上会自我暂停（理由见 §2.5）。

---

## 1. 现状与本方案的接入点

| 位置 | 现状 | 本方案怎么接 |
|---|---|---|
| `main.go` | `application.NewService(stockService)` / `portfolioService` | 新增一个 `CneUpdateService` 一起注册 |
| `internal/config/store.go` | JSON 设置文件，已有 `KlinePrefs` 这类强类型偏好 + `Sanitize()` | 加 `UpdatePrefs`，加到 `fileData` |
| `internal/cnlake/lake.go` | `Open(root)` / `Detect()` 直读 parquet | 复用它的 root 作为"湖在哪"的唯一来源 |
| `internal/service/` | 17 个文件，**目前没有任何 `os/exec`** | 本方案第一次引入外部进程调用，见 §4.2 |
| `frontend/.../settings/DataLakeCard.vue` | 「数据湖」卡片：湖路径、财报有没有入库、分区列表 | 在旁边加一张 `DataUpdateCard.vue` |
| `frontend/src/lib/backend.ts` | 类型化 `api` 包装（`api.getDataLakeStatus()` 等） | 加更新相关方法与类型 |
| `/settings` 路由 | `DataSourceView.vue` 渲染 `<DataLakeCard />` | 同页追加 `<DataUpdateCard />` |

> 更新与查询互不干扰：更新期间 `cnlake` 仍可正常读已提交的数据；
> 只是刚跑完的当天数据要等下一次读才可见。

---

## 2. 决定设计的 6 条实测事实

这些是实测出来的，不是推测；每一条都直接改变方案。

### 2.1 退出码不能当成功判据

2026-10-10 的一次 `--stale-only` 补跑：耗时 **8 分钟**、写入 **25,159 行**、
`exit=1`、manifest 里 run 状态 `failed`，而 8 个步骤里 **5 个成功**：

```
hot_rank              success  1.2s    100 rows
share_unlock_schedule success  1.3s    790 rows
etf_profiles          success  6.1s  1,694 rows
northbound_holdings   success  8.2s  3,933 rows
compact               success  142.2s 18,642 rows
fund_flow             failed   after 342.6s
economic_calendar     source unavailable（上游）
sector_fund_flow      source unavailable（上游）
```

反过来，**degraded 的运行会 `exit=0`**。
→ 结论：**必须同时读"运行状态"和"每个数据集是否真的写了行"**，不能只看退出码。

### 2.2 `--stale-only` 不覆盖日行情

补跑算出的落后清单是
`etf_profiles, share_unlock_schedule, economic_calendar, fund_flow, hot_rank, sector_fund_flow, northbound_holdings`
—— **里面没有 `daily_bars`**。而实测行情数据集当时全部停在 `2026-10-08`：

```
daily_bars / index_bars / adj_factors / sector_bars / trading_status   最新都是 2026-10-08
日历：10-08 交易日、10-09 交易日、10-10/10-11 休市
```

→ 结论：**"补抓落后"和"补某一天"是两种不同操作**，UI 上不能只给一个按钮。

### 2.3 非交易日跑普通日更会跳过行情组

当前时间是周六（非交易日），此时 `cne run daily` 按文档"非交易日调度组自动跳过，事件流仍会运行"，
**行情组根本不会跑**。想补周五必须显式指定 as-of 交易日：

```bat
cne run daily --trade-date 2026-10-09 --config configs\cnequity.toml
```

→ 结论：**漏日补跑必须用 `--trade-date`**，这是自动更新最容易漏掉的一环（§5.2 第 2 层）。

### 2.4 代理变量有大小写两份，只清一份会让整批步骤崩

第一次补跑时每个 step 都崩在构造 HTTP 客户端：

```
httpx.InvalidURL: Invalid port: ':1]'
```

原因：本机环境里同时存在 `NO_PROXY` 与 `no_proxy`，只清大写那份时，小写那份的
`::1,[::1]` 仍会被 httpx 读成代理挂载点。
⚠️ **Python 的 `os.environ` 在 Windows 上把键名统一成大写，所以从 Python 侧看不出有重复项** ——
排查时极易误判"已经清干净了"。补上小写清理后，同一命令正常取数。

→ 结论：`BuildEnv()` 必须**按大小写不敏感**过滤代理变量（§4.1），并写成单元测试。

### 2.5 cne 自带调度器在这台机器上不可用（所以本方案不用它）

实测证据：

```
settings.json    2026-10-08 18:37 被写回 daily=false / stale=false / events=false
state/scheduler/ 只有 daily-2026-10-08.done，没有 10-09 的 → 那天日更一次没跑
计划任务         228 个任务里找不到任何 cnequity 任务
last_tick        冻结不再推进
```

根因：cne 的 worker **每分钟自己重新校验一次**任务是否处于活动状态，
校验失败就 fail-closed 暂停调度；而 Windows 会**省略等于默认值的 XML 元素**（`RunLevel` 等），
cne 却要求它们显式存在 —— 它在这台机器上永远过不了自校验。

→ 结论：自动更新**由本项目自己负责**（计划任务 + 启动补跑），
并且**绝不要**去启用 cne 自带的调度器（既不工作，又会和本项目双跑）。

### 2.6 单次更新耗时 8 ~ 60 分钟

stale 补跑 8 分钟；一次完整日更（7 个调度组 + 审计）在 2026-10-08 跑了约 **58 分钟**。
→ 结论：**绝不能同步等待**，必须是"提交任务 → 后台跑 → 轮询状态"。

---

## 3. 设置数据模型

在 `internal/config/store.go` 里按现有 `KlinePrefs` 的写法加（同一文件、同一风格）：

```go
// UpdatePrefs 是 cne 数据更新的设置。
type UpdatePrefs struct {
	AutoDaily      bool     `json:"autoDaily"`       // 是否启用每日自动更新（计划任务）
	DailyTime      string   `json:"dailyTime"`       // "17:30"，盘后主日更
	AutoStale      bool     `json:"autoStale"`       // 是否启用"补抓落后"（计划任务）
	StaleTime      string   `json:"staleTime"`       // "21:00"
	CatchUpOnStart bool     `json:"catchUpOnStart"`  // 启动时发现落后就补（§5.2 第 2 层）
	Groups         []string `json:"groups,omitempty"`// 限定调度组；空=全部
	CNEPath        string   `json:"cnePath,omitempty"`    // cne 可执行文件；空=按 PATH 找
	ProjectDir     string   `json:"projectDir,omitempty"` // cne 的工作目录（须含 configs/cnequity.toml）
}

func DefaultUpdatePrefs() UpdatePrefs {
	return UpdatePrefs{
		AutoDaily:      false, // 默认不自动改用户机器上的计划任务
		DailyTime:      "17:30",
		AutoStale:      false,
		StaleTime:      "21:00",
		CatchUpOnStart: true,
		ProjectDir:     `D:\workspace\stockTool\CNEquity`,
	}
}

// Sanitize 收敛非法值，和 KlinePrefs.Sanitize 一样"永不报错，只纠正"。
func (p UpdatePrefs) Sanitize() UpdatePrefs { /* 校验 HH:MM（复用现有 clampInt 风格）；ProjectDir 不存在则回落默认 */ }
```

然后挂进已有的持久化结构：

```go
type fileData struct {
	// ... 现有字段 ...
	UpdatePrefs *UpdatePrefs `json:"updatePrefs,omitempty"`
}
```

**默认全部关闭自动行为**：注册计划任务会改动用户机器，必须是用户显式开启。

---

## 4. 后端模块

新增包 `internal/cne/`（与 `internal/cnlake/` 平级，职责：**调用外部 cne 命令**）。

### 4.1 `env.go` —— 构造子进程环境（含 §2.4 那个坑）

```go
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

// BuildEnv 返回给 cne 子进程用的环境：按大小写不敏感删掉所有代理变量，
// 再打开 UTF-8 输出，让 cne 的中文进度能正常落进日志。
func BuildEnv() []string
```

必须这样写（`strings.EqualFold` 或 `strings.ToUpper(key)`），
并配一个单元测试：构造 `{"no_proxy=::1,[::1]", "NO_PROXY=...", "PATH=..."}`，
断言输出里**一个 proxy 都不剩**。

理由见 §2.4：只清一种大小写会让 httpx 抛 `InvalidURL: Invalid port: ':1]'`，整批步骤全崩。

### 4.2 `runner.go` —— 手动更新的执行器

```go
type Job struct {
	ID        string    // uuid，返回给前端
	Kind      string    // full / stale / trade-date
	Args      []string  // 传给 `cne run daily` 的参数
	StartedAt time.Time
	LogPath   string
	cmd       *exec.Cmd
}

type Runner struct { /* mu, current *Job, prefs, ... */ }

func (r *Runner) Start(args []string) (*Job, error) // 单一飞行：已有任务在跑就返回错误
func (r *Runner) Status() *Status
func (r *Runner) Cancel() error
```

要点（每条都对应一个实测坑）：

| 要点 | 做法 | 为什么 |
|---|---|---|
| 不阻塞 UI | `cmd.Start()` 后立刻返回，不做 `Wait()` 同步等待 | 单次 8~60 分钟（§2.6） |
| 输出落文件 | `cmd.Stdout/Stderr` 指向 `data\cnequity\logs\` 下的日志文件 | 进程要活过请求；也便于 tail 进度 |
| 工作目录 | `cmd.Dir = ProjectDir` | cne 按相对路径找 `configs/cnequity.toml`，不对就 `ModuleNotFoundError` 之类 |
| 环境 | `cmd.Env = BuildEnv()` | §2.4 |
| 可取消 | Windows 用 `CREATE_NEW_PROCESS_GROUP`；取消时**杀进程树** | cne 会派生子进程，只杀父进程会留孤儿 python |
| 单一飞行 | 进程内互斥；再叠加"检测是否已有 cne 在跑" | cne 自己有 ingest 锁，第二个任务会失败或空等；提前拒绝体验更好 |
| 完成判定 | **不要只看 `exit code`**：同时读 manifest 的 run 状态 + `rows_written` | §2.1 |
| 参数透传 | `Args` 直接拼到 `cne run daily` 之后 | 一套执行器覆盖 full / stale / `--trade-date` / `--group` |

命令形态固定为：

```
<cne> run daily <args...> --config <ProjectDir>\configs\cnequity.toml
```

`<cne>` 取 `prefs.CNEPath`；为空则找 PATH 上的 `cne`，再兜底常见绝对路径。

### 4.3 `freshness.go` —— "差没差、差哪几天"

这是自动更新能不能真正补上数据的关键（§2.2、§2.3）。

```go
type DatasetFreshness struct {
	Name        string
	LatestDate  time.Time // 数据集里的最新日期
	LastTrading time.Time // 日历上最后一个已收盘交易日
	BehindDays  []time.Time // 缺失的交易日（按日历算）
}

func Check(root string, keyDatasets []string) ([]DatasetFreshness, error)
```

数据来源：`cnlake` 已有读 parquet 的能力；交易日历读 `curated/trading_calendar`
（列：`trade_date / is_trading / source / data_version / fetched_at`，）
或已经进 DuckDB 的 `trading_calendar` 视图。

"最后已收盘交易日"的口径：日历里 `is_trading = true` 且**日期 < 今天**的最大日期
（当天的数据在盘后也不一定齐，别把今天算成"应该有"）。

### 4.4 `schedule_windows.go` —— 计划任务的增删改查

封装 `schtasks`（不要手写 XML；cne 就是栽在"自己拼 XML 再回读校验"上）：

```go
const (
	TaskDaily = "cne-daily-update" // 盘后主日更
	TaskStale = "cne-daily-stale"  // 稍后补抓落后
)

func Query(name string) (*TaskInfo, error) // 下次运行时间 / 上次结果 / 状态
func Create(name, cmd string, at string) error
func Change(name, at string) error
func Delete(name string) error
```

`cmd` 指向一个**固定的包装脚本**（见 §6），而不是把参数直接塞进任务命令行 ——
包装脚本负责清环境、写日志、解析时间戳，任务定义保持稳定、可读、可复现。

### 4.5 `manifest.go` —— 结构化结果

`meta\manifest.db` 是 SQLite，本项目已依赖 `modernc.org/sqlite`，可直接读：

```sql
select run_id, job_name, status, started_at, finished_at, rows_written
from ingestion_runs order by started_at desc limit N;
```

用它做"最近一次更新结果"和"这次到底写了多少行"，
比解析中文日志可靠得多（日志只用来做**进度**）。

---

## 5. 自动更新：三层设计（重点是第 1、2 层）

桌面应用**关掉就不再运行**，所以不能把自动更新只放在应用里。

### 5.1 第 1 层：Windows 计划任务（骨架，必须有）

- 到点由系统拉起包装脚本，**freeStock 没开也会更新**；
- 应用负责创建 / 改时间 / 删除 / 展示状态（`schtasks`）；
- 时间取自 `UpdatePrefs.DailyTime` / `StaleTime`。

当前 CNEquity 侧已经装好两个任务，形态与本方案一致，可直接被接管：

```
cne-daily-update   每天 17:30  → cne-daily.cmd              主日更
cne-daily-stale    每天 21:00  → cne-daily.cmd --stale-only 补抓落后
```

> ⚠️ **单一所有者原则**：freeStock 接管时应当**改这两个任务，而不是再新建一套**；
> 同时确保 CNEquity 控制台里的「数据自动更新」卡片保持关闭（那是 cne 自带调度器，§2.5）。
> 两套并存 = 双跑 = 重复取数 + 抢锁。

### 5.2 第 2 层：启动时漏日补跑（必须有）

第 1 层只能保证"机器开着且当时能跑"。实际情况是：机器关机、休眠、当天网络故障、
上游熔断（本次实测 `push2` 熔断后"今天不再发请求"）—— 都会留下缺口。

所以应用启动时（以及每次更新完成后）跑一次新鲜度检查：

```
读各关键数据集的 max(trade_date)
读日历的最后已收盘交易日
若落后 -> 对每个缺失交易日依次执行：
        cne run daily --trade-date <那天>
```

**这一层才是真正补上数据的地方**：本次实测中，周五（10-09）的行情缺口，
普通日更和 `--stale-only` 都不会补，只有 `--trade-date 2026-10-09` 能补上（§2.2、§2.3）。

策略建议：

- 缺失 1~3 天：启动后自动补，UI 顶部给一条可关闭的提示；
- 缺失更多：只在卡片里提示"落后 N 个交易日"，由用户点「补跑」确认（避免一开机就跑几小时）。

### 5.3 第 3 层：应用内定时器（可选，默认关）

应用开着时到点触发。**默认关闭**，否则与第 1 层双跑。
只有在用户明确选择"不注册系统计划任务，仅在本应用运行时更新"时才启用。

### 5.4 双跑与并发防护

三道闸：

1. **单一所有者**：计划任务由本项目管理，cne 自带调度器保持关闭；
2. **进程内单一飞行**：`Runner` 同时只允许一个任务；
3. **跨进程检测**：启动前扫描是否已有 `cne` 进程（或 `tick.lock`/ingest 锁被占用），
   有就拒绝并说明"已有一个更新在进行"。

---

## 6. 包装脚本（任务的执行体）

任务命令行里放一个**固定脚本**，而不是一长串参数。本方案已在 CNEquity 侧做好并验证过
（`D:\workspace\stockTool\CNEquity\cne-daily.cmd`），要点：

```bat
setlocal
chcp 65001 >nul
set "PYTHONUTF8=1" & set "PYTHONIOENCODING=utf-8" & set "PYTHONUNBUFFERED=1"
rem 大小写各清一遍，见 §2.4
set "HTTP_PROXY=" & set "HTTPS_PROXY=" & set "ALL_PROXY=" & set "NO_PROXY="
set "http_proxy=" & set "https_proxy=" & set "all_proxy=" & set "no_proxy="
cd /d "<ProjectDir>" || exit /b 1
rem 时间戳走 PowerShell（不要用 %DATE%）：本机 %DATE% 是“周六 2026/10/10”，
rem cmd 又按 OEM 代码页 echo，会把非 UTF-8 字节混进 cne 的 UTF-8 日志，变乱码
"%CNE%" run daily %* --config "%CONFIG%" >> "%LOG%" 2>&1
```

三条踩过的坑，脚本里必须体现：

1. **文件必须 CRLF、无 BOM、纯 ASCII** —— 裸 LF 会被 cmd.exe 误解析；
2. **时间戳不要用 `%DATE%`** —— 中文星期 + OEM 代码页会污染日志（实测 `鍛ㄥ叚`）；
3. **参数用 `%*` 透传** —— 这样同一个脚本能同时支持 `--stale-only`、`--trade-date`、`--group`。

若 freeStock 想完全自治，可以把这份脚本**内置或首次运行时释放到 ProjectDir**，
但**不要两处各存一份**（两份会漂移，出问题时无法判断实际跑的是哪份）。

---

## 7. 服务接口（Wails 绑定）

新增 `internal/service/cneupdate.go`，在 `main.go` 里与现有服务一起注册：

```go
type CneUpdateService struct{ store *config.Store; runner *cne.Runner }

application.NewService(cneUpdateService)
```

方法（前端可直接调用）：

| 方法 | 说明 |
|---|---|
| `CheckFreshness() ([]DatasetFreshness, error)` | 各关键数据集最新日期 vs 最后交易日，含缺失交易日 |
| `ManualUpdate(kind string, tradeDate string) (*JobInfo, error)` | `kind`: `full` / `stale` / `date`；**立即返回 job** |
| `UpdateStatus() (*UpdateStatus, error)` | 当前/最近任务：阶段、已用时、日志尾部、是否可取消 |
| `CancelUpdate() error` | 取消（杀进程树） |
| `GetUpdatePrefs() / SetUpdatePrefs(UpdatePrefs) (UpdatePrefs, error)` | 读写设置（写入前 `Sanitize()`） |
| `ApplySchedule() (*ScheduleState, error)` | 按设置创建/修改/删除计划任务 |
| `ScheduleState() (*ScheduleState, error)` | 读回任务状态：是否注册、下次运行时间、上次结果 |
| `RecentRuns(limit int) ([]RunSummary, error)` | 从 manifest 读最近几次运行（状态 + 行数） |

返回给前端的结构建议带**人类可读的结论**，而不只是原始字段，例如：

```go
type UpdateStatus struct {
	Running    bool
	Kind       string
	ElapsedSec int
	LogTail    []string   // 最近 N 行，用于进度展示
	Steps      []StepInfo // 从日志解析出的 Step x success/failed
	Outcome    string     // "成功" / "部分成功（3 个数据集未成功）" / "失败"
	RowsWritten int64     // 来自 manifest，不是日志
}
```

`Outcome` 必须由 **manifest 状态 + 每个数据集是否写了行**共同决定（§2.1），
禁止用 `退出码 == 0` 直接当作"成功"。

---

## 8. 前端

### 8.1 新组件 `frontend/src/components/settings/DataUpdateCard.vue`

放在 `DataSourceView.vue` 里 `<DataLakeCard />` 旁边，沿用现有 `Card/Badge/Button` 组件与风格。

卡片内容：

1. **新鲜度**：表格 —— 数据集 / 最新日期 / 最后交易日 / 落后天数；
   落后的行用警示色（用一个 `Badge` 标"落后 N 天"）。
2. **手动更新**：三个入口
   - `更新到今天`（`full`）
   - `补抓落后`（`stale`）
   - `补指定交易日`（日期选择器 → `date`）
   按钮在任务运行期间禁用，显示已用时与 `取消`。
3. **进度**：`LogTail` 滚动显示 + 已解析出的步骤列表（`Step fund_flow starting / success / failed`）。
   cne 会定期打 `still working: fund_flow 2m50s (no output for 1m00s)`，
   可以直接拿来做"卡住没卡住"的判断。
4. **自动更新设置**：开关 + 时间输入（`DailyTime` / `StaleTime`）+ `启动时自动补跑` 开关；
   保存后调 `ApplySchedule()`，并把 `ScheduleState()` 的"下次运行时间"显示出来 ——
   **让用户能看到"确实排上了"**，而不是只看到一个开关。
5. **最近结果**：最近几次运行（时间、状态、写入行数），失败时把未成功的数据集列出来。

### 8.2 `frontend/src/lib/backend.ts`

按现有 `api.getDataLakeStatus()` 的写法，补上 §7 的方法与对应 TypeScript 类型
（`UpdatePrefs` / `DatasetFreshness` / `UpdateStatus` / `ScheduleState` / `RunSummary`）。

> 类型要与 Go 侧 `json` tag 对齐；`UpdatePrefs` 建议在前后端各写一处并互相注释指向。

---

## 9. 错误处理与"已知上游问题"

更新失败分三类，UI 要区别对待，否则用户会反复点重试：

| 类别 | 实测例子 | 建议 UI |
|---|---|---|
| **上游暂时不可用** | `push2 breaker tripped: ... not sent until tomorrow`（熔断，当天不再请求） | 提示"数据源熔断，明天会自动重试"，**不要**让用户反复点 |
| **上游接口变更** | `EastMoney datacenter RPT_ECONOMICCALENDAR rejected schema: 报表配置不存在 (code=9501)` | 提示"上游接口已变，需要等 cne 升级"，并给出数据集名 |
| **解析失败** | `同花顺 concept row without a board code` | 同上，归到"该数据集暂不可用"，其余数据集正常 |

这三条都是**当前真实存在**的（2026-10-10 实测），不是假设。
UI 应把"哪个数据集没成功、为什么"直接展示出来 —— 这正是只报"更新失败"会让人抓瞎的地方。

---

## 10. 落地顺序

| 阶段 | 内容 | 价值 |
|---|---|---|
| **1** | `internal/cne/env.go` + `runner.go` + `ManualUpdate/UpdateStatus/CancelUpdate` | 手动更新立刻可用，且是自足闭环 |
| **2** | `freshness.go` + 启动补跑（`--trade-date`） | 真正解决"漏了一天补不回来"（§2.2/§2.3） |
| **3** | `schedule_windows.go` + 自动更新设置 + `ApplySchedule` | 应用关着也能更新 |
| **4** | `manifest.go` + 结果/进度 UI 打磨 | 让"成功/部分成功/失败"说得清 |

建议**第 1、2 阶段先做**：即使完全不碰计划任务，也已经比"什么都没更新"好得多。

---

## 11. 验收清单（每条都可复现）

- [ ] 手动更新**立即返回**，UI 有进度；请求耗时 < 1s（不是 8 分钟）
- [ ] 运行中再次点更新被拒，提示明确（不是静默排队）
- [ ] 取消后**没有残留 python 进程**（任务管理器核对）
- [ ] 子进程环境里**没有任何 proxy 变量**（对 `BuildEnv` 写单测，构造大小写重复项）
- [ ] 非交易日补某一天：`--trade-date` 真的把那天补上（用 `max(trade_date)` 前后对比）
- [ ] 跨多个交易日缺口时，逐日补跑而不是只补最后一天
- [ ] 改时间后 `schtasks /query /tn cne-daily-update /fo LIST /v` 显示新时间
- [ ] 删除任务后 `ScheduleState()` 反映"未注册"，UI 不再显示错误的下次运行时间
- [ ] **部分成功的运行不会被显示成"成功"**（专门用一次有上游失败的运行验证）
- [ ] 上游熔断时不显示"可重试"，而是说明"明天自动重试"

---

## 12. 附：与 CNEquity 侧现状的关系

已经做好并在用的（可直接被本项目接管，**建议改而不是新增**）：

```
D:\workspace\stockTool\CNEquity\cne-daily.cmd     包装脚本（CRLF / 无 BOM / 纯 ASCII / 参数透传）
cne-daily-update   每天 17:30   → cne-daily.cmd
cne-daily-stale    每天 21:00   → cne-daily.cmd --stale-only
日志：data\cnequity\logs\daily-task-YYYY-MM-DD.log（含起止时间与退出码）
```

需要继续保持关闭的：

```
CNEquity 控制台 → 「数据自动更新」卡片（走 cne 自带调度器，§2.5 会自我暂停 → 双跑风险）
```

本项目若要用 DuckDB 语义查询（复权价、去重视图），参见《cne查询性能问题与接入方案.md》；
**日常读取继续用 `internal/cnlake` 直读 parquet 即可，不要为了读数据去起 `cne query` 子进程**
（单次 9~12 秒，其中约 6 秒是重建视图的固定开销）。
