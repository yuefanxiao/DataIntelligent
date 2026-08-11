# v1 build 04 — execute_sql 工具：PG 接线 + dbname 路由 + 限额包层

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/21
Status: assigned（yuefanxiao，2026-08-12 领取；实现 + 单测完成，PR 合入后关闭）
Blocked by (open blockers): 0

## 来源

docs/spec.md §2/§4.3/§4.5；ADR-0008；issue #15

## What to build

`execute_sql` 工具端到端：PG 客户端 + 可配置 DSN + 按 dbname 路由（Database 实体 = PG database）；校验层四段链全链挂载（03 的两段 + PG 物理边界：共享只读角色、连接级 statement_timeout、禁 SET ROLE + 限额包层 `SELECT * FROM (<用户 SQL>) _q LIMIT N`）；结构化 JSON 结果（列名+类型+行数组+元信息）；默认 500 行 / 硬上限 5000 + truncated 标记；可选 plan_id 透传（溯源，不校验）。结果编码 JSON、渲染交给 Agent。

## Acceptance criteria

- [x] 经 MCP 调 execute_sql 查白名单表成功，结果结构化 JSON（列名+类型+行+元信息）
- [x] 按 dbname 路由到正确 PG database
- [x] 限额包层：>500 行截断 + truncated 标记；>5000 硬上限行为正确
- [x] 未授权表 / 未知表 → 结构化拒绝（错误区分「无权限表」）；非 SELECT → 结构化拒绝
- [x] plan_id 透传（不校验）
- [x] 四段链端到端生效（03 单测之外的整链验证）

## 交付（2026-08-12）

- `internal/db`（新）：dbname 路由表（JSON env）+ 池集合；物理边界 = 连接级 statement_timeout + default_transaction_read_only + 只读角色 DSN（禁 SET ROLE 物理+分类双保险）
- `internal/gateway`：`WithExecuteSQL` 注入（未配置 = 结构化拒绝）；execute_sql 四段链 handler（03 校验 → 路由 → 限额包层 → 结果编码）；SQL/plan_id/dbname 入参；结果 JSON（columns/rows/meta）
- `internal/config`：DGW_PG_DATABASES / DGW_SQL_LIMIT / DGW_PG_STATEMENT_TIMEOUT_MS
- `cmd/dgw`：serve / serve-stdio 接线路由 + 限额
- e2e：Docker 一次性 postgres:17（本机 PG 无 server 二进制，CI 无 docker 则跳过）——官方 SDK 客户端全链路 5 例：主路径（含 numeric/timestamptz/jsonb/uuid 归一化）、dbname 路由（含缺省推断）、截断（600 行 → 500+truncated）、拒绝语义、statement_timeout

## 备注

- 测试期发现两个真实问题并修复：pgx 执行期错误（57014）在 rows.Err() 阶段浮现需按 pgError 分类；uuid 解码为原始 16 字节需格式化为规范文本
- 限额越界（<500 或 >5000）= 启动失败 fail fast；`>5000 硬上限行为正确` = 配置层拒绝越界
- 值归一化：numeric → 文本（与 psql 一致）、bytea → \x 十六进制、jsonb → 原生 JSON、时间 → RFC3339Nano

## Blocked by

- #20 — 校验层（已关闭 2026-08-12，PR #33 合入）
