# 验收重放框架（v1 build 12 起；build 14 = 主用例 + 13 服务用例矩阵 + 全量验收）

spec §5 测试决策**主 seam** 的落地：官方 go-sdk 客户端打自己的网关——HTTP
（Streamable HTTP + bearer）与 stdio（拉起 `dgw serve-stdio`）双形态真实
MCP 往返；用例按序重放 → 判定三件套逐项断言 → 报告留档（30min demo 与
团队评审用）。只测外部行为（工具协议层）、不测实现细节（spec §5 测试哲学）。

build 14 起验收环境带语义数据（run.sh semantic-sync 同步 samples/semantic
进运行时）与矩阵 fixture（fixture.sql），用例 52 条 = 主用例 + 13 服务
矩阵（39 例）+ 负向/边界 5 例 + 正向基础例；人读清单见
docs/acceptance-cases.md。

## 一次运行

```sh
cd deploy/accept
./run.sh
```

`run.sh` 全自动编排（约 5-8 分钟，取决于镜像拉取）：

1. 起 demo 主从 PG（独立 compose project `dgw-accept`，与 demo 栈隔离；
   镜像拉不下来时 `DGW_PG_REGISTRY=docker.1ms.run/library ./run.sh`）
2. 建 10 个持库 + 演示数据（orders 600 / big_events 6000 / iam.users）+
   矩阵 fixture（fixture.sql：13 服务矩阵用例的表与确定性数据）+ 真实
   provisioning（`deploy/provisioning/readonly-role.sql`，共享只读角色，
   `GRANT SELECT ON ALL TABLES` 一次覆盖 fixture 表）
3. 凭据/授权：dev-alice（主用户 + 矩阵 27 表）/ ghost（无 grants）/ p1-p5
   （并发探测）；语义数据同步（semantic-sync samples/semantic → 运行时；
   主用例检索/口径 dry-run 依赖；无向量通道 = 纯关键词检索，确定性）
4. 硬上限配置边界：`DGW_SQL_LIMIT=5001` 拒启（§4.9「不可配置超过」）
5. **HTTP 形态**（限 500）：用例重放 + 三件套 + JSONL 重放复现
6. **stdio 形态**（限 5000）：用例重放 + 三件套 + JSONL 重放复现（trunc-002
   「>5000 硬上限行为正确」在此形态覆盖）
7. 报告留档 `deploy/accept/reports/`（run 转写 + 每形态报告 + 重放报告）

## 目录结构

| 文件 | 说明 |
|---|---|
| `cases.yaml` | 用例定义（工具调用 + 期望断言）；build 14 = 主用例 + 13 服务矩阵 + 负向/边界 |
| `fixture.sql` | 矩阵 fixture（build 14）：10 个持库的真实表名 + 确定性数据（相对 now()） |
| `accept.go` | 重放 harness（官方 go-sdk 客户端；断言与报告；不含业务断言逻辑） |
| `run.sh` | 编排：PG 拉起 → fixture + provisioning → 语义同步 → 授权 → 网关拉起（双形态）→ 重放 → 三件套 → 报告 |
| `reports/` | 报告留档（gitignore；每次运行按时间戳归档） |

## 判定三件套（spec §6.4）

| 判定 | 实现 |
|---|---|
| (a) 数字一致 | `psql_compare` 用例：结果与 psql 同库同 SQL（同一共享只读角色、同一从库）逐项一致——按列类型归一化（int/numeric/text/timestamptz/bool/uuid/json…）；截断用例用同样的有界查询（LIMIT row_count）对照「返回的行与 psql 一致」 |
| (b) 执行记录可复现 | 网关 JSONL 完整记录整条调用链（工具/参数/耗时/状态/行数）与 harness 观测逐调用对照；chain 快照（`<报告>.chain.jsonl`）从头重放 → 同状态同行数同原因 |
| (c) 零未授权访问 | 全程无 auth_failure；被拒记录原因如实（reject 非空）；`permission_denied` 只出现在预期负向例上（数量精确） |

**（b）的已知边界**：执行记录字段契约（execrecord.ToolCall）不含语义
工具的结果内容（search_entities 的 hits 等）——重放对语义工具记录只
复现状态（主运行断言仍逐项校验结果）；并发用例的拒绝依赖时间窗口，
顺序重放不可复现，标记 `replay_skip` 跳过状态比对（执行记录仍在链上，
完整性照查）。

## 负向/边界 5 例（spec §6.3）

| 用例 | 断言 |
|---|---|
| `neg-001a/b/c` 未授权表拒绝 | ghost（无 grants）/ dev-alice（有角色读权无表授权）→ `permission_denied`/`not_granted`；非 public schema 引用 → `unknown_table`——「无权限表」的两种形态机器可区分 |
| `neg-002` 非 SELECT 拒绝 | DML/DDL/COPY/utility 六句 → `invalid_request`/`non_select`（AST 分类拒绝） |
| `trunc-001/002` LIMIT 截断 | >500 截断（HTTP 网关不设 DGW_SQL_LIMIT，走 §4.9 默认值路径）+ >5000 硬上限（stdio 限 5000）+ truncated 标记 + 有界查询 psql 对照；配置 5001 拒启 |
| `conc-001/002` 并发超限 | 同 key >2 / 进程级 >8 → `rate_limited`（key/process_concurrency_limit），不排队快速失败（`reject_within_ms` 断言被拒调用毫秒级返回；慢查询持闸 2s 保证窗口内确定性） |
| `neg-005` 无指标原料路径 | 无现成指标（search 零命中）→ 走表/列原料路径直查成功产出结果。build 14 换「无指标领域」fixture：验收环境带语义数据后「支付失败」已是指标；改查退款域（metrics.yaml 仅 payment_failure_rate 一个指标，`search_entities(type=metric, 退款)` 零命中）→ 直查 refund_orders 成功。隔离策略 = 依赖「退款域无指标」的 fixture 事实（新增退款指标时需换域），不依赖全局空存储 |

