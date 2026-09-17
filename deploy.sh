#!/usr/bin/env bash
# ============================================================================
#  wxcode + YYB Go 融合网关 · 一键部署
#
#  方式一（推荐，无需 clone）：
#    curl -fsSL https://raw.githubusercontent.com/SJZYKJ/wxcode-yyb-fusion-docker/main/deploy.sh | bash
#
#  方式二（已 clone 本仓库）：
#    ./deploy.sh
#
#  常用参数：
#    --dir <路径>        部署目录（默认：仓库目录，或 ./wxcode-yyb-fusion-docker）
#    --port <端口>       宿主机端口（默认 8088）
#    --bind <地址>       监听地址（默认 0.0.0.0；公网机建议 127.0.0.1 配合反代）
#    --token <令牌>      指定访问令牌（默认自动生成 48 位随机串）
#    --no-token          不启用访问令牌（⚠️ 取码接口裸奔，仅限完全可信内网）
#    --image-tag <标签>  指定镜像标签（默认 latest，可回滚到 SHA-0917-V1.0）
#    --build             用本仓库源码本地构建（compose.build.yaml）
#    --dry-run           只准备 .env 并打印要执行的命令，不真的启动容器
#    --logs              启动后跟踪容器日志
#    --uninstall         停止并删除容器（保留数据目录）
#    -h | --help         查看帮助
#
#  幂等：重复执行 = 升级重启；不会覆盖已有的 .env、访问令牌与数据。
# ============================================================================
set -euo pipefail

REPO_SLUG="SJZYKJ/wxcode-yyb-fusion-docker"
GH_RAW="${GH_RAW:-https://raw.githubusercontent.com/${REPO_SLUG}/main}"

DIR=""
PORT=""
BIND_ADDR=""
TOKEN=""
TOKEN_MODE="auto"      # auto | none
IMAGE_TAG=""
BUILD=0
DRY_RUN=0
FOLLOW_LOGS=0
UNINSTALL=0
FINAL_TOKEN=""
DATA_ABS=""
COMPOSE=()

# ---------------------------------------------------------------- 输出
if [ -t 1 ]; then
    C_RESET=$'\033[0m'; C_DIM=$'\033[2m'; C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_CYAN=$'\033[36m'; C_BOLD=$'\033[1m'
else
    C_RESET=""; C_DIM=""; C_RED=""; C_GREEN=""; C_YELLOW=""; C_CYAN=""; C_BOLD=""
fi
info() { printf '%s\n' "${C_CYAN}▸${C_RESET} $*"; }
ok()   { printf '%s\n' "${C_GREEN}✔${C_RESET} $*"; }
warn() { printf '%s\n' "${C_YELLOW}!${C_RESET} $*" >&2; }
die()  { printf '%s\n' "${C_RED}✘ $*${C_RESET}" >&2; exit 1; }
step() { printf '\n%s\n' "${C_BOLD}$*${C_RESET}"; }

usage() {
    cat <<'USAGE'
wxcode + YYB Go 融合网关 · 一键部署

用法：
  ./deploy.sh [选项]              # 在仓库目录里执行
  curl -fsSL <raw>/deploy.sh | bash   # 不 clone，直接部署

选项：
  --dir <路径>        部署目录（默认：仓库目录，或 ./wxcode-yyb-fusion-docker）
  --port <端口>       宿主机端口（默认沿用 .env，初始 8088）
  --bind <地址>       监听地址（默认 0.0.0.0）
  --token <令牌>      指定访问令牌（默认自动生成）
  --no-token          关闭访问令牌（⚠️ 不安全，仅限可信内网）
  --image-tag <标签>  指定镜像标签（默认 latest）
  --build             用仓库源码本地构建（需先跑 build-gateway.sh）
  --dry-run           只准备 .env + 打印命令，不启动容器
  --logs              启动后跟踪日志
  --uninstall         停止并删除容器（数据保留）
  -h, --help          显示本帮助

部署完成后：
  浏览器打开 http://<宿主IP>:<端口>/ 注册首个账号（自动成为管理员），
  再用 /scan 手机微信扫码添加账号；青龙脚本侧配置同名 YYB_API_TOKEN。
USAGE
    exit 0
}

