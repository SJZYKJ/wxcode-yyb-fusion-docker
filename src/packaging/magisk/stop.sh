#!/system/bin/sh

DATA_DIR=/data/adb/yyb-go
PID_FILE="$DATA_DIR/yyb-go.pid"
MODDIR=${0%/*}
BIN="$MODDIR/bin/yyb-go"

[ -f "$PID_FILE" ] || exit 0
pid=$(cat "$PID_FILE" 2>/dev/null)

if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
  if tr '\000' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -Fq "$BIN"; then
    kill "$pid" 2>/dev/null
    count=0
    while kill -0 "$pid" 2>/dev/null && [ "$count" -lt 10 ]; do
      sleep 1
      count=$((count + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null
    fi
  fi
fi

rm -f "$PID_FILE"
