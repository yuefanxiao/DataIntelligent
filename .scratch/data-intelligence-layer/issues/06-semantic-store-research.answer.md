## 06 Research 结论：语义层存储与检索方向

> 调研文件（含全部一手来源）：`docs/research/06-semantic-store-retrieval.md`（分支 `research/semantic-store`）
> 本结论**只是方向与取舍，最终决策归票据 07（本仓库 issue #8）**。

### 一、存储方案对比（事实）

**1. PostgreSQL 原生（表 + JSONB + pgvector）**
- 事实：JSONB 内置 + GIN 索引 + jsonpath，适合灵活元数据；pgvector（PG 13+，HNSW 免训练、支持 JSONB 过滤的近似检索）是事实标准向量扩展；官方 Go 客户端 pgvector-go（MIT，支持 pgx/GORM/Ent 等）活跃维护；原生 tsvector + pg_trgm 可做关键词/模糊检索；`WITH RECURSIVE` 可做浅层图遍历。
- 取舍：零新增基础设施（复用一主两从）、事务/备份/监控沿用现有体系、SQL 即检索语言；但 >2~3 跳图遍历用 SQL 表达笨拙；JSONB 不强制 schema，需应用层约定。
- 适用：元数据是关系型主体 + 少量灵活字段 + 可选向量检索的绝大多数场景。

**2. 图数据库（Neo4j 等）**
- Neo4j：Community Edition = GPLv3、**单实例无集群/HA/在线备份**（企业功能收费）；官方 Go 驱动（v5/v6）可用；新增一个 JVM 服务 = 独立备份/监控/升级成本。
- Apache AGE：PG 扩展，PG 内跑 openCypher，Apache 2.0、TLP 项目；但 2024-10 主要开发团队解散后节奏明显放缓（PG17 支持到 2025 初仍未合入 1.5.0 主线），有工程风险。
- Dgraph：Go 编写、v25 活跃维护（Apache-2.0），但分布式集群部署过重，DQL 非 Cypher。
- Kuzu：**上游 2025-10 已归档（只读）**，greenfield 不要押注。
- 取舍：多跳/变长路径/impact analysis 是原生强项；代价是新增第二套基础设施 + 运维 + License + Cypher 技能栈；元数据 MB 级量级完全用不满图数据库。
- 适用：lineage/血缘、多跳影响分析成为一等查询需求之后。

**3. 文件化语义模型（YAML / MDL 文件）**
- Wren AI MDL：YAML 源文件 → 编译 `mdl.json` → Wren Engine（Rust）执行；Wren 项目 = 文件为源 + 检索索引（LanceDB 向量库）为检索面。Wren/dbt 的**服务层/API 是闭源或非 Go**（dbt 服务层 + GraphQL/JDBC 仍需 dbt Cloud 订阅；MetricFlow 已于 2025-10 Apache 2.0 开源）。
- dbt 语义层：语义模型/指标 YAML 定义（旧规范 `semantic_models.yml`/`metrics.yml`，dbt Core 1.12+ 新规范 model 内嵌），是"指标口径代码化"的业界标准形态。
- Cube：自托管 Apache 2.0，YAML/JS/Python 模型文件，偏 BI（预聚合/Cube Store），比纯元数据存储重。
- 取舍：人可编辑/review/版本控制、编译期校验、与 dbt/Wren 生态对齐；但**文件本身不可查询，必须编译/同步到可查询运行时**，否则产生文件-库漂移。
- 适用：作为"企业口径"的**作者入口（source of truth）**，而非唯一存储。

**规模事实**：语义层是元数据（几十服务 → 约 10^4~10^5 列，MB 级），与业务数据量（千万~亿行）无关——任何方案都无性能问题，瓶颈在维护性/检索精度/运维成本，不在存储容量。

### 二、Metadata RAG：现状与做法

