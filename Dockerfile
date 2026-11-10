# FreeStock · 多阶段 Dockerfile（server 模式，Linux / NAS）
#
# 三个设计前提，都是踩过的坑：
#
#  1. **前端必须先构建**：main.go 用 `//go:embed all:frontend/dist`
#     把前端产物打进二进制。没跑过 vite build 时 go build 直接失败
#     （embed 目录为空/不存在）。所以 CGO/Go 阶段之前必须先有一个
#     pnpm build 阶段。
#  2. **依赖层单独缓存**：go.mod/go.sum 不变时不重复 `go mod download`。
#     前端同理（先只拷 package.json + lockfile 再 install）。
#  3. **二进制是静态的**（CGO_ENABLED=0）：不依赖宿主 glibc，
#     换基础镜像（debian / alpine）都不用重编，也方便交叉编译到 arm64。
#     本项目用的 modernc.org/sqlite 与 parquet-go 都是纯 Go 实现，关掉 CGO 完全没问题。
#
# 构建：
#   docker build -t freestock:latest .
# 指定版本/镜像源：
#   docker build --build-arg GO_VERSION=1.26 --build-arg NODE_VERSION=22 .
#
# ⚠ 这些 ARG 必须声明在**第一个 FROM 之前**：
# FROM 里可以引用全局 ARG（`${VAR:-default}`），但没声明过的变量
# 在部分 builder 上会展开成空串，得到 `node:-alpine` 这种非法镜像名。
ARG NODE_VERSION=22
ARG GO_VERSION=1.26
ARG DEBIAN_VERSION=bookworm
ARG PNPM_VERSION=12

# 可选：构建镜像时就把 cne 装进去。
# 留空（默认）则跳过，改为运行期装进 /data 上的 venv（见 docs §4.4）。
# 用法：docker build --build-arg CNE_PIP_SPEC='cne' .
# ⚠ 包名以 CNEquity 的发行说明为准；这里不写死，避免猜错。
ARG CNE_PIP_SPEC=

# ---------------------------------------------------------------------------
# 阶段 1：前端产物（frontend/dist）
# ---------------------------------------------------------------------------
# ⚠ 这两个阶段都固定在 **BUILDPLATFORM（构建机本身，通常 amd64）**，
# 只让产物交叉编译到目标架构 —— 这是多架构构建能不能在合理时间内跑完的关键。
#
# 如果不固定：buildx 会去拉 arm64 的 golang 镜像，然后在 QEMU 里**模拟执行
# Go 编译器**。编译 parquet-go + modernc sqlite 这种体量的依赖，
# 模拟编译要几十分钟（甚至超时）。Go 本来就原生支持交叉编译，
# 在 amd64 上编 arm64 二进制只要几秒。
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS frontend

# pnpm 版本与前端 lockfile 对齐（lockfileVersion 9.0）。
# ⚠ 必须**固定大版本**：pnpm 11+ 会校验 pnpm-workspace.yaml 里的
# allowBuilds，版本不对时 esbuild/vue-demi 的安装脚本会被跳过或直接报
# ERR_PNPM_IGNORED_BUILDS，表现为 vite build 阶段找不到 esbuild 二进制。
# 这里跟随开发机（pnpm 12.x）；如果构建报 pnpm 相关错误，
# 用 --build-arg PNPM_VERSION=<本机 pnpm --version> 覆盖即可。
ARG PNPM_VERSION
RUN npm install -g pnpm@${PNPM_VERSION} && pnpm --version

WORKDIR /src/frontend

# 先只拷依赖清单，最大化利用层缓存
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

# 再拷源码并构建（vue-tsc + vite build --mode production）
COPY frontend/ ./
RUN pnpm run build

# 产物自检：embed 需要 index.html 存在，缺了 go build 会报
# "pattern all:frontend/dist: no matching files found"，很难看懂。
RUN test -f dist/index.html || (echo "前端构建没有产出 dist/index.html" >&2; exit 1)

# ---------------------------------------------------------------------------
# 阶段 2：Go 二进制（-tags server，纯静态）
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

# 这两行**不要给默认值**：buildx 会自动注入目标平台。
# 一旦写成 `ARG TARGETARCH=amd64`，默认值会遮蔽注入值 ——
# arm64 构建会静默产出 amd64 二进制，直到运行时报 "exec format error"。
ARG TARGETOS
ARG TARGETARCH

# git 用于 go mod download 时拉取依赖（部分模块走 vcs）
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# 依赖层（go.mod/go.sum 不变就不重新下载）
COPY go.mod go.sum ./
RUN go mod download

# 源码（.dockerignore 已排除 .gotmp/.gocache/node_modules 等大目录）
COPY . .

# 用前面阶段构建好的前端产物覆盖（宿主机上的 dist 不可信，可能是旧的）
COPY --from=frontend /src/frontend/dist ./frontend/dist

# -tags server：wails 走无窗口 HTTP 服务端实现（application_server.go），
#               不链接 GTK/WebView，因此不需要图形库。
# CGO_ENABLED=0：纯静态 Go 二进制，不依赖目标机的 glibc/musl；
#               也是"能在 amd64 上交叉编译 arm64"的前提（关掉 CGO 才能跨架构）。
#               本项目用的 modernc.org/sqlite / parquet-go 都是纯 Go，关掉没问题。
# GOOS/GOARCH 取 buildx 注入的 TARGETOS/TARGETARCH。
# -trimpath -ldflags "-s -w"：去掉本机路径与调试符号，镜像更小且可复现。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -tags server -trimpath \
      -ldflags "-s -w" \
      -o /out/freestock-server . \
 && ls -lh /out/freestock-server

