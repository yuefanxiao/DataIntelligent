#!/bin/sh
# 网关容器入口（ADR-0009 部署形态）：
#   1. 以 root 准备两个可写挂载点并修正属主——SQLite /data、执行记录
#      /logs（named volume 初始 root 属主，非 root 用户写不进去）；
#   2. 降权到 dgw（uid 10001）执行 dgw 命令。
#
# /config 是宿主机 env 文件（0600）的只读挂载，凭证经 compose env_file
# 注入进程环境，不经命令行参数（ps 可见性），不在镜像里（ADR-0009
# 「凭证只存在该机 env 文件」）。
set -eu

for dir in /data /logs; do
  mkdir -p "$dir"
  chown -R dgw:dgw "$dir"
done

exec su-exec dgw /usr/local/bin/dgw "$@"
