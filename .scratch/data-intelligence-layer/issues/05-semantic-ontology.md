# 05 语义层本体模型：MDL vs 自研、v1 最小集合

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/6
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/6#issuecomment-5251572992

Part of #1

## Question

语义层本体模型长什么样？

- 实体/关系/指标如何表达：Wren MDL 风格 vs 自研 schema（YAML/JSON/DB 表）
- v1 最小集合：服务↔DB↔表↔列 + 简单指标 + 业务概念（如 payment_order → 支付订单，status 枚举含义）
- 术语需要 domain-modeling 钉死（Semantic Layer / Ontology / 指标 / 业务概念 的确切含义）

依赖：票据 09 的调研结果（Wren AI 的 MDL 做法）作为参考输入。

## Resolution（2026-08-11）

混合本体：UModel 图谱骨架（sets-and-links 承载服务↔库↔表↔列↔概念拓扑）+ OSI 语法形状（声明式 SQL 指标），自研补枚举取值语义与服务↔库映射。消费 = P2 工具发现为主 + P3 模糊检索 + light P5（公式可读 + dry-run），概念与指标双入口。v1 六类实体（Service/Database/Table/Column/Metric/BusinessConcept）+ 四种关系边（connects_to/contains/references/describes）+ 枚举挂列 + is_time；Database 粒度 = PG database；无指标走表/列原料路径，新指标 = 人工确认回写 YAML（指标沉淀）。

- 术语 → CONTEXT.md；架构决策 → docs/adr/0001-semantic-ontology-model.md；调研资产 → docs/research/05-agent-reasoning-consumption.md
- 下游：解封 07/08/12；输入 10（表达式可读+dry-run）、03（FQN 权限挂载点）、02（有界返回）、08（指标沉淀流程）
