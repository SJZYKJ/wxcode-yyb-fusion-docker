#!/system/bin/sh

# Read by Magisk's installer after this script is sourced.
# shellcheck disable=SC2034
SKIPUNZIP=0

case "$ARCH" in
  arm64) ;;
  *) abort "! YYB Go currently supports arm64 devices only (detected: $ARCH)" ;;
esac

ui_print "- Installing YYB Go for Android arm64"
ui_print "- Runtime data: /data/adb/yyb-go"
ui_print "- Default console: http://127.0.0.1:8000"

set_perm_recursive "$MODPATH" 0 0 0755 0644
set_perm "$MODPATH/bin/yyb-go" 0 0 0755
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/stop.sh" 0 0 0755
set_perm "$MODPATH/action.sh" 0 0 0755
set_perm "$MODPATH/uninstall.sh" 0 0 0755
