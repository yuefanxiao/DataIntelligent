# v1 build 06 — 执行记录：JSONL + 轮转 + 聚合摘要

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/23
Status: closed（2026-08-12，PR #38 合入 main；验收五条全过 + 双轴 code review + 对抗评审两轮收敛已修复）
Blocked by (open blockers): 0（#21/#22 已于 #34/#35 合入）

## 来源

docs/spec.md §2.4/§4.6；ADR-0006；issue #15

## What to build

执行记录：结构化 JSONL（宿主机文件 + 轮转），非 SQLite 证据存储、无 CLI 查询面、不接 OTel。字段契约 = 时间/用户/key/工具/参数（execute_sql 的 SQL 原文入库、不脱敏，宿主机权限即访问边界）/分阶段耗时（认证→权限→解析→执行→返回）/状态（成功/拒绝/超时/解析失败）/行数/truncated/plan_id/被拒原因。范围 = 六工具全记 + 认证失败 + 权限拒绝 + key 生命周期（CLI 侧一行）。保留期 = 原始 ~7 天轮转 + 聚合摘要 ~30 天（聚合摘要喂知识采集信号）。三用：排障 + tracing + 知识采集信号。

## Acceptance criteria

- [ ] execute_sql 全调用链落 JSONL：SQL 原文、分阶段耗时、状态、行数、truncated、plan_id、被拒原因
- [ ] 认证失败 / 权限拒绝 / 限流拒绝均落记录（被拒原因如实）
- [ ] key 创建/吊销（CLI 侧）各记一行
- [ ] 原始 7 天轮转 + 聚合摘要 30 天（可配）
- [ ] 从 JSONL 可完整重放一次调用链（§6.4(b) 判定三件套前置）

## Blocked by

- #T04 — execute_sql 工具
- #T05 — 负载防护
