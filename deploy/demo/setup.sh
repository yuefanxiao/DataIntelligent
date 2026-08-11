#!/bin/sh
# demo 全量验收编排（ADR-0009 部署形态；本地主从 PG 模拟 CNPG 从库）：
#
#   1. 起 demo 主从 PG（流复制）
#   2. 主库建 10 库 + bss 演示表/数据（生产形态：bss 域 schema 前缀）
#   3. 跑真实 provisioning（deploy/provisioning/readonly-role.sql，共享只读角色）
#   4. 从库 WAL 追平后，写 deploy/config/env（DSN → 从库），compose 起网关
#   5. key-create + grant + 真实 MCP 查询（mcp-ping）
#
# 失败场景演示（拒启）见 fail-demo.sh；语义仓库操作路径见
# ../semantic-repo/README.md（bootstrap.sh 打通）。
#
# 用法：./setup.sh [--skip-pg] [--skip-gateway]（可组合）
#   --skip-pg      复用已起的 PG 栈（跳过 1-3）
#   --skip-gateway 只准备 PG/凭证，不起网关（联调用）
# 环境：DGW_DEMO_REPL_PORT（从库宿主端口，默认 55432）、
#       DGW_HTTP_PORT（网关宿主端口，默认 8080；与 docker-compose.yml 同一变量）
set -eu

cd "$(dirname "$0")"
REPL_PORT="${DGW_DEMO_REPL_PORT:-55432}"
export DGW_HTTP_PORT="${DGW_HTTP_PORT:-8080}"
HTTP_PORT="$DGW_HTTP_PORT"
SKIP_PG=false
SKIP_GATEWAY=false
for arg in "$@"; do
  case "$arg" in
  --skip-pg) SKIP_PG=true ;;
  --skip-gateway) SKIP_GATEWAY=true ;;
  *) echo "未知参数: $arg" >&2; exit 2 ;;
  esac
done
ENV_FILE="../config/env"

psql_pri() { docker compose -f docker-compose.pg.yml exec -T pg-primary psql -U postgres -v ON_ERROR_STOP=1 "$@"; }
psql_rep() { docker compose -f docker-compose.pg.yml exec -T pg-replica psql -U postgres -v ON_ERROR_STOP=1 "$@"; }

if [ "$SKIP_PG" = false ]; then
  echo "==> [1/6] 起 demo 主从 PG（流复制）..."
  docker compose -f docker-compose.pg.yml up -d

  echo "==> [2/6] 等从库就绪（pg_is_in_recovery = t）..."
  i=0
  until [ "$(psql_rep -tAc "SELECT pg_is_in_recovery()" 2>/dev/null || true)" = "t" ]; do
    i=$((i + 1)); [ "$i" -gt 120 ] && { echo "从库就绪超时"; exit 1; }
    sleep 2
  done
  echo "    从库 recovery 形态确认：pg_is_in_recovery() = t"

  echo "==> [3/6] 主库准备生产形态 + 演示数据，然后跑真实 provisioning..."
  # 幂等提示：named volume 持久化，既有 demo 数据重跑会 CREATE DATABASE
  # 失败——指引 --skip-pg 复用或 down -v 重建。
  if psql_pri -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'bill'" | grep -q 1; then
    echo "    检测到既有 demo 数据（bill 库已存在）。"
    echo "    重跑请用 --skip-pg 复用 PG 栈；需重建先 docker compose -f docker-compose.pg.yml down -v"
    exit 1
  fi
  psql_pri -d postgres -c "CREATE DATABASE bill" -c "CREATE DATABASE wallet" \
    -c "CREATE DATABASE bss_invoice" -c "CREATE DATABASE subscription" -c "CREATE DATABASE promotion" \
    -c "CREATE DATABASE iam" -c "CREATE DATABASE iam_audit" \
    -c "CREATE DATABASE console" -c "CREATE DATABASE notification" -c "CREATE DATABASE ops_ticket" >/dev/null
  # bss 域 = 库内同名 schema 前缀（采集器生产形态约定；provisioning 的
  # GRANT 对象）
  psql_pri -d bill -c "CREATE SCHEMA bill" >/dev/null
  psql_pri -d wallet -c "CREATE SCHEMA wallet" >/dev/null
  psql_pri -d bss_invoice -c "CREATE SCHEMA bss_invoice" >/dev/null
  psql_pri -d subscription -c "CREATE SCHEMA subscription" >/dev/null
  psql_pri -d promotion -c "CREATE SCHEMA promotion" >/dev/null
  # 演示表建在 public schema（校验层 v1 只映射 public，spec §8；SQL 里
  # 用单段表名，授权 FQN = 服务.库.表）
  psql_pri -d bill -c "CREATE TABLE orders (
    id bigint PRIMARY KEY, status text NOT NULL, amount numeric(12,2),
    paid_at timestamptz NOT NULL DEFAULT now())" >/dev/null
  psql_pri -d bill -c "INSERT INTO orders (id, status, amount, paid_at) SELECT g,
    CASE WHEN g % 10 = 0 THEN 'refunded' ELSE 'paid' END, (g * 1.5)::numeric(12,2),
    now() - make_interval(mins => g) FROM generate_series(1, 600) g" >/dev/null
  # 真实 provisioning 脚本（可重放；ro_password 必填、ro_timeout 可选）
  docker compose -f docker-compose.pg.yml cp ../../deploy/provisioning/readonly-role.sql pg-primary:/tmp/readonly-role.sql
  docker compose -f docker-compose.pg.yml exec -T pg-primary sh -c \
    "psql -U postgres -d postgres -v ON_ERROR_STOP=1 -v ro_password=demo-ro-pass -f /tmp/readonly-role.sql"
  echo "    provisioning 完成（角色 dgw_reader + 10 库只读 + 角色级 statement_timeout=30s）"
  # demo 演示表建在 public（校验层 v1 只映射 public，spec §8）；provisioning
  # 按生产形态（schema 前缀）授 SELECT，此处补授 demo 表（demo 专属步骤）。
  psql_pri -d bill -c "GRANT SELECT ON orders TO dgw_reader" >/dev/null

  echo "==> [4/6] 等从库 WAL 追平（dgw_reader 可查从库）..."
  i=0
  until psql_rep -U dgw_reader -d bill -tAc "SELECT count(*) FROM orders" 2>/dev/null | grep -q 600; do
    i=$((i + 1)); [ "$i" -gt 60 ] && { echo "WAL 追平超时"; exit 1; }
    sleep 2
  done
  echo "    从库可读：orders = 600 行（角色/库/表/超时已复制）"
