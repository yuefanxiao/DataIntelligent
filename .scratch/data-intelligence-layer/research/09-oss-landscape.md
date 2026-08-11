# 09 Research: Text2SQL / 语义层 / 数据 Agent 开源方案现状对比

> 票据：GitHub issue #10（wayfinder:research）。调研日期：2026-08-11。
> 方法：GitHub API（star / license / 活跃度，截至 2026-08-11）+ 各项目官方 README / 官方文档为唯一事实来源；star 数均为实时抓取值。
> 背景：团队自建"企业数据语义层 + MCP 数据网关"（Go、自托管、生产 PG 只读）。本文件只做事实调研；SQL 生成路线决策见票据 10（grilling），本体模型参考见票据 05。

## TL;DR

1. **没有任何现成开源项目就是"Enterprise Data Context Layer"**。2025-2026 年该赛道发生三件大事：**Vanna 仓库归档**（2026-02）、**Timescale pgai 停止维护**（2026-02）、**MindsDB 改名 MindsHub 整体转向 agent 工作台**——纯 Text2SQL RAG 与"AI 数据库"路线都在退潮。
2. **概念上最接近的两个**：
   - **Wren AI**（17.2k★）：语义引擎（MDL）+ 内置 MCP server + 22+ 数据源 + 受管 Text2SQL（dry-run 校验、行数上限、结构化报错），是"语义层喂给 MCP agent"这条路走最远的一个；但 OSS 版**没有**行/列级权限与访问控制（商业版 Cloud/自托管），且整体是 Python 工具链 + 生成式 BI（dashboard）取向。
   - **OpenMetadata**（14.8k★）：2026 年官方定位就是 **"The Open Context Layer for AI"**——元数据知识图谱（130+ 连接器）+ 术语表/指标/领域/数据产品 + RBAC + 审计 + 内置 MCP server；但它**不做 Text2SQL 执行**，是"上下文层"而非"查询网关"。
3. **结论：自研 Go 工具层方向成立**。OSS 里不存在"Go 原生、可嵌入、MCP 一等公民、只读 PG 网关、带 RBAC+审计、服务↔schema 业务语义"的完整组合。建议：**形态借鉴 OpenMetadata（context）+ Wren AI（semantic engine for MCP + guardrails），指标定义借鉴 dbt MetricFlow / OSI，本体参考票据 05 单独展开**。可借鉴清单见文末。

---

## 1. 主对比表（8 个项目，star 截至 2026-08-11，GitHub API）

