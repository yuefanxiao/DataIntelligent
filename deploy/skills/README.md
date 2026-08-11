# Skills（随网关/采集器交付的使用指南）

两件 ≤1 页的 Skill 交付文档，分别面向网关与采集器的使用者：

- [`agent-skill.md`](agent-skill.md) —— **Agent Skill**：Coding Agent 经 MCP 连网关查询生产数据的六工具清单、标准工作流（发现→解析→执行）与回退路径（无现成指标走表/列原料、被拒后按结构化错误调整）。安装进 Coding Agent 的 skill 目录即可使用（要求网关已按 `deploy/README.md` 部署）。
- [`collector-workflow.md`](collector-workflow.md) —— **采集工作流 Skill**：封装 `dgw-collect` 采集器「跑采集 → YAML 草稿 → 人工 review → 合入 → 手动触发同步」全流程，供网关运维者/服务负责人使用。

两份内容与实现保持同步（工具面 = `internal/gateway/tools.go`；采集器 = `cmd/dgw-collect`；同步管线 = `cmd/dgw` 的 `semantic-sync`）。
