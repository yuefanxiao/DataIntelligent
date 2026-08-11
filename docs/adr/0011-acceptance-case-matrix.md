# 验收用例矩阵落地：语义环境、fixture 形态与无指标领域隔离（issue #31，v1 build 14）

build 14 = 主用例 + 13 服务用例矩阵 + 全量验收（spec §6.1/§6.2/§6.5）。
验收环境从 build 12 的「无语义数据」升级为「带语义数据 + 矩阵 fixture」，
这带来四个必须显式决策的问题：验收环境的语义数据从哪来、矩阵用例查什么
数据、负向例 5（无指标原料路径）在语义环境里怎么保真、无持库服务的用例
形态。本 ADR 记录四个决策（实现 = deploy/accept/{run.sh,cases.yaml,fixture.sql}
+ docs/acceptance-cases.md）。

## 决策 1：验收环境同步真实语义数据（samples/semantic 全量）

验收环境的运行时语义存储 = `dgw semantic-sync --dir samples/semantic` 同步
的全量数据（13 服务 / payment_failure_rate 指标 / 概念 / 枚举）。理由：

- 主用例的检索与 dry-run 展开是「语义数据上的外部行为」——用真实交付的
  语义语料才测得到真实行为；用最小 fixture 会测出「另一个系统的行为」；
- 用例兼作 golden 语料（09 采集回归复用），语料即验收所跑的数据；
- 无 DGW_OPENAI_API_KEY → 向量通道不配置 → 检索 = 纯关键词通道
  （确定性：无向量排序噪声，命中数与顺序稳定）。

## 决策 2：fixture 表建在 public schema（FQN = 服务.库.表）

矩阵 execute_sql 用例查的表（payment_orders / bills / audit_logs …）与
samples/semantic 结构一致，但建在**各库 public schema**（与 demo
orders/big_events 同构）。理由：

- v1 校验层 FQN 映射 = 服务.库.表，只解析未限定/public 引用（execute_sql.go
  resolve：非 public schema 引用 = unknown_table 拒绝）——bss 域真实生产的
  同名 schema 前缀（bill/wallet/…）在 v1 执行路径不可达；
- pg_schema 是语义元数据（get_entity 展示用），不参与执行；验收环境的
  部署形态差异（public vs schema 前缀）由 provisioning 覆盖（生产形态），
  验收执行形态保持与 demo 一致。

权衡：验收环境与生产形态存在 schema 差异，但 v1 执行路径本就只认
public——差异是「v1 简化」的如实反映，而非验收造假。未来 v1 执行路径
支持 schema 前缀时，fixture 换 schema 前缀 + provisioning 覆盖即可。

## 决策 3：neg-005 换「无指标领域」fixture（退款域）

build 12 的 neg-005 依赖「验收环境语义存储为空」（搜索零命中）——语义
环境落地后该前提消失（「支付失败」已是指标）。改为**退款域**：metrics.yaml
仅 payment_failure_rate 一个指标，`search_entities(type=metric, 退款)` 零
命中（2 字符查询走 LIKE 兜底，指标实体名/描述/FQN 均无「退款」）→ Agent
走表/列原料路径直查 refund_orders 成功。隔离策略 = 依赖「退款域无指标」
这一 fixture 事实（新增退款指标时换域），不依赖全局空存储（README 记录）。

## 决策 4：无持库服务的用例 = 语义元数据用例

dashboard-backend / ops-operation / usage-collection 无库无表，矩阵以
**语义元数据用例**覆盖（每服务 3 例）：服务实体可达（get_entity
kind=service）+ 描述精确断言（语义内容抽查）+ 拓扑如实（traverse_relations
contains 边为空，只返回起点）。理由：矩阵要求覆盖 13 个服务（spec §6.2），
无持库服务的「可测面」就是语义元数据；用例在验收环境与真实环境（语义
同步后）行为一致，兼作采集回归的实体存在性断言。

## Considered Options

- **验收语义数据：samples/semantic 全量 vs 最小 fixture**：最小 fixture =
  只有主用例指标，快但测不到全服务语义（矩阵的语义用例无从谈起）、golden
  语料也缩水；全量 = 真实交付物，一次到位。→ 全量（决策 1）。
- **fixture 表 schema：public vs bss 域同名 schema 前缀**：schema 前缀 =
  与生产形态一致，但 v1 校验层解析不到（unknown_table），需要改执行路径
  （超 scope）；public = 与 demo 一致、零执行路径改动。→ public（决策 2）。
- **neg-005：保持空语义存储 vs 无指标领域 fixture**：空存储 = 与「验收带
  语义数据」矛盾（主用例与 neg-005 不可兼得）；无指标领域 = 两全。
  → 退款域（决策 3）。
- **无持库服务：矩阵排除 vs 语义元数据用例**：排除 = 矩阵只有 10 服务，
  spec §6.2「覆盖 13 服务」不闭合；语义元数据用例 = 闭合且兼作采集回归。
  → 语义元数据用例（决策 4）。

来源：issue #31（2026-08-12），实现见 deploy/accept/ 与 docs/acceptance-cases.md。