| 项目 | star | License | 定位与核心思想 | 可自托管 | MCP | 权限/审计 | 企业语义（多服务/多库/业务语义）适配 | 社区活跃度 |
|---|---|---|---|---|---|---|---|---|
| **Wren AI**（Canner/WrenAI） | 17,222 | 多 license：core/sdk Apache-2.0，部分 AGPL/CC-BY | **语义引擎 + 受管 Text2SQL（GenBI）**：MDL 语义模型 + AI context layer（instructions.md/记忆）+ schema 检索 + dry-plan 校验 + 结构化报错，面向 AI agent 生成受管 SQL 与 dashboard；2026-05 起 wren-engine（Rust/DataFusion）并入本仓 core/ | ✅ pip 安装 CLI + wren-core；旧 Docker 版=GenBI Classic | ✅ **内置 MCP server**（mcp-server 模块，可嵌入 Claude/Cline/Cursor）+ skills（wren-sql 等） | OSS：仅 guardrails（dry-run、row limit、结构化错误、eval runner）；**行/列级安全与访问控制 = Cloud/自托管商业版**；OSS 无审计 | 中-高：MDL 是声明式语义模型，支持 22+ 数据源与跨方言统一 SQL（sqlglot/ibis）；但业务语义=手工 MDL 文件，服务↔schema 映射需自建 | 高：日更级提交（2026-08-11 有 release），Discord 活跃 |
| **Cube**（cube-js/cube） | 20,594 | Apache-2.0 / MIT 双许可 | **开源语义层（headless BI）**：用代码（YAML/JS）定义指标/维度/join/访问规则，经 SQL/REST/GraphQL API 暴露给 BI/自建应用/AI agent；内置关系型缓存层 | ✅ Docker 自托管 | ⚠️ OSS 无官方 MCP server（社区实现如 @athenaintelligence/cube-athena-mcp）；官方 MCP 方向 = Cube Cloud 的"MCP Connectors"（agent 外接工具） | OSS：JWT security context + 模型内 RLS 有；用户管理/审计/SSO = Cube Cloud 商业版 | 中：指标/维度范式强于文本语义；面向 BI 度量，不解决"服务↔schema 业务语义"；多库支持全 | 高：日更（2026-08-11），Slack 社区 |
| **dbt Semantic Layer / MetricFlow**（dbt-labs/dbt-core） | 13,617 | Apache-2.0 | **指标层**：YAML 语义模型 + MetricFlow 编译为多方言 SQL，保证指标口径一致（"governed metrics for AI"）；2025-10 Coalesce 宣布 MetricFlow 开源（Apache-2.0），配合 OSI（Open Semantic Interchange）标准倡议 | ⚠️ MetricFlow 引擎开源可自托管；**完整 SL 的 serving API / BI 集成仍要 dbt Cloud** | ✅ 本地 dbt MCP server（2025-04 开源）+ remote MCP server（2025 Coalesce GA）+ Fusion MCP tools；dbt Agents（beta） | 在 dbt Cloud 侧（plan 限权、Discovery API ACL）；不是执行网关，不做查询级审计 | 中：指标口径强（多库多方言）；但要求引入 dbt 工程工作流（项目/模型/transform），团队无 dbt 项目则采纳成本高 | 高：日更；但 SL 主线在 Cloud |
| **OpenMetadata** | 14,845 | Apache-2.0 | **数据上下文层（catalog + 知识图谱 + 治理）**：官方定位 "The Open Context Layer for AI"——技术元数据、列级血缘、术语表、指标、领域、数据产品、策略、数据合约、质量、记忆（对话/决策）统一为知识图谱；130+ 连接器 | ✅ 全开源，Docker/K8s 自托管 | ✅ **内置 MCP server**（README 明确列出） | ✅ 强：RBAC、团队/角色/策略、数据访问策略、审计（活动流） | 高：术语表/本体/领域/数据产品/数据合约正是"企业业务语义"形态；但**不生成/执行 SQL**，是上下文来源而非查询网关 | 高：日更（2026-08-11），活跃 release |
| **DB-GPT**（eosphoros-ai/DB-GPT） | 19,697 | MIT | **Agentic AI 数据助手/平台**：多 agent + AWEL 工作流 + RAG 知识库 + 多模型 + Text2SQL（S2SQL）+ 沙箱代码执行 + skills；既是产品也是搭 AI 数据应用的基础框架 | ✅ Docker/K8s/pip 自托管 | ⚠️ 0.7+ **作为 MCP client 消费** MCP server（agent 工具，0.7.1 加 SSE 鉴权）；本身不是 MCP 数据 server | 弱-中：平台基础用户管理近年逐步加入；细粒度 RBAC/审计偏企业版与社区路线图（OIDC 等为社区诉求） | 中：知识库+数据源可承载业务语义，但语义非结构化（prompt/RAG 为主），无声明式语义模型 | 高：日更（2026-08-08），中英社区大 |
| **Vanna** | 23,823 | MIT | **Text2SQL RAG 库**：训练（DDL/文档/SQL 示例）→ 生成 SQL → 执行，多库；v2.0 曾加 RLS/审计/限流/Web 组件；**2026-02 仓库归档，官方不再支持自托管维护，公司转向 Vanna Cloud** | ❌ 归档后官方不维护（库本身 MIT 仍可 fork 自用） | ❌ 无 | v2.0 代码含用户感知 RLS/审计日志，但已无人维护 | 低：纯 schema+示例级语义，无多服务/多库业务语义能力 | ❌ 归档（2026-02-02 archived）；生态冻结 |
| **Chat2DB**（OtterMind/Chat2DB，原 CodePhiliaX） | 27,931 | 修改版 Apache-2.0（5.3+ 带附加条件） | **AI 数据库客户端**：40+ 数据库的桌面/Docker Web SQL 工作台 + BYO-model AI 助手（生成/解释/优化 SQL）+ ER/仪表盘/数据管理；CLI 带 MCP | ✅ 社区版可自托管（明确 **single-user local-first**，README：无用户体系与授权边界） | ✅ 开源 CLI（Chat2DB-CLI）支持 MCP | ❌ 社区版无（单用户、无账号/授权）；企业版商业 | 低：面向 DBA 手工工具，无 server 侧语义层/网关形态 | 高：日更（v5.3.3，2026-08-06） |
| **MindsDB / MindsHub**（mindsdb/mindshub，原名 mindsdb/mindsdb） | 39,529 | MIT | **已转向**：原"AI 数据库"（SQL 桥接 ML/数据源）2025-2026 整体改名 **MindsHub Cowork**——开放 agent 工作台（模型路由、agents、artifacts、记忆/skills/定时任务、连接数据 vault），Text2SQL/语义层路线基本退场 | ✅ 可自托管（desktop 应用/源码构建） | 未见于 README | 平台账号体系在托管端；自托管细节有限 | 低（新方向下不相关）：已不是语义层/Text2SQL 产品 | 中：v26.1.0（2026-04），repo 改名后活跃度重新蓄积 |

