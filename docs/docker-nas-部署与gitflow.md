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
2. **cne 工程目录**：容器内路径（例如 `/cne`，取决于你在 compose 里挂到哪）
3. **cne 可执行文件**：**留空**（装好 cne 后会走 PATH 自动找到，见 §4.4）
4. **自动更新**：打开后即可看到「两个任务都已启用，由应用内生调度器执行」

### 4.4 cne 装在哪儿（重要）

**先说结论：cne 要装在容器里，不是装在 NAS 宿主机上。**

容器是独立命名空间，看不到宿主机的 `/usr/bin/cne`、`/volume1/@appstore/...`。
你在 NAS 上 `pip install cne` 装出来的那份，容器里**既看不见也执行不了** ——
不是 PATH 没配好，是根本没有那个文件系统视图。

（如果确实想复用宿主机那份，只能把它的目录挂进来：
`- /volume1/@appstore/cne:/opt/cne-host:ro`，然后界面里填 `/opt/cne-host/bin/cne`。
但这要求宿主机的 Python 与镜像里的 Python 版本、glibc 都对得上，**很容易踩坑**，
且 NAS 一升级 Python 就可能全废。所以只作为兜底，不推荐。）

#### 推荐做法：装进数据卷上的 venv

```bash
# 1. 以运行用户身份建 venv（放在 /data，容器重建也不丢）
docker compose exec -u 10001:10001 freestock \
  python3 -m venv /data/cne-venv

# 2. 装 cne（包名以 CNEquity 的发行说明为准）
docker compose exec -u 10001:10001 freestock \
  /data/cne-venv/bin/pip install --upgrade pip

docker compose exec -u 10001:10001 freestock \
  /data/cne-venv/bin/pip install cne

# 3. 验证（应该打印帮助）
docker compose exec -u 10001:10001 freestock cne --help
```

为什么这样能直接生效：镜像里已经把 `/data/cne-venv/bin` 放进了 `PATH`
（见 Dockerfile），而后端找 cne 的顺序是
**设置里的 CNEPath → `exec.LookPath("cne")` → 几个兜底路径**，
所以界面里的「cne 可执行文件」**留空**就能找到。

#### 另一种：构建镜像时就装好

```bash
docker build --build-arg CNE_PIP_SPEC='cne==1.2.3' -t freestock:latest .
```

装进 `/opt/cne-venv`（镜像内、不可变）。好处是版本确定、可复现；
坏处是每次升级 cne 都要重建镜像 —— 对 NAS 来说通常不如上面那种方便。

`CNE_PIP_SPEC` 刻意**没有默认值**：cne 的确切 PyPI 包名/版本以 CNEquity 的发行为准，
这里不猜。留空时构建会跳过这一步，不会产出"看着有 cne 其实坏的"镜像。

#### 为什么运行镜像是 Debian 而不是 alpine

cne 是 Python 包，依赖里几乎必然有 `pyarrow` / `pandas` / `duckdb` 这类**二进制 wheel**。
PyPI 上的预编译 wheel 是 **manylinux（glibc）** 的，而 alpine 是 **musl** ——
`pip install` 要么找不到匹配 wheel 直接失败，要么现场源码编译几十分钟然后大概率失败。

所以运行阶段用 `debian:bookworm-slim`。镜像从 ~80MB 涨到 ~250MB，
但**跑不了 cne 的镜像没有意义**，这个代价必须付。

#### 还需要挂数据湖（读写）

cne 会把新数据写进数据湖，所以 CNEquity 目录必须**读写挂载**（不要 `:ro`）：

```yaml
volumes:
  - /volume1/docker/CNEquity:/cne:rw
```

然后在界面里把「cne 工程目录」填 `/cne`（该目录下要有 `configs/cnequity.toml`）。
只读挂载的典型现象是：更新跑了很久，最后一步写盘失败，报错还很隐晦。

> ⚠ 如果你是把 Windows 上的 CNEquity 整个拷过来的，**检查 `configs/cnequity.toml`
> 里的路径**：里面若写着 `D:\...`，在容器里一律无效。要改成容器内路径
> （就是 `/cne` 这一套），数据湖根目录同理。

#### 如果你打算不用 Docker、直接在 NAS 上跑二进制

那更简单：`cne` 在 PATH 上就能被找到，后端 `exec.LookPath("cne")` 直接命中，
界面里「cne 可执行文件」留空即可。需要保证的是：

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
