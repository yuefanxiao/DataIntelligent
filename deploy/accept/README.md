# 验收重放框架（v1 build 12）

spec §5 测试决策**主 seam** 的落地：官方 go-sdk 客户端打自己的网关——HTTP
（Streamable HTTP + bearer）与 stdio（拉起 `dgw serve-stdio`）双形态真实
MCP 往返；用例按序重放 → 判定三件套逐项断言 → 报告留档（30min demo 与
团队评审用）。只测外部行为（工具协议层）、不测实现细节（spec §5 测试哲学）。

## 一次运行

```sh
cd deploy/accept
./run.sh
```

`run.sh` 全自动编排（约 3-5 分钟，取决于镜像拉取）：

1. 起 demo 主从 PG（独立 compose project `dgw-accept`，与 demo 栈隔离；
   镜像拉不下来时 `DGW_PG_REGISTRY=docker.1ms.run/library ./run.sh`）
2. 建库建表 + 演示数据（orders 600 / big_events 6000 / iam.users）+ 真实
   provisioning（`deploy/provisioning/readonly-role.sql`，共享只读角色）
3. 凭据/授权：dev-alice（主用户）/ ghost（无 grants）/ p1-p5（并发探测）
4. 硬上限配置边界：`DGW_SQL_LIMIT=5001` 拒启（§4.9「不可配置超过」）
5. **HTTP 形态**（限 500）：用例重放 + 三件套 + JSONL 重放复现
6. **stdio 形态**（限 5000）：用例重放 + 三件套 + JSONL 重放复现（trunc-002
   「>5000 硬上限行为正确」在此形态覆盖）
7. 报告留档 `deploy/accept/reports/`（run 转写 + 每形态报告 + 重放报告）

## 目录结构

| 文件 | 说明 |
|---|---|
| `cases.yaml` | 用例定义（工具调用 + 期望断言）；build 14 的 13 服务用例矩阵只增条目 |
| `accept.go` | 重放 harness（官方 go-sdk 客户端；断言与报告；不含业务断言逻辑） |
| `run.sh` | 编排：PG 拉起 → 网关拉起（双形态）→ 重放 → 三件套 → 报告 |
| `reports/` | 报告留档（gitignore；每次运行按时间戳归档） |

## 判定三件套（spec §6.4）

| 判定 | 实现 |
|---|---|
| (a) 数字一致 | `psql_compare` 用例：结果与 psql 同库同 SQL（同一共享只读角色、同一从库）逐项一致——按列类型归一化（int/numeric/text/timestamptz/bool/uuid/json…） |
| (b) 执行记录可复现 | 网关 JSONL 完整记录整条调用链（工具/参数/耗时/状态/行数）与 harness 观测逐调用对照；`--replay-from` 形态从 JSONL 读调用链、新会话从头重放 → 同状态同行数 |
| (c) 零未授权访问 | 全程无 auth_failure；被拒记录原因如实（reject 非空）；`permission_denied` 只出现在预期负向例上（数量精确） |

## 负向/边界 5 例（spec §6.3）

| 用例 | 断言 |
|---|---|
| `neg-001a/b` 未授权表拒绝 | ghost（无 grants）与 dev-alice（有角色读权无表授权）→ `permission_denied`/`not_granted`（错误区分「无权限表」） |
| `neg-002` 非 SELECT 拒绝 | DML/DDL/COPY/utility 六句 → `invalid_request`/`non_select`（AST 分类拒绝） |
| `trunc-001/002` LIMIT 截断 | >500 截断（限 500 形态）+ >5000 硬上限（限 5000 形态）+ truncated 标记；配置 5001 拒启 |
| `conc-001/002` 并发超限 | 同 key >2 / 进程级 >8 → `rate_limited`（key/process_concurrency_limit），不排队快速失败（慢查询持闸 2s 保证窗口内确定性） |
| `neg-005` 无指标原料路径 | 无现成指标（search 零命中）→ 走表/列原料路径直查成功产出结果 |

## 形态覆盖

stdio = 单 key 单进程（`serve-stdio` 的架构约束）：多身份用例（ghost、
进程级并发）仅 HTTP；其余用例双形态覆盖。并发用例标记 `replay_skip`
（顺序重放无法复现并发拒绝——闸在时间窗口内饱和；执行记录仍在链上，
完整性照查）。

## 加用例（build 14 矩阵）

编辑 `cases.yaml` 增条目即可，harness 不改。字段见文件头注释；要点：

- `modes: [http, stdio]` 双形态 / `[http]` 仅 HTTP；
- execute_sql 成功用例加 `psql_compare: true` 自动对照；
- 语义结果用 `paths: [{path: total, eq: 0}]` 做 JSON 路径断言；
- 多语句同期望用 `sqls`；流程用 `steps`；并发用 `concurrency` + `keys`。

用例兼作 golden 语料（09 采集回归复用，spec §6.2）。

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
