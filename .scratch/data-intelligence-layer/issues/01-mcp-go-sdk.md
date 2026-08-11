# 01 Research: MCP Go SDK 选型与协议能力

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/2
Status: resolved
Blocked by (open blockers): 0

Part of #1

## Question

Go 生态中，MCP server SDK 应选哪个？（官方 modelcontextprotocol/go-sdk vs mark3labs/mcp-go vs 其他候选）

对比维度：协议版本支持（2025-11-25 / 2026-07-28）、transport（stdio / Streamable HTTP / SSE）、认证与鉴权能力、工具声明与中间件、社区活跃度与维护方背书、迁移成本。

交付：推荐一个 + 理由 + 对 Gateway v1 工具面的能力边界影响（哪些能力 SDK 已提供、哪些要自己实现）。


## Answer

推荐官方 modelcontextprotocol/go-sdk v1.7.0+（唯一双协议时代 Go SDK：自动协商 2025-11-25/2026-07-28，内置 RequireBearerToken 认证中间件、conformance 套件、GitHub 官方生产验证）；mark3labs/mcp-go（协议停在 2025-11-25）作备选。需自实现：TokenVerifier、权限引擎、SQL 只读强制/限额/脱敏、审计落库。

完整调研报告见 issue 评论与分支 research 下的文件。