## 形态覆盖

stdio = 单 key 单进程（`serve-stdio` 的架构约束）：多身份用例（ghost、
进程级并发）仅 HTTP；其余用例双形态覆盖。并发用例标记 `replay_skip`
（顺序重放无法复现并发拒绝——闸在时间窗口内饱和；执行记录仍在链上，
完整性照查）。

## 主用例与 13 服务矩阵（build 14）

- 主用例「昨天支付失败率为什么上涨」= cases.yaml `main-001`（5 步流程，
  详见 docs/acceptance-cases.md §1）；
- 13 服务矩阵 = 10 个持库服务 × 3 条 execute_sql 用例 + 3 个无持库服务
  × 3 条语义元数据用例（服务实体可达 + 拓扑如实，口径见清单 §2.3）；
- 复杂用例覆盖校验层硬路径：CTE/子查询的 AST 分类与授权展开
  （bill-003 / audit-003 / main-001 步骤 5 等）；
- 全部成功 SQL 用例 `psql_compare: true`（三件套 (a) 数字一致）。

## 方案取舍（为何不是其他形态）

| 候选 | 排除理由 |
|---|---|
| go test 包（`go test ./accept/...`） | 验收 = 四趟运行（http/stdio × 主运行/重放）+ 报告留档，需要进程级退出码契约与报告产物；go test 的缓存/副作用语义会打架。主 seam 是协议层真实往返，不是单元测试 |
| shell 断言 | 逐列类型归一化对照（big.Rat/timestamptz 布局/jsonb 语义相等）、并发多集、JSONL 匹配在 shell 里不可实现；shell 只做编排（run.sh），断言留在 Go |
| Go 代码内嵌用例 | build 14 的 39+ 用例矩阵是数据不是代码；用例兼作 golden 语料（§6.2），YAML 可被采集回归直接复用 |
| 直接跑真实 ~/cloud/neo-cloud | 生产凭证/拓扑不进验收环境；用 demo 主从 PG + 伪造数据（orders/big_events/users），形态与生产一致（共享只读角色、从库、statement_timeout），数字可验证 |

**与 mcp-ping 的关系**（issue #10 交 #29 收敛）：`deploy/demo/mcp-ping.go`
保留为 demo 的单发探针（setup.sh 冒烟用，一次一查询）；验收职责由本框架
承接（用例重放 + 三件套 + 报告）。两者共用同一 go-sdk 客户端接线（bearer
transport 三行样板，不抽公共包——demo 与验收是不同目录的独立 main）。

## 加用例（build 14 矩阵已落地）

编辑 `cases.yaml` 增条目即可，harness 不改。字段见文件头注释；要点：

- `modes: [http, stdio]` 双形态 / `[http]` 仅 HTTP；
- execute_sql 成功用例加 `psql_compare: true` 自动对照；
- 语义结果用 `paths: [{path: total, eq: 0}]` 做 JSON 路径断言；
- 多语句同期望用 `sqls`；流程用 `steps`；并发用 `concurrency` + `keys`；
- 新表用例：先在 `fixture.sql` 建表（public schema + 相对 now() 的确定性
  数据），run.sh 授权列表补 FQN，语义数据里需有对应表实体（否则授权
  warnStaleGrants 告警）；
- 时间窗口用例统一 `[date_trunc('day', now()) - N day, date_trunc('day', now()))`
  半开区间（排除今日，跨天重跑窗口自动前移）。

用例兼作 golden 语料（09 采集回归复用，spec §6.2；契约见
docs/acceptance-cases.md §5）。

## 手动分段跑

```sh
# 只重放（网关已在跑；HTTP）
go run accept.go --mode http --addr http://127.0.0.1:8080 \
  --keys dev-alice=KEY,ghost=KEY,p1=KEY,... --log-dir /path/logs \
  --cases cases.yaml --report /tmp/report.md \
  --psql-prefix docker,compose,-f,/abs/docker-compose.pg.yml,-p,dgw-accept,exec,-T,pg-replica,psql,-U,dgw_reader,-t,-P,format=csv
# 重放复现（JSONL 调用链 → 新会话重放；链 = <报告>.chain.jsonl 快照）
go run accept.go --mode http --addr ... --keys ... \
  --replay-from /path/report.md.chain.jsonl --cases cases.yaml --report /tmp/replay.md
# 或直接对网关执行记录目录重放（全量 raw-*.jsonl）
go run accept.go --mode http --addr ... --keys ... \
  --replay-from /path/logs --cases cases.yaml --report /tmp/replay.md
```
