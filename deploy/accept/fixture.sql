-- 验收用例矩阵 fixture（v1 build 14；spec §6.1/§6.2）
-- ---------------------------------------------------------------------------
-- 13 服务用例矩阵的「伪造数据」：10 个持库各建矩阵用例所需的真实表名
-- （与 samples/semantic/services/ 结构一致）+ 确定性数据（generate_series，
-- 全部相对 now() 的时间偏移——重放同日可复现；跨天重跑重新建库，数据
-- 相对窗口自动前移）。
--
-- 形态约定（与 demo orders/big_events 同构，README「方案取舍」记录）：
--   - 表建在 public schema（v1 校验层 FQN 映射 = 服务.库.表，仅未限定/
--     public 引用可解析，execute_sql.go resolve）；真实生产的 bss 域同名
--     schema 前缀是 provisioning 覆盖的形态，pg_schema 元数据不参与执行；
--   - 只建用例引用到的表/列（矩阵是外部行为测试，不是结构全量镜像）；
--   - 数据量小（几十到几百行）+ 枚举状态覆盖 + 每服务一个「昨日/近期」
--     相对时间窗，行数断言可精确到个位。
--
-- 由 run.sh 在主库执行（先于 provisioning——provisioning 的
-- GRANT SELECT ON ALL TABLES IN SCHEMA public 一次性覆盖全部 fixture
-- 表，无需逐表补授）。独立执行：
--   docker compose -f ../demo/docker-compose.pg.yml -p dgw-accept \
--     exec -T pg-primary psql -U postgres -v ON_ERROR_STOP=1 -d postgres -f /path/fixture.sql
\set ON_ERROR_STOP on

-- ══ bss-bill（db=bill；矩阵用例 bill-001/002/003）══════════════════════════
\c bill
-- 账单明细（bill-001 时间窗口聚合：近 7 日按天 sum(bill_amount) → 7 行）
CREATE TABLE bills (
  id bigint PRIMARY KEY,
  bill_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  model varchar(64),
  quantity numeric(12,4),
  unit_price numeric(12,4),
  bill_amount numeric(12,2) NOT NULL,
  request_started_at timestamptz NOT NULL
);
INSERT INTO bills (id, bill_id, org_id, model, quantity, unit_price, bill_amount, request_started_at)
SELECT g, 'bill-' || g, 'org-' || (g % 12 + 1), 'qianshi',
  (g % 100)::numeric, 0.01,
  ((g % 100) * 0.01)::numeric(12,2),
  date_trunc('day', now()) - make_interval(days => g % 14, mins => g % 1440)
FROM generate_series(1, 420) g;

-- 结算批次（bill-002 枚举状态聚合：status ∈ settled/processing/failed_manual → 3 行）
CREATE TABLE settlement_batches (
  id bigint PRIMARY KEY,
  settlement_batch_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  settlement_cycle_started_at timestamptz NOT NULL,
  amount numeric(12,2) NOT NULL,
  currency varchar(8) DEFAULT 'CNY',
  bill_count int,
  status varchar(32) NOT NULL,
  attempt_count int DEFAULT 0,
  settled_at timestamptz
);
INSERT INTO settlement_batches (id, settlement_batch_id, org_id, settlement_cycle_started_at, amount, currency, bill_count, status, attempt_count, settled_at)
SELECT g, 'sb-' || g, 'org-' || (g % 12 + 1),
  date_trunc('day', now()) - make_interval(days => g % 14),
  (g * 1.1)::numeric(12,2), 'CNY', g % 50,
  CASE g % 3 WHEN 0 THEN 'settled' WHEN 1 THEN 'processing' ELSE 'failed_manual' END,
  g % 5,
  CASE WHEN g % 3 = 0 THEN now() - make_interval(days => g % 7) ELSE NULL END
FROM generate_series(1, 120) g;

