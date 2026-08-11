# SQL 生成路线 — 事实核实（票据 10 决议依据）

> 2026-08-11 由研究子代理实时核实（GitHub/官方文档/邮件列表为主源）。star 数、状态、版本均为当时抓取。用途：票据 10（issue #11）决议的支撑材料，决议见 ADR-0008。

## 一、Wren AI 自托管现状（2026-08）

- **经典自托管栈已冻结**：2026-05 重组后，经典 Docker 栈（wren-ui + wren-ai-service + wren-engine + qdrant）仅存于 `legacy/v1` 分支（tag `v1-final`），官方标注 "Wren GenBI Classic"、「不再有新特性或安全修复」。
- **组件语言**（legacy）：wren-ui = TypeScript/Next.js + Apollo GraphQL；wren-ai-service = Python 3.12/FastAPI（AI/RAG/Text2SQL）；wren-engine = **Rust + Apache DataFusion** 独立 HTTP 服务；wren-launcher = Go。
- **新 OSS（main 分支）**：`core/wren-core` = Rust 语义引擎（DataFusion），**可嵌入库**形态；`wren-core-py` = PyO3 绑定（PyPI `wren-core`）；`wren-core-wasm` = WASM；`wren/` = Python SDK + CLI（PyPI `wrenai`）；`wren-mdl` = MDL JSON schema；`sdk/wren-langchain`；`skills/`。
- **集成接口**：新 OSS **无 HTTP API**——只有 Python SDK/CLI、WASM、MCP server（agent 集成）；**无 Go 绑定**。Go 网关想用只能 Python/MCP sidecar。
- **License**：新 OSS 核心 Apache-2.0；legacy 分支根 LICENSE 为旧文件（历史上 AGPL-3.0 时代产物，用前需核实）。
- **商业版独占**：团队工作区、治理、**行/列级权限（RLS）、审计**——README 明示 commercial-only。
- **Guardrails（OSS 内）**：dry-plan（不执行展示展开 SQL）、dry-run（连库校验不返回行）、行数上限、结构化错误（带 hint）、policy checks（transpile 前）；eval runner 有提及。
- **部署足迹**：legacy = 5 容器（含 qdrant 向量库、LLM API key 必需）；新 OSS = pip 包（DuckDB 内嵌 + LanceDB 内存，首次依赖 ~800MB 含 torch）。
- **LLM 位置**：legacy = wren-ai-service 内，LiteLLM 配置（provider 无关，temperature 0/seed 0）；新 OSS = **不内置 LLM**（"the LLM lives entirely in the agent"），MDL→SQL 为确定性编译（sqlglot transpile + CTE 重写 + MDL 解析）。

### 结论
自托管 wren-engine 作 Go 网关组件 = 2026 年不可行：经典栈冻结不修安全、新 OSS 无 Go/HTTP 集成面、OSS 缺权限/审计。唯一受支持接口 = Python SDK/CLI 或 MCP server sidecar。

## 二、Go 侧 PostgreSQL SQL 解析与只读校验（2026-08）

- **wasilibs/go-pgquery**：libpg_query 编译为 **WASM** 的 cgo-free 移植，PG 真实语法；**sqlc 自 v1.25.0（2024-01）起生产使用**（"C-ya, cgo"），最佳无 CGO 选项。
- **pganalyze/pg_query_go v6**：cgo 强制（CGO_ENABLED=0 编不过），protobuf AST，无内置表提取 helper（需自行遍历 RangeVar）；v6 跟踪 PG 17（libpg_query 17-6.2.2）；PG 18 已在 libpg_query 但 Go 绑定未跟进。Bytebase 类工具历史选型。
- **auxten/postgresql-parser**：纯 Go，CockroachDB v20.1.11 派生，语法 ≈ PG 10 时代；最后 release 2022-08、低维护。仅 Atlas/Bytebase 报告使用。
- **让 PG 自己校验**（全部主源确认）：
  - `EXPLAIN (FORMAT JSON)` **不执行**语句；**无 ANALYZE 也做权限检查**（Tom Lane 确认：与真实运行同权检查），规划期间对引用表加共享锁；`EXPLAIN ANALYZE` 会执行。
  - `PREPARE` 只接受 SELECT/INSERT/UPDATE/DELETE/MERGE/VALUES——DDL/COPY/utility 在解析期被拒；EXECUTE 才完整执行（校验用 `EXPLAIN EXECUTE` 而非 EXECUTE）。
  - `SET TRANSACTION READ ONLY` 禁 DML/DDL/COPY FROM/TRUNCATE 等；**不禁止对 DML 的 EXPLAIN**（语句类型分类仍在网关侧）；nextval() 在只读事务报错（PG 9.0 起）。
  - 计划期只有 **IMMUTABLE** 函数可能被预求值（误标 IMMUTABLE 的副作用函数是漏洞面）；VOLATILE 不会在计划期执行。
  - 漏洞面：read-only ≠ 沙箱——COPY TO PROGRAM、dblink 出网、pg_read_file 类，仅由角色权限约束；函数内触达的表对 plan JSON 不可见。
- **执行限额技巧**：`SELECT * FROM (<用户 SQL>) _q LIMIT N` 包层（Limit 短路不扫描子节点）；注意数据修改 CTE、FOR UPDATE 等边角。
- 无现成「只读 SQL 强制」Go 库（sqly 是无关 SQLite/CSV CLI）；事实标准 = 解析器（wasilibs 或 pg_query_go）+ DB 侧 PREPARE/EXPLAIN/READ ONLY/超时/受限角色。

### 结论
最实用组合：**wasilibs/go-pgquery 解析分类 + AST 表提取**（授权依据），PG 侧 PREPARE 门 + `SET TRANSACTION READ ONLY` 事务内 `EXPLAIN (FORMAT JSON) EXECUTE`（权限自检、不执行）+ statement_timeout + 专用 SELECT-only 角色；执行时 LIMIT 包层。EXPLAIN/READ ONLY 是强力校验工具而非沙箱。

## 来源

[Canner/WrenAI](https://github.com/Canner/WrenAI) · [legacy/v1 分支](https://github.com/Canner/WrenAI/tree/legacy/v1) · [wren-engine（已归档）](https://github.com/Canner/wren-engine) · [OSS 架构文档](https://docs.getwren.ai/oss/reference/architecture) · [OSS quickstart](https://docs.getwren.ai/oss/get_started/quickstart) · [wren-core-py](https://github.com/Canner/WrenAI/tree/main/core/wren-core-py) · [wasilibs/go-pgquery（sqlc 博客）](https://sqlc.dev/posts/2024/01/04/sqlc-v1-25-0-c-ya-c-go/) · [pg_query_go](https://github.com/pganalyze/pg_query_go) · [EXPLAIN](https://www.postgresql.org/docs/current/sql-explain.html) · [SET TRANSACTION](https://www.postgresql.org/docs/current/sql-set-transaction.html) · [PREPARE](https://www.postgresql.org/docs/current/sql-prepare.html) · [volatility](https://www.postgresql.org/docs/current/xfunc-volatility.html) · [Tom Lane: EXPLAIN 权限检查](https://postgrespro.com/list/id/809558.1712256270@sss.pgh.pa.us)
