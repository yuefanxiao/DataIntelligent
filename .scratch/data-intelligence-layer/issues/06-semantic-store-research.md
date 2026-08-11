# 06 Research: 语义层存储与检索方向

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/7
Status: resolved
Blocked by (open blockers): 0

Part of #1

## Question

语义层知识应该存哪里、如何检索进 Agent 上下文？

对比：PostgreSQL 原生存储（表 + JSONB / pgvector）、图数据库（Neo4j 等）、文件化语义模型（YAML / Markdown / MDL 文件）、以及 Metadata RAG 的检索方式（embedding vs 关键词 vs 结构化查询；有没有成熟的 Metadata RAG 做法）。

结合 Go 生态与自托管部署成本，给出推荐方向（给事实与取舍，不要求最终决策——最终决策是票据 07）。


## Answer

语义层是 MB 级元数据（与业务数据量无关）；PG 原生（表 + JSONB + pgvector）作运行时存储 + YAML 文件作作者入口（自研 Go 编译/同步管线）；检索以结构化 SQL + 关键词为主，v1 跳过 embedding；v1 不引入图数据库（多跳遍历用关系边表 + WITH RECURSIVE）。

完整调研报告见 issue 评论与分支 research 下的文件。
