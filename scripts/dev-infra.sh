#!/usr/bin/env bash
# 在无 root 的 Debian/arm64 沙箱中启动便携 MySQL 8.4 与源码编译的 Redis 7.2。
# 生产环境请直接使用系统包/容器，本脚本仅为演示环境的可复现辅助。
set -euo pipefail

DL=${DL:-/tmp/dl}
MYSQL_HOME=${MYSQL_HOME:-/tmp/mysql}
MYSQL_DATA=${MYSQL_DATA:-/tmp/mysqldata}
MYSQL_RUN=${MYSQL_RUN:-/tmp/mysqlrun}
EXTRA_LIB=${EXTRA_LIB:-/tmp/extralib}
MYSQL_VER=${MYSQL_VER:-8.4.3}
REDIS_VER=${REDIS_VER:-7.2.7}

mkdir -p "$DL" "$MYSQL_RUN" "$EXTRA_LIB"

# --- Redis：源码编译（系统 cc 在本沙箱是包装器，显式指定 gcc） ---
if [ ! -x "$DL/redis-$REDIS_VER/src/redis-server" ]; then
  curl -sSL -o "$DL/redis.tar.gz" "https://download.redis.io/releases/redis-$REDIS_VER.tar.gz"
  tar -C "$DL" -xzf "$DL/redis.tar.gz"
  make -C "$DL/redis-$REDIS_VER" -j"$(nproc)" CC=gcc BUILD_TLS=no MALLOC=libc
fi
"$DL/redis-$REDIS_VER/src/redis-cli" ping >/dev/null 2>&1 || \
  "$DL/redis-$REDIS_VER/src/redis-server" --port 6379 --bind 127.0.0.1 \
    --daemonize yes --save "" --appendonly no
echo "redis: $("$DL/redis-$REDIS_VER/src/redis-cli" ping)"

# --- MySQL：官方 linux-glibc2.28-aarch64 通用二进制 ---
if [ ! -x "$MYSQL_HOME/bin/mysqld" ]; then
  curl -sSL -o "$DL/mysql.tar.xz" \
    "https://dev.mysql.com/get/Downloads/MySQL-8.4/mysql-$MYSQL_VER-linux-glibc2.28-aarch64.tar.xz"
  tar -C "$DL" -xf "$DL/mysql.tar.xz"
  rm -rf "$MYSQL_HOME"
  mv "$DL/mysql-$MYSQL_VER-linux-glibc2.28-aarch64" "$MYSQL_HOME"
fi

# mysqld 运行期需要 libaio.so.1：从 arm64 deb 中解包，不写入系统目录。
if [ ! -e "$EXTRA_LIB/libaio.so.1" ]; then
  curl -sSL -o "$DL/libaio.deb" \
    "http://deb.debian.org/debian/pool/main/liba/libaio/libaio1t64_0.3.113-9_arm64.deb"
  rm -rf "$DL/libaio" && mkdir -p "$DL/libaio"
  (cd "$DL/libaio" && ar x "$DL/libaio.deb" && tar xf data.tar.xz)
  cp "$DL"/libaio/usr/lib/aarch64-linux-gnu/libaio.so.1t64.* "$EXTRA_LIB/"
  ln -sf libaio.so.1t64.* "$EXTRA_LIB/libaio.so.1"
fi
export LD_LIBRARY_PATH="$EXTRA_LIB${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

if [ ! -d "$MYSQL_DATA/mysql" ]; then
  "$MYSQL_HOME/bin/mysqld" --no-defaults --initialize-insecure \
    --basedir="$MYSQL_HOME" --datadir="$MYSQL_DATA" --user="$(whoami)"
fi

if ! "$MYSQL_HOME/bin/mysql" -h127.0.0.1 -uroot -e "SELECT 1" >/dev/null 2>&1; then
  "$MYSQL_HOME/bin/mysqld" --no-defaults \
    --basedir="$MYSQL_HOME" --datadir="$MYSQL_DATA" \
    --socket="$MYSQL_RUN/mysql.sock" --port=3306 --bind-address=127.0.0.1 \
    --pid-file="$MYSQL_RUN/mysqld.pid" --mysqlx=OFF &
  for _ in $(seq 1 30); do
    "$MYSQL_HOME/bin/mysql" -h127.0.0.1 -uroot -e "SELECT 1" >/dev/null 2>&1 && break
    sleep 1
  done
fi
"$MYSQL_HOME/bin/mysql" -h127.0.0.1 -uroot \
  -e "CREATE DATABASE IF NOT EXISTS licensepool CHARACTER SET utf8mb4;"
echo "mysql: $("$MYSQL_HOME/bin/mysql" -h127.0.0.1 -uroot -N -e 'SELECT VERSION();')"
