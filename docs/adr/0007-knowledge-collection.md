# 知识采集与保鲜：结构自动 + 语义人工 + 采集器 CLI 与采集工作流 Skill

v1 知识采集 = **混合分工**：结构知识（表/列/类型/主外键/CHECK 枚举/索引）**自动采集**——以服务仓库 migration 文件为**主干源**（事实：neo-cloud 10 个持库服务全部 golang-migrate v4.19.1、纯 SQL up/down、目录命名统一、squash+增量纪律、可全量重建），GORM 模型作**交叉验证**，`calibrate` 子命令按需连只读从库做**生产校准**（共享只读角色，v1 低优先）；语义知识（描述/业务概念/指标口径/服务↔库映射）**人工维护**（05 已定人工权威），Coding Agent 经**采集工作流 Skill** 起草语义、抽查 CLI 输出，输出永远经人工确认才入 YAML——**Agent 是语义起草者与审查者，不是结构生产者**。

采集器 = 独立 Go CLI（`dgw-collect`，与同步管线同仓）；Skill（≤1 页）封装工作流：跑采集 → 生成 YAML 草稿 → 引导人工 review → 合入后由 CLI/CI 触发同步。增量触发 v1 = **手动 on-demand**；Gitea 后置（合入 main 的 PR 带约定 label → 自动触发增量更新）。变更准入 = 采集器生成 **diff 建议 → 人工 review（PR）→ 合入**，纯结构变更支持批量确认。校验三层：编译期（每次同步，原子拒绝）+ dry-run diff（每次同步前）+ **漂移报告**（手动 + 每周例行，YAML vs 真相源，只报告不自动改）。回滚 = 作者入口落**独立语义仓库**（内部 Gitea，按服务拆 YAML），commit 即版本机制，revert + 重跑管线即回滚，运行时 SQLite 可从 YAML 全量重建，墓碑传播删除。API 定义（protobuf/OpenAPI）采集 **v1 排除**。来源：票据 08（issue #9），2026-08-11 拍板。

## Considered Options

