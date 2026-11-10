# FreeStock · Docker / NAS 部署与 gitflow

本文是这份仓库「容器化」改动的落地说明。核心约束只有一条：

> **Windows 桌面版的行为必须原样不变**，Linux/NAS 是**并列新增**的一条构建路径。

---

## 1. 三种跑法，一张表

| 形态 | 构建标签 | 入口 | 定时任务由谁执行 |
|---|---|---|---|
| Windows 桌面（原样） | `desktop,production` | `go build` → `freestock.exe` | **Windows 任务计划程序**（schtasks） |
| Linux 桌面 | 默认（需 CGO+GTK） | `go build` | 进程内调度器 |
| **容器 / NAS** | `server` | `go build -tags server` | **应用进程内调度器**（Go goroutine） |

`-tags server` 是 wails v3 自带的「无窗口 HTTP 服务端」实现（见
`application_server.go`）。这套构建**不链接 GTK / WebView2**，所以能
`CGO_ENABLED=0` 静态编译，塞进任何 Linux 基础镜像都能跑。

> 运行镜像是 **Debian slim 而不是 alpine** —— 原因见 §4.4：
> cne 是 Python 包，它的二进制 wheel 是 glibc 的，alpine（musl）装不上。

---

## 2. 定时任务在 Linux/NAS 上怎么工作

### 2.1 为什么不用 cron

原来 `internal/cne/schedule_*.go` 的非 Windows 分支直接返回
「当前平台不支持计划任务」。容器化后这不可接受：界面上的「自动更新」
必须是能真跑的。

三条路都试过：

| 方案 | 问题 |
|---|---|
| 宿主机 cron / crond | 容器里通常没有 cron 守护进程；NAS 的 crond 各家路径不同；还要用户去改宿主机配置，违背"开箱即用" |
| systemd timer | 容器里没有 systemd（PID 1 是我们的进程） |
| **进程内 goroutine** | 应用本来就常驻（容器/服务），到点自己触发；没有外部依赖 |

选第三条。代价说清楚：**应用/容器停着就不会跑**。这在容器部署里是
合理预期（服务没起，本来就没有东西在跑），而且我们补了一层：

> 应用重启后，如果发现「今天还没跑过、而计划时间已过」，会**立刻补跑一次**。
> 这等价于 Windows 那边的 `StartWhenAvailable=true`。

### 2.2 状态存在哪

**不新增配置文件**。开关和时间就是 `config.json` 里已有的四个字段：

```json
{
  "updatePrefs": {
    "autoDaily": true,
    "dailyTime": "17:30",
    "autoStale": true,
    "staleTime": "21:00",
    "catchUpOnStart": true
  }
}
```

另有一个**运行时痕迹** `cne-schedule-state.json`（与 `config.json` 同目录）：

```json
{ "daily": "2026-11-10", "stale": "2026-11-10" }
```

它只记录「今天发过没有」，避免重启后重复跑。属于运行时状态，
不进用户设置，也不该被界面读写。

### 2.3 代码结构（改了什么）

```
internal/cne/
  clock.go              ← 新增（不带 build tag）validClock 从 windows 文件挪出来
  process_cmdline.go    ← 新增（不带 build tag）isUpdateCommand 挪出来
  process_other.go      ← 补齐 hiddenCommand/setupHiddenProcess/killProcessTree
  schedule_other.go     ← 从「不支持」改成委托给 LocalScheduler 接口
  schedule_settings_other.go ← 同上
  schedule_windows.go   ← **未改逻辑**，仅移走 validClock

internal/service/
  schedule_backend.go   ← 新增：scheduleBackend 接口 + ServiceStartup/Shutdown
  schedule_local.go     ← 新增（!windows）：进程内调度器 + LocalScheduler 实现
  schedule_windows.go   ← 新增（windows）：newScheduleBackend 返回 nil
  handles_other.go      ← 新增：openHandleCount 的非 Windows 实现

main.go                 ← 新增 Server 选项 + FREESTOCK_DATA_DIR 日志目录
internal/config/store.go ← FREESTOCK_DATA_DIR 覆盖配置路径（纯增量）
Dockerfile / .dockerignore / docker-compose.yml
.github/workflows/docker.yml / ci.yml
```

