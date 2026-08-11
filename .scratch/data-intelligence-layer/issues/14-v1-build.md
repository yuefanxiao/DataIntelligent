# 14 v1 最小完整闭环构建（13 服务全量）

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/15
Status: open
Blocked by (open blockers): 0

Part of #1

## Question (Task)

按票据「12 阶段切分与 v1 验收标准（最小闭环）」落地 v1 最小完整闭环——本 effort 内的执行票，先于 spec 撰写与团队评审。

### 范围

- 覆盖：`~/cloud/neo-cloud` 全部 13 个后端服务（bss×5、iam×2、console-backend、notification、ops×2、usage-collection、dashboard-backend）、10 个持库、每服务一库——不做试点子集
- 语义完成度：全服务——结构采集（自动）+ 语义人工确认（Agent 起草 + 服务负责人确认），13 服务分批推进：结构采集 → 语义起草 → 负责人确认 → 用例起草 → 确认

### 交付物

- 网关：Go MCP 网关（stdio + Streamable HTTP 双形态、bearer token、六只读工具 + Agent Skill，校验层照 ADR-0008）
- 语义仓库：内部 Gitea 独立仓库，全服务 YAML 作者入口
- 同步管线 CLI、采集器 CLI + 采集工作流 Skill（照 ADR-0007）
- grants YAML + 权限 CLI（照 ADR-0004）；执行记录 JSONL（照 ADR-0006）
- 验收套件：主用例「昨天支付失败率为什么上涨」+ 每服务 ≥2 简单 + ≥1 复杂用例 + 负向/边界 5 例（无 grants 测试用户）；兼作 golden 语料
- spec + phase plan：docs/spec.md（含验收标准附录与参数表）

### 已定参数（票据 12）

- 每 key 并发 2 / 进程级 8 / statement_timeout 30s（可配）/ SQL 限额 500-5000
- 判定三件套：(a) 数字与 psql 手工对照一致 (b) 执行记录 JSONL 完整可复现整条调用链 (c) 零未授权访问

### 完成后

写 spec + phase plan → 交团队评审（docs/spec.md PR + 30min demo，意见清零 + 拍板人确认）→ 评审通过进入阶段 2（运营化）

参考：ADR-0001~0010、docs/research/（01 SDK 选型、02 规划草案）
