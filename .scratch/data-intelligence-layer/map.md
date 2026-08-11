## Destination

产出《企业级 Data Intelligence Layer》架构规格与分阶段实施计划（spec + phase plan）：一个 Go 实现的 MCP 工具层，让开发人员的 Coding Agent（Claude Code 等）能安全查询生产 PostgreSQL（只读从库），内置语义层（服务关系、schema 业务语义、指标）、权限与审计。决策由 yuefanxiao 个人拍板；规格经团队评审后进入实施。

## Notes

- 领域：数据平台 / Agent 基建 / MCP 生态
- 消费方：开发人员经 Coding Agent（先在本地跑起来验证）
- 环境事实：neo-cloud 13 个 Go 服务（Kratos）、10 个持库、**每服务一库**（同一 CNPG 集群、一主两从）、schema 统一 golang-migrate v4.19.1 维护（+GORM 交叉验证）、v1 只接从库、数据量千万~亿级、团队主力语言 Go
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
- 终点形态（票据 12 定）：阶段 4 = 工作台（内置 Agent 查询界面 + 语义/权限/API key 管理面），开发与产品共用；工具层始终可经 MCP/Skill 独立集成
- 执行序（票据 12 定）：v1 全服务最小闭环构建先行（task 票 14）→ spec + phase plan（docs/spec.md）→ 团队评审（PR + 30min demo）→ 阶段 2-4

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
- [08 知识采集与保鲜：自动 vs 人工、增量更新](https://github.com/yuefanxiao/DataIntelligent/issues/9) — 混合分工：结构自动（migration 文件为主干 + GORM 交叉验证 + 按需 calibrate 生产校准）、语义人工（Agent 起草+审查、人工确认）；采集器 = Go CLI（与同步管线同仓、真实语料 golden test）+ 采集工作流 Skill；增量 v1 手动 on-demand、Gitea label 触发后置；变更准入 = diff 建议 + PR review（纯结构批量确认）；校验三层 = 编译期 + dry-run + 漂移报告（手动+每周、只报告不改）；回滚 = 独立语义仓库（内部 Gitea）+ revert + 全量重建；API 定义采集 v1 排除。事实确认：golang-migrate v4.19.1 + GORM（生产无 AutoMigrate）、每服务一库。输出 11（校准凭证）、12（golden 语料/drift 例行/Gitea 排期）；ADR-0007。
- [10 SQL 生成路线决策：Wren 自托管 vs 自研 vs LLM+校验](https://github.com/yuefanxiao/DataIntelligent/issues/11) — v1 路线 = 02 方案甲（Agent 侧规划）+ 05 指标确定性编译 + **网关确定性校验层**（execute_sql 内部强制管线：wasilibs/go-pgquery cgo-free AST 分类（非 SELECT 一律拒）→ AST 为唯一授权通道比对 03 表 FQN 白名单 → PG 共享只读角色/超时物理边界 → `SELECT * FROM (<sql>) _q LIMIT N` 限额包层）；失败=结构化错误回传调用方、网关无自愈循环；并发闸（每 key+进程级，超限结构化拒绝）+ statement_timeout 收紧，数值归 12；数据行流向消费方 Agent=产品设计，网关第三方出口仅元数据 embedding（07）；**Wren 自托管出局**（2026 事实：经典栈冻结 legacy/v1 不修安全、新 OSS 无 Go 绑定/无 HTTP API、RLS/审计=商业版）；不自研完整 compiler（自由诉求需 LLM 在前端、规划器级工程量）；v2 create_query_plan 引擎选型延后（12 排期时定，02 草案接口冻结、plan_id 已预留、引擎可插拔）。输出 03（校验层=授权物理实现）、12（落地/数值/排期）；ADR-0008。

- [11 部署拓扑：本地起步、接从库、高可用](https://github.com/yuefanxiao/DataIntelligent/issues/12) — 双传输实现（Streamable HTTP 为主 + bearer token 认证、stdio 调试形态；并发闸按守护进程语义，数值归 12）；部署位 = **内部开发机单机 Docker**（不进生产集群：SQLite/logs/env 三 volume、restart 兜底），数据库凭证仅存该机 env 文件、开发机零凭证；从库连接 = **可配置 DSN 口子** + dbname 路由（生产网络通路方案生产部署时定）；DB 角色 = 专用共享只读角色、**provisioning 开发自建**（服务器 root/kubectl 取 CNPG postgres 超管建角色）、网关永不超管；执行记录 JSONL = 网关机本地 volume；采集器保留 = 手动 on-demand 无轮询 + 采集工作流 Skill 记 v1 交付物；数据新鲜度 = 接受从库延迟 + 启动自检（pg_is_in_recovery + 角色级 statement_timeout，不过拒启）；监控/告警、正式上线回滚流程 = 后续优化项。解封 12；输出 12（交付物清单、数值按守护进程语义）；ADR-0009。
- [12 阶段切分与 v1 验收标准（最小闭环）](https://github.com/yuefanxiao/DataIntelligent/issues/13) — 时序 = v1 全服务最小闭环构建先行 → spec+phase plan（docs/spec.md）→ 团队评审（PR + 30min demo，意见清零+拍板）→ 阶段 2-4；v1 = 13 服务全量（~/cloud/neo-cloud，结构自动+语义人工确认全服务，交付物清单含采集工作流 Skill/golden 语料/验收套件）、负载防护 = 每 key 2/进程级 8/statement_timeout 30s（可配）、限额 500/5000；验收 = 主用例「昨天支付失败率为什么上涨」全流程 + 每服务 ≥2 简单+≥1 复杂 + 负向/边界 5 例（无 grants 测试用户），判定三件套（psql 对照/执行记录可复现/零未授权），用例兼作 golden 语料、v1 后按效果迭代；阶段 2=运营化（Gitea 触发/校准·drift 例行/监控告警+复制延迟/生产通路全团队接入）、阶段 3=能力深化（v2 规划引擎/权限优化/工作台管理面/采集源扩展）、阶段 4=工作台+开放（内置 Agent 界面、PM 接入、MCP/Skill 并存；编排形态阶段 3 末定）；里程碑制无日期；输出 14（v1 构建 task 票）；ADR-0010。

## Not yet specified

- 阶段 2-4 入口评审时开票（12 已定排期）：v2 规划引擎选型（10 接口冻结）、权限优化（列级/掩码/RLS/key 过期）、工作台管理面、Gitea 触发采集、监控/告警 + OTel、校准例行化、API 定义采集、neo-cloud 之外采集源扩展、复制延迟监测、生产网络通路（NodePort/port-forward/LB）
- 内置 Agent 编排形态（自研 vs 接现成，如 Claude Code 网页版/LangGraph）——12 已定阶段 3 末再定

## Out of scope

- 非 PostgreSQL 数据库支持
- 写操作 / DDL
- 独立 Agent 产品（工具层先行，Q6b）
- 跨会话记忆与自进化（长期愿景，目的地重画时再议）
- 数仓 / OLAP 分析







