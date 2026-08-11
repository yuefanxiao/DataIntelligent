package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// HTTPHandler 返回 Streamable HTTP 形态的完整 http.Handler：
// SDK 的 RequireBearerToken（认证 + 会话防劫持）+ 结构化 401/403 改写 +
// 认证失败执行记录。挂载到任意路径（cmd/dgw 挂 `/`；反代后按需加前缀）。
func (g *Gateway) HTTPHandler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return g.server
	}, nil)
	return structuredAuth(g.verifyToken, mcpHandler, g.recordAuthFailure)
}

// verifyToken 是 go-sdk auth.TokenVerifier：对凭据表做 sha256 哈希比对。
// 未命中 → ErrInvalidToken（401）；存储故障 → 其他错误（500）。
// opaque key 无过期（ADR-0004：v1 无过期策略），middleware 需开
// AllowMissingExpiration。UserID 进 TokenInfo（会话防劫持/审计）；
// key_id（凭据行 ID）与 auth_ms（认证阶段实测耗时，06 票执行记录分阶段
// 打点）进 Extra——SDK 中间件把同一 TokenInfo 挂进调用上下文，工具侧
// KeyFromContext / newStageTimer 可读。
func (g *Gateway) verifyToken(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	authStart := time.Now()
	k, err := credentials.VerifyKey(ctx, g.store.DB(), token)
	authMS := time.Since(authStart).Milliseconds()
	if err != nil {
		if errors.Is(err, credentials.ErrInvalidKey) {
			return nil, auth.ErrInvalidToken
		}
		return nil, fmt.Errorf("verify key: %w", err)
	}
	return &auth.TokenInfo{
		UserID: k.UserID,
		Extra:  map[string]any{"key_id": strconv.FormatInt(k.ID, 10), "auth_ms": authMS},
	}, nil
}

// recordAuthFailure 记录 HTTP 认证失败（401/403，kind=auth_failure）：身份
// 未知（认证失败无法归因 user/key），工具名不可知（MCP 请求体在认证层不
// 解析），被拒原因如实（gwerr unauthorized 原文）。记录失败只记日志，不
// 影响认证结果（执行记录是诊断设施，fail-open）。
func (g *Gateway) recordAuthFailure(message string) {
	if g.execlog == nil {
		return
	}
	e := gwerr.Unauthorized(message)
	if err := g.execlog.LogAuthFailure(time.Now(), e); err != nil {
		g.logger.Error("执行记录写入失败（不阻断认证）", "err", err)
	}
}

// structuredAuth 用 SDK 中间件做认证（其 401/会话防劫持语义原样保留），
// 但把 SDK 写入的纯文本 401 改写成统一结构化错误 JSON，并把认证失败事件
// 交给 onFail（执行记录）。
func structuredAuth(verifier auth.TokenVerifier, next http.Handler, onFail func(message string)) http.Handler {
	sdkMW := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		AllowMissingExpiration: true, // opaque key 无过期字段（ADR-0004）
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aw := &authResponseWriter{
			ResponseWriter: w,
			missingBearer:  !hasBearer(r),
			onFail:         onFail,
		}
		sdkMW(next).ServeHTTP(aw, r)
	})
}

// hasBearer 粗略判断请求是否携带 bearer 形态的凭据（SDK 中间件的失败原因
// 不外传，认证失败记录按此预判区分「缺凭据 vs 凭据无效」——被拒原因如实
// 的最接近粒度）。
func hasBearer(r *http.Request) bool {
	f := strings.Fields(r.Header.Get("Authorization"))
	return len(f) == 2 && strings.EqualFold(f[0], "bearer") && f[1] != ""
}

// authResponseWriter 把 SDK 中间件写出的认证失败响应改写成 gwerr JSON：
// 401（无/错 token）与 403（会话用户不匹配，session hijack 检测），并回调
// 执行记录。其余状态码/成功流原样透传（不做缓冲，SSE 流式不受影响）。
type authResponseWriter struct {
	http.ResponseWriter
	authFailed    bool
	missingBearer bool // 请求未携带 bearer 凭据（401 原因区分）
	onFail        func(message string)
}

// Unwrap 让 http.NewResponseController 能穿透本包装找到底层 Flusher——
// SSE 流的 Flush 依赖它（缺失会导致响应头/事件积压在缓冲里，客户端收不到）。
func (w *authResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *authResponseWriter) WriteHeader(code int) {
	if (code == http.StatusForbidden || code == http.StatusUnauthorized) && !w.authFailed {
		w.authFailed = true
		msg := w.authMessage(code)
		if w.onFail != nil {
			w.onFail(msg)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.ResponseWriter.WriteHeader(code)
		_, _ = w.ResponseWriter.Write(gwerr.Unauthorized(msg).JSON())
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

// authMessage 是认证失败的结构化文案：403 = 会话用户不匹配（SDK session
// hijack 检测）；401 区分缺凭据/凭据无效（被拒原因如实）。
func (w *authResponseWriter) authMessage(code int) string {
	switch {
	case code == http.StatusForbidden:
		return "session user mismatch"
	case w.missingBearer:
		return "missing bearer key"
	default:
		return "invalid or revoked bearer key"
	}
}

// Write 在认证失败后吞掉 SDK 后续写入的纯文本错误。
func (w *authResponseWriter) Write(p []byte) (int, error) {
	if w.authFailed {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}