# ---------------------------------------------------------------- 参数
while [ $# -gt 0 ]; do
    case "$1" in
        --dir)       DIR="${2:-}"; shift 2 ;;
        --port)      PORT="${2:-}"; shift 2 ;;
        --bind)      BIND_ADDR="${2:-}"; shift 2 ;;
        --token)     TOKEN="${2:-}"; shift 2 ;;
        --no-token)  TOKEN_MODE="none"; shift ;;
        --image-tag) IMAGE_TAG="${2:-}"; shift 2 ;;
        --build)     BUILD=1; shift ;;
        --dry-run)   DRY_RUN=1; shift ;;
        --logs)      FOLLOW_LOGS=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help)   usage ;;
        *)           die "未知参数：$1（用 --help 查看用法）" ;;
    esac
done

# ---------------------------------------------------------------- 定位部署目录
SELF="${BASH_SOURCE[0]:-}"
SELF_DIR=""
if [ -n "$SELF" ] && [ -f "$SELF" ]; then
    SELF_DIR="$(cd "$(dirname "$SELF")" && pwd)"
fi

if [ -z "$DIR" ]; then
    if [ -n "$SELF_DIR" ] && [ -f "$SELF_DIR/compose.yaml" ]; then
        DIR="$SELF_DIR"                            # 在仓库里执行：就地部署
    elif [ -f "./compose.yaml" ]; then
        DIR="$(pwd)"                               # 当前目录就是仓库
    else
        DIR="$(pwd)/wxcode-yyb-fusion-docker"      # 管道执行：另起一个目录
    fi
fi
mkdir -p "$DIR"
DIR="$(cd "$DIR" && pwd)"
ENV_FILE="$DIR/.env"

# ---------------------------------------------------------------- 工具
need_cmd() { command -v "$1" >/dev/null 2>&1; }

# Git Bash / MSYS / Cygwin 下 docker、curl 是原生 Windows 程序，不认 /c/... 这类
# MSYS 路径（会被当成相对路径写到奇怪的地方），所以递给它们之前先转成 C:/...
case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) NATIVE_PATHS=1 ;;
    *)                    NATIVE_PATHS=0 ;;
esac
native_path() {
    if [ "$NATIVE_PATHS" = "1" ]; then
        printf '%s' "$(printf '%s' "$1" | sed -E 's|^/([a-zA-Z])/|\1:/|')"
    else
        printf '%s' "$1"
    fi
}

check_docker() {
    if [ "$DRY_RUN" = "1" ]; then
        if need_cmd docker && docker compose version >/dev/null 2>&1; then
            COMPOSE=(docker compose)
        elif need_cmd docker-compose; then
            COMPOSE=(docker-compose)
        else
            COMPOSE=(docker compose)
            warn "dry-run：未检测到可用的 docker compose，仅打印将要执行的命令"
        fi
        return 0
    fi
    need_cmd docker || die "未找到 docker，请先安装：https://docs.docker.com/engine/install/"
    docker info >/dev/null 2>&1 || die "docker 守护进程不可用（可能没启动，或当前用户不在 docker 组）"
    if docker compose version >/dev/null 2>&1; then
        COMPOSE=(docker compose)
    elif need_cmd docker-compose; then
        COMPOSE=(docker-compose)
    else
        die "未找到 docker compose（Compose v2 插件或 docker-compose 均可）"
    fi
}