**Windows 侧唯一被碰到的共享文件**是 `schedule_windows.go`（移走一个
纯函数）和 `main.go`（新增 `Server` 字段，桌面构建里 wails 完全忽略）。
其余全是新增文件或 `_other.go`。

### 2.4 为什么接口要注入

`cne` 是「调用外部 cne 命令」的底层包。让它反向依赖 `config.Store`
会把两层绑死，也没法单测。所以：

- `cne` 定义 `LocalScheduler` 接口，只做转发
- `service` 用 `config.Store` + `cne.Runner` 实现它，并在 `ServiceStartup` 注册

没注册时返回**明确的**「调度器尚未初始化」，而不是假装成功 ——
静默成功是最坏的失败模式（界面显示已开启，实际什么都没做）。

---

## 3. 构建镜像

```bash
# 本地构建
docker build -t freestock:latest .

# 指定版本（Go 1.26 / Node 22 是当前验证过的组合）
docker build -t freestock:latest \
  --build-arg GO_VERSION=1.26 \
  --build-arg NODE_VERSION=22 \
  --build-arg PNPM_VERSION=12 .

# NAS（arm64 机型，如群晖 DS224+ 是 x86，DS223 是 arm）
docker buildx build --platform linux/arm64 -t freestock:arm64 .
```

镜像分三段：

1. **frontend**：`pnpm install --frozen-lockfile` → `pnpm run build`
   （`main.go` 用 `//go:embed all:frontend/dist`，dist 不存在时 `go build` 直接失败）
2. **builder**：`go mod download` → `go build -tags server`（`CGO_ENABLED=0`）
3. **runtime**：`debian:bookworm-slim` + `python3` + `tzdata`，非 root 用户（uid 10001）

> `tzdata` 不是可选项：定时任务按 **本地时间** 判断到点没有。
> 缺 tzdata 时 `time.Now()` 回落 UTC，「17:30」会变成次日 01:30 才跑。
>
> `python3` 是为了装 cne（见 §4.4）。运行镜像是 Debian 而非 alpine，
> 具体原因也写在 §4.4。

---

## 4. 跑容器

### 4.1 docker compose（推荐）

```bash
docker compose up -d
docker compose logs -f
docker compose down          # 数据留在 ./data
```

唯一必须挂载的目录是 `/data`：

| 路径 | 内容 |
|---|---|
| `/data/config.json` | 设置（**含明文授权码**，注意备份权限） |
| `/data/portfolio.db` | 组合库（SQLite，可能很大） |
| `/data/cache/` | 派生日线缓存 |
| `/data/logs/freeStock.log` | 应用日志 |
| `/data/cne-schedule-state.json` | 定时任务运行痕迹 |
| `/data/cne-venv/` | 运行期装的 cne（见 §4.4；装在卷上，重建容器不丢） |

### 4.2 群晖 / 威联通

**群晖 Container Manager**：
1. 「项目」→「新增」→「上传 compose」
2. 选本仓库的 `docker-compose.yml`
3. 把 `./data:/data` 改成 `/volume1/docker/freestock:/data`
4. 端口 8080 若被占用改左值（如 `18080:8080`）

**威联通 Container Station**：同样支持 compose，路径换成 `/share/Container/freestock`。

> ⚠ NAS 上的权限：镜像以 uid/gid **10001** 运行，但入口脚本会**自动**把
> `/data` chown 成 10001（见 `build/docker/entrypoint.sh`），所以通常不用手动处理。
> 若宿主机目录被系统保护（比如群晖的某些共享文件夹），入口会打一条警告，
> 这时再手动 `chown -R 10001:10001 /volume1/docker/freestock`。
> 想完全跳过 root 阶段：compose 里加 `user: "10001:10001"`（前提是目录已经可写）。

