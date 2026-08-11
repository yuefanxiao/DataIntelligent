#!/bin/bash
# 验收重放编排（v1 build 12；spec §5 测试决策主 seam / §6.3 负向边界 5 例 /
# §6.4 判定三件套 / §6.5 执行方式）。
#
# 注：shebang 用 bash 而非仓库惯例的 sh——需要进程替换
# `exec > >(tee -a "$RUN_LOG")` 做全程转写留档（sh 无此能力）；脚本其余
# 部分保持 sh 兼容写法。
#
#   1. 起 demo 主从 PG（独立 compose project dgw-accept，与 demo 栈隔离）
#   2. 主库建 10 个持库 + 演示表/数据（orders 600 / big_events 6000 /
#      iam.users）+ 矩阵 fixture（fixture.sql：13 服务矩阵用例的表与
#      确定性数据），跑真实 provisioning（readonly-role.sql）
#   3. 从库 WAL 追平（dgw_reader 可查从库）
#   4. 构建 dgw + accept 二进制
#   5. 语义数据同步（semantic-sync samples/semantic 进运行时；主用例
#      检索/口径 dry-run 依赖）+ 凭据/授权（dev-alice 主用户 / ghost 无
#      grants 用户 / p1-p5 并发探测；矩阵表逐表授权）
#   6. 硬上限配置边界检查（DGW_SQL_LIMIT=5001 应拒启）
#   7. 网关拉起（HTTP 不设 DGW_SQL_LIMIT 走 §4.9 默认 500 / stdio 限 5000
#      双形态）→ harness 用例按序重放 → 判定三件套断言 → 报告留档
#      （deploy/accept/reports/）
#   8. 每形态重放复现（chain 快照从头重放 → 同状态同行数）
#   9. 收尾（停 PG 栈）
#
# 依赖：docker、go、curl。PG 镜像拉不下来时可用
#   DGW_PG_REGISTRY=docker.1ms.run/library 覆盖（测试环境镜像源）。
# 环境：DGW_ACCEPT_TMP（工作目录，缺省 mktemp）、DGW_ACCEPT_HTTP_PORT
# （网关宿主端口，缺省 18082；端口冲突（如本机已有服务占用）换一个即可）、
# DGW_DEMO_REPL_PORT（从库端口，缺省 55432）。
set -eu

cd "$(dirname "$0")"
STAMP="$(date +%Y%m%d-%H%M%S)"
PROJ="dgw-accept"
PG_COMPOSE_ABS="$(cd ../demo && pwd)/docker-compose.pg.yml"
TMP="${DGW_ACCEPT_TMP:-$(mktemp -d "${TMPDIR:-/tmp}/dgw-accept.XXXXXX")}"
HTTP_PORT="${DGW_ACCEPT_HTTP_PORT:-18082}"
REPL_PORT="${DGW_DEMO_REPL_PORT:-55432}"
PG_REGISTRY="${DGW_PG_REGISTRY:-}"

REPORT_DIR="reports"
RUN_LOG="$REPORT_DIR/run-$STAMP.log"
mkdir -p "$REPORT_DIR" "$TMP/logs" "$TMP/logs-stdio" "$TMP/keylog"
# 全程转写（编排 + harness 输出）留档：报告目录下的 run-<stamp>.log。
exec > >(tee -a "$RUN_LOG") 2>&1

DGWBIN="$TMP/dgw"
ACCEPT="$TMP/accept"
HTTP_ADDR="127.0.0.1:$HTTP_PORT"
HTTP_URL="http://$HTTP_ADDR"
PG_ROUTES="[{\"dbname\":\"bill\",\"service\":\"bss-bill\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/bill?sslmode=disable\"},{\"dbname\":\"wallet\",\"service\":\"bss-wallet\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/wallet?sslmode=disable\"},{\"dbname\":\"bss_invoice\",\"service\":\"bss-invoice\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/bss_invoice?sslmode=disable\"},{\"dbname\":\"subscription\",\"service\":\"bss-subscription\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/subscription?sslmode=disable\"},{\"dbname\":\"promotion\",\"service\":\"bss-promotion\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/promotion?sslmode=disable\"},{\"dbname\":\"iam\",\"service\":\"iam\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/iam?sslmode=disable\"},{\"dbname\":\"iam_audit\",\"service\":\"iam-audit\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/iam_audit?sslmode=disable\"},{\"dbname\":\"console\",\"service\":\"console\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/console?sslmode=disable\"},{\"dbname\":\"notification\",\"service\":\"notification\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/notification?sslmode=disable\"},{\"dbname\":\"ops_ticket\",\"service\":\"ops-ticket\",\"dsn\":\"postgres://dgw_reader:demo-ro-pass@127.0.0.1:${REPL_PORT}/ops_ticket?sslmode=disable\"}]"
KEYS=""
STATUS=0

