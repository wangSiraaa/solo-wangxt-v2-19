#!/bin/sh
# 本地免 root 工具链环境(系统已安装 go/mariadb/redis 时可按需裁剪)
if [ -n "${BASH_SOURCE:-}" ]; then
  _SELF="$BASH_SOURCE"
elif [ -n "${ZSH_ARGZERO:-}" ]; then
  _SELF="$ZSH_ARGZERO"
else
  _SELF="$0"
fi
# 被 `source` 时 $0 可能是 shell,回退到仓库内的固定相对位置
case "$_SELF" in
  */env.sh) DIR=$(CDPATH= cd -- "$(dirname -- "$_SELF")" && pwd) ;;
  *) DIR="/workspace/scripts" ;;
esac
ROOT=$(cd "$DIR/.." && pwd)
export PREFIX="$ROOT/tools/mariadb"
export LD_LIBRARY_PATH="$PREFIX/usr/lib/aarch64-linux-gnu:${LD_LIBRARY_PATH:-}"
export PATH="$ROOT/tools/go/bin:$PREFIX/usr/bin:$PREFIX/usr/sbin:$ROOT/tools/redis-7.4.1/src:$PATH"
unset _SELF DIR ROOT
