#!/bin/sh
# FreeStock 容器入口。
#
# 职责只有一个：**把 /data 的属主修正成运行用户，然后降权执行**。
#
# 为什么需要它（这是 NAS 上最常见的"能启动但用不了"）：
#   compose 里 `- ./data:/data` 这种 bind mount，若宿主机目录不存在，
#   是 **Docker 以 root 建的**；而镜像以 uid 10001 (freestock) 运行。
#   现象是：应用起来了、网页能打开，但写 config.json 失败、日志写不进、
#   cne 装不进去 —— 每一步都报权限不足，很难看出根因在挂载目录属主上。
#
#   群晖的共享文件夹又常用自定义 uid（比如 1026），光靠文档让人手敲
#   `chown` 很容易漏。所以在入口里自动修一次。
#
# 性能考虑：**正常重启时走快路径**。只有当 /data 不可写、或顶层文件
# 属主不对时才递归 chown —— 避免每次启动都把 cache/ 下几千个文件重新
# chown 一遍（NAS 的机械盘上这能慢到几十秒）。
#
# 想要完全非 root：compose 里加 `user: "10001:10001"`
# （前提是宿主机目录已经 chown 好，否则会因无法修正属主而报错）。
set -eu

DATA_DIR="${FREESTOCK_DATA_DIR:-/data}"
APP_UID="10001"

log() { echo "[entrypoint] $*" >&2; }

# 以目标 uid 测试目录可写；root 用 setpriv 切换后测，避免 root 的权限掩盖问题
writable_as_app() {
    if command -v setpriv >/dev/null 2>&1; then
        setpriv --reuid="${APP_UID}" --regid="${APP_UID}" --init-groups \
            sh -c 'p="$1/.wprobe-$$"; if { : >"$p"; } 2>/dev/null; then rm -f "$p"; exit 0; else exit 1; fi' _ "$1"
    else
        # 没有 setpriv 时退化成"属主是否已经是目标 uid"
        [ "$(stat -c '%u' "$1" 2>/dev/null || echo -1)" = "${APP_UID}" ]
    fi
}

if [ "$(id -u)" = "0" ]; then
    if [ -d "$DATA_DIR" ]; then
        need_chown=0
        if ! writable_as_app "$DATA_DIR"; then
            need_chown=1
        elif find "$DATA_DIR" -maxdepth 1 -mindepth 1 ! -user "${APP_UID}" 2>/dev/null | grep -q .; then
            # 目录可写但里面的 config.json / portfolio.db 是 root 的，
            # 应用仍然改不了 → 也要修
            need_chown=1
        fi

        if [ "$need_chown" = "1" ]; then
            log "修正 ${DATA_DIR} 的属主为 ${APP_UID}（首次启动或权限被改过）"
            if ! chown -R "${APP_UID}:${APP_UID}" "$DATA_DIR" 2>/dev/null; then
                log "警告：无法修改 ${DATA_DIR} 的属主（只读挂载或受保护的共享目录？）"
                log "      若出现权限错误，请在 NAS 上执行："
                log "      chown -R ${APP_UID}:${APP_UID} <宿主机上对应 ${DATA_DIR} 的目录>"
            fi
        fi
    fi

    # setpriv 来自 util-linux，debian:bookworm-slim 自带
    if command -v setpriv >/dev/null 2>&1; then
        exec setpriv --reuid="${APP_UID}" --regid="${APP_UID}" --init-groups "$@"
    fi
    log "警告：找不到 setpriv，将以 root 继续运行（不推荐）"
else
    # ── 非 root 启动（compose 里显式设了 user:）────────────────────────────
    # 这时没法自动修属主，但至少要把"为什么写不了"直接说清楚 ——
    # 否则用户只看到一串 permission denied，根本想不到是 uid 不匹配。
    if [ -d "$DATA_DIR" ] && ! sh -c 'p="$1/.wprobe-$$"; { : >"$p"; } 2>/dev/null && rm -f "$p"' _ "$DATA_DIR"; then
        log "错误：以 uid $(id -u):$(id -g) 运行，但 ${DATA_DIR} 不可写"
        log "      当前属主：$(stat -c '%u:%g (%U:%G)' "$DATA_DIR" 2>/dev/null || echo 未知)"
        log "      修法（宿主机上二选一）："
        log "        1) chown -R $(id -u):$(id -g) <宿主机上对应 ${DATA_DIR} 的目录>"
        log "        2) 去掉 compose 里的 user:，让入口以 root 启动并自动修属主"
    fi
fi

exec "$@"
