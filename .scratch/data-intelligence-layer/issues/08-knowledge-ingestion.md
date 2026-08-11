# 08 知识采集与保鲜：自动 vs 人工、增量更新

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/9
Status: closed (resolved 2026-08-11)
Blocked by: 05 语义层本体模型（closed）

Part of #1

## Question

知识采集与保鲜：自动采集（DB schema / ORM / migration / API 定义）vs 人工维护 vs 混合？增量更新机制？准确性校验与错误回滚？（含确认生产 schema 维护方式：ORM + migration？）

## Resolution

决议全文见 GitHub issue 评论（ADR-0007）。要点：

- 事实确认：neo-cloud = 13 个 Go 服务（Kratos）、10 个持库、**每服务一库**（同一 CNPG 集群一主两从）；schema 维护 = golang-migrate v4.19.1 统一（纯 SQL up/down、squash 纪律、可全量重建）+ GORM v1.31.1 交叉验证（生产无 AutoMigrate）；枚举=CHECK 约束；COMMENT 极少；有手工 DDL 先例与迁移改写 → 文件高置信但非绝对真相。
- 分工：混合——结构自动（migration 文件为主干 + GORM 交叉验证 + 按需 calibrate 生产校准）、语义人工（Agent 起草+审查、人工确认）。
- 采集器 = 独立 Go CLI（与同步管线同仓、真实语料 golden test）+ 采集工作流 Skill（≤1 页，跑采集→草稿→引导 review→触发同步）。
- Agent 角色 = 语义起草者 + 审查者（非结构生产者）；输出永远经人工确认才入 YAML。
- 增量触发：v1 手动 on-demand；Gitea label 触发后置（合入 main 的 PR 带 label → 自动增量更新）。
- 变更准入：diff 建议 → 人工 review（PR）→ 合入；纯结构批量确认。
- 校验三层：编译期（每次同步、原子拒绝）+ dry-run diff（同步前）+ 漂移报告（手动+每周例行、只报告不改）。
- 回滚：独立语义仓库（内部 Gitea）+ commit revert + 运行时全量重建；墓碑传播删除。
- API 定义采集 v1 排除。
- 输出 11（校准凭证）、12（golden 语料/drift 例行/Gitea 排期）；ADR-0007；map Notes 环境事实修正。
- 假设：neo-cloud 即全部生产服务源（若非，采集源扩展为多仓库）。