### 4.3 首次配置

访问 `http://<NAS-IP>:8080`，在「设置」里：

1. **数据源**：默认 `http://127.0.0.1:7899` 指的是**容器自己**，
   要改成宿主机或上游的地址；或者切到 `cnlake` 直读本地数据湖
2. **cne 工程目录**：填**容器内路径**，且与宿主机完全一致（同路径挂载，详见 §4.4）
3. **cne 目录 / 可执行文件**：填宿主机 venv 目录（已同路径挂进容器），
   或装进 `/data/cne-venv` 后留空 —— 详见 §4.4
4. **自动更新**：打开后即可看到「两个任务都已启用，由应用内生调度器执行」

### 4.4 cne 装在宿主机 venv 里，容器怎么用它

你的情况：**cne 装在 NAS 宿主机的 Python venv 里**（`cne` 命令来自那个 venv），
而界面里的「cne 工程目录」是**数据目录**（含 `configs/cnequity.toml` 和数据湖）。

容器是另一个文件系统视图，看不到宿主机的 venv，也看不到宿主机上的数据目录。
所以只有**两条铁律**，其余都是配路径：

> **铁律 1：数据目录必须挂载，且挂载点与宿主机路径完全相同。**
> `configs/cnequity.toml` 里写的是**绝对路径**的数据湖位置。挂到别的路径（如 `/cne`）
> 就得改 toml；同路径挂载则一个字都不用改。
>
> **铁律 2：要让容器复用宿主机 venv 里的 cne，也得同路径挂载。**
> venv 里的 `cne` 是 pip 生成的启动脚本，第一行 shebang 写死解释器绝对路径
> （如 `#!/volume1/docker/cne-venv/bin/python`），而那个 python 通常是指向宿主机
> 基础解释器的**符号链接**。换路径挂载，这条链就断了。

#### 第一步：挂数据目录（一定要做）

```yaml
volumes:
  # 左右完全一样 → toml 里的路径不用动
  - /volume1/docker/CNEquity:/volume1/docker/CNEquity:rw
```

必须 `:rw` —— cne 要把新数据写进数据湖；只读会在最后一步失败，报错还很隐晦。

界面里「cne 工程目录」填同一个路径：`/volume1/docker/CNEquity`。

> ⚠ 如果是从 Windows 拷过来的 CNEquity，检查 `configs/cnequity.toml` 里有没有
> `D:\...` —— 那是没法在容器里生效的，要换成 Linux 路径。

#### 第二步：让容器能用 cne 命令

先确认这条链在容器里成不成立（**在宿主机上跑**，只读）：

```bash
sh build/docker/cne-host-check.sh                       # 自动找 command -v cne
sh build/docker/cne-host-check.sh /volume1/docker/cne-venv   # 或指定 venv 目录
```

它会打印 `cne` 的 shebang、顺着符号链接找到的**真实解释器**、Python 版本，
并直接给出结论。

**A. 能复用（解释器在 /usr/bin 或 /usr/local/bin，且版本与镜像一致 3.11）**

```yaml
volumes:
  - /volume1/docker/cne-venv:/volume1/docker/cne-venv:ro   # 同路径
```

界面里「cne 目录 / 可执行文件」填 `/volume1/docker/cne-venv`（**填目录就行**）。
只读挂载足够 —— cne 写的是数据湖（另一个挂载），不是它自己的安装目录。

**B. 不能复用（版本不一致 / 解释器在非标准路径）→ 容器里装一份**

```bash
docker compose exec -u 10001:10001 freestock \
  sh -c 'python3 -m venv /data/cne-venv && /data/cne-venv/bin/pip install -U pip cne'
```

