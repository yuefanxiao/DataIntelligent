# v1 build 12 — 验收重放框架 + 负向/边界 5 例 + 判定三件套

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/29
Status: closed（PR #43 合入 main，2026-08-12 关闭；交付摘要见 issue 评论）

## 交付摘要

1. **验收重放框架**（deploy/accept/）：半自动化重放——官方 go-sdk 客户端
   打自己的网关（HTTP + stdio 双形态真实 MCP 往返），用例按序重放，判定
   三件套逐项断言，报告留档（reports/，30min demo 与团队评审用）。
   - `accept.go`：harness（用例 = YAML 数据，无业务断言；build 14 的
     39+ 用例矩阵只增 cases.yaml 条目）；`--replay-from` 形态从 chain
     快照（<报告>.chain.jsonl）从头重放复现。
   - `cases.yaml`：负向/边界 5 例（§6.3 全过）+ 正向基础例。
   - `run.sh`：demo 主从 PG（独立 compose project dgw-accept）→
     provisioning → 凭据授权 → 网关拉起（HTTP 不设 DGW_SQL_LIMIT 走
     §4.9 默认 500 / stdio 限 5000）→ 四趟重放 → 报告留档。
   - `accept_test.go`：断言核心单测（compareCell 全类型归一化/路径断言/
     JSON 语义比较等——「测测试者」）。
2. **判定三件套（§6.4）**：
   - (a) psql 同库同 SQL 逐项一致（同一共享只读角色、同一从库；按列类型
     归一化：int/numeric(big.Rat)/timestamptz(多变体布局)/bool/uuid/bytea/
     jsonb 语义相等；截断用例用有界查询 LIMIT row_count 对照）。
   - (b) 执行记录 JSONL 完整记录调用链（工具/参数/耗时/状态/行数）与
     harness 观测逐调用对照 + chain 快照重放复现（同状态同行数同原因）。
   - (c) 零未授权访问：无 auth_failure；被拒原因如实落记录；
     permission_denied 只出现在预期负向例上（精确计数）。
3. **§6.3 负向/边界 5 例**：未授权表（neg-001a 无 grants 用户 /
   neg-001b 有角色读权无表授权 / neg-001c 非 public schema 引用 →
   not_granted 与 unknown_table 机器可区分）；非 SELECT（neg-002
   DML/DDL/COPY/utility 六句 → non_select）；LIMIT 截断（trunc-001 >500
   默认上限 + trunc-002 >5000 硬上限 + truncated 标记；5001 配置拒启由
   run.sh 环境检查验证）；并发超限（conc-001 同 key>2 / conc-002 进程级
   >8 → rate_limited key/process_concurrency_limit + reject_within_ms
   不排队断言）；无指标原料路径（neg-005 search 零命中 → 直查原料表成功）。
4. **形态覆盖**：stdio 单 key 单进程约束下多身份用例（ghost/进程级并发）
   仅 HTTP；并发用例标记 replay_skip（顺序重放无法复现并发拒绝，执行
   记录仍在链上，完整性照查）。README 记录 (b) 的已知边界（语义工具
   记录无结果内容，重放只复现状态）。

## 验证

- 全量验收通过（HTTP 17 例 + stdio 14 例全过，三件套全过，重放复现全过；
  报告 deploy/accept/reports/accept-20260812-061244-* 留档，gitignore）。
- `go test ./...` 全绿。
- code-review（双轴）+ code-review-adversarial（design + correctness 双
  persona）两轮收敛：P1（并发组记录忠实度）+ P2/P3 全修后 ready。

## 交接

- build 13（#30）：neg-005 断言「无指标」依赖验收环境语义存储保持为空
  （run.sh 不 sync 语义数据）；build 14 需带语义数据时换「无指标领域」
  fixture（cases.yaml 注释已记录）。
- mcp-ping（#10 收敛项）：保留为 demo 单发探针，验收职责由本框架承接
  （README「方案取舍」章节有说明）。

## 环境

- 验收环境端口：HTTP 网关默认 18082（DGW_ACCEPT_HTTP_PORT 可覆盖；本机
  18080/18081 被其他服务占用）；PG 从库 55432。
- 镜像受限时 DGW_PG_REGISTRY=docker.1ms.run/library 覆盖。
