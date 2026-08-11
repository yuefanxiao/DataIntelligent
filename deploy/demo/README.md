# demo：本地全量验证（主从流复制 + 网关 + 真实 MCP 查询 + 拒启演示）

在本地复现 ADR-0009 部署形态的完整验收面：**主 + 从**流复制 PG（模拟
CNPG 从库，`pg_is_in_recovery() = true`）+ compose 起网关 + 真实 MCP
往返（官方 go-sdk）+ 两个拒启失败场景 + 语义仓库操作路径。

```
./setup.sh       # 全流程：起主从 PG → provisioning → 网关 → MCP 查询
./fail-demo.sh   # 拒启演示：连主库 / 角色超时不一致（跑完恢复终态）
```

## setup.sh 做了什么

1. `docker-compose.pg.yml` 起主从 PG（从库 basebackup + hot_standby）；
2. 主库建 **10 个持库** + bss 域 schema 前缀 + `bill.orders` 演示数据
   （600 行，status/amount/paid_at，与 neo-cloud 生产形态同构）；
3. 跑**真实 provisioning**（`../provisioning/readonly-role.sql`，共享只读
   角色 dgw_reader + 角色级 statement_timeout=30s）——交付物 SQL 的实测；
4. 写 `../config/env`（0600，.gitignore 内；DSN → **从库**）→ compose
   起网关 → 启动日志断言「启动自检通过」；
5. `key-create` + `grant-add` → `mcp-ping.go` 真实 MCP 往返：
   initialize → tools/list → execute_sql（聚合 + 明细）。

## 关键路径

| 验证点 | 位置 |
|---|---|
| 三挂载 /data /logs /config 0600 | `docker compose -f ../docker-compose.yml exec dgw ls -ld /data /logs /config` |
| 凭证只在 env 文件 | `../config/env`（0600，gitignored；DSN 即凭证） |
| 启动自检两条硬校验 | 启动日志「启动自检通过：N 条 dbname 路由全部连到从库…」 |
| 拒启失败场景 | `./fail-demo.sh`（A 连主库 / B 超时不一致 / C 对照） |
| 语义仓库操作路径 | `../semantic-repo/bootstrap.sh`（本地裸仓库模拟 Gitea） |

## 参数

- `DGW_DEMO_REPL_PORT`（默认 55432）：从库宿主端口（网关 DSN 目标）；
- `DGW_HTTP_PORT`（默认 8080）：网关宿主端口（与 deploy/docker-compose.yml
  同一变量）；
- `./setup.sh --skip-pg`：复用已起 PG 栈；`--skip-gateway`：只准备
  PG/凭证。

## 清理

```sh
docker compose -f docker-compose.pg.yml down -v
docker compose -f ../docker-compose.yml down
rm -f ../config/env
```

## 说明

- demo PG 是**本地模拟**（trust 认证、非生产拓扑）；生产 provisioning 见
  `../provisioning/README.md`（CNPG 超管执行，口令认证）。
- 网关容器内经 `host.docker.internal` 访问宿主映射端口（容器内
  127.0.0.1 是容器自己）；生产部署 = 开发机网络直通从库。
- `mcp-ping.go` 是部署验收/排障探测（只测外部行为，无业务断言）；
  全量验收重放框架 = v1 build 12（issue #29）。
