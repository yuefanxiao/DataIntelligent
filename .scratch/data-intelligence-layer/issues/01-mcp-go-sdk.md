# 01 Research: MCP Go SDK 选型与协议能力

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/2
Status: open
Blocked by (open blockers): 0

Part of #1

## Question

Go 生态中，MCP server SDK 应选哪个？（官方 modelcontextprotocol/go-sdk vs mark3labs/mcp-go vs 其他候选）

对比维度：协议版本支持（2025-11-25 / 2026-07-28）、transport（stdio / Streamable HTTP / SSE）、认证与鉴权能力、工具声明与中间件、社区活跃度与维护方背书、迁移成本。

交付：推荐一个 + 理由 + 对 Gateway v1 工具面的能力边界影响（哪些能力 SDK 已提供、哪些要自己实现）。

