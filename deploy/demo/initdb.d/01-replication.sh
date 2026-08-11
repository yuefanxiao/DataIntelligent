#!/bin/sh
# demo：官方镜像 POSTGRES_HOST_AUTH_METHOD=trust 只对普通连接全开（pg_hba
# 的 replication 行仅 local/127.0.0.1）；demo 从库的流复制连接（docker 网络
# 段）需显式追加一行。initdb.d 脚本在首次启动、服务器起跑前执行，直接写
# 库文件（服务器启动时读取，无需 reload）；数据目录已存在时不再执行（幂等）。
echo "host replication all all trust" >> "$PGDATA/pg_hba.conf"
