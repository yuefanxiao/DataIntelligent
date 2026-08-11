# DataIntelligent

企业级 Data Intelligence Layer——让任何 Agent（Claude Code / Codex / 未来任何 MCP 客户端）安全、准确地理解并查询企业生产数据。

当前状态：**v1 构建中**。路线已由 wayfinder 地图与票据决议（GitHub Issues 为 canonical，`.scratch/` 本地镜像）固化在 `docs/spec.md`（v1 规格）与 `docs/adr/`；本仓库自 v1 build 01 起承载网关生产代码。

- 规格与验收标准：`docs/spec.md`（v1 最小完整闭环）
- 领域语言：`CONTEXT.md`（术语权威）
- 票据约定：`docs/agents/issue-tracker.md`
- 构建票：GitHub issues #18–#31（v1 build 01–14）

## 网关（`dgw`）

Go 实现的 MCP 数据网关（官方 go-sdk v1.7.0+，Streamable HTTP 主 + stdio 调试双形态）。

```sh
go build ./cmd/dgw

# 1. 创建凭据（明文仅打印一次，哈希落库）
dgw key-create --user dev-alice

# 2a. Streamable HTTP 守护进程（bearer 认证）
dgw serve

# 2b. stdio 调试形态（env 传 key）
DGW_API_KEY=<上面打印的 key> dgw serve-stdio
```

配置面 = env（`DGW_DB_PATH` / `DGW_HTTP_ADDR` / `DGW_API_KEY`），flag 可覆盖。
