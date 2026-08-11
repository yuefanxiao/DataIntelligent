# 06 Research: 语义层存储与检索方向

> wayfinder 票据 06（GitHub issue 7，label `wayfinder:research`），Part of #1。
> 本文件是调研事实与取舍，**不是最终决策**；决策归票据 07（GitHub issue 8，grilling「07 语义层存储与检索决策」）。
> 调研方式：一手来源（官方文档 / 官方仓库 / 论文），每条结论标注来源。

## 0. 问题重述

语义层知识（服务→DB→表→列→指标→业务概念 的本体/图谱形态）应该存哪里、如何检索进 Agent 上下文？

对比对象：
1. PostgreSQL 原生存储（表 + JSONB / pgvector）
2. 图数据库（Neo4j 等）
3. 文件化语义模型（YAML / Markdown / MDL 文件）
4. Metadata RAG 检索方式（embedding vs 关键词 vs 结构化查询；成熟做法）

环境约束（来自地图 issue #1）：Go 生态、自托管、几十个服务共用一个 PG（一主两从，v1 只读从库）、数据量千万~亿级、消费方是 Coding Agent 经 MCP 查询。

**关键前提事实**：语义层存的是**元数据**（表/列/指标/关系/业务描述），几十个服务的规模下是几 MB~几十 MB 的数据（列数约 10^4~10^5），与业务数据量（千万~亿行）无关。业务数据量影响的是网关 SQL 执行（在从库上跑），不影响语义层的存储选型。语义层量级不是任何存储方案的瓶颈——瓶颈在**维护性、检索精度、运维成本**。

## 1. 存储方案对比（事实 + 取舍）

### 1.1 PostgreSQL 原生：表 + JSONB + pgvector

