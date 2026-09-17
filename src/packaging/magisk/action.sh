#!/system/bin/sh

MODDIR=${0%/*}
DATA_DIR=/data/adb/yyb-go
CONFIG_FILE="$DATA_DIR/config.conf"
PID_FILE="$DATA_DIR/yyb-go.pid"

if [ ! -f "$PID_FILE" ] || ! kill -0 "$(cat "$PID_FILE" 2>/dev/null)" 2>/dev/null; then
  sh "$MODDIR/service.sh" || exit 1
fi

HOST=127.0.0.1
PORT=8000
if [ -f "$CONFIG_FILE" ]; then
  # shellcheck disable=SC1090
  . "$CONFIG_FILE"
fi

case "$HOST" in
  0.0.0.0|::|'[::]') browser_host=127.0.0.1 ;;
  *) browser_host=$HOST ;;
esac

am start -a android.intent.action.VIEW -d "http://$browser_host:$PORT/" >/dev/null 2>&1
