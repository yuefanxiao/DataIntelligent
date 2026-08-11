# SQL 生成路线：v1 Agent 侧规划 + 网关确定性校验层；Wren 出局；v2 引擎延后

SQL 生成路线 = **v1 网关零生成能力、零 LLM 集成**：SQL 由消费方 Coding Agent 生成（02 方案甲），指标路径走 05 已定的确定性 dry-run 展开，网关只做**确定性校验层**——`execute_sql` 内部强制管线，四段链：**AST 分类**（`wasilibs/go-pgquery`，libpg_query 的 WASM 移植，cgo-free、PG 17 真实语法、sqlc 2024 起生产背书，与 13 的 CGO-free 立场一致；非 SELECT 类——DML/DDL/COPY/utility/数据修改 CTE——一律拒绝）→ **表提取 + 授权**（AST 为唯一授权通道：语法层表引用，CTE/子查询/join 全可见，直接对 03 表 FQN 白名单比对，未知/未授权表拒绝；EXPLAIN JSON 是 planner 层——视图展开、函数内表不可见——不作授权依据）→ **PG 物理边界**（03 已定的低权限共享只读角色 + `statement_timeout` + 禁 SET ROLE）→ **执行限额**（`SELECT * FROM (<用户 SQL>) _q LIMIT N` 包层，Limit 短路不扫描，N = 02 已定 500/5000 + truncated）。校验失败 = **结构化错误回传调用方**（Wren guardrails 范式借用点），网关**无自愈循环、不重试**，审计记「被拒原因」（04 字段契约）；EXPLAIN (FORMAT JSON) EXECUTE 预检（不执行但查权限）留 v2 规划流程，v1 不做。另加**负载防护**：每 key 并发查询 + 进程级总并发双闸，超限结构化错误拒绝（不排队），`statement_timeout` 默认收紧（可配），数值归 12 定。来源：票据 10（issue #11），2026-08-11 拍板。

**Wren 自托管出局**——2026-08 事实（研究子代理核实，`.scratch/data-intelligence-layer/research/10-sql-generation-facts.md`）：经典自托管栈（wren-engine 独立 Rust 服务）冻结于 `legacy/v1` 分支（"Wren GenBI Classic"，官方声明不再修安全）；维护中的新 OSS wren-core 是 Rust 库，仅 PyO3/CLI/WASM/MCP 接口，**无 Go 绑定、无 OSS HTTP API**，Go 网关集成只能 Python/MCP sidecar；行/列级权限、审计、治理 = 商业版。09「不选型执行引擎」由倾向升级为事实结论。

**不自研完整 SQL Compiler/Planner**：自由诉求路径（自然语言→语义计划→SQL）必须有 LLM 在前端（v1 = 消费方 Agent 自带 LLM），全量规划器 = 查询规划器级工程量，v1 无收益；指标路径的确定性编译已由 05 定，不重复造。

**v2 `create_query_plan` 引擎不决策（延后）**：生成引擎（外部 LLM API vs 私有化模型 vs 不做网关侧规划）与诉求/元数据出机房边界，12 排期时定；接口形态 02 草案已冻结、`plan_id` 已在 execute_sql 预留溯源口子、引擎可插拔，翻案成本低。数据边界现状：数据行流向消费方 Agent = 产品设计，网关第三方出口仅两处——① 元数据 embedding（07 已接受）、② v2 规划 LLM 调用（未决策）；② 的时间点在执行前，payload 物理上无数据行（无自愈循环保证）。

## Considered Options

