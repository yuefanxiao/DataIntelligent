# 10 SQL 生成路线决策：Wren 自托管 vs 自研 vs LLM+校验

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/11
Status: closed（2026-08-11 决议）
Blocked by (open blockers): 0

Part of #1

## Question

基于票据 09 的事实，决定 SQL 生成路线：Wren 自托管 vs 自研 SQL Compiler/Planner vs LLM + 校验（含 Go 落地成本与 MCP 集成方式）。

## Resolution（2026-08-11，详见 issue 决议评论 + ADR-0008）

v1 路线 = 02 方案甲（Agent 侧规划）+ 05 指标确定性编译 + **网关确定性校验层**（execute_sql 内部强制管线：wasilibs/go-pgquery cgo-free AST 分类（非 SELECT 一律拒）→ AST 为唯一授权通道比对 03 表 FQN 白名单 → PG 共享只读角色/超时物理边界 → `SELECT * FROM (<sql>) _q LIMIT N` 限额包层）；失败=结构化错误回传调用方、网关无自愈循环；并发闸（每 key+进程级，超限结构化拒绝）+ statement_timeout 收紧，数值归 12；数据行流向消费方 Agent=产品设计，网关第三方出口仅元数据 embedding（07）；**Wren 自托管出局**（2026 事实：经典栈冻结 legacy/v1 不修安全、新 OSS 无 Go 绑定/无 HTTP API、RLS/审计=商业版）；不自研完整 compiler（自由诉求需 LLM 在前端、规划器级工程量）；v2 create_query_plan 引擎选型延后（12 排期时定，02 草案接口冻结、plan_id 已预留、引擎可插拔）。

事实支撑：`.scratch/data-intelligence-layer/research/10-sql-generation-facts.md`（2026-08 实时核实）。

输出：03（校验层=授权物理实现）、12（落地/数值/排期）；ADR-0008；CONTEXT 术语（查询规划更新 + 校验层 + 负载防护新增）。
