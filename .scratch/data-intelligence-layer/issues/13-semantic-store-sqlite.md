# 13 语义层运行时存储与检索：SQLite 轻量版

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/14
Status: open
Blocked by (open blockers): 0

Part of #1

## Question

修正票据 07 的存储引擎决定：语义层**运行时存储与检索**要一个 **SQLite 轻量版本**（单文件、零运维、本地/单机部署轻量跑起来）；**生产查询目标不变 = PostgreSQL 只读从库**（destination 不改，本票不涉及查询目标扩展）。

输入（2026-08-11 同场 grilling 已收敛）：

- 运行时存储 = SQLite 单文件（modernc.org/sqlite 纯 Go 无 CGO），WAL 模式，单写者（同步管线）+ 多读者（网关）；备份 = 文件拷贝
- 检索原语对照：FQN 精确 / 指标公式 / 枚举值 = SQLite 原生；边遍历 = WITH RECURSIVE 原生；关键词 = FTS5 前缀 + LIKE 兜底（10^5 行量级几十 ms）；向量 = Go 内存暴力余弦（1536 维 × 10^5 行 ≈ 几十 ms，零扩展依赖）或 sqlite-vec（SQL 内 KNN）
- 硬约束：单机部署；语义层多机/HA 需求出现时回 PG（升级路径记入 ADR）
- 不变项：YAML 作者入口 + 同步管线（幂等 upsert / 墓碑 / dry-run）+ 五条检索原语语义 + RRF 混合（关键词优先）+ OpenAI text-embedding-3 + 不引入图数据库；生产查询目标仍为 PG 从库

待决议时确认的默认项：

1. 向量实现：Go 暴力余弦（推荐，零依赖）vs sqlite-vec 扩展（SQL 内 KNN）
2. SQLite 是 v1 唯一运行时实现（推荐，避免维护两套检索实现；PG 版仅作为多机/HA 的升级路径记录在 ADR）还是 PG 版并行保留
3. 审计日志存储是否也 SQLite（联动票据 04，本票只留输入不拍板）
