# Gateway v1 工具面：六只读工具 + 一轻量 Agent Skill

v1 网关暴露 **6 个只读 MCP 工具**（细粒度一一对应五条检索原语 + 执行，原语直译命名）：`search_entities`（概念/指标双入口，关键词+向量 RRF 混合，≤20 条 + total）、`get_entity`（FQN 精确查询，含枚举挂列、is_time、关系摘要）、`traverse_relations`（类型化边遍历 connects_to/contains/references/describes）、`get_metric_definition`（指标口径 + 可选带时间参数展开 SQL，dry-run 不执行）、`list_enum_values`（列枚举值）、`execute_sql`（只读 SQL 执行；行数默认上限 500 / 硬上限 5000、超限截断 + truncated 标记；超时/成本走网关配置；可选 `plan_id` 溯源字段）。另交付 **1 轻量 Agent Skill**（≤1 页使用指南：工具清单、标准工作流、回退路径）。v1 **不做 `create_query_plan`**。来源：票据 02（issue #3），2026-08-11 拍板。

## Considered Options

- **原始只读 SQL 工具（选定，execute_sql）**：SQL 由 Agent 编写，网关做强制只读（DB 只读用户 + SQL 解析双重保证）、限额、脱敏、审计；「无指标走表/列原料路径」（票据 05/07）必须靠它落地；最小完整闭环一次查出来。→ 选定。
- **仅结构化查询、不暴露原始 SQL**：查询必须先经语义解析成结构化的查询对象——等于把「语义解析/翻译」做进网关，提前抢占票据 10（SQL 生成路线）的领地，v1 复杂度失控。→ 未选。
- **五条原语合并成大工具（如 search_entities 带 mode 参数）**：工具数少、Agent 选择负担小；但参数互相牵制（mode 枚举让 Agent 猜）、单工具返回难有界。→ 未选，五原语一一对应（票据 07 决议「五工具」）。
- **业务概念命名（search_business_concept 等，决策讨论早期思路）**：业务语言直觉；但概念入口已由 search_entities 的 type 参数覆盖，双命名制造别名噪音，权限（03）挂载点也会分裂。→ 未选，原语直译命名 + Agent Skill 教「何时用哪个」。
- **v1 做 create_query_plan（方案乙）**：网关侧生成 SQL（诉求→语义解析→展开指标→生成 SQL→校验），规划与执行分离、plan_id 串联审计、引擎可插拔；但规划 = 票据 10 的决策领地（Wren 自托管 vs 自研 vs LLM+校验），v1 做等于预判 10；指标路径的确定性展开已由 get_metric_definition 的 dry-run 覆盖。→ 未选；设计草案留档 docs/research/02-query-planning-design.md 作 10 输入，execute_sql 留 `plan_id` 溯源口子（v1 透传不校验），10 决议后纯增量添加、工具面不破坏。
- **结果 markdown 渲染 vs 结构化 JSON**：JSON（列名+类型+行数组+元信息）保留类型与元数据，渲染交给 Agent。→ 结构化 JSON，text content 承载。

## Consequences

- 工具面形状是票据 03 权限模型（FQN 权限挂载点）与 04 审计设计（`plan_id` 溯源字段）的输入；限额具体数值（超时/成本上限）由 12 阶段验收定，本 ADR 只定机制。
- 所有工具返回有界（列表 ≤20、SQL 行数默认 500/硬上限 5000 + truncated 标记），避免单次工具调用撑爆 Agent 上下文。
- 指标路径的「规划」= `get_metric_definition` 的 dry-run 展开（确定性编译：公式 + 时间参数 → 可执行 SQL）；自由诉求规划待 10 决议。
- 权限/脱敏/审计/只读强制是网关中间件（票据 01 已定自实现），不表现为独立工具——工具面保持纯查询语义。
- 解封 03/04/12；票据 10 决议后按需在工具面纯增量追加规划工具。
