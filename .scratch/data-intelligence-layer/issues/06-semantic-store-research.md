# 06 Research: 语义层存储与检索方向

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/7
Status: open
Blocked by (open blockers): 0

Part of #1

## Question

语义层知识应该存哪里、如何检索进 Agent 上下文？

对比：PostgreSQL 原生存储（表 + JSONB / pgvector）、图数据库（Neo4j 等）、文件化语义模型（YAML / Markdown / MDL 文件）、以及 Metadata RAG 的检索方式（embedding vs 关键词 vs 结构化查询；有没有成熟的 Metadata RAG 做法）。

结合 Go 生态与自托管部署成本，给出推荐方向（给事实与取舍，不要求最终决策——最终决策是票据 07）。

