# 语义层本体模型：UModel 图谱骨架 + OSI 语法形状的混合形态

v1 语义层本体采用混合形态：UModel 式 sets-and-links 对象图谱承载「服务↔库↔表↔列↔业务概念」拓扑（管"找什么"），OSI（Open Semantic Interchange）式声明式 SQL 指标挂载其上（管"怎么算"），自研补充枚举取值语义与服务↔库映射。Agent 通过工具发现循环（P2 为主 + 模糊检索 P3 补充）消费本体，指标与业务概念都是可检索的一等实体（双入口推理）。来源：票据 05（issue #6），2026-08-11 拍板。

## Considered Options

- **纯 UModel 式对象图谱**（阿里 UnifiedModel 元模型）：图谱/拓扑形态最贴、agent 推理范式（图上找线索、存储取证据）直接可抄；但无声明式指标口径层（指标只是标签+聚合器），支付成功率这类公式指标无法权威表达。
- **纯 MDL/OSI 式表中心模型**：指标口径强（SQL 表达式、同义词一等字段）、dbt v1.12+ 可原生解析；但无服务拓扑语义，Agent 无法"从服务找数据"，多服务/多库的企业语义缺失。
- **混合（选定）**：图谱管"找什么"（拓扑、概念、关系遍历），指标管"怎么算"（SQL 表达式口径）；两者靠 `describes` 关系衔接，`search_entities` 双入口命中。

## Consequences

- 本体 YAML 分拓扑层（UModel 式 entity_sets/links）与语义层（OSI 式 datasets/metrics/concepts）；**枚举取值语义**（挂列）与 **is_time 时间轴标注**是自研补充（OSI/MDL 均无）。
- 指标表达式保持机器可读（OSI `expression` 字段），供票据 10 的 dry-run 校验与 SQL 规划直接使用。
- 六类实体需要稳定 FQN（服务.库.表.列 / 指标 / 概念），是票据 03 权限引擎的挂载点。
- 无现成指标时 Agent 走表/列原料路径自行组合 SQL；新指标经人工确认回写 YAML（指标沉淀），自动回写/跨会话自进化明确排除在 v1 外。