download_files() {
    local f
    for f in compose.yaml compose.build.yaml .env.example; do
        [ -f "$DIR/$f" ] && continue
        need_cmd curl || die "缺少 curl，无法自动下载 $f；请 git clone 本仓库后执行 ./deploy.sh"
        info "下载 $f"
        curl -fsSL "${GH_RAW}/${f}" -o "$(native_path "$DIR/$f")" || die "下载失败：${GH_RAW}/${f}（可用 GH_RAW 指向镜像源）"
    done
}

# 写 .env：跨平台（macOS 的 sed -i 与 GNU 行为不同，统一走临时文件）
set_env() {
    local key="$1" val="$2" esc tmp
    esc="$(printf '%s' "$val" | sed -e 's/[\\&|]/\\&/g')"
    if grep -qE "^[[:space:]]*${key}=" "$ENV_FILE" 2>/dev/null; then
        tmp="$(mktemp)"
        sed "s|^[[:space:]]*${key}=.*|${key}=${esc}|" "$ENV_FILE" > "$tmp" && mv "$tmp" "$ENV_FILE"
    else
        printf '%s=%s\n' "$key" "$val" >> "$ENV_FILE"
    fi
}

get_env() {
    local line
    line="$(grep -E "^[[:space:]]*$1=" "$ENV_FILE" 2>/dev/null | tail -1 || true)"
    line="${line#*=}"
    line="${line%$'\r'}"
    line="${line%\"}"; line="${line#\"}"
    line="${line%\'}"; line="${line#\'}"
    printf '%s' "$line"
}

gen_token() {
    if need_cmd openssl; then openssl rand -hex 24; return; fi
    if need_cmd python3; then python3 -c 'import secrets;print(secrets.token_hex(24))'; return; fi
    if [ -r /dev/urandom ]; then LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c 48; printf '\n'; return; fi
    die "无法生成随机令牌，请用 --token <令牌> 手动指定"
}

prepare_env() {
    local existing_token=""
    if [ ! -f "$ENV_FILE" ]; then
        cp "$DIR/.env.example" "$ENV_FILE"
        ok "已从 .env.example 生成 $ENV_FILE"
    else
        info "沿用已有的 .env（不会覆盖你的配置）"
        existing_token="$(get_env YYB_API_TOKEN)"
    fi

    [ -n "$PORT" ]      && set_env YYB_PORT "$PORT"
    [ -n "$BIND_ADDR" ] && set_env YYB_BIND_ADDRESS "$BIND_ADDR"
    [ -n "$IMAGE_TAG" ] && set_env IMAGE_TAG "$IMAGE_TAG"

    if [ "$TOKEN_MODE" = "none" ]; then
        set_env YYB_API_TOKEN ""
        FINAL_TOKEN=""
        warn "已按要求关闭访问令牌：取码接口不再鉴权，端口可达即可取码"
    elif [ -n "$TOKEN" ]; then
        set_env YYB_API_TOKEN "$TOKEN"
        FINAL_TOKEN="$TOKEN"
        ok "已写入指定的访问令牌"
    elif [ -n "$existing_token" ]; then
        FINAL_TOKEN="$existing_token"
        ok "沿用已有访问令牌（要更换：--token <新令牌>）"
    else
        FINAL_TOKEN="$(gen_token)"
        set_env YYB_API_TOKEN "$FINAL_TOKEN"
        ok "已生成随机访问令牌并写入 .env"
    fi

    # 以 .env 为准回读端口，避免用户手改过端口后健康检查打错地方
    local env_port
    env_port="$(get_env YYB_PORT)"
    PORT="${env_port:-8088}"
}

