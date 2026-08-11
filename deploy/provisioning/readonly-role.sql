-- 共享只读角色 provisioning（ADR-0004 §4.4 / ADR-0009 §4.8；开发自建，SQL 可重放）
-- ---------------------------------------------------------------------------
-- 背景：网关/采集器永远只用这个只读角色连业务从库；超管凭证只在本脚本
-- 执行期间出现（CNPG postgres 超管），不进任何配置文件（开发机/CI 零凭证）。
--
-- 用法（CNPG 集群，取 postgres 超管后执行）：
--   kubectl exec -it <cnpg-cluster>-1 -- psql -U postgres -d postgres
--   \set ro_password '<强随机口令>'    -- 必填：角色口令（psql 变量）
--   \set ro_timeout '30s'              -- 可选：角色级 statement_timeout（默认 30s）
--   \i readonly-role.sql
-- 幂等：重跑安全（角色已存在只更新属性/授权）。新表出现后重跑本脚本即补
-- 授权——ALTER DEFAULT PRIVILEGES 只影响「执行本脚本的用户」后续建的对象，
-- 应用角色建的新表需重跑（v1 采集器 calibrate 漂移报告只报告不改，新表
-- 默认拒绝 + 同步管线通配覆盖告警，ADR-0004）。
--
-- 安全边界（本脚本落实）：
--   - 物理不能写：NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT + 只授
--     SELECT（+ USAGE/CONNECT）；网关校验层的「PG 物理边界」依赖它；
--   - 超时边界：角色级 statement_timeout（网关启动自检第二条硬校验，
--     值须与网关 env DGW_PG_STATEMENT_TIMEOUT_MS 一致，不一致拒启）；
--   - 收 SET ROLE 能力：NOINHERIT + 不授予任何角色成员资格 → 无法
--     SET ROLE 到其他角色（连自己的 PGUSER 也不能冒充别人）；
--   - 收临时表能力：REVOKE TEMPORARY（PUBLIC 默认持有 TEMP，逐库收回）。

\set ON_ERROR_STOP on
-- 角色口令必填（未 \set ro_password 会在首次使用处报 undefined variable
-- 终止）；ro_timeout 可选，缺省 30s（已定义则尊重用户预设值）。
\if :{?ro_timeout}
\else
\set ro_timeout '30s'
\endif

-- 1) 角色（物理边界 + 口令；幂等——已存在只更新属性）
--    psql 变量插值不进入 DO 块字符串，这里用 \gset + \if 条件分支：
SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dgw_reader') AS ro_exists \gset
\if :ro_exists
ALTER ROLE dgw_reader LOGIN PASSWORD :'ro_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
\else
CREATE ROLE dgw_reader LOGIN PASSWORD :'ro_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
\endif

-- 2) 角色级 statement_timeout（网关启动自检第二条硬校验依赖此设置）
ALTER ROLE dgw_reader SET statement_timeout = :'ro_timeout';

-- 3) 逐库授权（10 个持库；bss 域 = 库内同名 schema 前缀，其余 = public）
--    bss 域：bill / wallet / bss_invoice / subscription / promotion
--    iam 域：iam / iam_audit
--    其他：console / notification / ops_ticket
--    每段：收 TEMP → CONNECT → schema USAGE → 现存表 SELECT → 默认权限
--    （注意：GRANT SELECT 只授现表；ALTER DEFAULT PRIVILEGES 兜自己建的表）

\c bill
REVOKE TEMPORARY ON DATABASE bill FROM dgw_reader;
GRANT CONNECT ON DATABASE bill TO dgw_reader;
GRANT USAGE ON SCHEMA bill TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA bill TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA bill GRANT SELECT ON TABLES TO dgw_reader;

\c wallet
REVOKE TEMPORARY ON DATABASE wallet FROM dgw_reader;
GRANT CONNECT ON DATABASE wallet TO dgw_reader;
GRANT USAGE ON SCHEMA wallet TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA wallet TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA wallet GRANT SELECT ON TABLES TO dgw_reader;

\c bss_invoice
REVOKE TEMPORARY ON DATABASE bss_invoice FROM dgw_reader;
GRANT CONNECT ON DATABASE bss_invoice TO dgw_reader;
GRANT USAGE ON SCHEMA bss_invoice TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA bss_invoice TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA bss_invoice GRANT SELECT ON TABLES TO dgw_reader;

\c subscription
REVOKE TEMPORARY ON DATABASE subscription FROM dgw_reader;
GRANT CONNECT ON DATABASE subscription TO dgw_reader;
GRANT USAGE ON SCHEMA subscription TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA subscription TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA subscription GRANT SELECT ON TABLES TO dgw_reader;

\c promotion
REVOKE TEMPORARY ON DATABASE promotion FROM dgw_reader;
GRANT CONNECT ON DATABASE promotion TO dgw_reader;
GRANT USAGE ON SCHEMA promotion TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA promotion TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA promotion GRANT SELECT ON TABLES TO dgw_reader;

\c iam
REVOKE TEMPORARY ON DATABASE iam FROM dgw_reader;
GRANT CONNECT ON DATABASE iam TO dgw_reader;
GRANT USAGE ON SCHEMA public TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dgw_reader;

\c iam_audit
REVOKE TEMPORARY ON DATABASE iam_audit FROM dgw_reader;
GRANT CONNECT ON DATABASE iam_audit TO dgw_reader;
GRANT USAGE ON SCHEMA public TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dgw_reader;

\c console
REVOKE TEMPORARY ON DATABASE console FROM dgw_reader;
GRANT CONNECT ON DATABASE console TO dgw_reader;
GRANT USAGE ON SCHEMA public TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dgw_reader;

\c notification
REVOKE TEMPORARY ON DATABASE notification FROM dgw_reader;
GRANT CONNECT ON DATABASE notification TO dgw_reader;
GRANT USAGE ON SCHEMA public TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dgw_reader;

\c ops_ticket
REVOKE TEMPORARY ON DATABASE ops_ticket FROM dgw_reader;
GRANT CONNECT ON DATABASE ops_ticket TO dgw_reader;
GRANT USAGE ON SCHEMA public TO dgw_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO dgw_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dgw_reader;

-- 4) 验证（psql 对照）：应输出 superuser=off / 无成员资格 / timeout=30s
--    \c bill
--    SELECT rolsuper, rolcreatedb, rolcreaterole, rolinherit FROM pg_roles WHERE rolname='dgw_reader';
--    SELECT ARRAY(SELECT g.rolname FROM pg_auth_members m JOIN pg_roles g ON g.oid=m.roleid WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname='dgw_reader'));
--    \c bill
--    SHOW statement_timeout;  -- 以 dgw_reader 连接时 = 30s（角色级生效）
--    CREATE TEMP TABLE t(x int);  -- 应拒绝（无 TEMP 权限）
--    SET ROLE postgres;           -- 应拒绝（无成员资格）