原因：cne 的依赖（pyarrow / pandas / duckdb）是按 **Python 小版本**编好的 `.so`，
跨版本 import 会 `undefined symbol` 甚至段错误，硬挂不划算。

数据目录仍按第一步同路径挂载，宿主机那份 cne 完全不受影响（可以继续在宿主机上用）。
界面里填 `/data/cne-venv`，或**留空**（镜像已把 `/data/cne-venv/bin` 放进 PATH）。

**C. 构建镜像时就装好（可选，版本最确定）**

```bash
docker build --build-arg CNE_PIP_SPEC='cne==1.2.3' -t freestock:latest .
```

装进 `/opt/cne-venv`（镜像内、不可变）。升级 cne 要重建镜像。
`CNE_PIP_SPEC` 刻意**没有默认值**：包名/版本以 CNEquity 发行为准，这里不猜。

#### 先分清三个路径（很容易填错）

以 `/vol2/1001/stockTool/CNEquity` 这种典型布局为例：

| 路径 | 是什么 | 填到哪里 |
|---|---|---|
| `/vol2/1001/stockTool/CNEquity` | **工程目录**，里面有 `configs/cnequity.toml` | 设置 → 数据更新 →「cne 工程目录」 |
| `/vol2/1001/stockTool/CNEquity/data/cnequity` | **数据湖根目录**，里面有 `curated/` | 设置 → 数据源 →「数据湖根目录」 |
| `/vol2/1001/xxx/cne-venv` | **cne 的 venv**（`command -v cne` 能看到） | 设置 → 数据更新 →「cne 目录 / 可执行文件」 |

三者不是同一个东西。`configs/cnequity.toml` 里的数据湖路径通常就是第二行那个，
所以**同路径挂载后一个字都不用改**。

#### 属主：决定 cne 能不能写进数据湖

容器默认以 uid **10001** 运行。如果数据湖属于别的用户（NAS 上很常见，比如 1001），
cne 读得到、**写不进去**。两种解法，推荐第一种：

**A. 让容器以数据属主身份运行（不改宿主机任何权限）**

```yaml
services:
  freestock:
    user: "1001:1001"        # 换成 ls -ldn 看到的属主
```

前置：`./data` 也要属于这个 uid（否则应用写不了配置）：

```bash
mkdir -p ./data && sudo chown 1001:1001 ./data
```

**B. 把数据湖属主改成 10001**

```bash
chown -R 10001:10001 /vol2/1001/stockTool/CNEquity
```

会连宿主机上 cne 的写入身份一起改掉 —— 如果宿主机上还以别的用户跑 cne，别用这个。

查看当前属主与自己的 uid：

```bash
ls -ldn /vol2/1001/stockTool/CNEquity/data/cnequity   # 第一列是 uid、第二列是 gid
id -u; id -g                                          # 你在宿主机上的 uid/gid
```

#### 设置项填什么

| 设置项 | 填什么 | 说明 |
|---|---|---|
| cne 工程目录 | `/volume1/docker/CNEquity`（容器内路径） | 数据目录，**同路径**挂载 |
| cne 目录 / 可执行文件 | `/volume1/docker/cne-venv` 或空 | 见下表 |

第二个输入框**填目录也行**，不用非填到 `cne` 文件：

| 填法 | 例子 | 行为 |
|---|---|---|
| 目录 | `/volume1/docker/cne-venv` | 在目录里找 `cne`、`bin/cne`、`Scripts/cne.exe` |
| 可执行文件 | `/volume1/docker/cne-venv/bin/cne` | 直接用 |
| 裸命令名 | `cne` | 按 PATH 找 |
| 留空 | — | 按 PATH 找，再兜底 `/data/cne-venv/bin/cne` 等 |

找不到时会**列出找过哪些路径**；文件在但没执行位会提示 `chmod +x`
（从宿主机挂进来的文件常丢执行位，这个坑很常见）。

#### 验证（在容器里跑，宿主机通不算）

