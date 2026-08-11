// Package gateway 组装 MCP 网关（本票 = 骨架）：六只读工具注册为 stub、
// bearer 认证、双传输（Streamable HTTP 主 / stdio 调试）、表级授权运行时。
//
// 工具面（ADR-0003）与结构化错误（gwerr）在此固定为 v1 形状；后续票只换
// handler 内部实现（04 execute_sql、08 语义五工具），不破坏工具签名。
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/authz"
	"github.com/yuefanxiao/DataIntelligent/internal/config"
	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/loadgate"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// Implementation 标识是网关的协议自报身份（tools/list 等场景可见）。
const (
	implName    = "dgw"
	implVersion = "v0.1.0"
)

// authzReloadInterval 是权限热重载的轮询周期（CLI grant/revoke 后最多
// 一个周期内生效；spec §4.9 参数表无此项，进程内常量）。
const authzReloadInterval = 5 * time.Second

// Gateway 是一个网关实例：持有运行时存储、授权服务与 MCP server。
type Gateway struct {
	store    *store.Store
	authz    *authz.Service
	server   *mcp.Server
	execSQL  *executeSQLDeps    // execute_sql 运行时依赖（nil = 未配置，结构化拒绝）
	loadGate *loadgate.Gate     // 并发闸（负载防护，05 票；默认 2/8，WithLoadGate 可配）
	execlog  *execrecord.Logger // 执行记录（06 票；nil = 不记录）
	logger   *slog.Logger       // 网关日志（执行记录写入失败等兜底输出）
}

// Option 是 New 的可选注入（execute_sql 的 PG 路由与限额，04/05 票接线）。
type Option func(*options)

type options struct {
	execSQL *executeSQLDeps
	execlog *execrecord.Logger
	// 并发闸原始数值（WithLoadGate 注入；New 统一校验——与 WithExecuteSQL
	// 的限额校验同一惯例；gateSet=false 取 spec 默认 2/8）。
	gateSet               bool
	gatePerKey, gateTotal int
}

// executeSQLDeps 是 execute_sql 的运行时依赖：PG 路由 + 行数限额。
type executeSQLDeps struct {
	router *db.Router
	limit  int
}

// 行数限额范围（spec §4.9 参数表：默认 500 / 硬上限 5000；下限即默认值，
// 与 config 包同一事实源）。
const sqlLimitMax = 5000

// WithExecuteSQL 注入 execute_sql 的 PG 路由与行数限额（spec §4.9「env 可
// 覆盖」：默认 500，硬上限 5000 不可配置超过）。未调用时 execute_sql 返回
// 结构化「未配置」错误（fail closed，不静默降级）。
func WithExecuteSQL(router *db.Router, limit int) Option {
	return func(o *options) {
		o.execSQL = &executeSQLDeps{router: router, limit: limit}
	}
}

// WithLoadGate 注入并发闸数值（spec §4.9「env 可覆盖」：每 key 并发上限 /
// 进程级总并发上限；默认 2/8）。数值非法（<1 或进程级 < 每 key）由 New
// 校验 = 启动失败（配置错误 fail fast）。
func WithLoadGate(perKey, total int) Option {
	return func(o *options) {
		o.gateSet = true
		o.gatePerKey, o.gateTotal = perKey, total
	}
}

// WithExecLog 注入执行记录写入器（06 票；spec §4.6 六工具全记 + 认证失败 +
// key 生命周期）。nil = 不记录（测试便捷；生产 main 恒注入）。
func WithExecLog(l *execrecord.Logger) Option {
	return func(o *options) { o.execlog = l }
}

// New 构建网关：打开的 store 传入（调用方负责 Close），注册六工具，并把
// 表授权快照加载进内存（失败 = 启动失败——权限加载不完整绝不能带病服务）。
// 限额越界 / 并发闸数值非法 = 启动失败（配置错误 fail fast）。
func New(st *store.Store, logger *slog.Logger, opts ...Option) (*Gateway, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    implName,
		Version: implVersion,
	}, &mcp.ServerOptions{
		Instructions: "数据智能层网关：生产 PostgreSQL（只读从库）的安全 MCP 数据通道。" +
			"六只读工具 = 五个语义检索原语 + execute_sql；校验失败返回结构化错误。",
		Logger: logger,
	})

	g := &Gateway{store: st, server: server, authz: authz.New(st, logger), logger: logger}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	g.execlog = o.execlog
	// 并发闸恒启用（负载防护不缺席）：数值在此统一校验（option 只存数据，
	// New 校验上报——与 WithExecuteSQL 限额同一惯例）；未注入取 spec 默认。
	perKey, total := config.DefaultKeyConcurrency, config.DefaultProcessConcurrency
	if o.gateSet {
		perKey, total = o.gatePerKey, o.gateTotal
	}
	var err error
	if g.loadGate, err = loadgate.New(perKey, total); err != nil {
		return nil, err
	}
	g.execSQL = o.execSQL
	if g.execSQL != nil && g.execSQL.router == nil {
		return nil, fmt.Errorf("WithExecuteSQL 需要非 nil 的 PG 路由（db.NewRouter 构造）")
	}
	if g.execSQL != nil && (g.execSQL.limit < config.DefaultSQLLimit || g.execSQL.limit > sqlLimitMax) {
		return nil, fmt.Errorf("SQL 行数限额 %d 越界（范围 %d-%d，spec §4.9）", g.execSQL.limit, config.DefaultSQLLimit, sqlLimitMax)
	}

	if err := g.authz.Load(context.Background()); err != nil {
		return nil, err
	}
	registerTools(server, g)
	return g, nil
}

// StartAuthzReload 启动授权热重载轮询（goroutine）：CLI grant/revoke 后
// 无需重启网关即生效（默认 5s 周期）。serve / serve-stdio 启动时调用；
// ctx 取消即停。
func (g *Gateway) StartAuthzReload(ctx context.Context) {
	go g.authz.ReloadLoop(ctx, authzReloadInterval)
}

// StartAuthzReloadEvery 是 StartAuthzReload 的周期注入形态（测试 seam：
// 端到端测试用短周期加速轮询；生产一律用默认 5s）。
func (g *Gateway) StartAuthzReloadEvery(ctx context.Context, interval time.Duration) {
	go g.authz.ReloadLoop(ctx, interval)
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

// keyIDContextKey 是凭据 key 身份（每 key 并发闸粒度）的上下文键，
// 来源与 userIDContextKey 对应：stdio 预置 / HTTP 经 TokenInfo.Extra。
type keyIDContextKey struct{}

func withKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyIDContextKey{}, keyID)
}

// KeyFromContext 返回当前调用绑定的凭据 key 身份（ADR-0004 key→用户扁平
// 映射：一用户多 key，每 key 独立计数）：
//   - stdio 形态：ServeStdio 预置（withKeyID，进程的 key）；
//   - HTTP 形态：verifyToken 写入 auth.TokenInfo.Extra["key_id"]。
//
// 未认证的调用返回 "", false。
func KeyFromContext(ctx context.Context) (string, bool) {
	if keyID, ok := ctx.Value(keyIDContextKey{}).(string); ok && keyID != "" {
		return keyID, true
	}
	if ti := auth.TokenInfoFromContext(ctx); ti != nil {
		if keyID, ok := ti.Extra["key_id"].(string); ok && keyID != "" {
			return keyID, true
		}
	}
	return "", false
}