- **生成引擎：Wren 自托管 vs 自研 compiler vs LLM+校验 vs v1 不做（Agent 侧规划）**：Wren 经典栈冻结于 legacy/v1（不再修安全）、新 OSS 无 Go 绑定/无 HTTP API（仅 PyO3/CLI/WASM/MCP）、OSS 缺 RLS/审计（恰是自研卖点）→ 事实性出局；自研完整 compiler 在自由诉求路径仍需 LLM 在前端，等于自写查询规划器，v1 无收益；网关侧 LLM+校验是 v2 create_query_plan 的候选，v1 消费方自带 LLM（02 方案甲）已覆盖。→ v1 不做生成，只做校验层；v2 引擎延后。
- **校验层解析器：wasilibs/go-pgquery vs pg_query_go/v6 vs auxten/postgresql-parser**：wasilibs = libpg_query 编译为 WASM 的 cgo-free 移植，PG 17 真实语法、sqlc 2024 起生产使用（最硬背书），与 13 的 CGO-free 立场一致；pg_query_go/v6 = cgo 强制（CGO_ENABLED=0 编不过），语法最正统但拖累构建/分发；auxten = 纯 Go 但语法停在 PG 10 时代（CockroachDB 派生、低维护）。→ wasilibs/go-pgquery。
- **授权表提取通道：AST vs EXPLAIN JSON**：AST = 语法层，CTE/子查询/join 全可见，与语义层表 FQN 直接对齐；EXPLAIN JSON = planner 层，视图展开成基表、函数内表不可见，作授权依据会漏/漂移。→ AST 为唯一授权通道；EXPLAIN 仅留 v2 作校验/估算，不作授权依据。
- **校验失败处置：结构化错误回传 vs 网关自愈循环**：自愈循环把执行结果/错误回灌 LLM——延迟与成本在网关侧、审计要记自愈轨迹、且让「LLM 不见数据行」的边界失效。→ 结构化错误回传调用方（Wren 范式），重试由 Agent 侧决策。
- **负载防护：并发闸 vs 仅超时+事后审计**：statement_timeout 只挡单条语句，挡不住「故意串行/并行跑大量长查询」的速率攻击；并发闸 = 每 key + 进程级计数信号量，成本极低，是网关侧唯一有效维度。→ 双闸 + 超时收紧，数值归 12 定。
- **EXPLAIN 预检进 v1 vs 留 v2**：预检每次执行多一次规划往返（延迟），且 v1 已有 AST 授权 + PG 角色/超时双保险；EXPLAIN 的权限自检/成本估算语义放 v2 规划流程更值。→ 留 v2。
- **v2 引擎选型本轮决策 vs 延后**：v2 create_query_plan 无任何 v1 依赖（v1 工具面不含规划工具，02 已定），决策没有消费者；且数据出口边界（诉求/元数据是否出机房）随届时合规环境可变。→ 延后，12 排期时定，接口 02 草案已冻结、引擎可插拔。

## Consequences

- 校验层是 03 授权模型的物理实现（表 FQN → AST 提取比对），03「不可解析/未知表一律拒绝」在此落为代码；wasilibs/go-pgquery 同时服务 03 的解析器复用点。
- v1 网关对外流量仅元数据 embedding（07 已接受）；无 LLM 依赖意味着 v1 无供应商绑定、无 API 成本、无出机房审查负担。
- Go 落地成本：一个 WASM 解析依赖 + 标准库校验链（分类/遍历/授权比对/LIMIT 包层）+ 并发闸信号量；无 CGO、无 sidecar、无外部组件。
- 02 方案乙接口冻结为 v2 草案（request/time_range/filters → plan_id/intent/resolved/sql/tables/status），`execute_sql` 的 plan_id 参数启用为溯源/审计字段。
- 输出：12（校验层 + 并发闸进 v1 最小闭环；N/M/超时默认值、EXPLAIN 预检、v2 引擎选型与数据出口边界排期）。ADR-0003 的 create_query_plan 延后在此定案（v2、引擎未选），工具面决策原样有效。
- 环境事实（研究核实）：wasilibs/go-pgquery 由 sqlc 自 v1.25.0（2024-01）起生产使用；EXPLAIN 不执行语句但执行权限检查（Tom Lane 确认），计划期间对引用表加共享锁；`SET TRANSACTION READ ONLY` 不阻止 `EXPLAIN` DML 的规划，语句类型分类仍在网关侧；read-only 事务与 EXPLAIN 是强力校验工具而非沙箱（COPY TO PROGRAM/dblink/误标 IMMUTABLE 的副作用函数仅由角色权限约束）。
