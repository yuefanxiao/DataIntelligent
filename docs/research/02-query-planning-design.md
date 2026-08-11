# 查询规划（create_query_plan）设计草案 — 票据 10 的输入素材

> 来源：票据 02（issue #3）拷问过程中产出的「方案乙」设计。v1 决议**不做** create_query_plan；本草案记录网关侧规划的完整形态，供票据 10（SQL 生成路线决策：Wren 自托管 vs 自研 vs LLM+校验）决议后按需实现。

## 问题背景

决策讨论的编译器类比：`用户问题 → 语义解析 → 匹配语义模型 → 展开指标 → 生成 SQL → 执行`。「查询规划」就是中间三步——把业务诉求翻译成一段可执行只读 SQL。翻译发生在哪一侧，决定工具面：

- **Agent 侧规划（方案甲，v1 采纳）**：规划发生在 Coding Agent 自己的推理里，网关只供素材（search_entities / get_entity / traverse_relations / get_metric_definition / list_enum_values + execute_sql）。指标路径的「规划」已有等价物——`get_metric_definition` 的 dry-run 展开 = 确定性编译（公式 + 时间参数 → 可执行 SQL）；原料路径（无指标）的规划 = Agent 照 schema/join 边自己写 SQL。
- **网关侧规划（方案乙，本草案）**：网关自己生成 SQL，无论引擎是 Wren 自托管 / 自研 compiler / LLM+校验。

## 方案乙工具接口

```
create_query_plan(
  request: string,                      // 业务诉求，如「昨天支付失败率」
  time_range?: {from, to},              // 时间过滤是查询参数，不进指标定义
  filters?: [{column_fqn, op, value}],  // 附加过滤
) → {
  plan_id,                              // 幂等、可追溯
  intent,                               // 解析出的意图摘要
  resolved: [{fqn, kind, matched_by}],  // 语义解析命中链（概念→指标→表）
  sql,                                  // 生成的只读 SQL（引擎相关，票据 10 决定）
  tables: [fqn],                        // 触达的表/列 → 03 权限挂载点
  status: draft | validated
}

execute_plan(plan_id) → rows   // 或 execute_sql(plan_id, sql) 复用现有执行管线
```

## 设计要点

- **规划与执行分离**：规划时做权限预检（03 的 FQN 权限挂在 `tables` 列表上）、只读校验、成本估算；执行时凭 plan_id 走现有执行管线（限额/脱敏/审计全部复用，v1 工具面不破坏）。
- **审计天然串联**：plan_id 进审计记录，`resolved` 列表解释「这段 SQL 为什么长这样」——可解释性从根上解决。
- **引擎可插拔**：create_query_plan 内部是 10 决定的引擎，接口不变、引擎可换；若走 LLM 引擎，LLM 在网关侧调外部 API（票据 07 已接受元数据出机房，embedding 用 OpenAI text-embedding-3 先例）。
- **与 v1 的重叠**：指标路径的确定性展开已在 `get_metric_definition` 的 dry-run 里；create_query_plan 的增量价值在「多表 join + 自由诉求」路径——而这正是 10 的领地。

## 为什么 v1 不做（方案甲的理由）

1. 指标路径已有 dry-run 等价物；
2. 自由诉求规划 = 10 的决策领地，02 定会预判 10 的路线；
3. v1 消费方是 Coding Agent（自带 LLM），Agent 侧规划成本 ≈ 0；网关侧规划的服务对象（无 LLM 的 Agent、一致性管控）不是 v1 场景。

## 落地预留

`execute_sql` 带可选 `plan_id` 参数——v1 只当审计/溯源字段透传，不做校验。10 决议后加 create_query_plan 是纯增量：工具面不破坏、审计不重构。