psql_pri() { docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" exec -T pg-primary psql -U postgres -v ON_ERROR_STOP=1 "$@"; }
psql_rep() { docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" exec -T pg-replica psql -U postgres -v ON_ERROR_STOP=1 "$@"; }

# 镜像 registry 覆盖（网络受限环境；DGW_PG_REGISTRY=docker.1ms.run/library）
PG_IMAGE="${PG_REGISTRY:+$PG_REGISTRY/}postgres:17"

fail() { echo "❌ $*" >&2; STATUS=1; }

echo "==> [0/9] 前置检查（docker / go / curl）..."
command -v docker >/dev/null || { echo "缺少 docker"; exit 1; }
command -v go >/dev/null || { echo "缺少 go"; exit 1; }
command -v curl >/dev/null || { echo "缺少 curl"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker daemon 不可用"; exit 1; }

echo "==> [1/9] 起 demo 主从 PG（project ${PROJ}，image ${PG_IMAGE}）..."
if [ -n "$PG_REGISTRY" ]; then
  # 用 sed 在独立副本上替换镜像（不动 demo 栈的 compose 文件）
  PG_COMPOSE_ABS="$TMP/docker-compose.pg.yml"
  sed "s#image: postgres:17#image: $PG_IMAGE#g" "$(cd ../demo && pwd)/docker-compose.pg.yml" > "$PG_COMPOSE_ABS"
fi
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" down -v >/dev/null 2>&1 || true
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" up -d
i=0
until [ "$(psql_rep -tAc "SELECT pg_is_in_recovery()" 2>/dev/null || true)" = "t" ]; do
  i=$((i + 1)); [ "$i" -gt 120 ] && { echo "从库就绪超时"; exit 1; }
  sleep 2
done
echo "    从库 recovery 形态确认：pg_is_in_recovery() = t"

echo "==> [2/9] 主库建库建表 + 演示数据 + 真实 provisioning..."
# 10 个持库全建（provisioning 脚本逐库 \c，缺库即失败；生产形态同 demo）
psql_pri -d postgres -c "CREATE DATABASE bill" -c "CREATE DATABASE wallet" \
  -c "CREATE DATABASE bss_invoice" -c "CREATE DATABASE subscription" -c "CREATE DATABASE promotion" \
  -c "CREATE DATABASE iam" -c "CREATE DATABASE iam_audit" \
  -c "CREATE DATABASE console" -c "CREATE DATABASE notification" -c "CREATE DATABASE ops_ticket" >/dev/null
# bss 域 = 库内同名 schema 前缀（生产形态约定；provisioning 的 GRANT 对象）
for db in bill wallet bss_invoice subscription promotion; do
  psql_pri -d "$db" -c "CREATE SCHEMA $db" >/dev/null
done
# bill 域：orders（600 行，status 枚举 + 金额 + 时间，与 demo 同构）；
# big_events（6000 行，LIMIT 截断边界用例用）
psql_pri -d bill -c "CREATE TABLE orders (
  id bigint PRIMARY KEY, status text NOT NULL, amount numeric(12,2),
  paid_at timestamptz NOT NULL DEFAULT now())" >/dev/null
psql_pri -d bill -c "INSERT INTO orders (id, status, amount, paid_at) SELECT g,
  CASE WHEN g % 10 = 0 THEN 'refunded' ELSE 'paid' END, (g * 1.5)::numeric(12,2),
  now() - make_interval(mins => g) FROM generate_series(1, 600) g" >/dev/null
psql_pri -d bill -c "CREATE TABLE big_events AS
  SELECT g AS id, 'event-' || g AS name FROM generate_series(1, 6000) g" >/dev/null
# iam 域：users（角色只读权有、dev-alice 表授权无 → neg-001b）
psql_pri -d iam -c "CREATE TABLE users (id bigint PRIMARY KEY, name text NOT NULL)" >/dev/null
psql_pri -d iam -c "INSERT INTO users VALUES (1, 'alice'), (2, 'bob')" >/dev/null
# 13 服务用例矩阵 fixture（build 14）：10 个持库的真实表名 + 确定性数据
# （全部相对 now() 的时间偏移——重放同日可复现）。表建在 public schema
# （与 orders/big_events 同构；FQN 映射服务.库.表，docs/acceptance-cases.md
# 记录形态约定）。先于 provisioning 执行——GRANT SELECT ON ALL TABLES
# IN SCHEMA public 一次性覆盖全部 fixture 表，无需逐表补授。
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" cp fixture.sql pg-primary:/tmp/fixture.sql
psql_pri -d postgres -v ON_ERROR_STOP=1 -f /tmp/fixture.sql >/dev/null
echo "    fixture 完成：13 服务矩阵表 + 确定性数据（10 库）"
# 真实 provisioning（可重放；ro_password 必填）——按生产形态（schema 前缀）
# 授 SELECT；demo/fixture 表在 public，provisioning 的 ALL TABLES 已覆盖
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" cp "$(cd ../provisioning && pwd)/readonly-role.sql" pg-primary:/tmp/readonly-role.sql
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" exec -T pg-primary sh -c \
  "psql -U postgres -d postgres -v ON_ERROR_STOP=1 -v ro_password=demo-ro-pass -f /tmp/readonly-role.sql"
# bss 域 provisioning 只授同名 schema 前缀（生产形态：bill/wallet/…）；
# demo + fixture 表在 public schema——逐库补授 public（含 orders/big_events，
# 与「demo 表在 public，之后补授」同构）。
for db in bill wallet bss_invoice subscription promotion; do
  psql_pri -d "$db" -c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader" >/dev/null
done
echo "    provisioning 完成（角色 dgw_reader + 库只读 + 角色级 statement_timeout=30s）"

echo "==> [3/9] 等从库 WAL 追平（dgw_reader 可查从库）..."
i=0
until psql_rep -U dgw_reader -d bill -tAc "SELECT count(*) FROM orders" 2>/dev/null | grep -q 600; do
  i=$((i + 1)); [ "$i" -gt 60 ] && { echo "WAL 追平超时"; exit 1; }
  sleep 2
done
echo "    从库可读：orders = 600 行（角色/库/表/超时已复制）"

echo "==> [4/9] 构建 dgw + accept 二进制..."
( cd ../.. && go build -o "$DGWBIN" ./cmd/dgw && go build -o "$ACCEPT" ./deploy/accept )

echo "==> [5/9] 语义数据同步 + 凭据 + 授权（key-create 明文仅打印一次，不落日志）..."
create_key() { # $1=user；输出明文 key；key-create 失败即退出（不吞退出码）。
  # key 生命周期记录落 $TMP/keylog（不污染网关日志目录）。
  local out
  out="$(DGW_EXEC_LOG_DIR="$TMP/keylog" "$DGWBIN" key-create --user "$1" --db "$TMP/dgw.db" 2>"$TMP/key-create.err")" \
    || { echo "key-create $1 失败（详见 $TMP/key-create.err）" >&2; exit 1; }
  printf '%s\n' "$out" | tail -1
}
KEY_MAIN="$(create_key dev-alice)"
KEY_GHOST="$(create_key ghost)"
KEY_P1="$(create_key p1)"; KEY_P2="$(create_key p2)"; KEY_P3="$(create_key p3)"
KEY_P4="$(create_key p4)"; KEY_P5="$(create_key p5)"
# 语义数据同步进运行时（build 14）：主用例检索/口径 dry-run 依赖
# samples/semantic（13 服务 + payment_failure_rate 指标 + 概念）。先于
# 授权——grant-add 的残留授权告警依赖语义库已同步（warnStaleGrants）。
# 无 DGW_OPENAI_API_KEY → 向量通道不配置，检索 = 纯关键词（确定性）。
"$DGWBIN" semantic-sync --dir "$(cd ../.. && pwd)/samples/semantic" --db "$TMP/dgw.db" >/dev/null
echo "    语义数据已同步（samples/semantic → 运行时存储）"
"$DGWBIN" grant-add --user dev-alice --table bss-bill.bill.orders --db "$TMP/dgw.db" >/dev/null
"$DGWBIN" grant-add --user dev-alice --table bss-bill.bill.big_events --db "$TMP/dgw.db" >/dev/null
# 矩阵 fixture 表逐表授权（13 服务用例矩阵的 execute_sql 用例；FQN =
# 服务.库.表，与语义数据一致）。列表 = fixture.sql 建的表。
for t in \
  bss-bill.bill.bills bss-bill.bill.settlement_batches bss-bill.bill.settlement_attempts \
  bss-wallet.wallet.wallet_accounts bss-wallet.wallet.payment_orders \
  bss-wallet.wallet.wallet_transactions bss-wallet.wallet.refund_orders \
  bss-invoice.bss_invoice.invoice_applications bss-invoice.bss_invoice.tax_invoices \
  bss-invoice.bss_invoice.invoice_files \
  bss-subscription.subscription.model_pricing bss-subscription.subscription.tier_policy_versions \
  bss-subscription.subscription.tier_policy_items \
  bss-promotion.promotion.code_batches bss-promotion.promotion.codes \
  bss-promotion.promotion.code_redemptions \
  iam.iam.organizations iam.iam.organization_memberships \
  iam-audit.iam_audit.audit_logs iam-audit.iam_audit.audit_exports \
  console.console.approval_cases console.console.org_groups console.console.org_group_members \
  notification.notification.email_deliveries notification.notification.email_delivery_attempts \
  ops-ticket.ops_ticket.support_tickets ops-ticket.ops_ticket.support_ticket_sources; do
  "$DGWBIN" grant-add --user dev-alice --table "$t" --db "$TMP/dgw.db" >/dev/null
done
for u in p1 p2 p3 p4 p5; do
  "$DGWBIN" grant-add --user "$u" --table bss-bill.bill.orders --db "$TMP/dgw.db" >/dev/null
done
echo "    用户：dev-alice（主 + 矩阵 27 表）/ ghost（无 grants）/ p1-p5（并发探测）"
KEYS="dev-alice=$KEY_MAIN,ghost=$KEY_GHOST,p1=$KEY_P1,p2=$KEY_P2,p3=$KEY_P3,p4=$KEY_P4,p5=$KEY_P5"

echo "==> [6/9] 硬上限配置边界（DGW_SQL_LIMIT=5001 应拒启，§4.9）..."
# 后台探测 + 超时兜底：若 5001 未拒启（serve 一直在监听），10s 后杀掉并判败，
# 不因管道等待挂死整轮验收。
DGW_SQL_LIMIT=5001 DGW_PG_DATABASES="$PG_ROUTES" DGW_PG_STATEMENT_TIMEOUT_MS=30000 \
  "$DGWBIN" serve --db "$TMP/dgw.db" --addr "127.0.0.1:$((HTTP_PORT + 1))" >"$TMP/gw-5001.log" 2>&1 &
P5001=$!
i=0
while kill -0 "$P5001" 2>/dev/null && [ "$i" -lt 20 ]; do sleep 0.5; i=$((i + 1)); done
if kill -0 "$P5001" 2>/dev/null; then
  kill "$P5001" 2>/dev/null || true
  fail "5001 未拒启（硬上限配置边界失效，serve 仍在监听）"
elif grep -q "越界" "$TMP/gw-5001.log"; then
  echo "    ✅ 5001 拒启（行数上限越界 → 配置错误 fail fast）"
else
  fail "5001 拒启原因不符（日志无「越界」）"
fi

echo "==> [7/9] HTTP 形态：网关拉起（不设 DGW_SQL_LIMIT，§4.9 默认 500）+ 用例重放 + 三件套 + 重放复现..."
DGW_PG_DATABASES="$PG_ROUTES" DGW_PG_STATEMENT_TIMEOUT_MS=30000 \
  DGW_EXEC_LOG_DIR="$TMP/logs" DGW_EXEC_RAW_RETENTION_DAYS=7 DGW_EXEC_SUMMARY_RETENTION_DAYS=30 \
  "$DGWBIN" serve --db "$TMP/dgw.db" --addr "$HTTP_ADDR" >"$TMP/gw-http.log" 2>&1 &
GW_PID=$!
trap 'kill "$GW_PID" 2>/dev/null || true' EXIT
# 就绪判定 = 网关进程存活 + 自检通过 + HTTP 有应答（进程存活是硬闸：端口
# 被别的进程占用时网关已退出，但那个进程会应答 HTTP——不能拿它当网关测）。
i=0
until kill -0 "$GW_PID" 2>/dev/null && \
      grep -q "启动自检通过" "$TMP/gw-http.log" 2>/dev/null && \
      [ "$(curl -s -o /dev/null -w '%{http_code}' "$HTTP_URL/" 2>/dev/null || echo 000)" != "000" ]; do
  i=$((i + 1))
  if ! kill -0 "$GW_PID" 2>/dev/null; then
    echo "HTTP 网关进程已退出（端口被占或启动失败）"; tail -10 "$TMP/gw-http.log"; exit 1
  fi
  [ "$i" -gt 60 ] && { echo "HTTP 网关就绪超时"; tail -20 "$TMP/gw-http.log"; exit 1; }
  sleep 1
done
echo "    网关已起：${HTTP_URL}（启动自检通过）"

PSQL_PREFIX="docker,compose,-f,$PG_COMPOSE_ABS,-p,$PROJ,exec,-T,pg-replica,psql,-U,dgw_reader,-t,-P,format=csv"
if "$ACCEPT" --mode http --addr "$HTTP_URL" --keys "$KEYS" --cases cases.yaml \
    --log-dir "$TMP/logs" --report "$REPORT_DIR/accept-$STAMP-http.md" \
    --psql-prefix "$PSQL_PREFIX" --timeout 90s; then
  echo "    ✅ HTTP 重放通过（报告：$REPORT_DIR/accept-$STAMP-http.md）"
else
  fail "HTTP 重放失败（报告：$REPORT_DIR/accept-$STAMP-http.md）"
fi
# 报告是验收证据的留档：断言全过但报告缺失 = 证据不完整，判失败。
test -s "$REPORT_DIR/accept-$STAMP-http.md" || fail "HTTP 报告缺失或为空"
if "$ACCEPT" --mode http --addr "$HTTP_URL" --keys "$KEYS" --cases cases.yaml \
    --replay-from "$REPORT_DIR/accept-$STAMP-http.md.chain.jsonl" \
    --report "$REPORT_DIR/accept-$STAMP-http-replay.md" --timeout 90s; then
  echo "    ✅ HTTP 重放复现通过（报告：$REPORT_DIR/accept-$STAMP-http-replay.md）"
else
  fail "HTTP 重放复现失败（报告：$REPORT_DIR/accept-$STAMP-http-replay.md）"
fi
test -s "$REPORT_DIR/accept-$STAMP-http-replay.md" || fail "HTTP 重放报告缺失或为空"
kill "$GW_PID" 2>/dev/null || true
trap - EXIT

echo "==> [8/9] stdio 形态：serve-stdio（限 5000，trunc-002 硬上限覆盖）+ 重放复现..."
export DGW_API_KEY="$KEY_MAIN" DGW_DB_PATH="$TMP/dgw.db" DGW_PG_DATABASES="$PG_ROUTES" \
  DGW_SQL_LIMIT=5000 DGW_PG_STATEMENT_TIMEOUT_MS=30000 \
  DGW_EXEC_LOG_DIR="$TMP/logs-stdio" DGW_EXEC_RAW_RETENTION_DAYS=7 DGW_EXEC_SUMMARY_RETENTION_DAYS=30
if "$ACCEPT" --mode stdio --dgw-bin "$DGWBIN" --stdio-user dev-alice --keys "$KEYS" \
    --cases cases.yaml --log-dir "$TMP/logs-stdio" \
    --report "$REPORT_DIR/accept-$STAMP-stdio.md" \
    --psql-prefix "$PSQL_PREFIX" --timeout 90s; then
  echo "    ✅ stdio 重放通过（报告：$REPORT_DIR/accept-$STAMP-stdio.md）"
else
  fail "stdio 重放失败（报告：$REPORT_DIR/accept-$STAMP-stdio.md）"
fi
test -s "$REPORT_DIR/accept-$STAMP-stdio.md" || fail "stdio 报告缺失或为空"
if "$ACCEPT" --mode stdio --dgw-bin "$DGWBIN" --stdio-user dev-alice --keys "$KEYS" \
    --cases cases.yaml --replay-from "$REPORT_DIR/accept-$STAMP-stdio.md.chain.jsonl" \
    --report "$REPORT_DIR/accept-$STAMP-stdio-replay.md" --timeout 90s; then
  echo "    ✅ stdio 重放复现通过（报告：$REPORT_DIR/accept-$STAMP-stdio-replay.md）"
else
  fail "stdio 重放复现失败（报告：$REPORT_DIR/accept-$STAMP-stdio-replay.md）"
fi
test -s "$REPORT_DIR/accept-$STAMP-stdio-replay.md" || fail "stdio 重放报告缺失或为空"

echo "==> [9/9] 收尾..."
docker compose -f "$PG_COMPOSE_ABS" -p "$PROJ" down >/dev/null 2>&1 || true
echo "    PG 栈已停（volume 保留，重跑自动重建；清理：docker compose -f $PG_COMPOSE_ABS -p $PROJ down -v）"
echo "    工作目录：$TMP"

if [ "$STATUS" = 0 ]; then
  echo
  echo "✅ 验收通过：HTTP + stdio 双形态重放 + 判定三件套全过 + 报告留档"
  echo "   报告：$REPORT_DIR/accept-$STAMP-{http,stdio,http-replay,stdio-replay}.md"
else
  echo
  echo "❌ 验收存在失败（详见上述 FAIL 与报告）"
fi
exit "$STATUS"
