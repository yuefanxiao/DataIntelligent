# v1 build 08 — 语义工具五件套

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/25
Status: closed（PR #40 合入 main，2026-08-12）
Assignee: yuefanxiao（2026-08-12 领取）
Blocked by (open blockers): 0（#T06/#T07 已关闭）

## 来源

docs/spec.md §2.1/§4.3；ADR-0002/0003；issue #15

## What to build

五个只读语义工具（替换 01 的 stub）：`search_entities`（概念/指标双入口，关键词 FTS5 主通道 + sqlite-vec 向量兜底 RRF 混合，≤20 条 + total）、`get_entity`（FQN 精确查询，含枚举挂列、is_time、关系摘要）、`traverse_relations`（类型化边遍历 connects_to/contains/references/describes，双向多跳、有界）、`get_metric_definition`（指标口径 machine-readable + 可选带时间参数 dry-run 展开，不执行）、`list_enum_values`（列枚举取值，有界）。语义元数据面认证即读；结果一律结构化 JSON；全部调用走 06 执行记录埋点（六工具全记）。

## Acceptance criteria

- [ ] 五工具经 MCP 可调，替换 stub 生效，结果结构化 JSON
- [ ] search_entities 双入口：搜「支付失败」类查询命中概念/指标，RRF 混合，≤20 + total
- [ ] get_entity FQN 精确（枚举挂列、is_time、关系摘要齐全）
- [ ] traverse_relations 双向多跳有界遍历
- [ ] get_metric_definition 口径 machine-readable + dry-run 展开不执行
- [ ] list_enum_values 返回列枚举取值（CHECK 约束语义）
- [ ] 五工具调用均落执行记录（复用 06 写入器）

## Blocked by

- #T06 — 执行记录
- #T07 — 语义层运行时 + 同步管线
