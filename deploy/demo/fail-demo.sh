#!/bin/sh
# 失败场景演示（ADR-0009「启动自检不过拒启」）：在 demo 栈上复现两个
# 拒启形态 + 一个通过形态对照。演示完恢复角色边界与 env 文件。
#
#   场景 A（应拒启）：DSN 指向主库 → pg_is_in_recovery() = false
#   场景 B（应拒启）：角色级 statement_timeout 与 env 不一致（角色 15s vs env 30s）
#   场景 C（应通过）：从库 + 角色级 timeout 一致（对照组，演示后即为终态）
#
# 用法：./fail-demo.sh（需先跑过 ./setup.sh 起好 PG 栈）
set -eu

cd "$(dirname "$0")"
ENV_FILE="../config/env"
BACKUP="/tmp/dgw-demo-env-backup"
REPL_PORT="${DGW_DEMO_REPL_PORT:-55432}"

psql_pri() { docker compose -f docker-compose.pg.yml exec -T pg-primary psql -U postgres -v ON_ERROR_STOP=1 "$@"; }
gw_up() { docker compose -f ../docker-compose.yml up -d --force-recreate dgw >/dev/null 2>&1 || true; }
gw_log() { docker compose -f ../docker-compose.yml logs --tail 40; }
# 等角色配置经 WAL 复制到从库（SHOW 生效才做对照断言）
wait_rep_timeout() {
  i=0
  until docker compose -f docker-compose.pg.yml exec -T pg-replica \
    psql -U dgw_reader -d bill -tAc "SHOW statement_timeout" 2>/dev/null | grep -q "$1"; do
    i=$((i + 1)); [ "$i" -gt 60 ] && { echo "WAL 复制超时"; exit 1; }
    sleep 2
  done
}

echo "==> 场景 A：DSN 指向主库（应拒启：pg_is_in_recovery() = false）"
cp "$ENV_FILE" "$BACKUP"
PRIM_PORT="${DGW_DEMO_PRIM_PORT:-55431}"
sed -E "s/host.docker.internal:${REPL_PORT}/host.docker.internal:${PRIM_PORT}/" "$BACKUP" > "$ENV_FILE"
gw_up
sleep 2
if gw_log | grep -q "pg_is_in_recovery() = false"; then
  echo "    ✅ 场景 A 复现：连主库 → 拒启（日志点名 pg_is_in_recovery）"
else
  echo "    ❌ 场景 A 未复现"
  gw_log
  exit 1
fi
cp "$BACKUP" "$ENV_FILE" # 恢复 env（DSN → 从库）

echo
echo "==> 场景 B：角色级 statement_timeout 与 env 不一致（应拒启）"
psql_pri -c "ALTER ROLE dgw_reader SET statement_timeout = '15s'"
wait_rep_timeout "15s"
gw_up
sleep 2
if gw_log | grep -q "statement_timeout"; then
  echo "    ✅ 场景 B 复现：角色 15s vs env 30s → 拒启（日志点名 statement_timeout）"
else
  echo "    ❌ 场景 B 未复现"
  gw_log
  exit 1
fi

echo
echo "==> 恢复角色边界（15s → 30s，与 env 一致）"
psql_pri -c "ALTER ROLE dgw_reader SET statement_timeout = '30s'"
wait_rep_timeout "30s"

echo
echo "==> 场景 C：对照组（从库 + 角色级 timeout 一致，应通过）"
gw_up
sleep 2
if gw_log | grep -q "启动自检通过"; then
  echo "    ✅ 场景 C：自检通过，网关可用（终态）"
else
  echo "    ❌ 场景 C 未通过"
  gw_log
  exit 1
fi
echo
echo "✅ 失败场景演示完成：两条硬校验（连错主库 / 超时不一致）均已复现拒启"
