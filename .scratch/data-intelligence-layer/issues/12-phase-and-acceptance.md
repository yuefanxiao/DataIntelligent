# 12 阶段切分与 v1 验收标准（最小闭环）

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/13
Status: closed (2026-08-11)
Resolution: 决议评论 https://github.com/yuefanxiao/DataIntelligent/issues/13#issuecomment-5254120945
Blocked by (open blockers): 0

Part of #1

## Question

阶段切分与 v1 验收标准：最小完整闭环（Q11c）的范围边界、验收用例（如"昨天支付失败率为什么上涨"全流程跑通）、团队评审流程、后续阶段（2-4）怎么划分。

依赖：02（工具面）、03（权限）、05（本体模型）、07（存储检索）、10（SQL 路线）、11（部署）。

## Resolution（2026-08-11）

三轮回合拷问收口，决议要点：

- **时序**：v1 最小完整闭环构建先行 → 从真实构建经验写 spec + phase plan（docs/spec.md）→ 团队评审（PR + 30min demo）→ 阶段 2-4
- **v1 范围 = 全部服务**：~/cloud/neo-cloud 13 个后端服务 / 10 库，结构自动 + 语义人工确认全服务（分批：结构采集 → 语义起草 → 负责人确认 → 用例起草 → 确认）；交付物清单照 11 确认（网关双形态、语义仓库、同步管线、采集器 + 采集工作流 Skill、grants + 权限 CLI、执行记录 JSONL、golden 语料、验收套件、spec + phase plan）
- **负载防护数值**：每 key 并发 2 / 进程级 8 / statement_timeout 30s（可配）/ 限额 500-5000
- **验收**：主用例「昨天支付失败率为什么上涨」全流程 + 每服务 ≥2 简单 + ≥1 复杂用例 + 负向/边界 5 例（无 grants 测试用户）；判定三件套（psql 对照 / 执行记录可复现 / 零未授权）；半自动化重放；兼作 golden 语料；v1 后按效果迭代
- **评审**：docs/spec.md PR + 30min demo，团队全体，意见清零 + 拍板人确认；阶段 2-4 入口轻量评审
- **阶段 2 = 运营化**（Gitea 触发采集、校准/drift 例行、golden 回归自动化、监控告警 + 复制延迟、生产通路全团队接入）
- **阶段 3 = 能力深化**（v2 规划引擎选型落地、权限优化、工作台管理面、API 定义采集 + 采集源扩展）
- **阶段 4 = 工作台 + 开放**（内置 Agent 查询界面、PM 接入、MCP/Skill 并存；编排形态阶段 3 末定）；里程碑制无日期
- 输出：task 票 14（issue #15）v1 构建；ADR-0010；Out of scope「PM 自助 Web 查询界面」移入阶段 4
