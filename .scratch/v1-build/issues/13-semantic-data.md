# v1 build 13 — 全服务语义数据 + 主用例指标沉淀

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/30
Status: closed（PR #44 合入 main，2026-08-12 关闭，COMPLETED）

## 来源

docs/spec.md §6.1/§6.2；ADR-0007；issue #15

## What to build

全服务语义数据落地：13 个后端服务（bss×5、iam×2、console-backend、notification、ops×2、usage-collection、dashboard-backend）结构 YAML 全量（09 采集器产出 + 人工 review 合入语义仓库 + 07 管线同步进运行时，10 个持库全覆盖）；语义内容（描述/业务概念/枚举含义/指标口径）Agent 起草 + 服务负责人人工确认后回写（US-15/16，纯结构变更批量确认）；主用例指标「支付失败率」口径沉淀（表达式 + 聚合 + 过滤，machine-readable，dry-run 展开验证正确）。

## Acceptance criteria

- [x] 13 服务结构 YAML 全量落地并同步进运行时（10 个持库全覆盖）
- [x] 语义描述/概念/枚举经人工确认回写（服务负责人审查路径走通）
- [x] 主用例指标「支付失败率」口径定义 machine-readable，dry-run 展开正确
- [x] 全服务数据可经 08 工具抽查验证（search / get_entity / traverse_relations）

## Blocked by

- #T08 — 语义工具五件套
- #T09 — 采集器 CLI

## 交付摘要

1. **samples/semantic/services/**：13 服务结构 YAML 全量（dgw-collect 采集真实
   ~/cloud/neo-cloud 迁移：90 表 / 1254 列 / 284 枚举 / 30 引用边），3 个无持库
   服务（ops-operation / dashboard-backend / usage-collection）产出服务实体草稿；
   语义回写：服务/库/表/关键列描述、284 枚举中文含义、38 is_time 时间轴。
2. **metrics.yaml**：payment_failure_rate = `COUNT(*) FILTER (WHERE status = 4)::numeric
   / NULLIF(COUNT(*), 0)`（status=4=FAILED 与 neo-cloud PaymentOrderStatus proto 一致），
   ratio，tables=[bss-wallet.wallet.payment_orders]；dry-run 按 created_at 半开区间
   展开验证正确。
3. **concepts.yaml**：16 个业务概念（支付单/支付失败/退款/钱包/账单/结算/组织/工单/
   审批/模型定价…），「支付失败」双入口检索命中概念+指标。
4. **采集器扩展**（数据可持续前提）：manifest db 可选（无库服务只产服务实体草稿）、
   MergeSemantics（采集重跑按 FQN 保留已确认语义；KnownFields 严格解析防静默丢字段；
   列类型变化 is_time 随结构丢弃）、cmd 无库打印/calibrate 守卫。
5. **回归闸门**：internal/semantic/semantic_data_test.go（13 服务/10 持库覆盖断言 +
   08 工具抽查 + 指标 dry-run + 语义内容抽查）。
6. **文档**：ADR-0007 补记（语义保留合并/无库服务）、collector-workflow.md、verify.sh
   幂等收敛适配（两场景都验证 commit/revert 链路）、samples/semantic/README.md。

## 验证

- 全量测试 14 包全绿；verify.sh 全链路（bootstrap → 采集幂等收敛 → commit/push →
  同步 → revert 回滚）
- 真实网关 e2e：search_entities(支付失败) 命中概念+指标；get_metric_definition 带
  昨日窗口 dry-run 展开 SQL 正确；get_entity(payment_orders.status) 返回 7 枚举含义
- 运行时同步：实体 1384 / 边 1405 / 枚举 284
- 采集重跑幂等收敛：重采后语义全保留（46 描述/38 is_time/284 label），与种子逐字节一致
- 已知：bills.payment_channel GORM 模型列不在迁移结构 = ADR-0007 记载的手工 DDL
  先例（error 级门禁发现，草稿照写、交人确认）
