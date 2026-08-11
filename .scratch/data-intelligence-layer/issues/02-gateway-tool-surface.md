# 02 Gateway v1 工具面：暴露哪些 MCP 工具

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/3
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/3#issuecomment-5252743006
Blocked by (open blockers): 0

Part of #1

## Question

Gateway v1 应暴露哪些 MCP 工具？工具粒度与命名如何设计？

候选能力：schema 查询（服务→DB→表→列）、只读 SQL 执行、查询限额（行数/超时/成本）、结果脱敏、查询规划（参考 search_business_concept / get_metric_definition / create_query_plan / execute_query 思路）。

需要决定：v1 工具集边界（哪些做、哪些不做）、工具粒度（细工具 vs 大而全）、命名与语义。

## Resolution（2026-08-11）

v1 工具面 = **六只读工具 + 一轻量 Agent Skill**，细粒度一一对应、原语直译命名：

| 工具 | 语义 | 有界返回 |
|---|---|---|
| `search_entities` | 双入口（概念/指标）关键词+向量 RRF 混合检索；type 参数过滤实体类型 | ≤20 条 + total |
| `get_entity` | FQN 精确查询六类实体（含枚举挂列、is_time、关系摘要） | 单实体 + 摘要 |
| `traverse_relations` | 类型化边遍历（connects_to/contains/references/describes） | ≤20 条 |
| `get_metric_definition` | 指标口径（表达式/聚合/过滤）+ 可选带时间参数展开 SQL（dry-run，不执行） | 定义 + 展开 SQL |
| `list_enum_values` | 列枚举值 | 全量 |
| `execute_sql` | 只读 SQL 执行；行数默认上限 500 / 硬上限 5000、超限截断 + truncated 标记；超时/成本走网关配置；可选 `plan_id` 溯源字段（v1 透传不校验） | ≤上限 + 元信息 |

- 不做 `create_query_plan`：规划是票据 10 的领地；指标路径由 dry-run 展开覆盖；`execute_sql` 留 `plan_id` 溯源口子，10 决议后纯增量添加。方案乙草案 → docs/research/02-query-planning-design.md（10 的输入）
- 权限/脱敏/限额中间件/审计 = 网关层与其他票据，不进工具面；结果编码 = 结构化 JSON；限额数值归 12
- Agent Skill（交付物）：≤1 页使用指南（工作流：发现→解析→执行 + 回退路径），随网关分发

- 术语 → CONTEXT.md（Agent Skill、查询规划）；架构决策 → docs/adr/0003-gateway-tool-surface.md
- 下游：输出 03（FQN 工具面）、04（plan_id 溯源）、10（方案乙草案）、12（限额机制）；解封 03/04/12
