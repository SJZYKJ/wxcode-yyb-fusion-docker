#!/system/bin/sh

MODDIR=${0%/*}
sh "$MODDIR/stop.sh"

# Account credentials and configuration remain in /data/adb/yyb-go so an
# uninstall or module upgrade cannot remove them accidentally.
