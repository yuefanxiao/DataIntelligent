# 04 审计设计：记录内容、存储与保留期

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/5
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/5#issuecomment-5253219742

Part of #1

## Question

审计记什么（谁 / 何时 / 什么 SQL / 结果摘要 / 是否脱敏）、存哪里（PG 表？文件？）、保留期多长、如何被查询（谁来审计、怎么查）？

> 输入（票据 13 决议，2026-08-11）：语义层运行时存储已定为 SQLite 单文件（ADR-0005）；审计是相对同步管线的第二写者（网关进程写），SQLite WAL 支持多进程读写但写写互斥，10^5 行/日量级无压力——决议时评估「审计落 SQLite（同机单文件）」vs「落 PG 从库（与业务审计同面）」。见 https://github.com/yuefanxiao/DataIntelligent/issues/14#issuecomment-5252962691

## Resolution（2026-08-11）

「审计」重构为**执行记录**：v1 不建安全证据存储（SQLite + 保留期 + CLI 查询面）。执行记录 = 每次 MCP 工具调用的结构化 JSON 日志（JSONL，宿主机文件 + 轮转），三用途：排障（查询故障的事后回答）、tracing（分阶段耗时）、优化数据源（08 知识采集的信号）。六工具全记 + 认证失败/权限拒绝 + key 生命周期（CLI 一行）；字段契约 = 时间/用户/key/工具/参数（SQL 原文入库不脱敏）/分阶段耗时/状态/行数/truncated/plan_id/被拒原因；原始 ~7 天 + 聚合摘要 ~30 天；消费 = grep/jq/脚本聚合（08/10/12），无 CLI 审计工具。故障响应链（statement_timeout 自愈 + psql 手动杀 + 日志事后查）全程不需要证据存储；安全证据仅在合规政策驱动时按契约升级。解封 12；输出 08（字段契约）、10（超时/截断分布）、11（日志位置）。

- 术语「执行记录」→ CONTEXT.md（_Avoid_: 审计日志）；架构决策 → docs/adr/0006-execution-record.md（修正 ADR-0005 的审计输入）
