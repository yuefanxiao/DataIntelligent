# v1 build 10 — 部署 + 启动自检 + 语义仓库

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/27
Status: closed（PR #41 合入 main，2026-08-12 关闭）

## 交付摘要

1. **启动自检**（internal/db/selfcheck.go，不过拒启）：逐 dbname 三条硬校验——
   `pg_is_in_recovery() = true`（防连错主库）+ 角色级 `statement_timeout` 生效确认
   （纯净连接 SHOW，剔除网关连接级参数与 DSN options）+ `current_database()` 与
   路由 dbname 一致；`dgw selfcheck` 子命令 + `serve`/`serve-stdio` 拒启接线
   （gatewayOpts 返回 router，自检失败 log.Fatalf 不监听）。
2. **Docker 部署**（deploy/）：多阶段 Dockerfile（非 root + BASE_REGISTRY 可覆盖）、
   compose 单服务 + 三挂载（/data /logs /config 0600 只读）+ restart unless-stopped
   + healthcheck（401 探测，崩溃循环 external 信号）+ 默认 127.0.0.1 绑定 +
   DGW_IMAGE_TAG 版本化（回滚基线 = 旧 tag + SQLite 备份）。
3. **共享只读角色 provisioning**（deploy/provisioning/readonly-role.sql + README）：
   NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT、10 库只读（bss 域 schema 前缀）、
   角色级 statement_timeout、收 SET ROLE（无成员资格）/临时表（REVOKE TEMPORARY）/
   函数 EXECUTE（PUBLIC 收回——只对角色 REVOKE 无效，PUBLIC 是隐式成员）；
   psql \gset+\if 幂等。
4. **语义仓库**（deploy/semantic-repo/）：bootstrap.sh 本地 clone 操作路径 +
   verify.sh 全链路自动化（采集 → commit/push → 同步 → revert 回滚）。
5. **demo 验证套件**（deploy/demo/）：主从流复制 PG（pg_is_in_recovery()=t）+
   真实 provisioning + mcp-ping.go（官方 go-sdk MCP 往返探测）+ fail-demo.sh
   （连主库 / 超时不一致拒启）+ 三挂载自动断言。

## 验证

- go build/vet/test 全绿（selfcheck docker 主从 e2e：通过/主库拒启/超时不一致拒启/
  DSN 指错库拒启/不可达拒启）
- demo 实测：compose 起网关 → 自检通过 → MCP 查询真实数据 → 三挂载断言 →
  fail-demo A/B/C 全过；provisioning 幂等 + EXECUTE 收回实测（dgw_reader 执行函数被拒）
- code-review（双轴）+ code-review-adversarial 收敛轮 1（4×P2 全修 + P3 加固，
  复核 6/6 通过）

## 交接注意

- 自检结论 = 启动瞬间边界；CNPG failover 后需重启网关（deploy/README 已声明）。
- mcp-ping 与 #29（验收重放框架）职责重叠，README 已自注，交 #29 收敛。
- 校验层 v1 只映射 public schema（spec §8）；bss 域 schema 前缀表 execute_sql
  会 unknown_table 拒绝——已知 v1 边界（provisioning README 已声明）。
