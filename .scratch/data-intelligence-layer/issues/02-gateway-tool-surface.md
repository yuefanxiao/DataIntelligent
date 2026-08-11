# 02 Gateway v1 工具面：暴露哪些 MCP 工具

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/3
Status: open
Blocked by (open blockers): 1

Part of #1

## Question

Gateway v1 应暴露哪些 MCP 工具？工具粒度与命名如何设计？

候选能力：schema 查询（服务→DB→表→列）、只读 SQL 执行、查询限额（行数/超时/成本）、结果脱敏、查询规划（参考 search_business_concept / get_metric_definition / create_query_plan / execute_query 思路）。

需要决定：v1 工具集边界（哪些做、哪些不做）、工具粒度（细工具 vs 大而全）、命名与语义。