-- 结算尝试（bill-003 复杂：CTE + LEFT JOIN + 时间窗口的扣费失败归因）
CREATE TABLE settlement_attempts (
  id bigint PRIMARY KEY,
  settlement_batch_id varchar(64) NOT NULL,
  attempt_no int NOT NULL,
  trigger_type varchar(32) NOT NULL,
  status varchar(32) NOT NULL,
  request_amount numeric(12,2),
  bill_count int,
  error_code varchar(32),
  rpc_started_at timestamptz,
  created_at timestamptz NOT NULL
);
INSERT INTO settlement_attempts (id, settlement_batch_id, attempt_no, trigger_type, status, request_amount, bill_count, error_code, rpc_started_at, created_at)
SELECT g, 'sb-' || (g % 120 + 1), (g / 120) + 1, 'dispatcher',
  CASE g % 4 WHEN 0 THEN 'failed' WHEN 1 THEN 'succeeded' WHEN 2 THEN 'retrying' ELSE 'started' END,
  (g * 0.9)::numeric(12,2), g % 30,
  CASE g % 4 WHEN 0 THEN 'INSUFFICIENT_BALANCE' ELSE NULL END,
  now() - make_interval(mins => g),
  now() - make_interval(mins => g)
FROM generate_series(1, 480) g;

