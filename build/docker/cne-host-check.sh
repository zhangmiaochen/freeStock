#!/bin/sh
# 在 **NAS 宿主机**上运行（不是容器里），判断「宿主机 venv 里的 cne 能不能被容器用」。
#
# 用法：
#   ./cne-host-check.sh                                  # 自动用 command -v cne
#   ./cne-host-check.sh /volume1/docker/cne-venv         # 给 venv 根目录
#   ./cne-host-check.sh /volume1/docker/cne-venv/bin/cne # 给可执行文件
#
# 为什么需要它（这是唯一真正的坑）：
#   venv 里的 `cne` 是 pip 生成的启动脚本，**第一行 shebang 写死解释器绝对路径**，
#   例如 `#!/volume1/docker/cne-venv/bin/python`；而那个 python 通常是指向
#   宿主机基础解释器的**符号链接**（如 /usr/bin/python3.11）。
#
#   容器是另一个文件系统视图。要让 cne 跑起来，这条链必须整条成立：
#     脚本 shebang → venv/bin/python → 基础解释器 → 版本与镜像一致
#   少一环就是 bad interpreter / No module named cne / undefined symbol。
#
#   本脚本把这条链逐环打印出来并给出结论，省得在容器里猜。
#
# 只读，不修改任何东西。
set -eu

# 镜像里的 Python 版本（Debian 12 bookworm）。
# 可用 FREESTOCK_IMG_PY 覆盖 —— 将来若换基础镜像版本，这个脚本不用改。
IMG_PY="${FREESTOCK_IMG_PY:-3.11}"

ARG="${1:-}"
if [ -z "$ARG" ]; then
    ARG=$(command -v cne 2>/dev/null || true)
    if [ -z "$ARG" ]; then
        echo "✗ PATH 上找不到 cne，也没给参数。" >&2
        echo "  用法: $0 /volume1/docker/cne-venv" >&2
        exit 2
    fi
fi
if [ ! -e "$ARG" ]; then
    echo "✗ 不存在: $ARG" >&2
    exit 1
fi

# 目录 → 在常见布局里找 cne
CNE="$ARG"
if [ -d "$ARG" ]; then
    CNE=""
    for cand in "$ARG/bin/cne" "$ARG/cne" "$ARG/Scripts/cne.exe" "$ARG/bin/cne.exe"; do
        if [ -f "$cand" ]; then
            CNE="$cand"
            break
        fi
    done
    if [ -z "$CNE" ]; then
        echo "✗ 在 $ARG 里没找到 cne（找过 bin/cne、cne、Scripts/cne.exe）" >&2
        exit 1
    fi
fi

echo "启动脚本   : $CNE"
if [ -x "$CNE" ]; then
    echo "执行权限   : ✓"
else
    echo "执行权限   : ✗ 没有执行位 —— 先 chmod +x \"$CNE\""
fi

SHEBANG=$(head -n 1 "$CNE" | sed 's/^#!//')
PY="${SHEBANG%% *}"
echo "shebang    : $SHEBANG"

if [ ! -e "$PY" ]; then
    echo "✗ shebang 指向的解释器不存在: $PY" >&2
    echo "  这个 cne 在宿主机上就起不来，先修宿主机。" >&2
    exit 1
fi
PYREAL=$(readlink -f "$PY" 2>/dev/null || echo "$PY")
echo "解释器     : $PY"
[ "$PYREAL" != "$PY" ] && echo "             → $PYREAL"

PYVER=$("$PYREAL" -c 'import sys;print("%d.%d"%sys.version_info[:2])' 2>/dev/null || echo "?")
echo "Python     : $PYVER"

VENV_ROOT=$(dirname "$(dirname "$PY")")
if [ -f "$VENV_ROOT/pyvenv.cfg" ]; then
    echo "venv 根目录: $VENV_ROOT"
else
    echo "venv 根目录: （没找到 pyvenv.cfg，可能不是 venv 安装）"
    VENV_ROOT=""
fi

echo ""
echo "──────────────────────────────────────────────────────────"

case "$PYREAL" in
    /usr/bin/python3* | /usr/local/bin/python3*)
        INTERP_OK=1
        ;;
    *)
        INTERP_OK=0
        ;;
esac

if [ "$PYVER" = "$IMG_PY" ] && [ "$INTERP_OK" = "1" ]; then
    echo "✓ 解释器是标准路径且 Python 版本与镜像一致（$IMG_PY），可以同路径挂载直接复用。"
    if [ ! -x "$CNE" ]; then
        echo ""
        echo "  ⚠ 但当前**没有执行位**，挂进容器后会被拒绝执行："
        echo "    chmod +x \"$CNE\""
    fi
    echo ""
    if [ -n "$VENV_ROOT" ]; then
        echo "  compose 里加（左右必须完全一样）："
        echo "    - $VENV_ROOT:$VENV_ROOT:ro"
        echo ""
        echo "  界面里「cne 目录 / 可执行文件」填：$VENV_ROOT"
    else
        echo "  compose 里加（左右必须完全一样）："
        echo "    - $CNE:$CNE:ro"
        echo "    - $PYREAL:$PYREAL:ro"
        echo ""
        echo "  界面里填：$CNE"
    fi
    echo ""
    echo "  验证（必须在容器里跑通，宿主机通不算）："
    echo "    docker compose exec freestock \"$CNE\" --version"
    echo ""
    echo "  注意：只读挂载就够了；cne 写的是数据湖（另一个挂载），不是它自己的安装目录。"
else
    echo "⚠ 这条链在容器里对不上："
    [ "$PYVER" != "$IMG_PY" ] && echo "    Python 版本 $PYVER ≠ 镜像 $IMG_PY"
    [ "$INTERP_OK" = "0" ] && echo "    解释器在非标准路径（$PYREAL），容器里没有这个路径"
    echo ""
    echo "  硬挂大概率失败：cne 的依赖（pyarrow/pandas/duckdb）是按 Python 小版本"
    echo "  编好的 .so，跨版本 import 会 undefined symbol 甚至段错误。"
    echo ""
    echo "  推荐：容器里装一份，数据目录仍用宿主机那份（宿主机那份 cne 不受影响）"
    echo "    docker compose exec -u 10001:10001 freestock \\"
    echo "      sh -c 'python3 -m venv /data/cne-venv && /data/cne-venv/bin/pip install -U pip cne'"
    echo ""
    echo "  然后界面里填 /data/cne-venv（或留空，镜像已把该路径放进 PATH）。"
    echo "  ⚠ 数据目录（cne 工程目录）仍要同路径挂载 —— toml 里写的是绝对路径。"
fi
echo "──────────────────────────────────────────────────────────"