事实：
- **普通表 + 外键**：关系型建模，事务/约束/索引齐全，Go 生态 pgx 是事实标准驱动，零新组件。[PostgreSQL 官方文档](https://www.postgresql.org/docs/current/)
- **JSONB**：PostgreSQL 内置类型，支持 GIN 索引（`jsonb_ops` / `jsonb_path_ops`）与 jsonpath 查询，适合"列级/实体级灵活元数据"（描述、标签、属性字典），无需预先定死 schema。[JSON 类型官方文档](https://www.postgresql.org/docs/current/datatype-json.html)
- **pgvector**：PostgreSQL 官方生态的向量扩展（社区项目 pgvector/pgvector，非核心贡献，但已是事实标准），Postgres 13+ 可用；HNSW 索引（v0.5.0 起，免训练、无需重建）、IVFFlat；支持与 JSONB 过滤条件结合的近似检索（v0.8.0 起 HNSW 迭代扫描）。[pgvector 官方仓库](https://github.com/pgvector/pgvector)
- **Go 驱动**：pgvector-go 是官方 Go 客户端（MIT），支持 pgx / pg / GORM / Ent / Bun / sqlx，活跃维护。[pgvector-go 官方仓库](https://github.com/pgvector/pgvector-go)
- **关键词检索**：原生 `tsvector` 全文检索 + `pg_trgm` 三字母相似度（GIN/GiST 索引），适合表名/列名/指标名的模糊匹配。[pg_trgm 官方文档](https://www.postgresql.org/docs/current/pgtrgm.html) [全文检索官方文档](https://www.postgresql.org/docs/current/textsearch.html)
- **浅层图谱遍历**：`WITH RECURSIVE` 递归 CTE 是 SQL 标准能力，可做 2~3 跳的 lineage/关联查询。[递归查询官方文档](https://www.postgresql.org/docs/current/queries-with.html)

取舍：
- 优点：零新增基础设施（复用一主两从集群，从库可直接读）、备份/监控/高可用沿用现有体系、事务一致、SQL 即检索语言（网关本来就是 SQL 驱动的）、Go 生态最成熟。
- 缺点：多跳图遍历（>2~3 跳、变长路径、路径模式匹配）用 SQL 表达笨拙，需自写递归或枚举；JSONB 灵活但不强制 schema（需靠应用层约定）；pgvector 是第三方扩展（仍是一等公民，需要 DBA 安装/升级扩展）。

### 1.2 图数据库

事实（逐个）：
- **Neo4j**（市场最主流）：
  - 自托管 Community Edition 为 **GPLv3**、**单实例**：无集群/复制/HA，在线备份工具是 Enterprise 功能。[Neo4j 官方授权页](https://neo4j.com/licensing/) [Neo4j 官方授权 FAQ](https://neo4j.com/licensing/faq/)
  - 官方 Go 驱动存在（v5/v6，Bolt 协议，Apache 2.0 客户端），[官方 Go 手册](https://neo4j.com/docs/go-manual/current/) [github.com/neo4j/neo4j-go-driver](https://github.com/neo4j/neo4j-go-driver)
  - 部署成本：新增一个 JVM 服务（独立进程、独立备份/监控/升级节奏）。
- **Apache AGE**：PostgreSQL 扩展，在 PG 内提供 openCypher 图查询，与 SQL 混写；Apache TLP（2022-05 毕业）、Apache 2.0；Go 驱动存在。[Apache AGE 官网](https://age.apache.org/) [github.com/apache/age](https://github.com/apache/age)
  - 风险：2024-10 Bitnine 解散主要开发团队，开发明显放缓；1.5.0（2024-01）后 PG17 支持到 2025 年初仍在进行中。Azure 有官方支持（Azure Database for PostgreSQL Flexible Server），说明有背书但社区活力需观察。
- **Dgraph**：**Go 编写的**分布式图数据库，v25 活跃维护，仓库主许可证 Apache-2.0（历史版本曾 AGPLv3）；查询语言是 GraphQL+/DQL 而非 Cypher；部署=分布式集群（独立运维负担）。
- **Kuzu**：嵌入式图数据库，官方 Go 绑定 go-kuzu 曾存在，但**上游仓库已于 2025-10-10 归档（只读）**，社区 fork（RyuGraph）接续——greenfield 不建议押注。[github.com/kuzudb/kuzu](https://github.com/kuzudb/kuzu)
- 另注：DataHub 的架构是 MySQL + Elasticsearch + **Neo4j**（lineage 图）+ GraphQL API——图数据库在企业元数据平台里常用于 lineage 存储，检索走搜索引擎。

取舍：
- 优点：变长路径/多跳遍历/路径模式匹配（impact analysis、血缘、上下游影响）是原生能力；图模型贴合本体/图谱心智。
- 缺点：**新增一个独立基础设施**（自托管成本：进程、备份、监控、升级、License）；元数据量级（MB 级）完全用不满图数据库的能力；Go 驱动成熟度参差（Neo4j 官方驱动好，AGE/Dgraph 一般）；Cypher 技能与 SQL 栈割裂。

### 1.3 文件化语义模型（YAML / Markdown / MDL 文件）

事实：
- **Wren AI MDL（Modeling Definition Language）**：以 YAML 源文件描述模型/关系/计算/视图，编译成 `mdl.json` 清单；Wren Engine（Rust）执行语义解析→SQL 生成。[MDL 官方文档](https://docs.getwren.ai/oss/engine/concept/what_is_mdl) [Wren 项目结构](https://docs.getwren.ai/oss/reference/architecture)
  - 本项目系（WrenAI OSS）本身就是"文件化语义模型 + Metadata RAG"的完整参考实现。
- **dbt Semantic Layer**：语义模型与指标以 **YAML** 定义（`semantic_models.yml` / `metrics.yml` 旧规范；dbt Core 1.12+ 新规范为 model 内嵌 `semantic_model:` 块），MetricFlow 编译为 SQL。[dbt 语义层配置官方文档](https://docs.getdbt.com/reference/semantic-layer-reference) [semantic-model-properties](https://docs.getdbt.com/reference/semantic-model-properties) [metrics-overview](https://docs.getdbt.com/docs/build/metrics-overview)
  - 许可现状：MetricFlow 已于 2025-10 Coalesce **开源（Apache 2.0）**，并入 Open Semantic Interchange 计划；但**服务层与 GraphQL/JDBC 语义层 API 仍是 dbt Cloud 闭源付费功能**，纯自托管只有 CLI 本地查询。[dbt 语义层架构文档](https://docs.getdbt.com/docs/use-dbt-semantic-layer/sl-architecture)
- **Cube**：语义层，数据模型以 YAML/JS/Python 文件定义（`/model`），可完全自托管（Apache 2.0 backend），Cube Store（Rust）做预聚合缓存；面向 BI 场景（SQL/REST/GraphQL 端点），比"纯元数据存储"重。[cube.dev](https://cube.dev)
- **纯 Markdown 文件**：人读友好，但无结构约束，检索只能全文搜索，不适合作为唯一事实源（可做辅助文档）。

取舍：
- 优点：**人可编辑、可 review、可版本控制**（语义层本质是"企业口径的代码化表达"，与代码同仓/同评审流最自然）；与 dbt/Wren 生态对齐（未来可换引擎）；编译期校验（引用完整性、指标表达式合法性）早于运行时暴露错误。
- 缺点：文件本身不可查询——**必须编译/同步到某个运行时**（Wren 编到向量库 + Engine，dbt 编到 manifest + MetricFlow 引擎）；若不自动同步会产生"文件与库漂移"；Wren 服务端与 dbt 服务层是闭源/非 Go（Rust/Python），自建 Go 实现需要自己写解析与编译。

### 1.4 对比表

| 维度 | PG 原生（表+JSONB+pgvector） | 图数据库（Neo4j 等） | 文件化语义模型（YAML/MDL） |
|---|---|---|---|
| 新增基础设施 | 无（复用集群+扩展） | Neo4j: 新 JVM 服务；Dgraph: 分布式集群；Kuzu: 已归档 | 无（需编译/同步目标，可用 PG 或内存） |
| Go 生态 | pgx + pgvector-go（官方，最成熟） | Neo4j 官方 Go 驱动可；AGE/Dgraph 一般 | Wren/dbt 服务层非 Go；格式本身与语言无关 |
| 运维/备份 | 沿用现有 PG 体系 | 独立备份/监控/升级；Neo4j CE 无 HA | 文件=Git；运行时=PG 或自研 |
| 多跳图遍历 | 递归 CTE 勉强（≤2~3 跳） | 原生强项 | 依赖编译后的运行时 |
| 检索灵活性 | SQL + FTS + 向量全有 | Cypher + 全文（弱） | 取决于编译目标 |
| License 风险 | pgvector BSD（宽松） | Neo4j CE GPLv3；AGE Apache 2.0 | MDL/dbt 规范开源；dbt 服务层闭源 |
| 维护心智 | 表结构设计 + 约定 | Cypher 技能 + 第二套系统 | 文件规范 + 编译/同步管线 |

## 2. Metadata RAG：检索方式与成熟做法

### 2.1 三种检索方式的事实对比

| 方式 | 原理 | 强项 | 弱项 | 成本 |
|---|---|---|---|---|
| **结构化查询**（SQL/API 精确检索） | 按名称/类型/关系精确匹配 | 确定性强、零幻觉、快（ms 级）、可排序过滤 | 不处理同义/模糊描述 | 最低（纯 SQL） |
| **关键词检索**（FTS / pg_trgm / BM25） | 字面匹配 + 相似度 | 精确标识符（表名/列名）召回最好；无需 embedding 基建 | 语义同义（"支付失败率" vs "failure_rate"）召回差 | 低（PG 原生） |
| **Embedding 向量检索**（pgvector 等） | 语义相似度 | 自然语言描述 ↔ 业务概念匹配 | 精确标识符召回弱（常拼不对/泛化过度）；需 embedding 模型服务；结果不可解释 | 中-高（embedding 模型 + 索引；API 调用有数据出境问题，自托管要新基建） |

要点：**学术与工业界的共识是"混合"**——精确/词法路径负责"名实对应"，向量路径负责"语义联想"，且**对 schema 链接（schema linking）而言词法（BM25/关键词）往往强于向量**。

### 2.2 成熟 Metadata RAG 做法（一手来源）

- **Wren AI 的 "Metadata RAG"**（命名出处）：
  - 流程：MDL 重写为 DDL → 语义上下文（schema + 元数据 + 语义/关系）存入向量库 → 按用户问题检索相关片段 → 组装 prompt → LLM 生成 SQL → Engine 校验执行性，不对则重新生成。[Wren AI Service 官方文档](https://docs.getwren.ai/oss/concept/wren_ai_service)
  - 关键设计：**只把元数据（schema/文档/示例查询）发给 LLM，永远不发数据库内容**；按需只取相关模型的片段，避免 token 溢出。[Wren 官方文档](https://docs.getwren.ai/oss/concept/wren_ai_service)
  - 存储：Wren 项目把编译后的 MDL 与语义检索索引（LanceDB）放在项目目录，说明其事实存储=**文件为源 + 向量库为检索面**。[Wren 项目结构](https://docs.getwren.ai/oss/reference/architecture)
- **Vanna**（文本转 SQL RAG 框架）：训练/索引 DDL、文档与历史问题-SQL 对到向量库，查询时相似度检索（"Agentic Retrieval"）；支持多种向量后端（ChromaDB、pgvector 等）。[github.com/vanna-ai/vanna](https://github.com/vanna-ai/vanna) [vanna.ai](https://vanna.ai)
- **dbt 的路线是"结构化工具优先"**：官方 MCP server（dbt-mcp，Apache 2.0）把语义层暴露为**精确的工具调用**：`list_metrics`、`get_dimensions`、`get_entities`、`query_metrics`——即 Agent 通过结构化 API 拿精确元数据，而非 embedding 检索。[github.com/dbt-labs/dbt-mcp](https://github.com/dbt-labs/dbt-mcp)
- **企业元数据平台（DataHub / OpenMetadata）**：成熟企业实践=**关系库存元数据 + 搜索引擎（Elasticsearch/OpenSearch）做检索面 + REST/GraphQL API**；DataHub 还额外用 Neo4j 存 lineage 图。词汇表（glossary terms）/标签/域是它们承载"业务语义"的机制。**注意：这些平台的主流检索是关键词/结构化，embedding 不是核心**。[OpenMetadata 架构文档](https://docs.openmetadata.io) [DataHub](https://datahubproject.io)
- **学术/基准（text-to-SQL 上下文检索）**：
  - SchemaRAG（PACMMOD 2026, DOI 10.1145/3786696）：用 **BM25S（词法）采样 + schema 感知 embedding** 做 schema linking，在 Spider/BIRD 等基准上超过 4 个 SOTA。[github.com/chelsea2002/SchemaRAG](https://github.com/chelsea2002/SchemaRAG)
  - Rethinking Schema Linking（arXiv 2510.14296）：table-first + column-first 双向检索（结构化+词法+向量混合），在 BIRD/Spider 上验证。[arXiv 2510.14296](https://arxiv.org/abs/2510.14296)
  - 共同结论：**精确标识符靠词法/结构化，描述性语义靠向量，成熟系统两者都要，纯向量召回不足**。

### 2.3 对本项目的含义

- 本项目语义层实体**名称是精确标识符**（service/table/column/metric 名），用户问题里出现的是自然语言业务词——两种检索都缺一不可，但**v1 应以结构化 + 关键词为主**（确定、零成本、无数据出境），embedding 作为后续增强。
- 检索目标是把"相关上下文"装进 Agent 的 prompt/上下文：全量 dump（数十服务 × 数千表 × 数万列）远超 context，必须检索切片——这正是 Wren 做 Metadata RAG 的原因（[官方文档](https://docs.getwren.ai/oss/concept/wren_ai_service)）。
- MCP 侧的成熟形态（dbt-mcp）说明：把检索结果包成 **MCP 工具**（`search_concepts` / `get_table_schema` / `get_metric_definition`）是 Agent 消费语义层的标准姿势，工具背后可以是 SQL。

## 3. 结合 Go 生态与自托管部署成本的推荐方向

> 这是方向性建议（事实 + 取舍），**最终决策在票据 07（GitHub issue 8）**。

**方向 A（推荐）：PG 为运行时存储，文件为作者入口，检索以结构化+关键词为主**
1. **作者入口 = 文件化语义模型**（YAML，MDL 风格或自定规范）：人编辑、Git review、编译期校验；与 dbt/Wren 生态对齐，未来可换引擎。参考 Wren 的"源文件 → 编译产物"结构，但**自研 Go 编译器/同步器**（不依赖闭源服务层）。
2. **运行时存储 = 现有 PG 集群**：关系表（entity 分层：service/table/column/metric/concept + 关系边表）+ JSONB 存灵活元数据（描述/标签/属性字典）。零新增基础设施，复用一主两从（从库只读）、备份、监控；Go 侧 pgx + 可直接映射。
3. **检索 = 结构化 SQL 优先 + pg_trgm 关键词 + （可选 Phase 2）pgvector**：MCP 工具包住 SQL；embedding 仅在需要"自然语言↔业务概念"语义匹配时再加（届时需要决策 embedding 模型来源：外购 API 有数据出境问题 vs 自托管 Ollama/bge 类服务新增基建）。
4. **图遍历：v1 不引入图数据库**。关系边表 + `WITH RECURSIVE` 覆盖服务→DB→表→列→指标→概念的浅层遍历（这也是 v1 主查询形态）；若未来 lineage/impact analysis 变成一等查询需求，再评估 Apache AGE（留在 PG 内，注意开发放缓与版本兼容风险）vs Neo4j CE（单机 GPLv3，新增 JVM 服务与运维、无 HA）。

**方向 B（备选/混合）：PG + Apache AGE**——如果图查询需求在 v1 就明确（如跨多跳血缘），AGE 是"不新增服务"的折中，但需承担扩展开发节奏风险。

**不建议 v1**：Neo4j CE（新增服务、GPLv3、无 HA，元数据量级用不满）、Dgraph（分布式集群过重）、Kuzu（已归档）、dbt 服务层/Cube（闭源或 BI 向过重）、纯 embedding 检索（精确标识符召回弱 + embedding 基建/数据出境）。

**规模验证**：元数据 MB 级，任何方案都无性能问题；pgvector 若全量 embedding 数万列 × 1000 维 ≈ 数百 MB，仍在 PG 舒适区。真正的规模瓶颈是业务数据查询（从库执行），与语义层存储无关。

## 4. 给票据 07 的决策要点（速查）

1. 存储选型独立于业务数据量（元数据 MB 级）。
2. PG 原生 = 零新基建、Go 生态最成熟、SQL 即检索；代价是复杂图遍历需自写。
3. 文件化语义模型是"企业口径代码化"的成熟形态（Wren MDL / dbt YAML），但必须编译/同步到可查询运行时；Wren/dbt 服务层闭源或非 Go，自研 Go 管线是自托管的自然选择。
4. 图数据库只有"多跳遍历为一等需求"时才值得新增服务；届时 AGE（PG 内）与 Neo4j CE（新服务，GPLv3，无 HA）二选一。
5. Metadata RAG 成熟做法 = 混合检索：结构化/词法为主、向量为辅（Wren、Vanna、SchemaRAG、dbt-mcp 一致）；v1 可完全跳过 embedding。
6. 检索进 Agent 的标准形态：MCP 工具（包 SQL 结构化查询），参考 dbt-mcp 的 list_metrics/get_dimensions 模式。
7. 自托管成本排序：PG 原生 ≈ 文件化（0 新服务）< AGE（0 新服务但有扩展风险）< Neo4j CE（1 新 JVM 服务 + GPLv3 + 无 HA）< Dgraph（分布式集群）。

## 5. 来源

- PostgreSQL 官方文档：https://www.postgresql.org/docs/current/ （JSON: datatype-json.html；FTS: textsearch.html；pg_trgm: pgtrgm.html；递归: queries-with.html）
- pgvector 官方仓库：https://github.com/pgvector/pgvector （HNSW/IVFFlat/混合检索/迭代扫描）
- pgvector-go 官方仓库：https://github.com/pgvector/pgvector-go
- Neo4j 授权页：https://neo4j.com/licensing/ ；Go 手册：https://neo4j.com/docs/go-manual/current/ ；驱动：https://github.com/neo4j/neo4j-go-driver
- Apache AGE：https://age.apache.org/ ；https://github.com/apache/age （含 #2150 开发状态讨论）
- Dgraph：https://github.com/dgraph-io/dgraph （v25，Go，Apache-2.0）
- Kuzu（已归档）：https://github.com/kuzudb/kuzu ；Go 绑定：https://github.com/kuzudb/go-kuzu
- Wren AI 官方文档：MDL https://docs.getwren.ai/oss/engine/concept/what_is_mdl ；Wren AI Service（Metadata RAG 流程）https://docs.getwren.ai/oss/concept/wren_ai_service ；项目结构 https://docs.getwren.ai/oss/reference/architecture
- dbt 语义层：配置 https://docs.getdbt.com/reference/semantic-layer-reference ；架构（MetricFlow 开源/服务层闭源）https://docs.getdbt.com/docs/use-dbt-semantic-layer/sl-architecture ；dbt-mcp https://github.com/dbt-labs/dbt-mcp
- Cube：https://cube.dev
- Vanna：https://github.com/vanna-ai/vanna ；https://vanna.ai
- OpenMetadata：https://docs.openmetadata.io ；DataHub：https://datahubproject.io
- SchemaRAG 论文与代码：https://github.com/chelsea2002/SchemaRAG （PACMMOD 2026, DOI 10.1145/3786696）
- Rethinking Schema Linking：https://arxiv.org/abs/2510.14296
