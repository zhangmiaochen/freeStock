#!/bin/sh
# 在 **NAS 宿主机**上运行（不是容器里），用于确认「宿主机装的 cne 能不能被容器用」。
#
# 用法：
#   ./cne-host-check.sh /volume1/docker/cne-venv
#
# 为什么需要这个脚本：
#   `cne` 是 pip 生成的 Python 启动脚本，第一行 shebang 是**绝对路径**的解释器。
#   容器是另一个文件系统视图 —— 只有当那个解释器路径在容器里**也存在**时，
#   `cne` 才能跑起来。本脚本把这个路径找出来并给出结论，省得你在容器里
#   对着 "bad interpreter: No such file or directory" 猜。
#
# 它只读，不修改任何东西。
set -eu

if [ $# -lt 1 ]; then
    echo "用法: $0 <cne 所在目录，例如 /volume1/docker/cne-venv>" >&2
    exit 2
fi

DIR="${1%/}"

# 找到 cne 本体（支持目录 / venv 根目录 / 直接给可执行文件）
CNE=""
for cand in "$DIR" "$DIR/cne" "$DIR/bin/cne"; do
    if [ -f "$cand" ] && [ ! -d "$cand" ]; then
        CNE="$cand"
        break
    fi
done
if [ -z "$CNE" ]; then
    echo "✗ 在 $DIR 里没找到 cne（找过：$DIR、$DIR/cne、$DIR/bin/cne）" >&2
    exit 1
fi

echo "cne 本体: $CNE"
if [ ! -x "$CNE" ]; then
    echo "✗ 没有执行权限 —— 宿主机上先 chmod +x \"$CNE\""
    exit 1
fi
echo "执行权限: ✓"

# shebang 里的解释器路径
SHEBANG=$(head -n 1 "$CNE" | sed 's/^#!//')
echo "shebang : $SHEBANG"

PY="${SHEBANG%% *}"
if [ ! -e "$PY" ]; then
    echo "✗ shebang 指向的解释器不存在: $PY"
    echo "  这个 cne 在宿主机上都跑不起来，先修好宿主机。"
    exit 1
fi

# 顺着符号链接找到真正的解释器 —— 容器里需要的正是**这个**路径
REAL=$(readlink -f "$PY" 2>/dev/null || echo "$PY")
echo "真实解释器: $REAL"
echo "Python 版本: $("$REAL" --version 2>&1 || echo '无法执行')"

echo ""
echo "──────────────────────────────────────────────────────────"
case "$REAL" in
    /usr/bin/python3*|/usr/local/bin/python3*)
        echo "✓ 这个解释器路径在 Debian 镜像是**存在**的（/usr/bin/python3.x）"
        echo ""
        echo "  按原路径挂载即可，容器里应当能直接执行："
        echo "    compose 里加：  - $DIR:$DIR:ro"
        echo "    界面里填：      $DIR          （填目录就行，不用填到 cne 文件）"
        echo ""
        echo "  验证："
        echo "    docker compose exec freestock $CNE --version"
        ;;
    *)
        echo "⚠ 解释器在 **非标准路径**（不是 /usr/bin 或 /usr/local/bin）"
        echo "  容器里没有这个路径，直接挂载会报 bad interpreter。"
        echo ""
        echo "  两条路："
        echo "  1) 在容器里重建 venv（推荐，最省心）："
        echo "       docker compose exec -u 10001:10001 freestock \\"
        echo "         sh -c 'python3 -m venv /data/cne-venv && /data/cne-venv/bin/pip install cne'"
        echo "     然后界面里填 /data/cne-venv"
        echo ""
        echo "  2) 把宿主机解释器也按原路径挂进去（要求两边 glibc/Python 小版本一致）："
        echo "       - $REAL:$REAL:ro"
        echo "     （Python 通常还依赖 libpython 等 .so，容易缺库，不如方案 1）"
        ;;
esac
echo "──────────────────────────────────────────────────────────"
