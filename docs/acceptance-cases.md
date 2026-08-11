# v1 验收用例清单（spec §6.1/§6.2；issue #31）

用例 = `deploy/accept/cases.yaml`（工具调用 + 期望断言，官方 go-sdk 打网关
HTTP + stdio 双形态真实往返）；数据 = `deploy/accept/fixture.sql`（10 个持库
的确定性伪造数据，相对 `now()` 的时间偏移）；编排 = `deploy/accept/run.sh`
（PG 拉起 → fixture + provisioning → 语义同步 → 授权 → 双形态重放 →
判定三件套 → 报告留档）。本清单是矩阵的**人读视图**（机器视图 = cases.yaml）。

## 1. 主用例（§6.1）：昨天支付失败率为什么上涨

Agent 自由组合工具（不预设 SQL），5 步全流程：

| 步 | 工具 | 动作 | 断言 |
|---|---|---|---|
| 1 | search_entities | `"支付失败"` 双入口定位 | total=2（概念 + 指标都命中） |
| 2 | search_entities | `"支付失败"` type=metric 单入口 | total=1，hits[0].fqn=payment_failure_rate |
| 3 | get_metric_definition | 取口径 + 带时间参数 dry-run 展开 | time_applied=true，tables=[payment_orders]，dry_run_sql 全文精确断言 |
| 4 | execute_sql | 昨日趋势（近 7 日逐日失败率） | 7 行 + psql 对照 |
| 5 | execute_sql | 下钻归因（CTE + 多表 join + 窗口函数 + 时间窗口） | 8 行 + psql 对照 |

- **数据故事**（fixture.sql）：此前 13 天失败率 3/60 = 5%，昨日 43/100 =
  43% 激增，失败集中于昨日 channel=5（银行转账）——归因查询里该渠道
  failed 3 → 40（delta=37）一眼可见。
- **dry-run 断言用固定窗口**（2026-08-11T00:00:00Z → 08-12）：展开是纯
  函数，固定入参 = 确定性输出，精确断言展开 SQL 全文（口径/时间展开
  逻辑改动即用例失败，防止「展开错了但没测到」）。
- **趋势/归因 SQL 用相对时间窗口**（昨天 = `date_trunc('day', now()) -
  interval '1 day'`）：重放同日可复现；fixture 数据随 now() 前移，跨天
  重跑自动对齐。
- 判定三件套：步骤 4/5 走 psql_compare（(a)）；全部调用落执行记录并重放
  复现（(b)）；全程零未授权（(c)）。

## 2. 每服务用例矩阵（§6.2）：13 服务 × ≥2 简单 + ≥1 复杂

### 2.1 总表

| 服务 | 持库 | 简单用例 | 复杂用例 | 用例 id |
|---|---|---|---|---|
| bss-bill | bill | 账单时间窗口聚合 / 结算批次枚举聚合 | 结算扣费失败归因（CTE + LEFT JOIN） | bill-001/002/003 |
| bss-wallet | wallet | 支付单枚举过滤 / 流水 tx_type 聚合 | 退款归因（退款单 JOIN 支付单） | wallet-001/002/003 |
| bss-invoice | bss_invoice | 申请枚举聚合 / 申请时间窗口 | 开票链路（申请 LEFT JOIN 发票 LEFT JOIN 文件） | invoice-001/002/003 |
| bss-subscription | subscription | 定价模型×计量项聚合 / 30 日定价计数 | 档位策略（明细 JOIN 版本） | sub-001/002/003 |
| bss-promotion | promotion | 兑换记录枚举聚合 / 兑换码枚举聚合 | 兑换失败归因（记录 JOIN 码 JOIN 批次） | promo-001/002/003 |
| iam | iam | 组织枚举聚合 / 组织时间窗口 | 组织成员归因（成员 JOIN 组织） | iam-001/002/003 |
| iam-audit | iam_audit | 近 1 日事件类型聚合 / 导出枚举聚合 | 审计量趋势（子查询 + 窗口函数 lag） | audit-001/002/003 |
| console | console | 审批 execute_status 聚合 / 审批时间窗口 | 分组覆盖归因（成员 JOIN 分组） | console-001/002/003 |
| notification | notification | 投递枚举聚合 / 投递时间窗口 | 投递失败归因（投递 LEFT JOIN 尝试） | notif-001/002/003 |
| ops-ticket | ops_ticket | 工单枚举聚合 / 工单时间窗口 | 工单来源归因（工单 JOIN 来源） | ticket-001/002/003 |
| dashboard-backend | —（无持库） | 服务实体可达 / 服务实体描述 | 语义拓扑遍历（无 contains 边，如实返回起点） | dash-001/002/003 |
| ops-operation | —（无持库） | 服务实体可达 / 服务实体描述 | 语义拓扑遍历 | ops-001/002/003 |
| usage-collection | —（无持库） | 服务实体可达 / 服务实体描述 | 语义拓扑遍历 | usage-001/002/003 |

39 例 = 10 个持库服务 × 3（execute_sql）+ 3 个无持库服务 × 3（语义元数据）。

