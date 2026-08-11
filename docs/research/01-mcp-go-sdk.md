# 01 Research: MCP Go SDK 选型与协议能力

- 票据: [GitHub issue #2](https://github.com/yuefanxiao/DataIntelligent/issues/2)（Part of #1，wayfinder:research）
- 调研日期: 2026-08-11
- 信息来源: 一手来源为主（官方仓库源码/README/CHANGELOG/release notes、MCP 规范仓库、官方博客、GitHub API 实时数据）；社区文章仅作佐证
- 结论预览: **推荐官方 `github.com/modelcontextprotocol/go-sdk`（v1.7.0+）**。它是 Go 生态中唯一同时支持 2026-07-28 与 2025-11-25 双协议时代的 SDK，自带认证中间件与安全防护；mark3labs/mcp-go 生态大但停留在 2025-11-25（2026-07-28 支持仍为 open issue），不适合作为新项目底座。

---

## 1. MCP 协议版本现状（2026-08-11 时点）

MCP 规范仓库（modelcontextprotocol/specification）已发布的协议版本：

| 版本 | 状态 | 要点 |
|---|---|---|
| 2024-11-05 | 历史 | 首个正式版；HTTP+SSE transport |
| 2025-03-26 | 历史 | 引入 Streamable HTTP |
| 2025-06-18 | 历史 | OAuth 2.0 授权、elicitation |
| 2025-11-25 | 当前广泛部署 | 大改版：Task 能力、cacheable list、鉴权修订 |
| **2026-07-28** | **最新（2026-07-28 发布）** | 无状态核心、`server/discover`、HTTP header 标准化、MRTR、正式弃用 roots/sampling/logging |

2026-07-28 版的核心变化（官方博客 [blog.modelcontextprotocol.io/posts/2026-07-28/](https://blog.modelcontextprotocol.io/posts/2026-07-28/) + 官方 go-sdk v1.7.0 release notes）：

- **无状态核心（SEP-2575）**：取消 `initialize`/`notifications/initialized` 握手与 `Mcp-Session-Id` header；每个请求在 `_meta` 里自带协议版本、客户端能力与身份；新增可选 `server/discover` RPC 让客户端提前发现能力。任何请求可打到 LB 后任意实例。
- **HTTP 标准化（SEP-2243）**：Streamable HTTP 请求必须带 `Mcp-Method` / `Mcp-Name`（等）header，网关无需解析 JSON body 即可路由。
- **MRTR（SEP-2322）**：server 发起的采样/elicitation/roots 请求改为工具调用内嵌 `inputRequests`/`inputResponses` 多轮往返。
- **Cacheable list results（SEP-2549）**：`tools/list` 等返回 `ttlMs`/`cacheScope`，客户端可缓存。
- **弃用（SEP-2577）**：roots、sampling、logging 正式弃用（≥12 个月过渡期）；旧 HTTP+SSE transport 官方弃用（一年 offramp）。
- **授权变更**：客户端须校验 `iss`（RFC 9207）；Dynamic Client Registration 弃用、改 Client ID Metadata Documents（CIMD）。
- 四个 Tier 1 SDK（TS / Python / Go / C#）均已在发布日同步支持，Rust 处于 beta。

> 实务含义：2026-08 的新服务应当选一个**双协议时代都能跑**的 SDK——对 2025-11-25 客户端（Claude Code 等存量客户端）走旧握手，对 2026-07-28 客户端走无状态协议。

## 2. 候选 SDK 概览

| 候选 | 仓库 | 维护方 | 最新版本 | 协议支持 | 状态判断 |
|---|---|---|---|---|---|
| **官方 go-sdk** | [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) | MCP 官方 org（Anthropic 主导），README 明示与 Google 协作维护 | **v1.7.0**（2026-07-28） | **2026-07-28 + 2025-11-25 + 更早全部**（自动协商） | 首选 |
| **mark3labs/mcp-go** | [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) | 社区（Mark3Labs，原 Ed Zynda） | v0.57.0（2026-07-23） | 仅 2025-11-25（含更早） | 次选，2026-07-28 支持未落地 |
| metoro-io/mcp-golang | [metoro-io/mcp-golang](https://github.com/metoro-io/mcp-golang) | 社区 | — | 2025-11-25 时代 | 1.2k stars，最后 push 2026-02-25，已停滞 |
| ThinkInAIXYZ/go-mcp | [ThinkInAIXYZ/go-mcp](https://github.com/ThinkInAIXYZ/go-mcp) | 社区 | — | 2025-11-25 时代 | 672 stars，社区小，无官方背书 |

（数据来自 2026-08-11 GitHub API 实测：stars/pushed_at/最新 release tag。）

## 3. 对比：官方 go-sdk vs mark3labs/mcp-go

### 3.1 协议版本支持

| 维度 | 官方 go-sdk v1.7.0 | mark3labs v0.57.0 |
|---|---|---|
| 最新协议 | **2026-07-28（完整）**，与规范同日发布 | 2025-11-25（README 明示） |
| 旧版兼容 | 2025-11-25\*、2025-06-18、2025-03-26、2024-11-05；连接时自动协商最高共同版本；`server/discover` 失败自动回退 legacy `initialize` | 2025-06-18、2025-03-26、2024-11-05（向后兼容） |
| 生命周期模型 | 双模型透明支持：legacy 握手（≤2025-11-25）+ 2026-07-28 无状态（`StreamableHTTPOptions.Stateless=true` 时开放新协议，否则客户端协商降到 2025-11-25） | 仅 stateful（依赖 `Mcp-Session-Id`，2026-07-28 规范已废除该 header） |
| 2026-07-28 落地状态 | 已发布（v1.7.0，2026-07-28；release notes 逐条列出 SEP-2575/2243/2322/2549/2577 实现） | **未支持**：issue [#948](https://github.com/mark3labs/mcp-go/issues/948)「spec: support the 2026-07-28 specification」（2026-08-05 开，open）；issue [#928](https://github.com/mark3labs/mcp-go/issues/928)（SEP-2575 无状态 + `server/discover` + `Mcp-Method/Name` header 校验）未合并；`mcp-spec-check` 对 stock server 唯一的 required-check 失败即 `server/discover` Method not found |

官方 go-sdk 版本支持矩阵（README「Version Compatibility」表）：v1.0.0–1.1.0 → 2025-06-18；v1.2.0–1.3.1 → 2025-11-25 部分；v1.4.0–1.6.1 → 2025-11-25（客户端 OAuth 实验性）；v1.7.0+ → 2026-07-28。

### 3.2 Transport

| 维度 | 官方 go-sdk | mark3labs/mcp-go |
|---|---|---|
| stdio | `mcp.StdioTransport`（server）、`mcp.CommandTransport`（client 起子进程） | `server.ServeStdio` |
| Streamable HTTP | `mcp.StreamableHTTPHandler`（`http.Handler`，可挂任意 net/http 路由）；支持 stateful 与 **stateless（2026-07-28）** 两种模式 | `server.NewStreamableHTTPServer`（`http.Handler`）；另有非 net/http 框架的 `Handle` 入口（fasthttp/fiber 适配）；支持流恢复（pluggable EventStore，`Last-Event-ID`/standalone GET——该机制在 2026-07-28 已移除） |
| SSE | `mcp.SSEHandler`（保留作 2024-11-05 legacy 兼容） | `server.NewSSEServer`（legacy；`SetConnectionLostHandler`） |
| 其他 | InMemory transport（测试/进程内），自定义 transport 接口 | in-process transport |
| HTTP 安全默认值 | DNS rebinding 防护（loopback 请求校验 Host header）；`CrossOriginProtection` 选项 | DNS rebinding 防护（localhost 校验，可禁用）；CORS 配置（`WithStreamableHTTPCORS`/`WithSSECORS`，默认关） |

对 Gateway 而言：**stdio 与 Streamable HTTP 两条路两边都齐**。Claude Code 消费方支持 stdio（本地）/ Streamable HTTP（远程，官方推荐）/ 旧 SSE（[Claude Code MCP 文档](https://code.claude.com/docs/en/mcp)）。差异在于官方 go-sdk 的 Streamable HTTP 可以**同时**服务 2025-11-25 与 2026-07-28 客户端（stateless 开关 + 协商），mark3labs 只有 stateful 一条路。

### 3.3 认证与鉴权（server 侧）

| 维度 | 官方 go-sdk | mark3labs/mcp-go |
|---|---|---|
| Bearer token 校验 | **内置** `auth.RequireBearerToken` 中间件 + `TokenVerifier` 接口（你的校验逻辑注入进来）；校验成功把 `auth.TokenInfo`（含 userID）放进 request context；`TokenInfo.userID` 用于**防止 session hijacking**（同 session 必须同一用户）；401 时自动带 `WWW-Authenticate` 指向 protected resource metadata | 无内置 token 校验中间件；server 侧只有 **OAuth Protected Resource Metadata 广告**（RFC 9728 `/.well-known/oauth-protected-resource`，`WithProtectedResourceMetadata`）——只声明"我要求 OAuth"，不验证任何 token |
| OAuth 资源服务器 | `auth` 包 + `oauthex` 包（DCR、token exchange、resource metadata、audience 处理）；客户端 OAuth 为实验性支持 | 客户端 OAuth 完整（client/oauth.go、OAuth2 授权码流）；server 侧无资源服务器实现 |
| 会话安全 | sessionInfo 记录 userID，后续请求必须同 userID（防劫持）；docs/protocol.md 专列 Security 节：Confused Deputy、Token Passthrough、SSRF、Session Hijacking、Issuer Mix-Up | 依赖 `Mcp-Session-Id` 的 stateful 会话（2026-07-28 已废除该模型）；tool filter / session 机制可做鉴权但无 token 层 |

Gateway 场景（Coding Agent 带 API key / OAuth token 访问网关）：官方 go-sdk 直接给中间件 + 防劫持，只需实现 `TokenVerifier`（校验自家 IdP 的 JWT / API key 存储）；mark3labs 则要自己在 HTTP 层写 token 校验。

### 3.4 工具声明与中间件

| 维度 | 官方 go-sdk | mark3labs/mcp-go |
|---|---|---|
| 工具声明 | `mcp.AddTool(server, &mcp.Tool{...}, handler)`；**类型化 handler** `ToolHandlerFor[In, Out]`——输入输出直接写 Go struct，`jsonschema` tag 自动生成 JSON Schema，**server 侧自动做输入校验/默认值/输出校验**；`ToolHandler` 低层接口 | `mcp.NewTool` builder DSL（`WithString/WithNumber/WithArray/WithEnum/Required/Pattern...`），handler 内 `request.RequireString()` 等类型安全取值；typed tools / structured I/O（struct tags）也有 |
| 中间件 | **`Server.AddReceivingMiddleware` / `AddSendingMiddleware`**（方法级 middleware 链，工具/资源/提示所有 method 通吃）；MRTR 双向中间件内置 | `server.WithToolHandlerMiddleware`（仅 tool handler 级）、`WithPromptHandlerMiddleware`；`WithRecovery` panic 恢复；`WithHooks` request hooks（遥测/日志）；tracing 支持 |
| 会话/权限挂钩 | capability inference（加工具自动声明能力）、`ServerOptions` 各 feature handler；无 per-session 工具过滤 | per-session tools（`SessionWithTools`）、`WithToolFilter`（按 session 过滤 tools/list）、session context |
| 额外能力 | prompts、resources（含 template）、elicitation、completion、`subscriptions/listen`（2026-07-28 统一订阅流）、cacheable list、MRTR 双端 shim（legacy 客户端兼容） | prompts、resources/templates、completion providers、task-augmented tools（2025-11-25 Tasks）、sampling/roots/elicitation（2025-11-25 风格） |

两边都能声明工具、都能挂中间件。差异：官方 go-sdk 的中间件在**协议方法层**（一次挂钩覆盖 tools/call、resources/read 等所有方法，天然适配 Gateway 的"每个 MCP 请求都过审计/权限/限额"需求），且类型化工具自动做 schema 生成与校验；mark3labs 的中间件在 handler 层，粒度更细但需逐个挂钩。

### 3.5 社区活跃度与维护方背书

| 维度 | 官方 go-sdk | mark3labs/mcp-go |
|---|---|---|
| Stars / forks（2026-08-11 实测） | 4,962 / 504 | 8,992 / 864 |
| 背书 | **MCP 官方 org（Anthropic 主导）+ Google 协作**；规范仓库 Tier 1 SDK 名单内；GitHub 官方 MCP server 生产使用（[2026-07-23 GitHub changelog](https://github.blog/changelog/2026-07-23-github-mcp-server-supports-the-next-mcp-specification/)：基于 v1.7.0-pre.3 服务 50 万+ 用户） | 社区项目（Mark3Labs，原 Ed Zynda），Discord 社区活跃 |
| 发布节奏 | v1.0.0 于 2025-09-30；之后 ~monthly；v1.7.0 与规范同日发布 | v0.x 每月一发（v0.54→0.57 密集） |
| 成熟度 | 稳定 API（v1 semver），official conformance 测试套件（conformance_test.go），`docs/rough_edges.md` 只列 v2 才处理的命名级小瑕疵 | 0.x，无 conformance 套件；README 自述「advanced capabilities still in progress」 |
| License | Apache-2.0（新代码）/ MIT（既有代码） | MIT |

mark3labs 星数更高、教程与第三方示例更多，但**维护方背书、规范同步速度、conformance 保障**都弱于官方 SDK——后者正体现在 2026-07-28 支持一发布即到位 vs 前者至今 open issue。

### 3.6 迁移成本

- **官方 go-sdk 起点成本**：API 与 mark3labs 不同（typed `ToolHandlerFor` + struct tags vs builder DSL），但公开文档（docs/ 下 protocol/server/client/quick_start）完整、示例代码多，示例即两段式 Hello World；对 Gateway 这种新建代码，无历史包袱。
- **mark3labs 隐性迁移成本**：现在选它，等 2026-07-28 落地（issue #948/#928 合并、出 v0.58+）时，`Mcp-Session-Id` 会话模型被废除、`server/discover` 与 header 校验成为必选，工具过滤/会话机制建立在 stateful 模型上，届时需要一次破坏性迁移。官方 SDK 则通过双模型透明协商**把这个迁移前置消化掉了**。
- 两条路都不存在"换 SDK 无痛"的问题，但对新建 Gateway v1，选择官方 SDK 等于**今天就把 2026-07-28 的迁移做完**。

## 4. 推荐

> **推荐：`github.com/modelcontextprotocol/go-sdk`（官方 SDK，v1.7.0+），服务端走 Streamable HTTP（Claude Code 远程模式）为主、stdio（本地调试）为辅。**

理由（按权重）：

1. **协议领先且双时代兼容**：唯一支持 2026-07-28 的 Go SDK，且对 2025-11-25 客户端（Claude Code 当前实际走的协议）自动协商降级、`server/discover` 失败自动回退 legacy `initialize`——Gateway 不用做任何客户端版本判断。
2. **维护方背书与生产验证**：MCP 官方 org + Google 协作；GitHub 官方 MCP server 生产级使用（50 万+ 用户）；v1 稳定 API + conformance 套件；规范发布当日同步发版。
3. **认证与安全默认值**：内置 Bearer token 中间件 + session hijack 防护 + DNS rebinding 防护 + 安全文档（docs/protocol.md Security 节）——与 Gateway 的"认证/审计/防滥用"诉求直接对位，只差一个 `TokenVerifier` 实现。
4. **方法级中间件**：`AddReceivingMiddleware/AddSendingMiddleware` 让 Gateway 的鉴权/审计/限额/脱敏以横切方式覆盖所有 MCP 方法。
5. **迁移成本为零**：不存在"先上 mark3labs 再等 2026-07-28 重写"的二阶成本。

选 mark3labs 的唯一充分理由：团队已有大量基于其 API 的代码（本仓库无），或极度看重 builder DSL 与 per-session 工具过滤（本仓库无此存量诉求）。其余候选（mcp-golang / ThinkInAIXYZ/go-mcp）因停滞或社区过小直接排除。

## 5. 对 Gateway v1 工具面的能力边界影响

### 5.1 SDK 已提供（直接用）

- **协议层**：双时代生命周期（握手/无状态协商）、`server/discover`、`tools/list`/`tools/call`、resources/prompts、elicitation、MRTR（含对 legacy 客户端的兼容 shim）、`subscriptions/listen`、cacheable list、ping/progress/cancellation、JSON-RPC 错误码。
- **Transport**：stdio、Streamable HTTP（stateful + stateless 开关）、SSE（legacy）；`http.Handler` 形态可嵌进现有 net/http 服务；DNS rebinding 防护。
- **工具声明**：`AddTool` + 类型化 handler，Go struct 自动生成 input schema 并做输入校验/默认值/输出校验；能力自动推断。
- **中间件**：方法级 sending/receiving middleware 链——Gateway 的审计日志、权限检查、限流、结果脱敏的**挂钩点**。
- **认证骨架**：`RequireBearerToken` 中间件、`TokenInfo`（含 userID）进 context、防 session hijack、401 时 `WWW-Authenticate` + protected resource metadata；`oauthex` 的 DCR/token exchange（若将来要 OAuth 全套）。
- **客户端**：完整 `ClientSession`（CallTool/列表/elicitation/MRTR）——Gateway 的集成测试可以直接用官方客户端打自己。
- **2026-07-28 就绪**：`StreamableHTTPOptions.Stateless=true` 即开放无状态协议给新客户端；不开则存量客户端协商到 2025-11-25，两条路都有官方保障。

### 5.2 需要自己实现（Gateway 域内职责）

- **TokenVerifier**：接入自家认证（API key 存储 / IdP JWT 校验 / OAuth 授权服务器对接）。SDK 提供中间件与 context 传递，校验逻辑是 Gateway 的（与票据 03 权限模型联动）。注意：SDK 只做**资源服务器侧**（验证 token）；**授权服务器本身**（发 token、登录页、刷新）要自己搭或接现成 IdP。
- **权限引擎**：用户/角色 → 工具可见性 → 查询范围（schema 白名单、库/表/列级权限）。SDK 无 RBAC；在工具声明层（按身份裁剪 tools/list）与 handler 层（调用前检查）实现。
- **SQL 安全与执行控制**：只读强制、SQL 解析/校验、行数/超时/成本限额、结果脱敏——MCP 层不碰 SQL，全部是 Gateway 业务代码（在 handler + middleware 里做）。
- **工具集定义**：v1 工具面（schema 查询、只读查询、查询规划等，见票据 03）用 `AddTool` 声明，handler 里编排语义层/执行器。
- **审计**：用 sending/receiving middleware 记录每个 MCP 请求（who/what/result），落库逻辑自己写。
- **多协议并存细节**：若开启 stateless 模式，需确认自家负载均衡/日志链路支持 `Mcp-Method`/`Mcp-Name` header 路由（SEP-2243）；不做也无碍（协商降级）。

## 6. 落地注意事项

1. **Go 版本**：SDK 只支持受支持的 Go 版本（README 明示），v1.7.0 要求较新的 Go toolchain，CI/本地需对齐。
2. **Claude Code 现状**：Claude Code 客户端目前走 2025-11-25 生命周期（stdio 本地 / Streamable HTTP 远程 + OAuth）；用官方 SDK 时它会被协商到 legacy 路径，**无需任何特殊处理**，等 Claude Code 升级到 2026-07-28 客户端后自动切无状态路径。
3. **弃用特性**：roots/sampling/logging 已弃用，新代码不要用（尤其不要用 sampling 做 Gateway 内部 LLM 调用，12 个月过渡期后可能移除）。
4. **监视 mark3labs #948/#928**：若未来某天官方 SDK 出问题，mark3labs 的 2026-07-28 支持进度可作为备选切换的观察点（但目前无切换理由）。
5. **版本钉死**：v1.7.0 于 2026-07-28 发布，推荐直接用 `go get github.com/modelcontextprotocol/go-sdk/mcp@v1.7.0` 起步，后续随 semver 升级。

## 7. 来源

一手来源：

- 官方 go-sdk 仓库 README / docs（protocol.md、server.md、client.md、quick_start.md、rough_edges.md）/ v1.7.0 release notes / GitHub API（stars、release 时间线）: https://github.com/modelcontextprotocol/go-sdk
- mark3labs/mcp-go README（含 transports、OAuth、session、middleware 章节）/ v0.57.0 release notes / issues #948、#928 / GitHub API: https://github.com/mark3labs/mcp-go
- MCP 规范仓库（docs/specification/2024-11-05 … 2026-07-28）: https://github.com/modelcontextprotocol/specification
- MCP 2026-07-28 发布博客: https://blog.modelcontextprotocol.io/posts/2026-07-28/
- SEP 文档: https://modelcontextprotocol.io/seps/2575-stateless-mcp 、/2567-sessionless-mcp 、/2243-http-standardization 、/2577-deprecate-roots-sampling-and-logging （另 SEP-2322 MRTR、SEP-2549 cacheable list）
- GitHub 官方 MCP server 采用 go-sdk 的公告: https://github.blog/changelog/2026-07-23-github-mcp-server-supports-the-next-mcp-specification/
- Claude Code MCP 文档（transports / OAuth）: https://code.claude.com/docs/en/mcp
- 备选候选仓库实测（2026-08-11 GitHub API）: metoro-io/mcp-golang、ThinkInAIXYZ/go-mcp
