## Destination

产出《企业级 Data Intelligence Layer》架构规格与分阶段实施计划（spec + phase plan）：一个 Go 实现的 MCP 工具层，让开发人员的 Coding Agent（Claude Code 等）能安全查询生产 PostgreSQL（只读从库），内置语义层（服务关系、schema 业务语义、指标）、权限与审计。决策由 yuefanxiao 个人拍板；规格经团队评审后进入实施。

## Notes

- 领域：数据平台 / Agent 基建 / MCP 生态
- 消费方：开发人员经 Coding Agent（先在本地跑起来验证）
- 环境事实：几十个服务、公用一个 PG（一主两从）、v1 只接从库、数据量千万~亿级、团队主力语言 Go
- 已定决策（第 1、2 轮拷问）：
  - 终点 = spec + phase plan（Q1a）；拍板人 = yuefanxiao（Q5）
  - 只做工具层，不做 Agent 产品（Q6b）——先在本地 Coding Agent 跑起来
  - 语义层必须做，本体/图谱形态，从服务↔schema 映射起步（Q2、Q11c）
  - v1 = 最小完整闭环：语义层最小版 + 网关只读查询一次做出来（Q11c）
  - 语言 = Go（Q9）；SDK 倾向官方 modelcontextprotocol/go-sdk，票据 01 钉死
  - 跨会话记忆/自进化是长期愿景，本 effort 不做（Q7）
- 每次解决票据的 session 应 consult：/grilling、/domain-modeling
- Tracker：GitHub Issues 为 canonical，本地 .scratch/ 镜像同步（见 docs/agents/issue-tracker.md）
- 背景文档：docs/decision-discussion.md

## Decisions so far

