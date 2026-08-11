package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// HTTPHandler 返回 Streamable HTTP 形态的完整 http.Handler：
// SDK 的 RequireBearerToken（认证 + 会话防劫持）+ 结构化 401/403 改写。
// 挂载到任意路径（cmd/dgw 挂 `/`；反代后按需加前缀）。
func (g *Gateway) HTTPHandler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return g.server
	}, nil)
	return structuredAuth(g.verifyToken, mcpHandler)
}

// verifyToken 是 go-sdk auth.TokenVerifier：对凭据表做 sha256 哈希比对。
// 未命中 → ErrInvalidToken（401）；存储故障 → 其他错误（500）。
// opaque key 无过期（ADR-0004：v1 无过期策略），middleware 需开
// AllowMissingExpiration。
func (g *Gateway) verifyToken(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	userID, err := credentials.Verify(ctx, g.store.DB(), token)
	if err != nil {
		if errors.Is(err, credentials.ErrInvalidKey) {
			return nil, auth.ErrInvalidToken
		}
		return nil, fmt.Errorf("verify key: %w", err)
	}
	return &auth.TokenInfo{UserID: userID}, nil
}

// structuredAuth 用 SDK 中间件做认证（其 401/会话防劫持语义原样保留），
// 但把 SDK 写入的纯文本 401 改写成统一结构化错误 JSON。
func structuredAuth(verifier auth.TokenVerifier, next http.Handler) http.Handler {
	sdkMW := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		AllowMissingExpiration: true, // opaque key 无过期字段（ADR-0004）
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aw := &authResponseWriter{ResponseWriter: w}
		sdkMW(next).ServeHTTP(aw, r)
	})
}

// authResponseWriter 把 SDK 中间件写出的认证失败响应改写成 gwerr JSON：
// 401（无/错 token）与 403（会话用户不匹配，session hijack 检测）。
// 其余状态码/成功流原样透传（不做缓冲，SSE 流式不受影响）。
type authResponseWriter struct {
	http.ResponseWriter
	authFailed bool
}

// Unwrap 让 http.NewResponseController 能穿透本包装找到底层 Flusher——
// SSE 流的 Flush 依赖它（缺失会导致响应头/事件积压在缓冲里，客户端收不到）。
func (w *authResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *authResponseWriter) WriteHeader(code int) {
	if code == http.StatusForbidden && !w.authFailed {
		w.authFailed = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.ResponseWriter.WriteHeader(code)
		_, _ = w.ResponseWriter.Write(gwerr.Unauthorized("session user mismatch").JSON())
		return
	}
	if code == http.StatusUnauthorized && !w.authFailed {
		w.authFailed = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.ResponseWriter.WriteHeader(code)
		_, _ = w.ResponseWriter.Write(gwerr.Unauthorized("missing or invalid bearer key").JSON())
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write 在认证失败后吞掉 SDK 后续写入的纯文本错误。
func (w *authResponseWriter) Write(p []byte) (int, error) {
	if w.authFailed {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}