fi

if [ "$SKIP_PG" = true ]; then
  echo "==> [4/6] --skip-pg：复用已起 PG 栈（跳过 provisioning，请自行保证 dgw_reader 就绪）"
fi

echo "==> [5/6] 写凭证 env 文件（deploy/config/env，0600；DSN → 从库）..."
mkdir -p "$(dirname "$ENV_FILE")"
cat > "$ENV_FILE" <<EOF
DGW_DB_PATH=/data/dgw.db
DGW_EXEC_LOG_DIR=/logs
DGW_HTTP_ADDR=:8080
DGW_SQL_LIMIT=500
DGW_PG_STATEMENT_TIMEOUT_MS=30000
DGW_KEY_CONCURRENCY=2
DGW_PROCESS_CONCURRENCY=8
DGW_EXEC_RAW_RETENTION_DAYS=7
DGW_EXEC_SUMMARY_RETENTION_DAYS=30
# demo：从库经 host.docker.internal（容器内 127.0.0.1 是容器自己）
DGW_PG_DATABASES=[{"dbname":"bill","service":"bss-bill","dsn":"postgres://dgw_reader:demo-ro-pass@host.docker.internal:${REPL_PORT}/bill?sslmode=disable"},{"dbname":"iam","service":"iam","dsn":"postgres://dgw_reader:demo-ro-pass@host.docker.internal:${REPL_PORT}/iam?sslmode=disable"}]
EOF
chmod 600 "$ENV_FILE"
echo "    已写 ${ENV_FILE}（0600，.gitignore 内）"

if [ "$SKIP_GATEWAY" = true ]; then
  echo "==> --skip-gateway：PG/凭证就绪，网关留给你起（docker compose -f ../docker-compose.yml up -d --build）"
  exit 0
fi

echo "==> [6/6] 构建镜像 + 起网关（启动自检不过 = 拒启）..."
docker compose -f ../docker-compose.yml up -d --build
sleep 2
docker compose -f ../docker-compose.yml logs --tail 20
docker compose -f ../docker-compose.yml logs | grep -q "启动自检通过" \
  || { echo "!! 网关未通过启动自检（拒启）"; exit 1; }
echo "    网关已起：http://127.0.0.1:${HTTP_PORT}"

echo "==> 创建凭据 + 授权（明文仅打印一次）..."
KEY_OUT="$(docker compose -f ../docker-compose.yml exec -T dgw dgw key-create --user dev-alice)"
KEY="$(printf '%s\n' "$KEY_OUT" | tail -1)"
echo "    key: ${KEY}"
docker compose -f ../docker-compose.yml exec -T dgw dgw grant-add --user dev-alice --table bss-bill.bill.orders

echo "==> 真实 MCP 往返（HTTP 形态，官方 go-sdk）..."
go run mcp-ping.go --addr "http://127.0.0.1:${HTTP_PORT}" --key "$KEY" \
  --dbname bill --sql "SELECT status, count(*) FROM orders GROUP BY status ORDER BY 2 DESC"
go run mcp-ping.go --addr "http://127.0.0.1:${HTTP_PORT}" --key "$KEY" \
  --dbname bill --sql "SELECT * FROM orders LIMIT 3"

echo
echo "==> 三挂载断言（AC2：/data /logs 可写、/config/env 0600 只读）..."
docker compose -f ../docker-compose.yml exec -T dgw sh -c '
  test -w /data || { echo "FAIL: /data 不可写"; exit 1; }
  test -w /logs || { echo "FAIL: /logs 不可写"; exit 1; }
  test -r /config/env || { echo "FAIL: /config/env 不可读"; exit 1; }
  test "$(stat -c %a /config/env)" = "600" || { echo "FAIL: /config/env 非 0600"; exit 1; }
  echo "    OK: /data /logs 可写（dgw 属主）；/config/env 0600 只读挂载"
'
echo
echo "✅ demo 验收通过：compose 起网关 + 启动自检通过 + 三挂载 + MCP 查询成功"
echo "   失败场景（拒启）：./fail-demo.sh"
echo "   语义仓库操作路径：../semantic-repo/verify.sh"
