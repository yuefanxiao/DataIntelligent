# 06 Research: 语义层存储与检索方向

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/7
Status: closed (2026-08-11)
Blocked by (open blockers): 0

Part of #1

## Question

语义层知识应该存哪里、如何检索进 Agent 上下文？

对比：PostgreSQL 原生存储（表 + JSONB / pgvector）、图数据库（Neo4j 等）、文件化语义模型（YAML / Markdown / MDL 文件）、以及 Metadata RAG 的检索方式（embedding vs 关键词 vs 结构化查询；有没有成熟的 Metadata RAG 做法）。

结合 Go 生态与自托管部署成本，给出推荐方向（给事实与取舍，不要求最终决策——最终决策是票据 07）。

## Answer

见 `docs/research/06-semantic-store-retrieval.md`（调研文件，含一手来源）与 `.scratch/data-intelligence-layer/issues/06-semantic-store-research.answer.md`（GitHub 评论正文）。

一行 gist：语义层是 MB 级元数据（与业务数据量无关）；推荐 PG 原生表 + JSONB 为运行时存储、YAML 文件为作者入口（自研 Go 编译/同步）、结构化 SQL + pg_trgm 关键词检索为主（embedding/pgvector 留作 Phase 2）、v1 不引入图数据库；Metadata RAG 成熟做法 = 混合检索（词法/结构化为主、向量为辅），Agent 侧以 MCP 工具包 SQL 为标准形态。
