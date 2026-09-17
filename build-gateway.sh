#!/usr/bin/env bash
# 交叉编译 YYB Go 融合网关的 Linux 预编译二进制（Dockerfile 依赖它们）。
#
# 用法：
#   ./build-gateway.sh            # 编译 amd64 + arm64
#   ./build-gateway.sh amd64      # 只编译 amd64
#
# 产物：
#   gateway/yyb-go-amd64
#   gateway/yyb-go-arm64
#
# 说明：store 用的是纯 Go 的 modernc.org/sqlite，因此 CGO_ENABLED=0 即可
# 静态交叉编译，无需交叉工具链。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
SRC="$ROOT/src"
OUT="$ROOT/gateway"

# ⚠️ 关键：go 是原生 Windows 程序，看不懂 MSYS 风格的 /d/xxx 路径。
# 直接传过去它会把 "/d/腾讯AI工作/..." 当成相对路径，拼成 "D:\d\腾讯AI工作\..."
# 这个「幽灵目录」——构建看似成功，实际产物根本不在仓库里。
# 所以凡是交给 go 的路径，都必须先转成 D:/xxx 形式。
winpath() {
    case "$1" in
        /[a-zA-Z]/*) printf '%s' "$(printf '%s' "$1" | sed -E 's|^/([a-zA-Z])/|\1:/|')" ;;
        *) printf '%s' "$1" ;;
    esac
}

OUT_WIN="$(winpath "$OUT")"

if [ ! -d "$SRC/cmd/yyb-go" ]; then
    echo "找不到 $SRC/cmd/yyb-go，请在仓库根目录运行本脚本" >&2
    exit 1
fi

ARCHES=("$@")
if [ ${#ARCHES[@]} -eq 0 ]; then
    ARCHES=(amd64 arm64)
fi

export CGO_ENABLED=0
: "${GOPROXY:=https://goproxy.cn,direct}"
export GOPROXY

mkdir -p "$OUT"

for arch in "${ARCHES[@]}"; do
    case "$arch" in
        amd64|arm64) ;;
        *) echo "不支持的架构: $arch（仅 amd64 / arm64）" >&2; exit 1 ;;
    esac
    echo "==> 编译 linux/$arch ..."
    (
        cd "$SRC"
        GOOS=linux GOARCH="$arch" \
            go build -trimpath -ldflags "-s -w" \
            -o "${OUT_WIN}/yyb-go-$arch" ./cmd/yyb-go
    )
    # 校验产物真的落到了仓库里（防止再出现幽灵目录那种"假成功"）
    if [ ! -f "$OUT/yyb-go-$arch" ]; then
        echo "!! $OUT/yyb-go-$arch 未生成，构建路径可能又被 MSYS 转换搞错了" >&2
        exit 1
    fi
    echo "    -> gateway/yyb-go-$arch ($(du -h "$OUT/yyb-go-$arch" | cut -f1))"
done

echo
echo "完成。接着同步 Web 面板模板并重建镜像："
echo "  cp -r src/resource/templates/. gateway/resource/templates/"
echo "  docker compose -f compose.build.yaml up -d --build"