### 2.2 形态与断言

- 全部矩阵用例双形态覆盖（modes: http + stdio）；成功 SQL 用例全部
  `psql_compare: true`（三件套 (a) 与 psql 同库同 SQL 逐项一致）。
- 简单 = 单表 / 时间窗口过滤 / 枚举状态过滤 / 聚合；复杂 = 核心链路
  多表 join + 聚合 + 时间窗口/窗口函数。
- **校验层硬路径覆盖**（复杂用例的子集）：CTE 的 AST 分类与授权展开 =
  bill-003、main-001 步骤 5；子查询的表提取与授权展开 = audit-003、
  main-001 步骤 5；LEFT JOIN 三表提取 = invoice-003、promo-003、notif-003。

### 2.3 无持库服务的用例口径

dashboard-backend / ops-operation / usage-collection 是聚合网关/编排/采集
角色，无表可查（采集器只产出服务实体草稿）。矩阵以**语义元数据用例**覆盖：
服务实体在语义仓库可达（get_entity kind=service + 描述精确断言，语义内容
抽查）+ 拓扑如实（traverse_relations contains 边为空，只返回起点）。这
三类用例在验收环境与真实环境（语义同步后）行为一致，兼作采集回归的
「服务实体存在性」断言。

## 3. 负向/边界 5 例（§6.3，build 12 保留，neg-005 换域）

| 用例 | 断言 | build 14 变更 |
|---|---|---|
| neg-001a/b/c 未授权表拒绝 | ghost / 无表授权 / 非 public schema 引用 → permission_denied + not_granted / unknown_table | 不变 |
| neg-002 非 SELECT 拒绝 | DML/DDL/COPY/utility 六句 → invalid_request/non_select | 不变 |
| trunc-001/002 LIMIT 截断 | >500 默认上限 / >5000 硬上限 + truncated + psql 有界对照 | 不变 |
| conc-001/002 并发超限 | 同 key >2 / 进程级 >8 → rate_limited（不排队） | 不变 |
| neg-005 无指标原料路径 | 无现成指标 → 走表/列原料路径直查成功 | **换「无指标领域」fixture**：验收环境带语义数据后，「支付失败」已是指标；改为退款域（metrics.yaml 仅 payment_failure_rate 一个指标，search_entities(type=metric, 退款) 零命中）→ 直查 refund_orders 成功。隔离策略：不依赖全局空存储，依赖「退款域无指标」这一 fixture 事实（新增退款指标时需换域） |

## 4. fixture 数据契约

- **表名/列名与 samples/semantic/services/ 一致**（真实业务表名），值域与
  枚举 label 对齐（payment_orders.status 2=成功 4=失败、channel 2/4/5 等）——
  语义元数据与可查数据互相对应，用例兼作 golden 语料才有意义。
- **表建在 public schema**（与 demo orders/big_events 同构）：v1 校验层
  FQN 映射 = 服务.库.表，只解析未限定/public 引用（execute_sql.go
  resolve）；真实生产的 bss 域同名 schema 前缀由 provisioning 覆盖，是
  部署形态差异，pg_schema 元数据不参与验收执行。
- **确定性**：generate_series + CASE 表达式，行数可精确断言；所有时间列
  用 `date_trunc('day', now()) - make_interval(...)` 相对偏移，重放同日
  可复现；窗口查询统一 `[date_trunc('day', now()) - N day, date_trunc('day', now()))`
  半开区间（排除今日，跨天重跑窗口自动前移）。
- **授权**：run.sh 对 fixture 表逐表 `grant-add`（服务.库.表 FQN 与语义
  数据一致）；PG 侧由 provisioning 的 `GRANT SELECT ON ALL TABLES IN
  SCHEMA public` 一次性覆盖（fixture 先于 provisioning 执行）。
- **规模**：27 张新表（bill 3 / wallet 4 / bss_invoice 3 / subscription 3 /
  promotion 3 / iam 2 / iam_audit 2 / console 3 / notification 2 /
  ops_ticket 2），单表几十到几百行，全量验收分钟级完成。

## 5. golden 语料复用契约（09 采集回归）

用例 = 工具调用 + 期望断言，与数据存储无关，可被采集回归直接复用：

- **execute_sql 用例** = Agent 可执行查询的回归集：采集器/语义数据改动后
  重放，行数与 psql 对照即验证「语义描述没有骗人」；
- **语义工具用例**（search/get_entity/traverse/get_metric_definition）=
  检索与口径回归：命中数/FQN/展开 SQL 精确断言，语义内容改动即用例失败；
- **新增用例**（v1 后按实际效果迭代，不冻结）只改 cases.yaml；用例字段
  契约见文件头注释，harness（accept.go）不含业务断言逻辑。

## 6. 文档索引

- 编排与三件套：deploy/accept/README.md
- 数据与授权形态：deploy/accept/fixture.sql 头注释、deploy/provisioning/readonly-role.sql
- 决策记录：docs/adr/0011-acceptance-case-matrix.md
- 语义数据：samples/semantic/README.md
