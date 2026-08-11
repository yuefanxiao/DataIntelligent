# 01 Research: MCP Go SDK 选型与协议能力

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/2
Status: closed
Blocked by (open blockers): 0

Part of #1

## Question

Go 生态中，MCP server SDK 应选哪个？（官方 modelcontextprotocol/go-sdk vs mark3labs/mcp-go vs 其他候选）

对比维度：协议版本支持（2025-11-25 / 2026-07-28）、transport（stdio / Streamable HTTP / SSE）、认证与鉴权能力、工具声明与中间件、社区活跃度与维护方背书、迁移成本。

交付：推荐一个 + 理由 + 对 Gateway v1 工具面的能力边界影响（哪些能力 SDK 已提供、哪些要自己实现）。

## Resolution（2026-08-11）

推荐官方 `modelcontextprotocol/go-sdk`（v1.7.0+）：唯一支持 2026-07-28 协议且自动兼容 2025-11-25 的 Go SDK，官方 org + Google 维护、GitHub 官方 MCP server 生产验证、内置 Bearer token 中间件与 session 防劫持、方法级中间件。调研报告：docs/research/01-mcp-go-sdk.md。Gateway v1 需自实现：TokenVerifier（接自家 IdP/API key）、权限引擎、SQL 安全与限额、审计落库、工具集定义。