prepare_data_dir() {
    local raw abs
    raw="$(get_env DATA_DIR)"
    raw="${raw:-./data}"
    case "$raw" in
        /*)  abs="$raw" ;;
        ./*) abs="$DIR/${raw#./}" ;;
        *)   abs="$DIR/$raw" ;;
    esac
    mkdir -p "$abs/db" "$abs/avatars" "$abs/qr"
    DATA_ABS="$abs"
    ok "数据目录：$DATA_ABS"
}

compose_cmd() {
    local args=()
    if [ "$BUILD" = "1" ]; then
        args=(-f "$(native_path "$DIR/compose.build.yaml")")
    else
        args=(-f "$(native_path "$DIR/compose.yaml")")
    fi
    if [ "$DRY_RUN" = "1" ]; then
        printf '%s\n' "${C_DIM}   [dry-run] ${COMPOSE[*]} ${args[*]} $*${C_RESET}"
        return 0
    fi
    "${COMPOSE[@]}" "${args[@]}" "$@"
}

wait_health() {
    need_cmd curl || { warn "未找到 curl，跳过健康检查"; return 0; }
    local url="http://127.0.0.1:${PORT}/health" i
    info "等待服务就绪（$url）"
    for ((i = 1; i <= 30; i++)); do
        if curl -fsS --noproxy '*' -m 3 "$url" >/dev/null 2>&1; then
            ok "健康检查通过"
            return 0
        fi
        sleep 2
    done
    warn "等待超时，下面附上容器日志（排查用）："
    compose_cmd logs --tail=60 || true
    return 1
}

host_ip() {
    local ip=""
    if need_cmd hostname; then ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"; fi
    if [ -z "$ip" ] && need_cmd ip; then
        ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1); exit}' || true)"
    fi
    printf '%s' "${ip:-<宿主IP>}"
}

# ---------------------------------------------------------------- 主流程
step "① 检查 Docker 环境"
check_docker
ok "compose 命令：${COMPOSE[*]}"

step "② 准备编排文件"
download_files
ok "部署目录：$DIR"

if [ "$UNINSTALL" = "1" ]; then
    step "③ 停止并删除容器（数据保留）"
    compose_cmd down || true
    ok "已停止。数据仍在：${DATA_ABS:-$DIR/data}"
    exit 0
fi

step "③ 生成配置（.env）"
prepare_env

step "④ 准备数据目录"
prepare_data_dir

step "⑤ 启动服务"
if [ "$BUILD" = "1" ]; then
    info "本地构建模式（compose.build.yaml）——请确认已跑过 ./build-gateway.sh"
    compose_cmd up -d --build
else
    info "拉取镜像并启动"
    compose_cmd pull
    compose_cmd up -d
fi

if [ "$DRY_RUN" != "1" ]; then
    step "⑥ 健康检查"
    wait_health || true
fi

IP="$(host_ip)"
step "部署完成"
cat <<EOF
   面板地址   ${C_BOLD}http://${IP}:${PORT}/${C_RESET}          （首次注册的账号即管理员）
   扫码登录   http://${IP}:${PORT}/scan          （手机微信扫码，自动保存登录态）
   健康检查   http://${IP}:${PORT}/health
   访问令牌   ${C_BOLD}${FINAL_TOKEN:-（未启用）}${C_RESET}
   数据目录   ${DATA_ABS}
   容器名     wxcode-yyb-gateway

下一步：
   1) 浏览器打开面板 → 注册第一个账号（自动成为管理员）
   2) 打开 /scan → 手机微信扫码，把微信号添加进来
   3) 青龙环境变量里给脚本配同名令牌：YYB_API_TOKEN=${FINAL_TOKEN:-<空>}
      （顺丰中秋 / sfsy日常版 / 移动云盘 三个脚本已内置支持，两边值必须一致）

常用命令：
   看日志   ${COMPOSE[*]} -f "$DIR/compose.yaml" logs -f --tail=100
   升级     ./deploy.sh                       # 重复执行 = 拉新镜像 + 重启
   停服     ./deploy.sh --uninstall
   改端口   编辑 .env 里的 YYB_PORT 后重跑 ./deploy.sh
EOF

if [ "$FOLLOW_LOGS" = "1" ] && [ "$DRY_RUN" != "1" ]; then
    info "跟踪容器日志（Ctrl+C 退出，容器继续运行）"
    compose_cmd logs -f --tail=100
fi
