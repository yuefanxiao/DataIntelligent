# Map: Enterprise Data Intelligence Layer

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/1
Status: open

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
- [06 Research: 语义层存储与检索方向](https://github.com/yuefanxiao/DataIntelligent/issues/7) — 语义层是 MB 级元数据（与业务数据量无关）；PG 原生（表 + JSONB + pgvector）作运行时存储 + YAML 文件作作者入口（自研 Go 编译/同步管线）；检索以结构化 SQL + 关键词为主，v1 跳过 embedding（免模型基建与数据出境）；v1 不引入图数据库（多跳遍历用关系边表 + WITH RECURSIVE）。
- [09 Research: 开源方案现状对比（含高 star 项目）](https://github.com/yuefanxiao/DataIntelligent/issues/10) — 无现成开源项目等价于 Enterprise Data Context Layer：OpenMetadata（context + RBAC + 审计 + MCP，但不做 SQL 执行）与 Wren AI（语义引擎 + 受管 Text2SQL + MCP，OSS 缺企业权限/审计）各占一半；Vanna 已归档、pgai 停维护、MindsDB 转向 → 自研薄 Go 工具层方向成立，语义语法借鉴 MDL/MetricFlow（票据 05 参考），guardrails 借鉴 Wren（票据 10 参考），不选型任何项目作执行引擎。

## Not yet specified

- 管理工作台/控制台形态（权限与 API key 的更新界面）——权限模型票据解决后可能浮出
- API key 的生成 / 分发 / 轮换机制细节
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