- 三种检索方式：**结构化查询**（SQL/API，精确、确定性、零幻觉、零成本）、**关键词**（FTS/pg_trgm/BM25，精确标识符召回最好、无需 embedding 基建）、**embedding 向量**（自然语言↔业务概念语义匹配，但精确标识符召回弱、需 embedding 模型服务、有数据出境/自托管问题）。**成熟做法 = 混合，且 schema 链接场景词法/结构化通常强于向量。**
- 业界实证（一手来源）：
  - Wren AI "Metadata RAG"：MDL 重写为 DDL → 元数据上下文入向量库 → 按问题检索片段 → prompt → 生成 SQL → 校验循环；**只发元数据、永不发数据内容**。
  - Vanna：DDL/文档/历史 Q&A 入向量库做相似度检索（Agentic Retrieval），多向量后端（ChromaDB、pgvector 等）。
  - dbt 官方 MCP server（dbt-mcp）：语义层暴露为**精确结构化工具**（`list_metrics`/`get_dimensions`/`get_entities`/`query_metrics`）——结构化优先的 Agent 消费模式。
  - 企业元数据平台（OpenMetadata/DataHub）：关系库存元数据 + Elasticsearch/OpenSearch 检索面 + REST/GraphQL API（DataHub 另用 Neo4j 存 lineage）——关键词/结构化为主，embedding 非核心。
  - 学术：SchemaRAG（PACMMOD 2026）用 BM25S 词法采样 + schema 感知 embedding；Rethinking Schema Linking（arXiv 2510.14296）table-first+column-first 双向混合检索——均验证混合检索、词法为主。
- 对本项目含义：全量 dump（数万列）远超 Agent context，必须检索切片；**MCP 工具包 SQL 结构化查询是把语义层送进 Agent 的标准形态**（参考 dbt-mcp）。

### 三、推荐方向（方向，非决策）

**方向 A（推荐）：PG 为运行时存储 + 文件为作者入口 + 结构化/关键词检索为主**

1. **作者入口 = 文件化语义模型**（YAML，MDL 风格或自定规范）：Git review、编译期校验、与 dbt/Wren 生态对齐；**自研 Go 解析/编译/同步器**（不依赖闭源服务层）。
2. **运行时 = 现有 PG 集群**：关系表（entity 分层 service/table/column/metric/concept + 关系边表）+ JSONB 存灵活元数据；零新增基础设施，复用一主两从/备份/监控；Go 侧 pgx + 直接映射。
3. **检索 = 结构化 SQL 优先 + pg_trgm 关键词；pgvector + embedding 作为 Phase 2 可选**（届时需决策 embedding 模型来源：外购 API 数据出境 vs 自托管 Ollama/bge 新增基建）。
4. **图数据库 v1 不引入**：关系边表 + `WITH RECURSIVE` 覆盖浅层遍历；未来若 lineage/impact analysis 成为一等需求，再评估 Apache AGE（PG 内，注意开发放缓）vs Neo4j CE（新 JVM 服务、GPLv3、无 HA）。

**不建议 v1**：Neo4j CE（新服务/GPLv3/无 HA，量级用不满）、Dgraph（分布式过重）、Kuzu（已归档）、dbt 服务层/Cube（闭源或 BI 过重）、纯 embedding 检索（精确标识符召回弱 + embedding 基建/数据出境）。

**自托管成本排序**：PG 原生 ≈ 文件化（0 新服务）< AGE（0 新服务，有扩展风险）< Neo4j CE（1 新 JVM 服务）< Dgraph（分布式集群）。

### 四、给票据 07 的决策输入（速查）

1. 存储选型独立于业务数据量（元数据 MB 级，任何方案无性能问题）。
2. PG 原生 = 零新基建、Go 生态最成熟、SQL 即检索；代价是复杂图遍历自写。
3. 文件化语义模型是"企业口径代码化"成熟形态（Wren MDL / dbt YAML），必须编译到可查询运行时；Wren/dbt 服务层闭源或非 Go → 自研 Go 管线是自托管的自然选择。
4. 图数据库仅在"多跳遍历为一等需求"时值得新增服务。
5. Metadata RAG 成熟做法 = 混合检索（结构化/词法为主、向量为辅）；v1 可完全跳过 embedding。
6. 语义层进 Agent 的标准形态 = MCP 工具包 SQL（参考 dbt-mcp 模式）。
