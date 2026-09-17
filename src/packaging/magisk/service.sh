#!/system/bin/sh

MODDIR=${0%/*}
DATA_DIR=/data/adb/yyb-go
RESOURCE_DIR="$DATA_DIR/resource"
CONFIG_FILE="$DATA_DIR/config.conf"
PID_FILE="$DATA_DIR/yyb-go.pid"
LOG_FILE="$DATA_DIR/yyb-go.log"
BIN="$MODDIR/bin/yyb-go"
WXCODE_APK="$MODDIR/wxcode/wxcode_2.1.0.apk"
WXCODE_MARK="$DATA_DIR/wxcode.installed"

# --- wxcode Xposed 模块：确保已安装（late_start 时 pm 可用） ---
install_wxcode() {
  if ! pm list packages 2>/dev/null | grep -q '^package:xiaojw.wxcode$'; then
    if [ -f "$WXCODE_APK" ] && command -v pm >/dev/null 2>&1; then
      if pm install -r "$WXCODE_APK" >/dev/null 2>&1; then
        echo "$(date '+%F %T') wxcode: installed wxcode_2.1.0.apk" >> "$LOG_FILE"
      else
        echo "$(date '+%F %T') WARNING: wxcode install failed (retry next boot)" >> "$LOG_FILE"
      fi
    fi
  else
    touch "$WXCODE_MARK" 2>/dev/null
  fi
}

# --- yyb-go 服务：与官方 YYB Go Magisk 模块保持一致的启动逻辑 ---
start_yyb_go() {
  mkdir -p "$RESOURCE_DIR/db" "$RESOURCE_DIR/avatars" "$RESOURCE_DIR/qr"
  cp -af "$MODDIR/resource/." "$RESOURCE_DIR/"

  if [ ! -f "$CONFIG_FILE" ]; then
    cp "$MODDIR/config.conf.example" "$CONFIG_FILE"
    chmod 0600 "$CONFIG_FILE"
  fi

  # Older Windows-built packages could copy CRLF into the persistent config.
  # Normalize it before sourcing so values such as PORT do not end in "\r".
  CONFIG_TMP="$CONFIG_FILE.tmp.$$"
  if tr -d '\r' < "$CONFIG_FILE" > "$CONFIG_TMP"; then
    chmod 0600 "$CONFIG_TMP"
    mv -f "$CONFIG_TMP" "$CONFIG_FILE"
  else
    rm -f "$CONFIG_TMP"
    echo "$(date '+%F %T') ERROR: failed to normalize $CONFIG_FILE" >> "$LOG_FILE"
    exit 1
  fi

  # shellcheck disable=SC1090
  . "$CONFIG_FILE"

  : "${HOST:=127.0.0.1}"
  : "${PORT:=8000}"
  : "${KEEPALIVE_INTERVAL:=30m}"
  : "${KEEPALIVE_AHEAD:=45m}"
  : "${YYB_DNS_SERVERS:=223.5.5.5:53,119.29.29.29:53}"
  : "${YYB_AUTH_DRIVER:=sqlite}"

  if [ -f "$PID_FILE" ]; then
    old_pid=$(cat "$PID_FILE" 2>/dev/null)
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
      if tr '\000' ' ' < "/proc/$old_pid/cmdline" 2>/dev/null | grep -Fq "$BIN"; then
        return 0
      fi
    fi
    rm -f "$PID_FILE"
  fi

  export PANEL_TYPE QL_URL QL_CLIENT_ID QL_CLIENT_SECRET
  export DAIDAI_URL DAIDAI_APP_KEY DAIDAI_APP_SECRET
  export YYB_QINGLONG_SERVER YYB_QINGLONG_REPO
  export YYB_DNS_SERVERS WXCODE_URLS
  export YYB_AUTH_DRIVER YYB_AUTH_DSN YYB_AUTH_MYSQL_DSN
  export YYB_ADMIN_USER YYB_ADMIN_PASSWORD
  export YYB_COOKIE_SECURE
  export GIN_MODE=release
  export SSL_CERT_DIR=/system/etc/security/cacerts

  set -- \
    -host "$HOST" \
    -port "$PORT" \
    -resource-root "$RESOURCE_DIR" \
    -keepalive-interval "$KEEPALIVE_INTERVAL" \
    -keepalive-ahead "$KEEPALIVE_AHEAD"

  if [ -n "$TCP_PROXY" ]; then
    set -- "$@" -tcp-proxy "$TCP_PROXY"
  fi

  if [ "$HOST" != "127.0.0.1" ] && [ "$HOST" != "localhost" ] && [ "$YYB_AUTH_DRIVER" = "none" ]; then
    echo "$(date '+%F %T') WARNING: YYB Go is exposed without browser authentication" >> "$LOG_FILE"
  fi

  if command -v nohup >/dev/null 2>&1; then
    nohup "$BIN" "$@" >> "$LOG_FILE" 2>&1 &
  else
    "$BIN" "$@" >> "$LOG_FILE" 2>&1 &
  fi

  pid=$!
  echo "$pid" > "$PID_FILE"
  sleep 2
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$PID_FILE"
    echo "$(date '+%F %T') ERROR: YYB Go failed to start" >> "$LOG_FILE"
    exit 1
  fi
}

mkdir -p "$DATA_DIR"
install_wxcode
start_yyb_go