# ---------------------------------------------------------------------------
# 阶段 3：运行时
# ---------------------------------------------------------------------------
# ⚠ **必须用 glibc 基础镜像（Debian），不能用 alpine。**
#
# 原因不是口味，是 cne 跑不起来：cne 是 Python 包，依赖里几乎必然有
# pyarrow / pandas / duckdb 这类**带二进制 wheel** 的东西，而 PyPI 上的
# 预编译 wheel 是 manylinux（glibc）的 —— alpine 是 musl，pip 找不到
# 匹配的 wheel，要么直接报错，要么现场编译几十分钟还大概率失败。
#
# 镜像因此从 ~80MB 变成 ~250MB。这个代价必须付：**跑不了 cne 的镜像没有意义**。
FROM debian:${DEBIAN_VERSION}-slim

# tzdata 是**必须**的：定时任务按 Asia/Shanghai 的本地时间判断"到点没有"，
# 缺 tzdata 时 time.Now() 回落到 UTC，17:30 会变成次日 01:30 才跑。
# python3 / python3-venv：cne 是 Python 工具，装它需要 venv+pip。
# ca-certificates：HTTPS 数据源 / 邮件 / pip 源。
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata wget \
      python3 python3-venv python3-pip \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 freestock \
 && useradd --uid 10001 --gid freestock --create-home --shell /bin/bash freestock \
 && mkdir -p /data && chown freestock:freestock /data

COPY --from=builder /out/freestock-server /usr/local/bin/freestock-server

# 可选：把 cne 装进镜像（不开则留到运行期装进 /data，见文档 §4.4）
#
# ⚠ 这里**不做 `|| true` 兜底**：装了却装坏，镜像应该直接构建失败，
# 而不是产出一个"看起来有 cne、其实一跑就报错"的镜像 ——
# 那种镜像到点会静默失败，最难排查。
ARG CNE_PIP_SPEC
RUN if [ -n "$CNE_PIP_SPEC" ]; then \
      python3 -m venv /opt/cne-venv \
      && /opt/cne-venv/bin/pip install --no-cache-dir --upgrade pip \
      && /opt/cne-venv/bin/pip install --no-cache-dir "$CNE_PIP_SPEC" \
      && ln -sf /opt/cne-venv/bin/cne /usr/local/bin/cne \
      && test -x /usr/local/bin/cne; \
    fi

# 入口脚本负责：把 /data 属主修正成运行用户，然后降权执行。
# 见 build/docker/entrypoint.sh 里的说明 —— 这是 NAS 上"能启动但写不了文件"
# 的常见根因（bind mount 目录是 Docker 以 root 建的）。
COPY build/docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

# 注意：**入口不 drop 到 USER freestock**，而是保持 root 进入脚本，
# 由脚本 chown 之后用 setpriv 降权 —— 这样才能修正挂载目录属主。
# 想要完全非 root（不做 chown），compose 里加 `user: "10001:10001"` 即可。

# 配置、组合库（portfolio.db）、缓存、日志全部落在 /data —— 这是唯一需要挂卷的目录。
#
# ⚠ **HOME 必须指到 /data**，不能留默认值（会变成 /root 或 /home/freestock）：
#   cne 会读写 `~/.config/cnequity/`（Linux 上它的默认配置位置，也可能存凭据/缓存）。
#   如果 HOME 指向一个不存在或不可写的目录，cne 要么读不到配置，要么写不进去 ——
#   两种都是"跑起来了但结果不对"，很难查。
#   指到 /data 后：`~` 永远可写（入口脚本会 chown），且内容随数据卷持久化。
ENV FREESTOCK_DATA_DIR=/data \
    HOME=/data \
    XDG_CONFIG_HOME=/data/.config \
    TZ=Asia/Shanghai \
    WAILS_SERVER_HOST=0.0.0.0 \
    WAILS_SERVER_PORT=8080

# ⚠ **PATH 里要包含 /data/cne-venv/bin**：
# 用户把 cne 装在数据卷上（推荐做法，见 docs §4.4）时，`cne` 命令就是
# 从那里被找到的 —— runner.executable() 会 exec.LookPath("cne")。
# 不写进 PATH 的话，用户必须在界面里手填绝对路径，多一步且容易填错。
#
# PYTHONDONTWRITEBYTECODE：容器以非 root 运行，写不了 site-packages 的
# __pycache__，关掉可以避免一堆无害但吓人的权限告警。
ENV PATH="/data/cne-venv/bin:/opt/cne-venv/bin:${PATH}" \
    PYTHONDONTWRITEBYTECODE=1

# WORKDIR 用 /data：cne 的工程目录默认也在这里，相对路径不会乱跑。
WORKDIR /data
VOLUME ["/data"]

EXPOSE 8080

# wails server 模式自带 /health（见 application_server.go）
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${WAILS_SERVER_PORT}/health" > /dev/null 2>&1 || exit 1

# 用 exec 形式：SIGTERM 直接到进程，容器停止时能优雅关闭（wails 有信号处理）
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/freestock-server"]