---

## 2. 每个项目与"自研 Go 工具层"的差距

### Wren AI —— 语义层喂 MCP agent 的标杆，但被商业版卡住权限
- 最贴近"语义引擎 + MCP"组合：MDL（声明式语义模型）、schema 检索、dry-plan 校验、行数上限、结构化报错、eval runner——这些 guardrails 思路**值得逐条抄进自研网关**（票据 10 的 SQL 生成决策可参考）。
- 差距：① OSS 无 RBAC/行级权限/审计（全在 Cloud/自托管商业版）——这恰是我们 v1 的核心卖点；② 工具链 Python（CLI/wren-ai-service）+ Rust 核心（DataFusion），与 Go 网关集成要跨语言；③ 取向是"生成式 BI + dashboard 部署"，对我们（coding agent 查生产 PG）偏重；④ 业务语义要手工写 MDL 文件，服务↔schema 映射仍得自建。**结论：可借鉴不可依赖。**

### Cube —— 开源语义层的正确形态，但不是 context layer
- 证明"headless 语义层"可自托管、可用代码版本化管理；指标/维度/join 声明式定义 + SQL/REST/GraphQL 暴露是成熟范式。
- 差距：① 无官方 OSS MCP server（社区实现质量参差）；② 语义是 BI 度量取向（measure/dimension），不表达"服务与 schema 的关系、字段业务含义"这类本体语义；③ 权限在 OSS 只有 security context/RLS，无用户管理与审计；④ Node/TypeScript 栈，且自带缓存/查询执行层，与"薄网关 + 生产 PG 直连"重叠又冲突。

### dbt Semantic Layer / MetricFlow —— 指标口径治理的标准答案（部分）
- MetricFlow 开源（Apache-2.0，计划捐基金会）+ OSI 标准（Snowflake/Salesforce/Databricks/Sigma 联合）→ **指标定义语法值得作为票据 05 本体/指标模型的参考**，避免发明私有格式。
- 差距：① 完整 SL（serving、BI 集成）仍在 dbt Cloud；② 要求 dbt 工程工作流，团队无此基建，采纳成本高；③ 只管"指标算得一致"，不管"agent 怎么安全地查到数据"（执行、权限、审计是云侧或缺失）。