- **采集分工：全自动 vs 全人工 vs 混合**：全自动把语义（描述/概念/口径）也交给机器——违背 05「YAML 作者入口 + 指标沉淀=人工确认」的口径权威；全人工则结构部分（10 个服务、数百张表）成本不可接受。→ 混合：结构自动、语义人工（+ Agent 起草辅助）。
- **结构源：生产 introspection vs 服务仓库文件解析 vs 两者**：introspection 永远真实、一库覆盖全，但采集器必须持有生产只读凭证、且每次采集触碰生产；事实（neo-cloud 探查）显示 migration 文件统一（golang-migrate v4.19.1、squash 纪律）可高置信重建，但存在手工 DDL 先例（payment_channel 事件）与迁移事后改写——文件非绝对真相。→ 文件为主干（零凭证、可 CI、diff 可 review）+ 按需校准子命令兜底（共享只读角色，低优先）。
- **采集器形态：独立 Go CLI vs Skill 内嵌脚本 vs CLI + Skill 封装**：CLI 是确定性函数（同输入同输出，golden test 可验证——neo-cloud 真实迁移语料即测试集）；Agent/脚本是非确定性（不可断言对错、不可复用、CI 无法挂载）。→ CLI 为主 + Skill 封装：机械逻辑进 CLI（可测、可 CI 复用、Gitea 触发可直接调），Skill 只做工作流引导。
- **Agent 在采集中的角色：采集执行者 vs 语义起草者 + 审查者**：Agent 直接读文件产 YAML 会放弃确定性验证链（golden test/交叉验证/校准全部失效）；但 CLI 干不了语义——Skill 引导 Agent 读服务代码（常量/使用处/接口注释）起草描述与枚举含义、抽查草稿 vs 源文件（QA 角色，如「migration 12 张表 vs 草稿 11 张」）。→ 语义起草 + 审查，输出永远经人工确认。
- **增量触发：定时 vs 事件 vs 手动 + 后置 Gitea**：定时是盲采（无变更也跑）；事件（服务发布 hook/webhook）要动服务发布链路；手动 on-demand 与 03 新表告警合流（采集 diff 即告警信号）。→ v1 手动 CLI；Gitea label 触发（合入 main 的 PR 带 label → 增量更新）后置排期。
- **变更准入：直接自动改写 YAML vs diff 建议 + 人工 review**：自动写入绕过「YAML 是口径权威 + git review」（07）且让回滚失去抓手。→ diff 建议 + PR review，纯结构变更批量确认（负担≈零）。
- **校验时机：仅编译期 vs 三层**：编译期（每次同步：FQN 唯一/引用完整性/指标 SQL 可解析/枚举合法，失败原子拒绝）+ dry-run diff（同步前展示增删改）已由 07 定；漂移报告（YAML vs 真相源对照，漏采/过期/漂移清单）是新层——「校验自动采集没采错」的闭环（人工语义会过期），只报告不自动改。→ 三层全要；drift 手动 + 每周例行。
- **回滚：git revert + 全量重建 vs 运行时快照**：作者入口独立仓库版本化（commit/revert/review），运行时 SQLite 可从 YAML 全量重建（ADR-0005 备份=文件拷贝），墓碑传播删除——运行时快照是重复机制。→ revert + 全量重建，不引入额外机制。
- **API 定义采集（protobuf/OpenAPI）进 v1 vs 排除**：v1 消费场景是「安全查数」，API 定义是服务拓扑延伸且各服务协议栈异构。→ v1 排除，列入 Not yet specified。
- **文件静态解析 vs scratch-PG 回放（09 实现后补记）**：把迁移链应用到一个临时 PG 再 introspection 是「可全量重建」最忠实的读法——零生产凭证、无解析盲区（RENAME/分区/引擎语义全覆盖），代价是采集器要起 PG 且运行慢。v1 采用文件静态解析（pg_query WASM）：零依赖、确定性、golden 可直接断言；解析盲区（动态 DO 块 DDL 等）由 GORM 交叉验证 + calibrate 兜底（payment_channel 先例已被门禁实证捕获）。scratch-PG 回放列入后置评估。

## Consequences

- 采集器与同步管线同仓维护（共享 FQN/枚举/校验代码），真实语料 golden test（neo-cloud 10 个持库服务的迁移文件即测试集）是采集器正确性的第一道闸；GORM 模型交叉验证免费第二道。
- 校准子命令是「同步管线零生产凭证」（ADR-0002）的明确例外：按需、只读、用 03 共享只读角色连只读从库，凭证与部署位置归 11；常规采集路径不触碰生产。
- 与 03 新表告警合流：采集 diff 发现新表 → 变更建议 → 确认入 YAML → 授权重展开告警（新表默认拒绝不变）。
- 04 执行记录信号（被拒查询/原料路径/搜索关键词）是语义补全与漂移报告的输入；drift 报告同时是「无描述表/列」的产出面。
- 环境事实修正（neo-cloud 探查）：13 个 Go 服务（Kratos v2.9）、10 个持库、**每服务一库**（同一 CNPG 集群、一主两从）、golang-migrate v4.19.1 统一、GORM v1.31.1（生产无 AutoMigrate）、枚举=CHECK 约束（无 CREATE TYPE）、COMMENT ON 极少（仅 subscription 一服务）、TimescaleDB（iam-audit/bill）；docker-compose 开发布局（单库）与生产不一致——采集器解析生产形态（每服务一库/schema 前缀），不以 compose 为准。假设：neo-cloud 即全部生产服务源，若非，采集源扩展为多仓库。
- 输出：11（校准凭证位置/采集器部署）、12（golden test 语料、drift 例行、Gitea 触发排期）；ADR-0002/0005 的同步管线决策原样有效，本 ADR 不修正。
