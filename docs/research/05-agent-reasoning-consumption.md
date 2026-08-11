# 05 Research 资产：UModel 与「Agent 推理消费语义层」的开源模式

> 票据：GitHub issue #6（wayfinder:grilling）解决过程中的两个背景调研（2026-08-11，后台 subagent 产出）。
> 方法：一手来源（官方文档 / 仓库 / arXiv）；本文件只做事实与模式归纳，决策见票据 05 决议。

## 一、UModel（阿里云 UnifiedModel）

**定位**：面向企业 AI 的对象图谱语义层（为 AIOps 根因分析而生，受 Palantir ontology 哲学启发），2026-06 开源（Apache-2.0，~300★，Go/React/TS）。自带 MCP server + Claude Code/Codex/Cursor skills + `explain-before-execute`；arXiv 2606.04799 称 RCA 定位精度 +8%，阿里云生产 1 年+。注意与 Altova UModel（商业 UML 工具）重名。

**元模型（sets-and-links）**：
- `EntitySet`：业务/运维对象类（服务、实例、数据库、资产）
- `DataSet`：六类（metric/log/trace/event/profile/runbook）
- `Storage`：物理位置，与数据集语义解耦
- `Link` 四种语义边：`data_link`（EntitySet→DataSet）、`entity_set_link`（EntitySet↔EntitySet 拓扑：contains/calls/instance_of）、`storage_link`、`runbook_link`
- 运行时实例化出 Entity + Relation 记录（边只承载语义）

**Agent 推理方式**：结构化 SPL 查询（非 embedding）：`.entity` 定位对象 → `.entity_set` 取信号（返回可执行查询计划，参数从图带出）→ `.topo` 图遍历 → 「图上找线索、原始存储取证据」两步模式。MCP 默认只读、写工具默认关。

**缺口**：无单位/时间粒度/枚举语义；指标只是 dataset 标签+聚合器（无声明式口径层）；权限在 OSS 核心外。

**判定**：借模式不借模型——三样可抄：sets-and-links 元模型、「图上找线索+存储取证据」检索模式、安全 agent MCP 惯例（只读默认、先计划后执行）。

## 二、Agent 推理消费语义层的五种模式（开源全景）

| # | 消费模式 | 代表 | 对本体的要求 |
|---|---|---|---|
| P1 | **Prompt 注入**：会话开始时塞业务规则/词汇 | Wren instructions、dbt Agent Skills | 规则/术语成文可分块，体积 <2-3k tokens |
| P2 | **工具发现循环**：list→get→query，语义模型活在工具输出、不进 prompt | dbt SL MCP、Cube MCP、OpenMetadata MCP | 可查询实体索引 + 稳定 FQN；指标一等实体（公式/维度）；类型化关系双向可遍历 |
| P3 | **向量 RAG**：按 chunk 检索 schema/描述/示例 | Wren memory、Vanna、OM semantic_search | 每实体 2-3 句业务描述 + 同义词；示例 NL→SQL；可重建索引 |
| P4 | **图遍历 + 社区摘要** | GraphRAG / LightRAG | 类型化边 + 实体/关系摘要；社区聚类（索引成本高） |
| P5 | **语义编译/规划**：引擎把语义引用展开成 SQL | Wren planner、MetricFlow 编译 | 指标公式机器可读；join 关系可展开；dry-run 校验面 |

实证要点：Vanna 论文结论「上下文策略决定准确率、非模型」→ P3 值得补；「Death of Schema Linking」→ schema 足够小时无需检索，但企业级数万列必然超 context → 检索切片必须。GraphRAG 社区摘要适合非结构化语料，对受治理的企业本体是过度工程。

## 三、v1 推荐组合（已被票据 05 采纳）

- **P2 为主**：`search_entities` / `list_metrics` / `get_metric_definition` / `get_related_entities`（typed 边遍历）等只读工具，Claude Code 原生擅长该循环；本体不进上下文窗口，网关逐工具治理。
- **P3 补充**：一个语义搜索工具，索引实体描述 + 同义词（双入口：概念与指标都可被命中）。
- **light P5**：指标公式机器可读 + 执行前 dry-run（EXPLAIN，大数据下毫秒级）；完整 MDL 式编译留票据 10。
- **light P1**：小 context.md（命名约定、默认表、注意事项），<2-3k tokens。
- **P4 跳过**：社区摘要不做；但关系类型化、双向可遍历——工具驱动的「服务→库→表→列→指标」行走就是本场景的图推理形态。

## 四、来源（一手）

- UModel：https://github.com/alibaba/UnifiedModel · https://arxiv.org/abs/2606.04799 · https://alibaba.github.io/UnifiedModel/en/concepts/object-graph-semantic-layer.html
- Wren AI context/memory/MCP：https://docs.getwren.ai/oss/concepts/what_is_context · memory_system · https://docs.getwren.ai/cp/guide/integrations/wrenai-mcp
- GraphRAG：https://microsoft.github.io/graphrag/query/overview/ · LightRAG：https://github.com/HKUDS/LightRAG
- dbt SL MCP：https://docs.getdbt.com/docs/build/semantic-layer-mcp-server · dbt Agent Skills：https://github.com/dbt-labs/dbt-agent-skills
- Cube MCP：https://docs.cube.dev/admin/ai/mcp-connectors · OpenMetadata MCP：https://docs.open-metadata.org/latest/how-to-guides/mcp/reference
- Schema linking：RASL（arXiv 2507.23104）、Death of Schema Linking（arXiv 2408.07702）