- [01 Research: MCP Go SDK 选型与协议能力](https://github.com/yuefanxiao/DataIntelligent/issues/2) — 推荐官方 modelcontextprotocol/go-sdk v1.7.0+（唯一双协议时代 Go SDK：自动协商 2025-11-25/2026-07-28，内置 RequireBearerToken 认证中间件、conformance 套件、GitHub 官方生产验证）；mark3labs/mcp-go（协议停在 2025-11-25）作备选。需自实现：TokenVerifier、权限引擎（联动 03）、SQL 只读强制/限额/脱敏、审计落库。
- [06 Research: 语义层存储与检索方向](https://github.com/yuefanxiao/DataIntelligent/issues/7) — 语义层是 MB 级元数据（与业务数据量无关）；PG 原生（表 + JSONB + pgvector）作运行时存储 + YAML 文件作作者入口（自研 Go 编译/同步管线）；检索以结构化 SQL + 关键词为主，v1 跳过 embedding（免模型基建与数据出境）；v1 不引入图数据库（多跳遍历用关系边表 + WITH RECURSIVE）。（embedding 一条已被票据 07 修正：v1 引入向量 + 外部 API）
- [09 Research: 开源方案现状对比（含高 star 项目）](https://github.com/yuefanxiao/DataIntelligent/issues/10) — 无现成开源项目等价于 Enterprise Data Context Layer：OpenMetadata（context + RBAC + 审计 + MCP，但不做 SQL 执行）与 Wren AI（语义引擎 + 受管 Text2SQL + MCP，OSS 缺企业权限/审计）各占一半；Vanna 已归档、pgai 停维护、MindsDB 转向 → 自研薄 Go 工具层方向成立，语义语法借鉴 MDL/MetricFlow（票据 05 参考），guardrails 借鉴 Wren（票据 10 参考），不选型任何项目作执行引擎。

- [05 语义层本体模型：MDL vs 自研、v1 最小集合](https://github.com/yuefanxiao/DataIntelligent/issues/6) — 混合本体：UModel 式 sets-and-links 图谱承载服务↔库↔表↔列↔概念拓扑 + OSI 式声明式 SQL 指标挂载，自研补枚举取值语义与服务↔库映射；消费=工具发现为主(P2)+模糊检索(P3)+公式机器可读/dry-run(light P5)，概念与指标双入口；六类实体 + 四种关系边(connects_to/contains/references/describes) + 枚举挂列 + is_time；Database 粒度=PG database；无指标走表/列原料路径，新指标=人工确认回写 YAML（指标沉淀）；解封 07/08/12，输出 10（表达式可读+dry-run）、03（FQN 权限挂载点）、02（有界返回）。
- [07 语义层存储与检索决策](https://github.com/yuefanxiao/DataIntelligent/issues/8) — 存储=同机房独立 PG 实例（同步管线零生产凭证）+ 按服务拆 YAML 作者入口 + 自研 Go 同步管线（幂等 upsert + 墓碑 + dry-run diff）；运行时只查 PG 不查 YAML；检索=五条数据层原语（FQN 精确/双入口关键词/类型化边遍历/指标公式/枚举值，工具协议留 02），search_entities 走 RRF 混合（pg_trgm 关键词主通道 + pgvector 向量兜底，不设固定比例）；v1 引入向量，embedding=外部 OpenAI text-embedding-3（接受元数据出机房）；不引入图数据库。输出 08/02/10/11。⚠️ 存储载体已被票据 13 修正为 SQLite（v1），检索/管线决策原样有效。
- [02 Gateway v1 工具面：暴露哪些 MCP 工具](https://github.com/yuefanxiao/DataIntelligent/issues/3) — v1 工具面 = 六只读工具 + 一轻量 Agent Skill，细粒度一一对应、原语直译命名（search_entities 双入口 RRF / get_entity FQN 精确 / traverse_relations 边遍历 / get_metric_definition 公式+dry-run 展开 / list_enum_values / execute_sql 只读）；有界返回（语义列表 ≤20+total、SQL 默认 500/硬上限 5000 + truncated）；execute_sql 留可选 plan_id 溯源口子；v1 不做 create_query_plan（方案乙草案入 docs/research/02-query-planning-design.md 作 10 输入）；结果编码 JSON、限额数值归 12。输出 03/04/10/12；解封 03/04/12。
- [03 权限模型：粒度、API key 机制与维护方](https://github.com/yuefanxiao/DataIntelligent/issues/4) — 双表面授权：execute_sql 默认拒绝（表级 FQN 白名单；指标/概念授权编译期展开为表授权；不可解析/未知表一律拒绝），语义元数据面认证即读；key→用户扁平 grants（opaque 随机串哈希存储、一用户多 key、吊销即时、v1 不设过期）；grants YAML + CLI 维护（编译进语义存储 PG 权限表、热重载）；服务/库级通配=作者入口语法糖、展开=快照（新表默认拒绝+管线告警+重展开确认，`*` 不开放）；PG 侧一个共享只读角色只保「物理不能写」（timeout/禁 SET ROLE），细粒度全在网关侧；v1 无管理员角色；列级/行级/掩码后置优化、管理工作台 v2。解封 12；输出 04（key→用户聚合）、10（解析器复用）、08（新表告警流程）。
- [13 语义层运行时存储与检索：SQLite 轻量版](https://github.com/yuefanxiao/DataIntelligent/issues/14) — 修正 07 存储载体：v1 运行时存储 = SQLite 单文件（modernc 纯 Go 无 CGO、WAL、单写者管线+多读者网关、备份=文件拷贝），v1 唯一实现；向量=sqlite-vec（v1.47.0 起内置 CGO-free 移植，SQL 内 KNN，退路=Go 暴力余弦）；PG 仅作多机/HA 升级路径记入 ADR-0005（ADR-0002 加修正指针）；授权数据/权限表随载体落 SQLite；审计存储方向留输入给 04。不变：YAML 作者入口+同步管线+五条原语+RRF+OpenAI embedding+不引入图数据库；生产查询目标仍为 PG 从库。

- [04 审计设计：记录内容、存储与保留期](https://github.com/yuefanxiao/DataIntelligent/issues/5) — 「审计」重构为**执行记录**：六工具全记 + 认证失败/拒绝 + key 生命周期（CLI 一行）的结构化 JSONL 日志（宿主机文件 + 轮转），字段契约 = 时间/用户/key/工具/参数（SQL 原文入库不脱敏，宿主机权限即访问边界）/分阶段耗时/状态/行数/truncated/plan_id/被拒原因；原始 ~7 天 + 聚合摘要 ~30 天；不做 SQLite 证据存储、不做 CLI 查询面、不接 OTel（安全证据仅合规政策驱动时按契约升级）；驱动 = 排障（statement_timeout 自愈 + psql 手动杀 + 日志事后查）+ tracing + 优化数据源（08 的信号：被拒查询/原料路径/搜索关键词）。解封 12；输出 08（字段契约）、10（超时/截断分布）、11（日志位置）；ADR-0006 修正 ADR-0005 的审计输入。

## Not yet specified

- 权限后续优化：列级权限（拒绝/掩码）、行级 RLS、key 过期策略（03 决议后置，12 排期时定）
- 管理工作台/控制台形态（权限与 API key 的更新界面 + 管理员口令）——03 已定 v2 候选，v1 用 grants YAML + CLI
- 复制延迟容忍度与数据新鲜度约定
- Schema 维护方式（ORM + migration？）——知识采集票据前需确认
- 网关自身的高可用 / 监控 / 告警需求

## Out of scope

- PM 自助 Web 查询界面（v1 消费方是开发人员）
- 非 PostgreSQL 数据库支持
- 写操作 / DDL
- 独立 Agent 产品（工具层先行，Q6b）
- 跨会话记忆与自进化（长期愿景，目的地重画时再议）
- 数仓 / OLAP 分析