```bash
docker compose exec freestock cne --version
docker compose exec freestock /volume1/docker/cne-venv/bin/cne --version
```

| 报错 | 原因 | 处理 |
|---|---|---|
| `bad interpreter` | shebang 指向的解释器容器里没有 | 同路径挂 venv，或走方案 B |
| `No module named cne` | venv 没挂 / 挂错路径 | 检查挂载点是否与宿主机一致 |
| `undefined symbol` | Python 小版本不一致 | 走方案 B |
| `Permission denied` | 丢了执行位 | `chmod +x`（界面也会提示） |

#### 如果你打算不用 Docker、直接在 NAS 上跑二进制

那更简单：`cne` 在 PATH 上就能被找到，界面里「cne 目录 / 可执行文件」留空即可。
需要保证的是：

- 用 `-tags server` 构建的二进制（见 §3），或干脆用 Docker；
- 运行时把 `FREESTOCK_DATA_DIR` 指到一个可写目录（不设也能跑，会落到程序目录或用户配置目录）；
- `TZ=Asia/Shanghai` 要设对，否则定时任务的"17:30"会按 UTC 算。

两种跑法在**定时任务语义**上是一样的：都由应用进程内调度器执行，
容器/进程停着就不会跑。

#### 容器里跑 cne 的两个附带好处

- `BuildEnv()` 会剥掉所有代理变量（`HTTP_PROXY` 等，含小写变体），
  这在 NAS 上很常见有用 —— 宿主机常年挂着代理，会让 cne 的 httpx 崩在
  `Invalid port: ':1]'` 这类诡异错误上。
- 取消更新走 `syscall.Kill(-pid)` 杀整个进程组，不会留下孤儿 python
  （cne 会派生子进程）。

---

## 5. GitHub Actions

### 5.1 `docker.yml` —— 自动打包镜像

| 触发 | 行为 |
|---|---|
| push `main`/`master` | 构建 **amd64 + arm64**，推送 `:latest` `:edge` `:<branch>` |
| push tag `v1.2.3` | 推送 `:1.2.3` `:1.2` `:latest` |
| PR | **只构建验证，不推送** |
| 手动 | `workflow_dispatch` |

推到 GHCR：`ghcr.io/<owner>/<repo>:<tag>`。

前提：
- 仓库 Settings → Actions → Workflow permissions 里
  `Read and write permissions`（或用 `GITHUB_TOKEN` 默认 package:write）
- 首次推送后，在 Packages 里把镜像设为 public（若要公开拉取）

拉取：

```bash
docker pull ghcr.io/<owner>/<repo>:latest
```

### 5.2 `ci.yml` —— 防回归

每次 push/PR **同时**编三种目标：

- `windows/amd64` + `desktop,production` ← **原有桌面功能，不许坏**
- `linux/amd64` + `server`
- `linux/arm64` + `server` ← NAS 常见架构

外加 `go vet -tags server ./...` 与三个纯逻辑包的 `go test -short`。

> 这不是形式主义：本次改动就踩过 `undefined: validClock` /
> `undefined: hiddenCommand` / `undefined: isUpdateCommand` /
> `openHandleCount` 四处「Windows 有、Linux 没有」的编译坑，
> 双平台构建是唯一能自动抓住它们的办法。

---

## 6. gitflow

### 6.1 分支模型

```
main        ← 永远可发布；推送即构建 :latest
  ↑ merge --no-ff
develop     ← 集成分支；推送即构建 :edge
  ↑ merge --no-ff
feature/*   ← 从 develop 拉，一个功能一条
release/*   ← 从 develop 拉，只修 bug、只改版本号
hotfix/*    ← 从 main 拉，修完同时合回 main 与 develop
```

### 6.2 一次功能开发

```bash
git switch develop
git pull origin develop
git switch -c feature/docker-nas
# ...写代码...
git add -A
git commit -m "feat(docker): 容器化并支持 Linux/NAS 定时任务"
git push -u origin feature/docker-nas
# 开 PR → develop；CI 必须三个编译目标全绿
```

