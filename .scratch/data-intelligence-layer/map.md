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

<!-- 已关闭票据一行：标题 + 一句 gist + 链接 -->

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

