#!/bin/sh
# 停止后端、MariaDB、Redis(仅本工作区实例)。
ROOT=$(cd "$(dirname "$0")/.." && pwd)
. "$ROOT/scripts/env.sh"

if [ -f "$ROOT/run/server.pid" ]; then
  kill "$(cat "$ROOT/run/server.pid")" 2>/dev/null && echo "[stop] license server stopped"
  rm -f "$ROOT/run/server.pid"
fi
if [ -f "$ROOT/run/mysql.pid" ]; then
  mariadb-admin --socket="$ROOT/run/mysql.sock" -u root shutdown 2>/dev/null \
    || kill "$(cat "$ROOT/run/mysql.pid")" 2>/dev/null
  sleep 1
  if ! mariadb --socket="$ROOT/run/mysql.sock" -u root -e "SELECT 1" >/dev/null 2>&1; then
    echo "[stop] mariadb stopped"
  fi
fi
redis-cli -h 127.0.0.1 -p 6379 shutdown nosave 2>/dev/null && echo "[stop] redis stopped"
