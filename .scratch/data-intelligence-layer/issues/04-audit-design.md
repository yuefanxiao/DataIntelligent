# 04 审计设计：记录内容、存储与保留期

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/5
Status: open
Blocked by (open blockers): 1

Part of #1

## Question

审计记什么（谁 / 何时 / 什么 SQL / 结果摘要 / 是否脱敏）、存哪里（PG 表？文件？）、保留期多长、如何被查询（谁来审计、怎么查）？

> 输入（票据 13 决议，2026-08-11）：语义层运行时存储已定为 SQLite 单文件（ADR-0005）；审计是相对同步管线的第二写者（网关进程写），SQLite WAL 支持多进程读写但写写互斥，10^5 行/日量级无压力——决议时评估「审计落 SQLite（同机单文件）」vs「落 PG 从库（与业务审计同面）」。见 https://github.com/yuefanxiao/DataIntelligent/issues/14#issuecomment-5252962691

