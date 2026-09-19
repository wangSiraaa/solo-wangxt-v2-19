#!/bin/sh
# 启动便携 MariaDB 与 Redis(免 root)。首次运行自动初始化 MySQL 数据目录。
set -e
. "$(dirname "$0")/env.sh"

ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$ROOT/run" "$ROOT/data/mysql" "$ROOT/data/redis"

# ---- MariaDB ----
if [ ! -d "$ROOT/data/mysql/mysql" ]; then
  echo "[infra] initializing mariadb data dir..."
  perl "$PREFIX/usr/bin/mariadb-install-db" \
    --basedir="$PREFIX/usr" \
    --datadir="$ROOT/data/mysql" \
    --no-defaults \
    --auth-root-authentication-method=normal \
    --skip-test-db >/dev/null
fi

if ! mariadb --socket="$ROOT/run/mysql.sock" -u root -e "SELECT 1" >/dev/null 2>&1; then
  echo "[infra] starting mariadb on 127.0.0.1:3306 ..."
  mariadbd --no-defaults \
    --datadir="$ROOT/data/mysql" \
    --socket="$ROOT/run/mysql.sock" \
    --port=3306 --bind-address=127.0.0.1 \
    --pid-file="$ROOT/run/mysql.pid" \
    --log-error="$ROOT/run/mysql.err" &
  for i in $(seq 1 30); do
    mariadb --socket="$ROOT/run/mysql.sock" -u root -e "SELECT 1" >/dev/null 2>&1 && break
    sleep 1
  done
  echo "[infra] mariadb ready"
else
  echo "[infra] mariadb already running"
fi

# 库与账号幂等创建(服务可能是之前启动的)
mariadb --socket="$ROOT/run/mysql.sock" -u root -e "
  CREATE DATABASE IF NOT EXISTS license CHARACTER SET utf8mb4;
  CREATE DATABASE IF NOT EXISTS license_test CHARACTER SET utf8mb4;
  CREATE USER IF NOT EXISTS 'app'@'localhost' IDENTIFIED BY 'apppass';
  CREATE USER IF NOT EXISTS 'app'@'127.0.0.1' IDENTIFIED BY 'apppass';
  GRANT ALL ON license.* TO 'app'@'localhost';
  GRANT ALL ON license.* TO 'app'@'127.0.0.1';
  GRANT ALL ON license_test.* TO 'app'@'localhost';
  GRANT ALL ON license_test.* TO 'app'@'127.0.0.1';
  FLUSH PRIVILEGES;"

# ---- Redis ----
if ! redis-cli -h 127.0.0.1 -p 6379 ping >/dev/null 2>&1; then
  echo "[infra] starting redis on 127.0.0.1:6379 ..."
  redis-server --port 6379 --bind 127.0.0.1 \
    --dir "$ROOT/data/redis" --daemonize yes --save "" --appendonly no \
    --pidfile "$ROOT/run/redis.pid"
  sleep 1
fi
echo "[infra] redis: $(redis-cli -h 127.0.0.1 -p 6379 ping)"
