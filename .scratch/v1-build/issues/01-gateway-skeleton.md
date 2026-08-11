# v1 build 01 — 网关骨架：MCP 双传输 + bearer 认证 + SQLite 地基

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/18
Status: closed（2026-08-11，commit 1b1ee36 合入 main；六条验收全过 + 双轴 code review 无遗留）
Blocked by (open blockers): 0

## 来源

docs/spec.md（v1 规格，PR #16）§2/§4.8/§4.9；ADR-0009；[issue #15](https://github.com/yuefanxiao/DataIntelligent/issues/15)（v1 构建实施，评审通过后开——本批票）

## What to build

网关以官方 go-sdk v1.7.0+ 启动 MCP 服务（双协议时代自动协商），双传输形态：Streamable HTTP 为主（`RequireBearerToken` + 自实现 `TokenVerifier`），stdio 为调试形态（env 传 key）。SQLite 运行时库地基（modernc.org/sqlite 纯 Go、WAL、单写者/多读者），凭据表就绪（`dgw_` 前缀 opaque 随机串、sha256 哈希存储、明文仅创建时打印一次）。六工具全部注册为 stub，调用返回结构化「未实现」错误；结构化错误格式统一（可区分语法错误 vs 无权限 vs 限流），为后续票的拒绝语义打底。

## Acceptance criteria

- [ ] Streamable HTTP 形态启动，bearer 认证生效：无/错 token → 结构化认证失败；正确 key → 放行
- [ ] stdio 调试形态经 env 传 key 可连接
- [ ] SQLite 运行时库初始化（WAL、单写多读），凭据表可用（哈希存储、明文不落库）
- [ ] tools/list 可见六工具；stub 调用返回结构化「未实现」错误（不 panic）
- [ ] 结构化错误格式统一（错误类别 + 机器可读字段），调用方可区分错误类型
- [ ] key 创建引导路径：明文仅打印一次（供 02 权限 CLI 复用）

## Blocked by

- None — can start immediately.

## Resolution (2026-08-11)

已实施（commit `1b1ee36`，main）：

- 双传输：Streamable HTTP 主（go-sdk `RequireBearerToken` + 自实现 `TokenVerifier`，401/403 结构化改写）+ stdio 调试（`DGW_API_KEY` env 传 key，无效拒绝启动）。
- SQLite 运行时库（modernc 纯 Go）：WAL + NORMAL + busy_timeout，`dgw_credentials` 表（sha256 哈希、明文不落库、role 预留、revoked_at）。
- 六工具 stub（ADR-0003 真实输入 schema + readOnly），调用返回结构化 `not_implemented`。
- `gwerr` 统一错误：kind + code + details（为 02/03/05 拒绝语义打底）。
- CLI：`dgw serve` / `serve-stdio` / `key-create`（明文仅打印一次）。
- 测试：官方 go-sdk 客户端打真实网关（HTTP + stdio 双形态），`-race` 全绿。
- code review 修复：DSN 路径转义（`?` 截断缺陷）+ 回归测试、401/403 统一结构化、WAL 单写多读并发测试。