### 6.3 发布

```bash
git switch -c release/v1.1.0 develop
# 只允许：改版本号、修 bug、更新 CHANGELOG
git commit -am "chore(release): v1.1.0"
git switch main
git merge --no-ff release/v1.1.0
git tag -a v1.1.0 -m "v1.1.0"
git push origin main --follow-tags     # → 触发多架构镜像 :1.1.0
git switch develop
git merge --no-ff release/v1.1.0
git push origin develop
git branch -d release/v1.1.0
```

### 6.4 紧急修复

```bash
git switch -c hotfix/v1.1.1 main
# 修
git switch main && git merge --no-ff hotfix/v1.1.1
git tag -a v1.1.1 -m "v1.1.1"
git push origin main --follow-tags
git switch develop && git merge --no-ff hotfix/v1.1.1 && git push
```

### 6.5 提交信息

```
<type>(<scope>): <subject>

type: feat | fix | docs | refactor | test | chore | perf | build
scope: docker | cne | schedule | config | frontend | service
```

例：

```
feat(schedule): Linux/NAS 用进程内调度器替代 schtasks
fix(config): FREESTOCK_DATA_DIR 不可写时回落到默认路径
build(docker): 多阶段构建 + tzdata + 非 root 用户
```

---

## 7. 本地验证清单

容器化改动最容易在「另一半平台」上翻车。发布前跑一遍：

```bash
# 1. Windows 桌面版必须还能编（原有功能不许回归）
$env:GOOS='windows'; $env:CGO_ENABLED='0'
go build -tags 'desktop,production' -o .tmp/freeStock.exe .

# 2. Linux server 版（容器里跑的那个）
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -tags server -o .tmp/freeStock-server .

# 3. arm64（NAS）
$env:GOARCH='arm64'
go build -tags server -o .tmp/freeStock-server-arm64 .

# 4. 静态检查两套标签都要过
go vet -tags 'desktop,production' ./...
$env:GOOS='linux'; go vet -tags server ./...

# 5. 前端 embed 依赖：dist 必须存在，且资源能被服务
cd frontend && pnpm run build && cd ..
go test -run TestEmbeddedFrontendIsServed .

# 6. 运行时冒烟（容器）
docker compose up -d
curl -fsS http://127.0.0.1:8080/health      # {"status":"ok"}
curl -fsS http://127.0.0.1:8080/ | head -c 200

# 7. 数据卷与权限（入口脚本会自动 chown，这里确认一下）
docker compose exec freestock ls -l /data
docker compose exec -u 10001:10001 freestock sh -c 'id; test -w /data && echo "可写 ✓"'

# 8. cne 是否可用（装完才有）
docker compose exec -u 10001:10001 freestock sh -c 'command -v cne && cne --help | head -5'
```

---

## 8. 已知限制

1. **cne 要装进容器，不能复用 NAS 宿主机上那份**（命名空间隔离）。
   装法见 §4.4；装完界面里 CNEPath 留空即可
2. **镜像约 250MB**（Debian + Python），比 alpine 版大 —— 这是为了能跑 cne
3. **容器停止期间不会执行定时任务**；重启后补跑当天错过的那一次
4. **不提供认证**。wails server 模式没有登录层 —— NAS 只在内网用，
   不要把 8080 直接暴露到公网；需要外网访问请套 Nginx + Basic Auth / mTLS
5. **`config.json` 里授权码是明文**（沿用原设计）。挂载卷权限收紧，
   不要提交到仓库
6. **`/data` 必须持久化**。用 `docker compose down -v` 或删卷会丢配置与组合库，
   以及装在 `/data/cne-venv` 里的 cne
7. **容器内没有认证也不做多用户隔离** —— 谁能访问 8080 谁就能看你的组合数据
