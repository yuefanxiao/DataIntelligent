# v1 build 14 — 主用例 + 每服务用例矩阵 + 全量验收

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/31
Status: closed（PR #45 合入 main + PR #46 收敛轮 2 补充合入，2026-08-12 关闭，COMPLETED）

## 来源

docs/spec.md §6.1/§6.2/§6.5；issue #15

## What to build

全量验收：主用例「昨天支付失败率为什么上涨」全流程（Agent 自由组合工具不预设 SQL：search_entities 双入口定位 → get_metric_definition 取口径 + 带时间参数 dry-run 展开 → execute_sql 跑昨日趋势 → 下钻归因：多表 join / 窗口函数 / 时间窗口）；每服务用例矩阵（13 服务 × ≥2 简单 + ≥1 复杂，约 39+ 例；简单 = 单表/时间窗口过滤/枚举状态过滤/聚合；复杂 = 核心链路多表 join + 聚合 + 时间窗口/窗口函数，覆盖校验层硬路径：子查询/CTE 的 AST 分类与授权展开）；用例落 docs/ 用例清单 + 验收脚本，兼作 golden 语料（知识采集回归用）；全量验收运行（12 框架重放全部用例）+ 判定三件套 + 报告留档。

## Acceptance criteria

- [x] 主用例全流程跑通（搜索→口径→趋势→下钻归因），判定三件套全过
- [x] 13 服务用例矩阵：每服务 ≥2 简单 + ≥1 复杂（39+ 例）落 docs/ 清单 + 脚本
- [x] 复杂用例覆盖校验层硬路径（子查询/CTE 的 AST 分类与授权展开）
- [x] 全量验收运行：全部用例三件套断言通过 + 报告留档
- [x] 用例兼作 golden 语料（09 回归复用）

## Blocked by

- #T12 — 验收重放框架 + 负向/边界 5 例 + 判定三件套
- #T13 — 全服务语义数据 + 主用例指标沉淀

## 交付摘要

1. **主用例 main-001**：6 步全流程（双入口定位按 FQN 断言 → 口径 + 带时间
   dry-run 展开全文精确断言 → 昨日趋势 → 下钻归因：CTE + 多表 join + 窗口
   函数 + 时间窗口），判定三件套全过。数据故事（fixture.sql）：此前 13 天
   3/60=5%，昨日 43/100=43% 激增，失败集中 channel=5 银行转账（failed
   3→40，delta=37）。
2. **13 服务用例矩阵 39 例**：10 持库服务 × 3（2 简单 + 1 复杂）+ 3 无持库
   服务 × 3（语义元数据用例）；复杂用例覆盖校验层硬路径（CTE = bill-003/
   main 步骤 6，子查询 = audit-003）。用例落 docs/acceptance-cases.md 清单
   + deploy/accept（cases.yaml + fixture.sql + run.sh），兼作 golden 语料。
3. **fixture.sql**：10 库 27 张真实表名 + 确定性数据（相对 now() 日边界
   锚定——行桶与运行时刻无关，全天稳定）；表建 public schema（FQN 服务.
   库.表，ADR-0011 记录）。
4. **run.sh 扩展**：10 库路由、fixture、semantic-sync（samples/semantic →
   运行时）、bss 域 public 补授、27 表授权 + 表名级自检（fixture 每张表
   必须有授权 FQN 后缀，防漂移 fail fast）、DGW_OPENAI_API_KEY 密闭（检索
   确定性）。
5. **neg-005 换域**：验收环境带语义数据后「支付失败」已是指标——换「退款
   域无指标」fixture（搜索零命中 → 直查 refund_orders 成功）。
6. **neg-006（收敛轮 2 新增）授权展开负向**：CTE 引用未授权表（iam.users）
   → not_granted——多表引用逐表展开、非只查首表（校验层硬路径的安全方向
   回归；授权若退化为只查首表，本用例立即从 not_granted 变成功暴露）。
7. **拓扑断言闭环（收敛轮 2）**：dash-003/ops-003/usage-003 补 `edges`
   空数组断言——仅 nodes[0].fqn 验不出「无 contains 边」（遍历节点按 FQN
   排序，起点恒排首位）；accept.go eqValue 新增数组分支 + 单测。
8. **accept.go**：date 类型 psql 对照归一化（pgx 固定 UTC 解码 → RFC3339
   vs psql YYYY-MM-DD）+ 单测。
9. **文档**：docs/acceptance-cases.md（用例清单 + golden 语料契约——复用
   前提如实化：execute_sql 断言依赖 fixture+语义环境）、ADR-0011（四决策）、
   README 更新（主用例 6 步、用例 53 条 = 主用例 1 + 矩阵 39 + 负向 6 +
   边界 4 + 正向基础 3）。
10. **评审**：code-review 两轴 + code-review-adversarial 4 角色（P1 时刻
   漂移经日边界锚定修复：audit-001 白天约 12 小时必挂 / wallet-003 UTC
   00:07-01:27 只返回 1 行——此前绿跑全落在 UTC 23:39-23:59 幸运窗口；
   修复后数学模拟 1440 运行时刻 × 22 项断言全恒定 + UTC 失败窗口实测
   复跑验证）。

## 验证

- 全量验收多次复跑全过（含 UTC 00:17-00:2x 原必失败窗口）：HTTP/stdio
  双形态全过、37/37 psql 对照一致、重放复现通过、permission_denied=4
  （预期 4，neg-001a/b/c + neg-006 精确计数）、报告留档
  deploy/accept/reports/
- go test 全量 14 包全绿
- 用例行数断言全天稳定（日边界锚定，与运行时刻无关）：1440 运行时刻
  数学模拟 22 项断言全恒定 + 实跑验证
