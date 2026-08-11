# Agent Skill：经 MCP 网关查询生产数据（六工具）

连接形态：Streamable HTTP（bearer 凭据）或 stdio（`DGW_API_KEY` env）。六工具**全部只读**；语义元数据面（前五个）认证即读，业务数据面（`execute_sql`）默认拒绝、表级白名单授权。结果一律结构化 JSON；失败一律结构化错误 `{kind, code, message, details}`。

## 工具清单

| 工具 | 用途 | 语义与限制 |
|---|---|---|
| `search_entities` | 双入口关键词检索：按业务概念/指标定位实体（`type` 限定 concept/metric，缺省混合） | ≤20 条 + total |
| `get_entity` | FQN 精确查询单实体（含枚举挂列、is_time、关系摘要） | 单实体 |
| `traverse_relations` | 沿类型化关系边遍历：`connects_to`/`contains`/`references`/`describes`，`direction` out/in/both | `max_depth` 缺省 1、硬上限 5 |
| `get_metric_definition` | 读指标口径（expression+aggregation+filter 机器可读）；`time_range` 参数做 **dry-run 展开（不执行）** | 单指标 |
| `list_enum_values` | 查询列枚举取值（status 类字段业务含义） | 有界 |
| `execute_sql` | 只读 SQL 执行（校验层四段链：AST 分类→表授权→物理边界→限额包层） | 默认 500 / 硬上限 5000 行 + truncated；多库时 `dbname` 必填；并发闸每 key 2 / 进程级 8，超限快速拒绝不排队；statement_timeout 30s；`plan_id` 溯源透传 |

## 标准工作流：发现 → 解析 → 执行

1. **发现**：`search_entities` 按业务诉求搜（如"支付失败率"）→ 拿到实体 FQN；
2. **解析**：`get_entity` 看结构（列类型/枚举/is_time/关系摘要）→ `traverse_relations` 沿 `references` 找可 join 表 → 指标用 `get_metric_definition` 读口径并带时间参数 dry-run 展开出可执行 SQL；
3. **执行**：`execute_sql` 跑展开 SQL（指标路径）；或自由组合多表 SQL（原料路径）。

## 回退路径

- **无现成指标**：`search_entities` 搜不到指标 → 走表/列原料路径：`search_entities` type=concept 定位业务概念 → `get_entity` 看列 → `traverse_relations` 找 join → `execute_sql` 自行组合。探索出的口径可走指标沉淀（人工确认回写 YAML）。
- **被拒后调整**（按错误 `kind`；网关不重试、无自愈循环）：
  - `permission_denied`：表未授权 → 改用已授权表/口径，或请运维 `dgw grant-add`；
  - `rate_limited`：并发超限（每 key 2 / 进程 8）→ 稍后重试（不排队）；
  - `invalid_request`：看 `details.reason`——`syntax_error`/参数类 → 修正后重试；`timeout`（statement_timeout 30s 内未完成）→ 缩小时间窗/简化聚合后重试；
  - `internal`：服务端故障，调用方不可自愈 → 上报网关运维者。
