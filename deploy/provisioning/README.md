# 共享只读角色 provisioning 指引（spec §4.4〔ADR-0004〕/ spec §4.8〔ADR-0009〕）

网关/采集器**只用这个只读角色**连业务从库；超管凭证只在一次性
provisioning 期间出现，**不进任何配置文件**（开发机/CI 零凭证，
安全分界线 = 内部开发机 + 该角色）。

## 步骤

1. **取 CNPG postgres 超管**（开发自建：服务器 root / kubectl）：

   ```sh
   kubectl exec -it <cnpg-cluster>-1 -- psql -U postgres -d postgres
   ```

2. **执行脚本**（可重放；`\set ro_password` 必填）：

   ```sql
   \set ro_password '<强随机口令>'
   \set ro_timeout '30s'        -- 可选，须与网关 env DGW_PG_STATEMENT_TIMEOUT_MS 一致
   \i readonly-role.sql
   ```

   脚本幂等：重跑安全；新表出现后重跑即补授权。

3. **验证**（脚本末尾附 psql 对照；或直接跑网关自检）：

   ```sh
   # 从库上以 dgw_reader 连接
   \c bill
   SHOW statement_timeout;   -- 30s（角色级生效）
   CREATE TEMP TABLE t(x int);  -- 应拒绝（无 TEMP 权限）
   SET ROLE postgres;            -- 应拒绝（无成员资格）
   SELECT * FROM orders LIMIT 1; -- 应成功（SELECT 只读）
   ```

4. **写网关 env 文件**（`deploy/config/env`，chmod 600）：DSN 用
   `postgres://dgw_reader:<口令>@replica-host:5432/<库>`。

## 安全边界（脚本落实，勿手改）

| 边界 | 落实 |
|---|---|
| 物理不能写 | `NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT` + 只授 SELECT/USAGE/CONNECT |
| 超时 | 角色级 `statement_timeout`（网关启动自检第二条硬校验，不过拒启） |
| 收 SET ROLE | `NOINHERIT` + 不授任何角色成员资格 |
| 收临时表 | 逐库 `REVOKE TEMPORARY` |

## 注意

- `ALTER DEFAULT PRIVILEGES` 只影响执行脚本的用户后续建的对象；应用角色
  建的新表需**重跑本脚本**补授权（幂等）。新表在网关侧默认拒绝 + 同步
  管线通配覆盖告警（ADR-0004），采集器 `calibrate` 漂移报告只报告不改。
- 网关 `DGW_PG_STATEMENT_TIMEOUT_MS` 与角色 `statement_timeout` **必须一致**
  ——启动自检硬校验，不一致拒启（防「网关以为自己有超时、角色没有」）。
- 只读角色口令轮换 = 重跑脚本（`ALTER ROLE ... PASSWORD` 幂等）+ 更新
  网关 env 文件 + 重启网关。口令经 psql 变量传入（`\set ro_password` /
  `-v ro_password=...`），含引号/反斜杠的字符需按 psql 变量转义规则处理。
- **v1 校验层只映射 public schema**（spec §8「v1 表均在 public schema」）：
  bss 域表在生产库实际落 `schema <库名>` 前缀（采集器生产形态，本脚本的
  GRANT 按此授权），但 v1 execute_sql 对非 public schema 的表会
  `unknown_table` 拒绝——这是已知 v1 边界（查询侧），结构采集/授权按
  生产形态（真相侧）不变；新表/漂移由采集器 `calibrate` 例行对照报告。
