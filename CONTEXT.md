# Enterprise Data Intelligence Layer（数据智能层）

面向生产 PostgreSQL（只读从库）的 MCP 数据网关：让开发人员的 Coding Agent 能安全查询生产数据，内置语义层（服务关系、schema 业务语义、指标）、权限与审计。本上下文描述语义层的领域语言。

## Language

**语义层 Semantic Layer**:
在数据库物理结构（表/列/外键）之上建立的业务语义模型——实体、关系、指标、业务概念；是网关向 Agent 暴露的知识面。
_Avoid_: 知识图谱（那是语义层的存储形态之一，不是并列的另一层）、Metadata RAG、GraphRAG

**本体 Ontology**:
语义层的内容结构——实体类型（服务/库/表/列/指标/业务概念）与它们之间的关系、约束。
_Avoid_: 知识图谱、Schema

**指标 Metric**:
口径唯一确定（定义表达式 + 聚合方式 + 过滤条件）的可计算数值；口径是权威的，任何 Agent 查到同一指标得到同一解释。表达式直接写 SQL，时间过滤不进入指标定义（是查询参数）。
_Avoid_: 度量（沿用 Decision Discussion 旧称）、KPI

**业务概念 Business Concept**:
业务语言中的一个名词（支付订单、订单状态），描述「业务里是什么」，对应到一个或多个表/列/指标；是 Agent 理解业务语义的入口，与表/列/指标之间是多对多关系（`describes`）。
_Avoid_: 术语表条目（OpenMetadata 式 glossary 是 v2 治理层，非 v1 形态）

**指标沉淀**:
把 Agent 探索出的有用查询口径，经人工确认后写回 YAML 并编译进本体、成为正式指标的过程——v1 新指标的唯一来源。
_Avoid_: 自进化（Agent 自动回写本体——长期愿景，本 effort 不做）

**关系边**:
本体中实体之间带类型的连接（`connects_to` 服务↔库、`contains` 库→表、`references` 表↔表 join 条件、`describes` 概念↔表/列/指标）；双向可遍历，是 Agent 图式推理的通道。
