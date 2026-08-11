# 07 语义层存储与检索决策

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/8
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/8#issuecomment-5251745493
Blocked by (open blockers): 0

Part of #1

## Question

基于票据 05（本体模型形态）与 06（存储检索事实），决定语义层知识的存储与检索方案。

输入：05 的本体模型决策 + 06 的事实对比。

## Resolution（2026-08-11）

存储 = 同机房**独立 PG 实例**（同步管线零生产凭证）+ 按服务拆 YAML 作者入口（+ 全局指标/概念文件）+ 自研 Go 同步管线（幂等 upsert + 墓碑 + dry-run diff）；运行时只查 PG 不查 YAML。检索 = 五条数据层原语（FQN 精确 / 双入口关键词 / 类型化边遍历 / 指标公式 / 枚举值，工具协议留 02），search_entities 走 RRF 混合（pg_trgm 关键词主通道 + pgvector 向量兜底，不设固定比例）；v1 引入向量，embedding = 外部 OpenAI text-embedding-3（接受元数据出机房）；不引入图数据库（边表 + WITH RECURSIVE）。Agent 消费侧 = 五工具 + 轻量 Agent Skill（留 02）。

- 术语 → CONTEXT.md；架构决策 → docs/adr/0002-semantic-store-retrieval.md
- 下游：解封 08（采集按服务落盘 + 墓碑语义）；输出 02（工具协议 + Skill）、10（指标公式机器可读）、11（语义实例部署细节）、03（FQN 权限挂载点）
