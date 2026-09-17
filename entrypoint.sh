#!/bin/sh
# wxcode-yyb-fusion-gateway entrypoint
# 以 root 启动，修复 bind-mount 数据目录的写权限，然后降权到 yyb 用户运行。
set -e

# 数据目录来自 compose 的 bind mount（宿主机 ./data/*），属主可能是 root，
# 非 root 的 yyb 用户无法写入导致 SQLite 报 "unable to open database file: out of memory (14)"。
for d in /app/resource/db /app/resource/avatars /app/resource/qr; do
  mkdir -p "$d"
  chown -R yyb:yyb "$d" 2>/dev/null || chmod -R 0777 "$d"
done

run_as_yyb() {
  if command -v su-exec >/dev/null 2>&1; then
    su-exec yyb:yyb /app/yyb-go "$@"
  elif command -v setpriv >/dev/null 2>&1; then
    setpriv --reuid=yyb --regid=yyb --clear-groups /app/yyb-go "$@"
  else
    return 1
  fi
}

# 优先以 yyb 用户运行（最小权限）；若环境缺少 SETUID/SETGID 能力导致降权失败，
# 则回退为 root 直接运行（数据目录权限已修复，仍可正常工作）。
if ! run_as_yyb "$@"; then
  exec /app/yyb-go "$@"
fi