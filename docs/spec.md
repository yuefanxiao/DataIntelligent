# v1 规格：Enterprise Data Intelligence Layer（最小完整闭环）

> 状态：**待评审**（评审对象 = 本文件 PR + 30min demo；门禁 = 意见清零 + 拍板人确认）
> 关联：task 票 14（[issue #15](https://github.com/yuefanxiao/DataIntelligent/issues/15)）、ADR-0001~0010、[CONTEXT.md](../CONTEXT.md)（领域术语权威）、docs/research/
> 执行序（ADR-0010 修正）：本 spec 先行 → 团队评审 → 评审通过后实施 v1 构建 → 阶段 2-4

## 1. Problem Statement

开发人员查询生产数据的现状：psql 直连从库（凭据分散在个人手里、无权限管控、无访问记录）；业务语义靠问人（表/列含义、指标口径无权威来源，同一数字多人各算各的）；Coding Agent 没有安全通道触达生产数据（要么不给、要么给了就裸奔）。

本 effort 的目标：一个 Go 实现的 MCP 数据网关，让开发人员的 Coding Agent（Claude Code 等）能**安全查询生产 PostgreSQL（只读从库）**——内置语义层（服务关系、schema 业务语义、指标口径）、表级权限与执行记录。v1 = 最小完整闭环：一次做出来、跑起来、验收掉，覆盖 neo-cloud 全部 13 个后端服务。

## 2. Solution

**消费形态**：开发人员 → Coding Agent（自带 LLM，SQL 由 Agent 侧规划）→ MCP（Streamable HTTP 为主、stdio 调试）→ 网关（六只读工具）→ 两个只读端点：业务从库（execute_sql）+ 语义层 SQLite（五个语义工具）。

**四件套**：

1. **语义层**：YAML 作者入口（内部 Gitea 语义仓库，按服务拆文件 + 全局指标/概念）→ 自研 Go 同步管线编译 → SQLite 单文件运行时存储（modernc 纯 Go、WAL、备份=文件拷贝）；五条检索原语 + RRF 混合（关键词 FTS5 主通道 + sqlite-vec 向量兜底），embedding 用外部 OpenAI text-embedding-3（v1 引入向量，元数据出机房已接受）。
2. **校验层**（execute_sql 强制四段链）：AST 分类（wasilibs/go-pgquery，非 SELECT 一律拒）→ 表提取 + 授权比对（AST 为唯一授权通道，表 FQN 白名单，未知/未授权表拒绝）→ PG 物理边界（共享只读角色 + statement_timeout + 禁 SET ROLE）→ 限额包层（`SELECT * FROM (<sql>) _q LIMIT N`）。校验失败 = 结构化错误回传调用方，网关不重试、无自愈循环。
3. **权限**：双表面——业务数据面默认拒绝、表级 FQN 白名单授权（指标/概念授权编译期展开为表授权）；语义元数据面认证即读。凭据 = opaque 随机串（`dgw_` 前缀、sha256 哈希存储、明文仅创建时打印一次），key→用户扁平 grants，一用户多 key、吊销即时；grants YAML + CLI 维护，编译进 SQLite 权限表，网关启动加载内存 + 热重载。
4. **执行记录**：六工具全记 + 认证失败/权限拒绝 + key 生命周期 = 结构化 JSONL（宿主机文件 + 轮转），字段契约含 SQL 原文（不脱敏，宿主机权限即访问边界）、分阶段耗时、状态、行数、truncated、plan_id、被拒原因；原始 ~7 天 + 聚合摘要 ~30 天。

**部署**：内部开发机单机 Docker（三 volume：SQLite/执行记录/env 0600），数据库凭证只存该机 env 文件；启动自检两条硬校验（`pg_is_in_recovery()` + 角色级 statement_timeout 生效确认，不过拒启）；DSN 可配置 + 按 dbname 路由（生产网络通路生产部署时定）。

**负载防护数值**（见 §4.9 参数表）：每 key 并发 2 / 进程级 8 / statement_timeout 30s（可配）/ SQL 限额 500-5000。

**验收**：主用例 + 每服务用例矩阵 + 负向/边界 5 例 + 判定三件套（见 §6）。

## 3. User Stories

### 开发人员（经 Coding Agent 消费）

1. 作为开发人员，我想让我的 Coding Agent 经 MCP 连网关查询生产只读数据，以便排障不再 psql 直连、不再找同事要凭据。
2. 作为开发人员，我想用自然语言搜索业务概念与指标（双入口，如搜"支付失败"命中概念或指标），以便在不知道表名的情况下定位数据。
3. 作为开发人员，我想按 FQN 精确获取任一实体（服务/库/表/列/指标/概念）的细节（含枚举挂列、is_time、关系摘要），以便确认结构与语义。
4. 作为开发人员，我想沿类型化关系边遍历（connects_to/contains/references/describes，双向、多跳），以便理解「服务→库→表→列→指标→概念」链路与可 join 关系。
5. 作为开发人员，我想读取指标口径（表达式 + 聚合 + 过滤，机器可读），并可选带时间参数做 dry-run 展开（不执行），以便拿到可执行 SQL 并确认数字怎么算。
6. 作为开发人员，我想查询列的枚举取值，以便看懂 status 类字段的业务含义。
7. 作为开发人员，我想执行只读 SQL（结果有界：默认 500 行/硬上限 5000 + truncated 标记，结构化 JSON），以便自由组合多表查询。
8. 作为开发人员，我想完成主用例「昨天支付失败率为什么上涨」全流程（搜索→口径→趋势→下钻归因），以便验证最小闭环真实可用。
9. 作为开发人员，我想在无现成指标时走表/列原料路径（自己组合 SQL），以便探索不卡壳；沉淀的口径经人工确认回写 YAML 成为正式指标。
10. 作为开发人员，我想在查询被拒/超限/超时时收到结构化错误（区分语法错误 vs 无权限 vs 限流），以便 Agent 侧决定是否调整重试。
11. 作为开发人员，我想有随网关交付的 Agent Skill（≤1 页：工具清单、标准工作流、回退路径），以便少试错、用对工具。
12. 作为开发人员，我想多个设备/工具各持一把 key 共用同一身份，以便审计按用户聚合、按设备区分。
13. 作为开发人员，我想网关启动自检不过就不提供服务，以便「连的一定是从库、一定有超时边界」可被机器验证。
14. 作为开发人员，我想并发超限时快速失败（不排队），以便慢查询不排队拖垮从库。

### 服务负责人（语义权威）

15. 作为服务负责人，我想审查 Agent 起草的语义（描述/业务概念/指标口径/枚举含义），确认后才入 YAML，以便口径权威且不被机器改写。
16. 作为服务负责人，我想对纯结构采集 diff 做批量确认（PR review 形态），以便结构变更的准入负担≈零。

### 拍板人与评审团队

17. 作为拍板人，我想评审 spec（docs/spec.md PR）+ 30min demo 重放验收用例，以便验收门槛可判、意见清零才放行。
18. 作为评审团队，我想看到验收判定三件套的证据（psql 对照 / 执行记录复现 / 零未授权访问），以便 v1 达标有客观依据。

### 网关运维者（宿主机 CLI 使用者）

19. 作为网关运维者，我想用权限 CLI 创建/吊销 key、增删表授权、查看授权快照，以便权限变更走 git review 且吊销即时生效。
20. 作为网关运维者，我想读执行记录 JSONL（谁/何时/哪把 key/跑了什么 SQL/分阶段耗时/状态/行数），以便排障、发现昂贵查询与授权缺口。
21. 作为网关运维者，我想跑同步管线 CLI（dry-run diff → 确认 → 应用，幂等），以便作者入口变更进运行时、可回滚（revert + 全量重建）。
22. 作为网关运维者，我想跑采集器 CLI（结构自动采集 → YAML 草稿 + GORM 交叉验证 + 按需校准），以便结构知识零手工、可 golden test 验证。
23. 作为网关运维者，我想备份 SQLite（WAL checkpoint + 文件拷贝）与回滚（旧镜像 tag + 文件恢复），以便有兜底基线。
24. 作为网关运维者，我想在语义仓库（内部 Gitea）review 并合入 YAML 变更，以便作者入口版本化、变更可追溯。

## 4. Implementation Decisions

### 4.1 本体模型（ADR-0001）

混合形态：UModel 式 sets-and-links 图谱承载**拓扑**（服务↔库↔表↔列↔业务概念，"找什么"）+ OSI 式声明式 SQL 指标承载**口径**（"怎么算"），自研补充枚举取值语义（挂列）与 is_time 时间轴标注。

- **六类实体**：service / database / table / column / metric / concept；枚举取值挂列（list_enum_values 的数据来源）。
- **四种关系边**（双向可遍历）：`connects_to`（服务↔库）、`contains`（库→表、表→列）、`references`（表↔表 join 条件）、`describes`（概念↔表/列/指标）。
- **稳定 FQN**：`服务.库.表.列` / 指标 / 概念——同一命名空间同时是权限挂载点。
- **指标**：口径唯一（expression 机器可读 + 聚合 + 过滤）；时间过滤是查询参数、不进指标定义；无现成指标时走表/列原料路径，新指标 = 人工确认回写 YAML（指标沉淀），自动回写/跨会话自进化排除在 v1 外。

### 4.2 作者入口与运行时存储（ADR-0002 修正、ADR-0005）

- **作者入口** = 按服务拆 YAML + 全局指标/概念文件，落**内部 Gitea 独立语义仓库**；commit/revert/review 即版本机制与变更闸门。
- **运行时存储** = SQLite 单文件（modernc.org/sqlite 纯 Go 无 CGO、WAL 模式、单写者同步管线 + 多读者网关）；v1 唯一实现，PG 仅作多机/HA 升级路径（接口抽象保留）。
- **同步管线**（自研 Go）：编译校验（FQN 唯一/引用完整性/指标 SQL 可解析/枚举合法，失败原子拒绝）→ dry-run diff（增删改清单）→ 幂等 upsert + 墓碑软删除应用。运行时**只查 SQLite，不查 YAML**。
- 备份 = WAL checkpoint + 文件拷贝；回滚 = git revert + 全量重建。

### 4.3 检索原语与工具面（ADR-0002/0003）

五条检索原语 → 五个只读语义工具 + 执行工具：

| 工具 | 语义 | 有界返回 |
|---|---|---|
| `search_entities` | 双入口（概念/指标）关键词+向量 RRF 混合检索 | ≤20 条 + total |
| `get_entity` | FQN 精确查询（含枚举挂列、is_time、关系摘要） | 单实体 |
| `traverse_relations` | 类型化边遍历（connects_to/contains/references/describes） | 有界 |
| `get_metric_definition` | 指标口径 + 可选带时间参数 dry-run 展开（不执行） | 单指标 |
| `list_enum_values` | 列枚举取值 | 有界 |
| `execute_sql` | 只读 SQL 执行，校验层四段链，可选 plan_id 透传（溯源，不校验） | 默认 500 / 硬上限 5000 + truncated |

- 另交付 **1 轻量 Agent Skill**（≤1 页：工具清单、标准工作流"发现→解析→执行"、回退路径）。
- 结果一律结构化 JSON（列名+类型+行数组+元信息），渲染交给 Agent。
- v1 **不做** `create_query_plan`（方案乙草案在 docs/research/02-query-planning-design.md，接口冻结为 v2 输入；execute_sql 已留 plan_id 口子，引擎可插拔）。

### 4.4 权限模型（ADR-0004）

- **双表面**：业务数据面（execute_sql）**默认拒绝**、表级 FQN 白名单授权；语义元数据面（五个语义工具）**认证即读**（敏感信息不进语义层 YAML 面）。
- **授权粒度** = 表 FQN；指标/概念授权在同步管线编译期展开为底层表授权（杜绝"指标有权底层没权"的悬空）；服务/库级通配 = 作者入口语法糖，编译期展开为具体表清单快照（新表默认拒绝 + 管线告警 + 重展开确认，`*` 不开放）。
- **凭据** = opaque 随机串（`dgw_` 前缀），sha256 哈希存储（明文仅创建时打印一次），key→用户扁平 grants（一用户多 key、吊销即时、v1 无过期、数据结构留 role 字段）。
- **维护方** = grants YAML + 权限 CLI（仅宿主机可运行），编译进 SQLite 权限表，网关启动加载内存 + 热重载；v1 无管理员角色。
- **PG 侧** = 一个共享只读角色（NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT、10 库 GRANT SELECT、角色级 statement_timeout、收 SET ROLE/临时表能力）——只保"物理不能写"，细粒度全在网关侧；网关永不超管。

### 4.5 校验层（ADR-0008）

`execute_sql` 内部强制四段链（任何一段失败 = 结构化错误回传，网关不重试、无自愈循环；执行记录记"被拒原因"）：

1. **AST 分类**：wasilibs/go-pgquery（libpg_query WASM 移植，cgo-free，PG 17 真实语法）——非 SELECT 类（DML/DDL/COPY/utility/数据修改 CTE）一律拒绝。
2. **表提取 + 授权**：AST 为唯一授权通道（语法层表引用，CTE/子查询/join 全可见）→ 对表 FQN 白名单逐表比对；未知/未授权表拒绝；EXPLAIN（planner 层）不作授权依据。
3. **PG 物理边界**：共享只读角色 + statement_timeout + 禁 SET ROLE。
4. **执行限额**：`SELECT * FROM (<用户 SQL>) _q LIMIT N` 包层（Limit 短路不扫描），N = 500/5000，超限截断 + truncated 标记。

**负载防护**（并发闸，机制见 §4.9 数值）：每 key 并发 + 进程级总并发双信号量，超限结构化错误拒绝（不排队）；statement_timeout 默认收紧（可配）。v1 无 LLM 集成、无外部 SQL 生成依赖。

### 4.6 执行记录（ADR-0006）

- 形态 = 结构化 JSONL（宿主机文件 + 轮转），**非** SQLite 证据存储、无 CLI 查询面、不接 OTel。
- **范围**：六工具全记 + 认证失败/权限拒绝 + key 生命周期（CLI 侧一行）。
- **字段契约**：时间、用户、key、工具、参数（execute_sql 的 SQL 原文入库、不脱敏）、分阶段耗时（认证→权限→解析→执行→返回）、状态（成功/拒绝/超时/解析失败）、行数、truncated、plan_id（透传）、被拒原因。
- **保留期**：原始 ~7 天轮转 + 聚合摘要 ~30 天（聚合摘要喂知识采集信号：被拒查询/原料路径/搜索关键词）。
- 三用：排障（statement_timeout 自愈链的"刚才是哪个查询"）+ tracing（分阶段耗时）+ 知识采集信号。

### 4.7 知识采集（ADR-0007）

- **分工**：结构知识自动（采集器），语义知识人工（Agent 起草 + 服务负责人确认）——Agent 是语义起草者与审查者，不是结构生产者。
- **结构源**：服务仓库 migration 文件为**主干**（golang-migrate v4.19.1、纯 SQL up/down、目录命名统一、可全量重建）+ GORM 模型交叉验证 + `calibrate` 子命令按需连只读从库做生产校准（共享只读角色，v1 低优先）；采集解析**生产形态**（每服务一库/schema 前缀），不以 docker-compose 布局为准。
- **采集器** = 独立 Go CLI（与同步管线同仓），确定性可测（neo-cloud 真实迁移语料即 golden test 集）；触发 = **手动 on-demand**（无轮询/定时）；采集工作流 Skill（≤1 页）封装：跑采集 → 生成 YAML 草稿 → 引导人工 review → 合入后手动触发同步。
- **变更准入**：采集器生成 diff 建议 → 人工 review（PR）→ 合入；纯结构变更支持批量确认。
- **三层校验**：编译期（每次同步原子拒绝）+ dry-run diff（同步前展示）+ 漂移报告（手动 + 每周例行，YAML vs 真相源对照，只报告不改）。
- **回滚**：独立语义仓库 commit 即版本，revert + 重跑管线即回滚；墓碑传播删除。
- **排除**：API 定义（protobuf/OpenAPI）采集 v1 不做。

### 4.8 部署拓扑（ADR-0009）

- **形态**：内部开发机**单机 Docker**（compose 单服务；volume 三样：SQLite `/data`、执行记录 `/logs`、配置 env `/config` 0600；`restart: unless-stopped`）；不进生产集群。
- **凭证边界**：数据库凭证只存在该机 env 文件，开发机/CI 零凭证；网关/采集器只用专用共享只读角色，永不超管。
- **传输**：**双传输**——Streamable HTTP 为主（守护进程形态，go-sdk `RequireBearerToken` + 自实现 `TokenVerifier`），stdio 为调试形态（env 传 key）；官方 go-sdk v1.7.0+（双协议时代自动协商：2025-11-25 存量客户端走 legacy 握手、2026-07-28 客户端走无状态，DNS rebinding 防护、session hijack 防护开箱即用）。
- **从库连接**：可配置 DSN 口子（host/port/用户/密码）+ 按 dbname 路由（Database 实体 = PG database）；生产网络通路方案（NodePort/port-forward/LB）生产部署时定，v1 不锁。
- **启动自检**（不过拒启）：`pg_is_in_recovery() = true`（防连错主库）+ 角色级 statement_timeout 生效确认。
- **数据新鲜度**：接受从库异步复制延迟（秒级，v1 消费场景无感）；延迟监测后置。
- **监控/告警、正式上线/回滚流程**：v1 不做；观测面 = 执行记录 + docker restart 兜底；回滚基线 = 旧镜像 tag + SQLite 文件备份恢复。

### 4.9 负载防护与参数表（ADR-0010，env 可覆盖）

| 参数 | 默认值 | 说明 |
|---|---|---|
| 每 key 并发查询上限 | **2** | 超出结构化拒绝（不排队） |
| 进程级总并发上限 | **8** | 守护进程语义；stdio 调试形态下退化为每进程闸 |
| statement_timeout | **30s**（可配） | 网关连接级 + 角色级双设置 |
| SQL 行数默认上限 | **500** | 超出截断 + truncated 标记 |
| SQL 行数硬上限 | **5000** | 不可配置超过该值 |
| 语义列表返回上限 | **20** + total | search_entities 等 |
| 执行记录原始保留 | **7 天**轮转 | 可配 |
| 执行记录聚合摘要保留 | **30 天** | 可配 |
| embedding 模型 | OpenAI **text-embedding-3**（外部） | 元数据出机房已接受；回退路径 = 自托管 bge-m3 |
| 凭据前缀 | `dgw_` | 固定 |
| MCP 传输 | Streamable HTTP 主（bearer）/ stdio 调试 | 双形态 |

## 5. Testing Decisions

**测试哲学**：只测外部行为、不测实现细节；验收 = 半自动化重放脚本驱动（MCP 工具协议层），判定三件套客观可证。

**测试 seam（由高到低）**：

1. **验收套件（主 seam）**：官方 go-sdk 客户端打自己的网关——真实 MCP 协议往返（HTTP + stdio 双形态），覆盖主用例 + 用例矩阵 + 负向/边界例。用例兼作 golden 语料（知识采集回归用）。
2. **校验层 seam**：单元测试——AST 分类（非 SELECT 拒绝集）、表提取（CTE/子查询/join 全可见）、授权比对（白名单命中/未知表拒绝/通配展开快照）、限额包层（截断行为）、并发闸（每 key/进程级超限）。
3. **同步管线 seam**：dry-run diff 确定性测试（幂等 upsert/墓碑/全量重建，同输入同输出）。
4. **采集器 seam**：golden test——neo-cloud 真实迁移语料（10 个持库服务的迁移文件即测试集），GORM 交叉验证为第二道闸。

**验证内容**：§6 验收标准全文。

## 6. Acceptance Criteria（验收标准附录）

### 6.1 主用例「昨天支付失败率为什么上涨」全流程

Agent 自由组合工具完成（不预设 SQL）：`search_entities("支付失败")` 双入口定位 → `get_metric_definition` 取口径 + 带时间参数 dry-run 展开 → `execute_sql` 跑昨日趋势 → 下钻归因（多表 join / 窗口函数 / 时间窗口）。判定：三件套全过（§6.4）。

### 6.2 每服务用例矩阵（13 服务 × ≥2 简单 + ≥1 复杂，约 39+ 例）

- 覆盖 `~/cloud/neo-cloud` 全部 13 个后端服务（bss×5、iam×2、console-backend、notification、ops×2、usage-collection、dashboard-backend）、10 个持库。
- **简单用例**（每服务 ≥2）：单表查询、按时间窗口过滤、枚举/状态过滤、聚合统计等。
- **复杂用例**（每服务 ≥1）：核心链路多表 join + 聚合 + 时间窗口/窗口函数（覆盖校验层硬路径：子查询/CTE 的 AST 分类与授权展开）。
- 用例落 `docs/` 用例清单 + 验收脚本，兼作 golden 语料；v1 后按实际效果迭代、不冻结。

### 6.3 负向/边界 5 例

1. **未授权表拒绝**：无 grants 测试用户访问未授权表 → 结构化拒绝（错误区分"无权限表"）。
2. **非 SELECT 拒绝**：DML/DDL/COPY/utility 语句 → AST 分类拒绝。
3. **LIMIT 截断**：>500 行结果截断 + truncated 标记；>5000 硬上限行为正确。
4. **并发超限**：同 key 并发 >2 / 进程级 >8 → 结构化拒绝（不排队）。
5. **无指标原料路径**：无现成指标的领域，Agent 走表/列原料路径成功产出结果。

### 6.4 判定三件套（验收通过的必要充分条件）

- **(a) 数字一致**：每个用例结果与 psql 手工对照**逐项一致**。
- **(b) 执行记录可复现**：执行记录 JSONL 完整记录整条调用链（工具/参数/耗时/状态/行数），可从头重放复现。
- **(c) 零未授权访问**：全程（含负向例）零未授权访问；被拒原因如实落记录。

### 6.5 验收执行方式

半自动化重放脚本（网关拉起 → 用例按序重放 → 三件套逐项断言 → 输出报告）；demo = 30min 现场重放主用例 + 抽查用例矩阵；报告与执行记录留档评审。

## 7. Out of Scope

- 非 PostgreSQL 数据库支持
- 写操作 / DDL
- 独立 Agent 产品（工具层先行；工作台 = 阶段 4 终端形态）
- 跨会话记忆与自进化（长期愿景）
- 数仓 / OLAP 分析
- 网关侧 SQL 生成 `create_query_plan`（v2 规划引擎，选型延后；接口已冻结、plan_id 已预留）
- 列级/行级权限、脱敏/掩码、key 过期策略（阶段 3 权限优化）
- 管理工作台界面（阶段 3 管理面 + 阶段 4 工作台）
- 监控/告警、OTel、正式上线/回滚流程、复制延迟监测（阶段 2 运营化）
- Gitea label 触发增量采集、校准/漂移例行化（阶段 2）
- API 定义（protobuf/OpenAPI）采集、neo-cloud 之外采集源（阶段 3）
- PM 等非研发消费方接入（阶段 4）

## 8. Phase Plan 与 Further Notes

### 评审流程

评审对象 = 本文件（PR 形式）+ 30min 现场 demo；评审人 = 团队全体；门禁 = PR 意见清零 + 拍板人确认；阶段 2-4 各自入口**轻量评审**（一句话确认 + 变更记录），不再全量。

### 阶段 2：运营化

Gitea label 触发增量采集、校准例行化 + drift 每周例行、golden 回归自动化、监控/告警 + 复制延迟监测、生产网络通路正式进生产 + 全团队开发人员接入、执行记录超时/截断分布纳入监控。

### 阶段 3：能力深化

v2 规划引擎选型与落地（入口评审时开票）、权限优化（列级/掩码/RLS/key 过期）、工作台管理面（权限/API key/语义管理）、API 定义采集 + neo-cloud 之外采集源扩展。

### 阶段 4：工作台 + 开放

内置 Agent 查询界面、PM 等非研发消费方接入——终点形态为开发与产品共用工作台 Agent 界面；工具层始终可经 MCP/Skill 独立集成；内置 Agent 编排形态（自研 vs 接现成）阶段 3 末再定。

### 环境事实与假设（来自票据 08 探查）

- neo-cloud：13 个 Go 服务（Kratos v2.9）、10 个持库、**每服务一库**（同一 CNPG 集群、一主两从）、golang-migrate v4.19.1 统一、GORM v1.31.1（生产无 AutoMigrate）、枚举 = CHECK 约束（无 CREATE TYPE）、COMMENT ON 极少（仅 subscription 服务）、TimescaleDB（iam-audit/bill）；docker-compose 开发布局（单库）与生产不一致——采集器解析生产形态。
- 假设：neo-cloud 即全部生产服务源（若非，采集源扩展为多仓库，阶段 3）。
- 里程碑制：无日历日期，按阶段推进。

### 术语

领域术语以 [CONTEXT.md](../CONTEXT.md) 为准（语义层/本体/指标/校验层/负载防护/执行记录/授权/凭据/墓碑/采集器/同步管线…）。
