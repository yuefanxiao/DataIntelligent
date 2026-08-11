// Package gateway 组装 MCP 网关（本票 = 骨架）：六只读工具注册为 stub、
// bearer 认证、双传输（Streamable HTTP 主 / stdio 调试）。
//
// 工具面（ADR-0003）与结构化错误（gwerr）在此固定为 v1 形状；后续票只换
// handler 内部实现（04 execute_sql、08 语义五工具），不破坏工具签名。
package gateway

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// Implementation 标识是网关的协议自报身份（tools/list 等场景可见）。
const (
	implName    = "dgw"
	implVersion = "v0.1.0"
)

// Gateway 是一个网关实例：持有运行时存储与 MCP server。
type Gateway struct {
	store  *store.Store
	server *mcp.Server
}

// New 构建网关：打开的 store 传入（调用方负责 Close），注册六 stub 工具，
// 返回可 Serve 的实例。
func New(st *store.Store, logger *slog.Logger) *Gateway {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    implName,
		Version: implVersion,
	}, &mcp.ServerOptions{
		Instructions: "数据智能层网关：生产 PostgreSQL（只读从库）的安全 MCP 数据通道。" +
			"六只读工具 = 五个语义检索原语 + execute_sql；校验失败返回结构化错误。",
		Logger: logger,
	})

	g := &Gateway{store: st, server: server}
	registerTools(server)
	return g
}

// Server 暴露底层 mcp.Server（测试与 stdio 形态使用）。
func (g *Gateway) Server() *mcp.Server { return g.server }

// userIDContextKey 是网关自身的身份上下文键：stdio 形态由 ServeStdio 预置；
// HTTP 形态的身份在 auth.TokenInfo 里（SDK 中间件写入请求上下文并随会话传播）。
// 下游（工具 handler、后续审计中间件）统一经 UserFromContext 读取，两种来源
// 归一，不依赖中间件执行顺序。
type userIDContextKey struct{}

func withUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserFromContext 返回当前调用绑定的用户身份：
//   - stdio 形态：ServeStdio 预置（withUserID）；
//   - HTTP 形态：auth.TokenInfo.UserID（RequireBearerToken 校验成功注入）。
//
// 未认证的调用返回 "", false。
func UserFromContext(ctx context.Context) (string, bool) {
	if userID, ok := ctx.Value(userIDContextKey{}).(string); ok && userID != "" {
		return userID, true
	}
	if ti := auth.TokenInfoFromContext(ctx); ti != nil && ti.UserID != "" {
		return ti.UserID, true
	}
	return "", false
}
