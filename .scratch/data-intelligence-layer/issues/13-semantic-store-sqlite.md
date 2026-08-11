# 13 语义层运行时存储与检索：SQLite 轻量版

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/14
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/14#issuecomment-5252962691
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

## Resolution（2026-08-11）

修正票据 07 的存储引擎决定：语义层运行时存储与检索 = **SQLite 轻量版**；生产查询目标不变 = PostgreSQL 只读从库（本票不涉及查询目标扩展）。

1. **向量实现 = sqlite-vec（SQL 内 KNN）**（拍板人选择，非默认推荐）——关键事实核查：modernc.org/sqlite **v1.47.0（2026-03-17）起内置 sqlite-vec 的 CGO-free 移植**（`import _ "modernc.org/sqlite/vec"`，v1.50.0 升级到 sqlite-vec v0.1.9），选 sqlite-vec **不需要** CGO、不需要加载 .so，「纯 Go 无 CGO」前提保持；`vec0` 虚拟表把 KNN 收进 SQL 面（`vec_distance_*`）。退路：sqlite-vec 卡住时 Go 内存暴力余弦作实现层替换，检索接口不变。
2. **SQLite 是 v1 唯一运行时实现**——检索实现只有一套（五条原语 + RRF）；PG 版不写代码，仅作多机/HA 升级路径记录（存储/检索接口抽象保留，切实现 + 数据迁移）。
3. **审计日志方向只留输入，04 拍板**：审计是相对同步管线的第二写者（网关进程写），SQLite WAL 支持多进程读写但写写互斥，10^5 行/日量级无压力；04 决议时评估「审计落 SQLite（同机单文件）」vs「落 PG 从库」。

- 交付物：docs/adr/0005-semantic-store-sqlite.md（新 ADR）；ADR-0002 顶部加修正指针（07 检索/作者入口/同步管线决策原样有效）；CONTEXT.md「运行时存储」术语改为 SQLite 单文件 + 升级路径
- 下游推论：03 决议「授权数据编译进语义存储权限表」随载体修正落到 SQLite（同一同步管线写入，网关加载内存 + 热重载不变）；08/11 不硬编码存储载体，无需改票
- 不变项：YAML 作者入口 + 同步管线（幂等 upsert/墓碑/dry-run）+ 五条原语 + RRF（关键词优先）+ OpenAI text-embedding-3 + 不引入图数据库；生产查询目标仍为 PG 从库
