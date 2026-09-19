#!/bin/sh
# 构建并启动 Go 后端(自动迁移、种子数据、托管前端产物、30s 过期回收)。
set -e
. "$(dirname "$0")/env.sh"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$ROOT/run"

echo "[server] building..."
cd "$ROOT/backend"
go build -o "$ROOT/run/license-server" .

if [ -f "$ROOT/run/server.pid" ] && kill -0 "$(cat "$ROOT/run/server.pid")" 2>/dev/null; then
  echo "[server] already running (pid=$(cat "$ROOT/run/server.pid")), stopping it"
  kill "$(cat "$ROOT/run/server.pid")"; sleep 1
fi

echo "[server] starting on :8080, logs: $ROOT/run/server.log"
cd "$ROOT/backend" # 工作目录决定 migrations/ 与 frontend-dist/ 的相对路径
nohup "$ROOT/run/license-server" > "$ROOT/run/server.log" 2>&1 &
echo $! > "$ROOT/run/server.pid"
sleep 2
tail -5 "$ROOT/run/server.log"
echo "[server] pid=$(cat "$ROOT/run/server.pid")  console: http://127.0.0.1:8080"
