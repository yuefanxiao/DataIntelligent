# 05 语义层本体模型：MDL vs 自研、v1 最小集合

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/6
Status: open
Blocked by (open blockers): 1

Part of #1

## Question

语义层本体模型长什么样？

- 实体/关系/指标如何表达：Wren MDL 风格 vs 自研 schema（YAML/JSON/DB 表）
- v1 最小集合：服务↔DB↔表↔列 + 简单指标 + 业务概念（如 payment_order → 支付订单，status 枚举含义）
- 术语需要 domain-modeling 钉死（Semantic Layer / Ontology / 指标 / 业务概念 的确切含义）

依赖：票据 09 的调研结果（Wren AI 的 MDL 做法）作为参考输入。

