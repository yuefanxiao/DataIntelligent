# v1 build 05 — 负载防护：并发闸 + statement_timeout

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/22
Status: assigned（yuefanxiao，2026-08-12 领取；实现 + 单测完成，PR 合入后关闭）
Blocked by (open blockers): 0

## 来源

docs/spec.md §2 负载防护数值/§4.5/§4.9 参数表；ADR-0010；issue #15

## What to build

负载防护（并发闸）：每 key 并发上限 2 + 进程级总并发上限 8 双信号量（守护进程语义；stdio 调试形态退化为每进程闸），超限结构化拒绝、不排队；statement_timeout 默认 30s（可配），网关连接级设置（与角色级双设置呼应，角色侧在 10 provisioning）；参数表全部 env 可覆盖。

## Acceptance criteria

- [ ] 同 key 并发 >2 → 结构化拒绝（不排队、不影响其他 key）
- [ ] 进程级并发 >8 → 结构化拒绝
- [ ] statement_timeout 默认 30s 生效且可配
- [ ] §4.9 参数表各参数 env 可覆盖
- [ ] 并发超限行为符合 §6.3 负向例 4（结构化拒绝、快速失败）

## Blocked by

- #21 — execute_sql 工具