-- ══ bss-wallet（db=wallet；矩阵 wallet-001/002/003 + 主用例 main-001 + neg-005）══
\c wallet
-- 钱包账户（主用例下钻 join 侧；org_type 枚举 individual/team/unspecified）
CREATE TABLE wallet_accounts (
  org_id varchar(36) PRIMARY KEY,
  balance numeric(14,2) NOT NULL,
  frozen_amount numeric(14,2) DEFAULT 0,
  currency varchar(8) DEFAULT 'CNY',
  status smallint NOT NULL DEFAULT 1,
  org_type varchar(16) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO wallet_accounts (org_id, balance, frozen_amount, currency, status, org_type, created_at)
SELECT 'org-' || g, (g * 1000)::numeric(14,2), (g * 10)::numeric(14,2), 'CNY',
  CASE g % 3 WHEN 0 THEN 1 WHEN 1 THEN 2 ELSE 1 END,
  CASE g % 3 WHEN 0 THEN 'individual' WHEN 1 THEN 'team' ELSE 'unspecified' END,
  now() - make_interval(days => g)
FROM generate_series(1, 20) g;

-- 支付单（主用例数据故事：此前 13 天 3/60=5% 失败率，昨日 43/100=43%
-- 激增——失败集中 channel=5 银行转账；status 2=成功 4=失败，channel
-- 2=微信 4=支付宝 5=银行转账，与 samples/semantic 枚举一致）
CREATE TABLE payment_orders (
  id bigint PRIMARY KEY,
  order_id varchar(128) NOT NULL,
  org_id varchar(36) NOT NULL,
  requested_amount numeric(12,2) NOT NULL,
  paid_amount numeric(12,2),
  channel smallint NOT NULL,
  status smallint NOT NULL,
  created_at timestamptz NOT NULL
);
-- day_ago：13..1 = 每日常规 60 笔（3 失败 / 57 成功）；1 = 昨日追加 100 笔
-- （40 失败全 channel=5）；0 = 今日 40 笔（2 失败）——窗口查询上界
-- date_trunc('day', now()) 排除今日。
WITH s AS (
  SELECT g,
    CASE WHEN g <= 780 THEN 14 - ((g - 1) / 60 + 1)
         WHEN g <= 880 THEN 1
         ELSE 0 END AS day_ago,
    (g - 1) % 60 AS i
  FROM generate_series(1, 920) g
)
INSERT INTO payment_orders (id, order_id, org_id, requested_amount, paid_amount, channel, status, created_at)
SELECT g, 'po-' || g, 'org-' || (g % 20 + 1),
  (i % 50 + 1)::numeric(12,2), (i % 50 + 1)::numeric(12,2),
  CASE
    WHEN day_ago = 1 AND i >= 60 THEN 5
    WHEN day_ago = 0 AND i >= 38 THEN 4
    WHEN i % 20 = 0 THEN CASE day_ago % 3 WHEN 0 THEN 2 WHEN 1 THEN 4 ELSE 5 END
    ELSE (i % 2) * 2 + 2
  END AS channel,
  CASE
    WHEN day_ago = 1 AND i >= 60 THEN 4
    WHEN day_ago = 0 AND i >= 38 THEN 4
    WHEN i % 20 = 0 THEN 4
    ELSE 2
  END AS status,
  date_trunc('day', now()) - make_interval(days => day_ago, mins => i)
FROM s;

-- 钱包流水（wallet-002 聚合：tx_type 1..5 → 5 行）
CREATE TABLE wallet_transactions (
  id bigint PRIMARY KEY,
  transaction_id varchar(128) NOT NULL,
  org_id varchar(36) NOT NULL,
  tx_type smallint NOT NULL,
  amount numeric(12,2) NOT NULL,
  balance_after numeric(14,2),
  created_at timestamptz NOT NULL
);
INSERT INTO wallet_transactions (id, transaction_id, org_id, tx_type, amount, balance_after, created_at)
SELECT g, 'tx-' || g, 'org-' || (g % 20 + 1), (g % 5) + 1,
  ((g % 100) * 1.5)::numeric(12,2), (g * 10)::numeric(14,2),
  now() - make_interval(days => g % 7, mins => g % 1440)
FROM generate_series(1, 300) g;

-- 退款单（wallet-003 复杂：join payment_orders 退款归因；neg-005 无指标
-- 原料路径直查目标；status 5=退款成功 6=失败 7=待重试）
CREATE TABLE refund_orders (
  id bigint PRIMARY KEY,
  refund_id varchar(128) NOT NULL,
  org_id varchar(36) NOT NULL,
  payment_order_id varchar(128) NOT NULL,
  amount numeric(12,2) NOT NULL,
  status smallint NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO refund_orders (id, refund_id, org_id, payment_order_id, amount, status, created_at)
SELECT g, 'rf-' || g, 'org-' || (g % 20 + 1), 'po-' || (g * 3),
  (g * 0.5)::numeric(12,2),
  CASE g % 3 WHEN 0 THEN 5 WHEN 1 THEN 6 ELSE 7 END,
  now() - make_interval(days => g % 7, mins => g % 1440)
FROM generate_series(1, 90) g;

-- ══ bss-invoice（db=bss_invoice；矩阵 invoice-001/002/003）════════════════
\c bss_invoice
-- 开票申请（invoice-001/002：枚举状态聚合 + 时间窗口）
CREATE TABLE invoice_applications (
  application_id varchar(64) PRIMARY KEY,
  org_id varchar(36) NOT NULL,
  status varchar(16) NOT NULL,
  invoice_type varchar(16) NOT NULL,
  amount numeric(12,2) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO invoice_applications (application_id, org_id, status, invoice_type, amount, created_at)
SELECT 'app-' || g, 'org-' || (g % 20 + 1),
  CASE g % 3 WHEN 0 THEN 'issued' WHEN 1 THEN 'in_progress' ELSE 'failed' END,
  CASE g % 2 WHEN 0 THEN 'normal' ELSE 'special' END,
  (g * 0.8)::numeric(12,2),
  now() - make_interval(days => g % 14, mins => g % 1440)
FROM generate_series(1, 150) g;

-- 税务发票（invoice-003 三表 join 的开票链路）
CREATE TABLE tax_invoices (
  tax_invoice_id varchar(64) PRIMARY KEY,
  application_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  invoice_type varchar(16),
  amount numeric(12,2),
  status varchar(16) DEFAULT 'issued',
  issued_at timestamptz
);
INSERT INTO tax_invoices (tax_invoice_id, application_id, org_id, invoice_type, amount, status, issued_at)
SELECT 'tax-' || g, 'app-' || (g % 150 + 1), 'org-' || (g % 20 + 1),
  CASE g % 2 WHEN 0 THEN 'normal' ELSE 'special' END, (g * 0.8)::numeric(12,2), 'issued',
  now() - make_interval(days => g % 14, mins => g)
FROM generate_series(1, 120) g;

-- 发票文件（invoice-003）
CREATE TABLE invoice_files (
  file_id varchar(64) PRIMARY KEY,
  application_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  file_name varchar(128),
  size_bytes bigint,
  status varchar(16) DEFAULT 'bound',
  created_at timestamptz NOT NULL
);
INSERT INTO invoice_files (file_id, application_id, org_id, file_name, size_bytes, status, created_at)
SELECT 'file-' || g, 'app-' || (g % 150 + 1), 'org-' || (g % 20 + 1),
  'invoice-' || g || '.pdf', (g * 1000)::bigint, 'bound',
  now() - make_interval(days => g % 14, mins => g % 60)
FROM generate_series(1, 100) g;

-- ══ bss-subscription（db=subscription；矩阵 sub-001/002/003）══════════════
\c subscription
-- 模型定价（sub-001/002：模型×计量项聚合 + 30 日窗口计数）
CREATE TABLE model_pricing (
  id bigint PRIMARY KEY,
  model varchar(191) NOT NULL,
  meter smallint NOT NULL,
  amount numeric(20,8) NOT NULL,
  unit_type varchar(32),
  currency varchar(8) DEFAULT 'CNY',
  effective_from timestamptz NOT NULL
);
INSERT INTO model_pricing (id, model, meter, amount, unit_type, currency, effective_from)
SELECT g, CASE g % 3 WHEN 0 THEN 'qianshi' WHEN 1 THEN 'kimi-k2.5' ELSE 'qianshi' END,
  (g % 4) + 1, ((g % 100) * 0.001)::numeric(20,8), 'token', 'CNY',
  now() - make_interval(days => g % 30, hours => g % 24)
FROM generate_series(1, 180) g;

-- 档位策略版本 + 明细（sub-003 复杂：join 的档位阈值×版本）
CREATE TABLE tier_policy_versions (
  id bigint PRIMARY KEY,
  version int NOT NULL,
  upgrade_strategy varchar(32),
  downgrade_strategy varchar(64)
);
INSERT INTO tier_policy_versions VALUES
  (1, 1, 'AUTO_UPGRADE', 'NO_DOWNGRADE'),
  (2, 2, 'AUTO_UPGRADE', 'MONTHLY_ACTIVE_CONSUMPTION_DOWNGRADE');

CREATE TABLE tier_policy_items (
  id bigint PRIMARY KEY,
  policy_version_id bigint NOT NULL,
  tier_id varchar(32) NOT NULL,
  label varchar(64),
  sort_order int,
  is_floor bool,
  required_score int,
  rpm int,
  tpm int,
  concurrency int
);
INSERT INTO tier_policy_items (id, policy_version_id, tier_id, label, sort_order, is_floor, required_score, rpm, tpm, concurrency)
SELECT g, (g % 2) + 1, 'tier-' || (g % 4), 'T' || (g % 4),
  g % 4, (g % 4) = 0, (g % 100) * 10,
  (g % 50) + 10, (g % 500) + 100, (g % 10) + 1
FROM generate_series(1, 40) g;

-- ══ bss-promotion（db=promotion；矩阵 promo-001/002/003）══════════════════
\c promotion
-- 兑换码批次（promo-003 三表 join 的批次侧）
CREATE TABLE code_batches (
  batch_id varchar(64) PRIMARY KEY,
  name varchar(128) NOT NULL,
  channel varchar(32),
  effect_type varchar(32) NOT NULL,
  code_mode varchar(32) NOT NULL,
  total_count int NOT NULL,
  status varchar(16) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO code_batches (batch_id, name, channel, effect_type, code_mode, total_count, status, created_at)
SELECT 'batch-' || g, '批次' || g, 'channel-' || (g % 2), 'VOUCHER_REDEEM', 'shared',
  g * 10, CASE g % 3 WHEN 0 THEN 'active' WHEN 1 THEN 'terminated' ELSE 'active' END,
  now() - make_interval(days => g % 14)
FROM generate_series(1, 6) g;

-- 兑换码（promo-002 枚举状态聚合；status ∈ used/active/revoked）
CREATE TABLE codes (
  code_id varchar(64) PRIMARY KEY,
  batch_id varchar(64) NOT NULL,
  code_hash varchar(64) NOT NULL,
  status varchar(16) NOT NULL,
  redemption_count int DEFAULT 0,
  valid_until timestamptz
);
INSERT INTO codes (code_id, batch_id, code_hash, status, redemption_count, valid_until)
SELECT 'code-' || g, 'batch-' || (g % 6 + 1), md5('code-' || g),
  CASE g % 4 WHEN 0 THEN 'used' WHEN 1 THEN 'active' WHEN 2 THEN 'revoked' ELSE 'used' END,
  g % 5, now() + make_interval(days => 30)
FROM generate_series(1, 240) g;

-- 兑换记录（promo-001/003：状态聚合 + 三表 join 失败归因）
CREATE TABLE code_redemptions (
  redemption_id varchar(64) PRIMARY KEY,
  campaign_id varchar(64),
  code_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  status varchar(16) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO code_redemptions (redemption_id, campaign_id, code_id, org_id, status, created_at)
SELECT 'rd-' || g, 'camp-1', 'code-' || (g % 240 + 1), 'org-' || (g % 20 + 1),
  CASE g % 5 WHEN 0 THEN 'failed' WHEN 1 THEN 'processing' ELSE 'succeeded' END,
  now() - make_interval(days => g % 7, mins => g % 1440)
FROM generate_series(1, 180) g;

-- ══ iam（db=iam；矩阵 iam-001/002/003）════════════════════════════════════
\c iam
-- 组织（iam-001/002：枚举状态聚合 + 时间窗口）
CREATE TABLE organizations (
  organization_id varchar(64) PRIMARY KEY,
  name varchar(128) NOT NULL,
  status varchar(16) NOT NULL,
  organization_type varchar(16) NOT NULL,
  owner_user_id varchar(64),
  created_at timestamptz NOT NULL
);
INSERT INTO organizations (organization_id, name, status, organization_type, owner_user_id, created_at)
SELECT 'org-' || g, '组织' || g,
  CASE g % 4 WHEN 0 THEN 'active' WHEN 1 THEN 'frozen' WHEN 2 THEN 'active' ELSE 'cancelling' END,
  CASE g % 2 WHEN 0 THEN 'individual' ELSE 'team' END,
  'user-' || (g % 10 + 1),
  now() - make_interval(days => g % 30, mins => g)
FROM generate_series(1, 40) g;

-- 组织成员（iam-003 复杂：join organizations 的类型×成员数）
CREATE TABLE organization_memberships (
  id bigint PRIMARY KEY,
  organization_id varchar(64) NOT NULL,
  user_id varchar(64) NOT NULL,
  role_id bigint,
  status varchar(16) NOT NULL DEFAULT 'active',
  joined_at timestamptz NOT NULL
);
INSERT INTO organization_memberships (id, organization_id, user_id, role_id, status, joined_at)
SELECT g, 'org-' || (g % 40 + 1), 'user-' || (g % 30 + 1), (g % 5) + 1, 'active',
  now() - make_interval(days => g % 10, mins => g % 1440)
FROM generate_series(1, 200) g;

-- ══ iam-audit（db=iam_audit；矩阵 audit-001/002/003）══════════════════════
\c iam_audit
-- 审计日志（audit-001/003：事件类型聚合 + 子查询/窗口函数；700 行，event_type
-- 4 值循环、近 1 日窗口恰含 bill.view + user.login_failed 两值）
CREATE TABLE audit_logs (
  event_id varchar(64) PRIMARY KEY,
  event_type varchar(128) NOT NULL,
  service varchar(64) NOT NULL,
  actor_type varchar(16) NOT NULL,
  actor_name varchar(64),
  org_id varchar(36),
  resource_type varchar(64),
  resource_id varchar(64),
  status varchar(16),
  archive_status varchar(16) NOT NULL DEFAULT 'ACTIVE',
  occurred_at timestamptz NOT NULL
);
INSERT INTO audit_logs (event_id, event_type, service, actor_type, actor_name, org_id, resource_type, resource_id, status, archive_status, occurred_at)
SELECT 'evt-' || g,
  CASE g % 4 WHEN 0 THEN 'user.login_failed' WHEN 1 THEN 'wallet.recharge' WHEN 2 THEN 'bill.view' ELSE 'org.update' END,
  CASE g % 4 WHEN 0 THEN 'iam' WHEN 1 THEN 'wallet' WHEN 2 THEN 'bill' ELSE 'iam' END,
  CASE g % 3 WHEN 0 THEN 'user' WHEN 1 THEN 'admin' ELSE 'system' END,
  'actor-' || (g % 20), 'org-' || (g % 20 + 1),
  CASE g % 4 WHEN 0 THEN 'user' WHEN 1 THEN 'wallet' WHEN 2 THEN 'bill' ELSE 'org' END,
  'res-' || (g % 50), CASE g % 5 WHEN 0 THEN 'failed' ELSE 'succeeded' END, 'ACTIVE',
  now() - make_interval(days => g % 14, mins => g % 1440)
FROM generate_series(1, 700) g;

-- 审计导出（audit-002 枚举状态聚合）
CREATE TABLE audit_exports (
  export_id varchar(64) PRIMARY KEY,
  status varchar(16) NOT NULL,
  start_time timestamptz,
  end_time timestamptz,
  row_count int,
  error varchar(128)
);
INSERT INTO audit_exports (export_id, status, start_time, end_time, row_count, error)
SELECT 'exp-' || g,
  CASE g % 3 WHEN 0 THEN 'SUCCEEDED' WHEN 1 THEN 'RUNNING' ELSE 'FAILED' END,
  now() - make_interval(hours => g * 2), now() - make_interval(hours => g * 2 - 1),
  g * 100, CASE WHEN g % 3 = 2 THEN 'timeout' ELSE NULL END
FROM generate_series(1, 12) g;

-- ══ console（db=console；矩阵 console-001/002/003）════════════════════════
\c console
-- 审批实例（console-001/002：execute_status 聚合 + 时间窗口）
CREATE TABLE approval_cases (
  approval_id varchar(64) PRIMARY KEY,
  template_code varchar(64) NOT NULL,
  action_type varchar(32),
  business_type varchar(32),
  business_id varchar(64),
  org_id varchar(36),
  wecom_status varchar(16) NOT NULL,
  execute_status varchar(16) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO approval_cases (approval_id, template_code, action_type, business_type, business_id, org_id, wecom_status, execute_status, created_at)
SELECT 'appr-' || g, 'tpl-' || (g % 3 + 1), 'refund',
  CASE g % 2 WHEN 0 THEN 'refund' ELSE 'voucher' END, 'biz-' || g, 'org-' || (g % 20 + 1),
  CASE g % 3 WHEN 0 THEN 'approved' WHEN 1 THEN 'processing' ELSE 'rejected' END,
  CASE g % 3 WHEN 0 THEN 'succeeded' WHEN 1 THEN 'running' ELSE 'failed' END,
  now() - make_interval(days => g % 14, mins => g % 1440)
FROM generate_series(1, 120) g;

-- 组织分组 + 成员（console-003 复杂：join 的组×成员数）
CREATE TABLE org_groups (
  group_id varchar(64) PRIMARY KEY,
  name varchar(128) NOT NULL,
  deleted_at timestamptz
);
INSERT INTO org_groups (group_id, name, deleted_at)
SELECT 'grp-' || g, '分组' || g, NULL FROM generate_series(1, 5) g;

CREATE TABLE org_group_members (
  id bigint PRIMARY KEY,
  group_id varchar(64) NOT NULL,
  org_id varchar(36) NOT NULL,
  org_type varchar(16) NOT NULL,
  joined_at timestamptz NOT NULL
);
INSERT INTO org_group_members (id, group_id, org_id, org_type, joined_at)
SELECT g, 'grp-' || (g % 5 + 1), 'org-' || (g % 20 + 1),
  CASE g % 2 WHEN 0 THEN 'individual' ELSE 'team' END,
  now() - make_interval(days => g % 30, mins => g % 1440)
FROM generate_series(1, 100) g;

-- ══ notification（db=notification；矩阵 notif-001/002/003）════════════════
\c notification
-- 邮件投递（notif-001/002：状态聚合 + 时间窗口）
CREATE TABLE email_deliveries (
  delivery_id varchar(64) PRIMARY KEY,
  source_service varchar(64) NOT NULL,
  org_id varchar(36),
  business_key varchar(64),
  template_code varchar(64) NOT NULL,
  recipient_email varchar(128) NOT NULL,
  status varchar(16) NOT NULL,
  attempt_count int DEFAULT 0,
  sent_at timestamptz,
  created_at timestamptz NOT NULL
);
INSERT INTO email_deliveries (delivery_id, source_service, org_id, business_key, template_code, recipient_email, status, attempt_count, sent_at, created_at)
SELECT 'dlv-' || g, CASE g % 3 WHEN 0 THEN 'bss-bill' WHEN 1 THEN 'iam' ELSE 'console' END,
  'org-' || (g % 20 + 1), 'biz-' || g, 'tpl-' || (g % 4 + 1),
  'user-' || (g % 20) || '@example.com',
  CASE g % 4 WHEN 0 THEN 'succeeded' WHEN 1 THEN 'failed' WHEN 2 THEN 'sending' ELSE 'pending' END,
  g % 3,
  CASE WHEN g % 4 = 0 THEN now() - make_interval(hours => g % 24) ELSE NULL END,
  now() - make_interval(days => g % 7, mins => g % 1440)
FROM generate_series(1, 200) g;

-- 投递尝试（notif-003 复杂：LEFT JOIN 的失败率归因）
CREATE TABLE email_delivery_attempts (
  attempt_id varchar(64) PRIMARY KEY,
  delivery_id varchar(64) NOT NULL,
  provider varchar(32) NOT NULL,
  status varchar(16) NOT NULL,
  error_message varchar(128),
  started_at timestamptz,
  finished_at timestamptz
);
INSERT INTO email_delivery_attempts (attempt_id, delivery_id, provider, status, error_message, started_at, finished_at)
SELECT 'att-' || g, 'dlv-' || (g % 200 + 1), CASE g % 2 WHEN 0 THEN 'smtp' ELSE 'sendgrid' END,
  CASE g % 3 WHEN 0 THEN 'succeeded' WHEN 1 THEN 'failed' ELSE 'sending' END,
  CASE WHEN g % 3 = 1 THEN '451 relay refused' ELSE NULL END,
  now() - make_interval(mins => g), now() - make_interval(mins => g - 1)
FROM generate_series(1, 260) g;

-- ══ ops-ticket（db=ops_ticket；矩阵 ticket-001/002/003）═══════════════════
\c ops_ticket
-- 工单来源（ticket-003 join 侧）
CREATE TABLE support_ticket_sources (
  id bigint PRIMARY KEY,
  name varchar(64) NOT NULL,
  app_token varchar(64),
  active_for_submit bool DEFAULT true,
  sync_enabled bool DEFAULT true
);
INSERT INTO support_ticket_sources (id, name, app_token, active_for_submit, sync_enabled)
SELECT g, '来源' || g, md5('src-' || g), true, true FROM generate_series(1, 3) g;

-- 工单（ticket-001/002/003：状态聚合 + 时间窗口 + join 来源）
CREATE TABLE support_tickets (
  id bigint PRIMARY KEY,
  source_id bigint NOT NULL,
  ticket_ref varchar(32) NOT NULL,
  user_id varchar(64),
  org_id varchar(36),
  title varchar(128) NOT NULL,
  category varchar(32),
  status varchar(16) NOT NULL,
  submitted_at timestamptz NOT NULL
);
INSERT INTO support_tickets (id, source_id, ticket_ref, user_id, org_id, title, category, status, submitted_at)
SELECT g, (g % 3) + 1, 'TK-' || g, 'user-' || (g % 30 + 1), 'org-' || (g % 20 + 1),
  '工单' || g, CASE g % 3 WHEN 0 THEN 'billing' WHEN 1 THEN 'technical' ELSE 'account' END,
  CASE g % 4 WHEN 0 THEN 'closed' WHEN 1 THEN 'processing' WHEN 2 THEN 'replied' ELSE 'submitted' END,
  now() - make_interval(days => g % 14, mins => g % 1440)
FROM generate_series(1, 160) g;