### OpenMetadata —— 唯一把"Context Layer for AI"当定位的
- 与我们 destination 的命名（Enterprise Data Context Layer）几乎逐字对应：知识图谱、术语表/本体、领域、数据产品、数据合约、RBAC、审计、**内置 MCP server**。
- 差距：① 是 catalog/治理平台（Java + React + 多组件），不是 Go 可嵌入库，也不做只读查询网关与 SQL 执行；② 接入是"采集/同步"模型，实时性、与网关执行链路的集成需另建；③ 重量级（多服务部署），与我们"薄工具层"哲学冲突。**形态参考价值 > 复用价值。**

### DB-GPT —— 证明"数据 agent 平台"是产品级红海
- 能力最全（agent/AWEL/RAG/沙箱/Text2SQL），MIT 且非常活跃。
- 差距：① 是**完整产品**（自带前端/应用模型），我们是"工具层供任何 MCP 客户端使用"——定位直接冲突（地图 Q6b 已定"不做 Agent 产品"）；② 权限/审计弱，不适合企业生产数据直连；③ Python 生态，MCP 是消费方而非一等 server。

### Vanna —— 归档即信号
- 曾是最流行 Text2SQL RAG 库；归档（2026-02）+"官方不再支持自托管"证明**纯 RAG 式 Text2SQL 无法构成企业语义层**（无治理、无权限、无版本化语义）。若票据 10 讨论"RAG vs 语义模型"，这是最强反面论据之一。
- 可借用的只有思想：用 DDL/文档/示例做检索增强（我们知识采集票据 08 可参考），但不要选型它。

### Chat2DB —— 不在同一赛道
- 是 DBA 客户端（单用户、无授权边界），社区版明确不面向服务端网关场景；改名/迁移到 OtterMind 公司后企业功能全部商业化。**参考价值：MCP CLI 形态 + 修改版 Apache-2.0 许可先例**，其他无。

### MindsDB/MindsHub —— 大型项目转向的警示
- 39.5k★ 的"AI 数据库"整体转向 agent 工作台：**"SQL 桥接 AI"叙事退潮**，企业若押注"让开源项目替你实现语义层"有转向风险。自研薄层 + 开放协议（MCP）反而是对冲。

---

## 3. 其他相关项目（star > 3k，简要）

| 项目 | star | 一句话 |
|---|---|---|
| **LangChain**（langchain-ai/langchain） | 143,950 | 框架：`create_sql_agent` + `SQLDatabaseToolkit`（list tables / schema / query / checker），纯 prompt 级 SQL agent，无治理/权限/语义模型；agent 循环编排思想可参考，不建议作为数据层 |
| **LlamaIndex**（run-llama/llama_index） | 51,555 | 框架：`NLSQLTableQueryEngine` / `SQLTableRetrieverQueryEngine`（schema 索引 + 检索），官方文档自己警告 context 溢出并要求手工补表描述；同样只借思想（检索增强、schema 索引），项目主线已转向文档 agent/OCR |
| **modelcontextprotocol/servers**（官方示例） | 89,424 | 含 Postgres MCP server：**"裸 SQL"基线**——直连读库、无语义/权限/审计；正是自研网关要超越的起点，也证明"做一个 PG 只读 MCP server"门槛不高 |
| **Timescale pgai** | 5,811（已归档） | 曾是最贴近"PG 原生语义目录 + Text2SQL"（Semantic Catalog 自动生成库描述）；**2026-02 停止维护**（README 明示）→ PG 原生路线无活跃维护者，是自研 PG 原生语义层的窗口机会 |
| **Apache Superset** | 74,209 | BI 平台（dataset/metrics 自带语义层 + AI assistant beta），自托管重、非 MCP 优先，作为 BI 消费端参考即可 |
| **SQLChat**（sqlchat/sqlchat） | 5,845 | 轻量 chat SQL 客户端（MIT），无语义层/权限，不在赛道内 |

---

## 4. 结论

1. **最接近 "Enterprise Data Context Layer" 方向的两个**：
   - **OpenMetadata**（语义/治理/上下文形态，含 MCP、RBAC、审计）——形态与命名最一致；
   - **Wren AI**（语义引擎 + MCP + 受管 Text2SQL 的工程范式）——执行与 agent 集成范式最一致。
   - 两者互补，恰等于我们 v1 要拼的两半（语义上下文 + 受管查询执行）；而两者 OSS 各自缺另一半。
2. **自研差距判断**：不存在可"直接自托管即得"的项目；差距主要在于组合缺失（Go 原生 + MCP 一等 + RBAC/审计 + 服务↔schema 本体语义 + 只读 PG 网关）。薄工具层自研的"重复造轮子"风险集中在语义模型语法与 guardrails，前者可抄 MDL/MetricFlow/OSI/OpenMetadata 术语表，后者可抄 Wren 的 dry-run/row-limit/结构化报错。
3. **对票据 10（SQL 生成路线）的输入**：Vanna 归档 + MindsDB 转向 + Wren 坚持"语义模型 + 检索"三重证据表明：**语义模型优先于纯 RAG**；SQL 生成路线应参考 Wren 的"MDL 规划 + dry-plan 校验 + 结构化错误"而非 Vanna 式训练 RAG。
4. **对票据 05（本体模型）的输入**：OpenMetadata 的 glossary/domain/data product/ontology 与 dbt MetricFlow/OSI 的语义模型语法，是两个首选参考形态。

---

## 5. 来源（一手）

- GitHub API（star/license/archived/pushed_at，2026-08-11）：Canner/WrenAI、cube-js/cube、dbt-labs/dbt-core、eosphoros-ai/DB-GPT、vanna-ai/vanna、OtterMind/Chat2DB、mindsdb/mindshub、timescale/pgai、run-llama/llama_index、langchain-ai/langchain、open-metadata/OpenMetadata、modelcontextprotocol/servers、apache/superset、sqlchat/sqlchat、defog-ai/defog
- 各仓库 README / LICENSE 文件（gh api readme/license 拉取，一手）
- Wren AI：https://github.com/Canner/WrenAI （GenBI 定位、MDL、license 路径图、OSS vs Cloud 权限说明）；wren-engine MCP server 模块见 Canner/wren-engine（2026-01 活跃，已并入 Canner/WrenAI core/）
- Cube：https://github.com/cube-js/cube 、https://docs.cube.dev （Cube Core vs Cube 商业版、MCP Connectors）
- dbt：https://github.com/dbt-labs/dbt-core ；MetricFlow 开源公告（Coalesce 2025-10）：https://www.getdbt.com/blog/dbt-agents-remote-dbt-mcp-server-trusted-ai-for-analytics 、docs.getdbt.com/docs/dbt-cloud-apis/mcp
- OpenMetadata：https://github.com/open-metadata/OpenMetadata （"Open Context Layer for AI"、MCP server）
- DB-GPT：https://github.com/eosphoros-ai/DB-GPT 、docs.dbgpt.cn（0.7.0 MCP 支持）
- Vanna：https://github.com/vanna-ai/vanna （archived 2026-02）
- Chat2DB：https://github.com/OtterMind/Chat2DB 、Chat2DB-CLI（MCP）
- MindsDB：https://github.com/mindsdb/minds（MindsHub 定位）
- pgai：https://github.com/timescale/pgai （2026-02 停止维护声明）
- LangChain SQL agent：python.langchain.com（create_sql_agent / SQLDatabaseToolkit）；LlamaIndex Text2SQL：developers.llamaindex.ai（NLSQLTableQueryEngine / SQLTableRetrieverQueryEngine）
