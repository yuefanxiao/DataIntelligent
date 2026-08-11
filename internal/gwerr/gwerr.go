// Package gwerr 定义网关的统一结构化错误格式（spec §4.5/§6.3、ADR-0008）。
//
// 所有面向调用方（Coding Agent）的失败都以 Kind + 稳定 Code + Message +
// Details 的 JSON 结构编码：调用方凭 kind 即可区分「语法错误 vs 无权限 vs
// 限流」，决定是否调整后重试；网关侧不重试、无自愈循环。
//
// 承载面有两处，格式同一：
//   - HTTP 认证失败：401/403 响应体被网关的 auth 中间件改写为 gwerr JSON
//     （服务端内部 500 保持 SDK 原生 body，调用方按状态码处理）。
//   - 工具调用失败：CallToolResult{IsError:true} 的 text content。
package gwerr

import (
	"encoding/json"
	"fmt"
)

// Kind 是错误类别，机器可读且稳定。
type Kind string

const (
	// KindInvalidRequest 请求本身不合法（参数错误、SQL 语法错误等）。
	KindInvalidRequest Kind = "invalid_request"
	// KindUnauthorized 认证失败：无/错凭据（401）。
	KindUnauthorized Kind = "unauthorized"
	// KindPermission 认证通过但无权限（表级授权拒绝，ADR-0004 默认拒绝）。
	KindPermission Kind = "permission_denied"
	// KindRateLimited 并发超限（每 key/进程级闸，快速失败不排队）。
	KindRateLimited Kind = "rate_limited"
	// KindNotImplemented 工具尚未实现（v1 构建期 stub）。
	KindNotImplemented Kind = "not_implemented"
	// KindInternal 服务端内部错误（存储故障等，调用方不可自愈）。
	KindInternal Kind = "internal"
)

// Error 是网关的统一结构化错误。
type Error struct {
	Kind    Kind           `json:"kind"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s [%s]: %s", e.Kind, e.Code, e.Message)
}

// JSON 返回错误的结构化 JSON 文本（工具调用的 content / HTTP 401 响应体）。
func (e *Error) JSON() []byte {
	b, err := json.Marshal(e)
	if err != nil {
		// Error 的全部字段均可 JSON 序列化，此分支不可达；兜底保可读性。
		return []byte(`{"kind":"internal","code":"DGW_INTERNAL","message":"marshal error"}`)
	}
	return b
}

// NotImplemented 构造 stub 未实现错误（本票六工具全部返回）。
func NotImplemented(tool string) *Error {
	return &Error{
		Kind:    KindNotImplemented,
		Code:    "DGW_NOT_IMPLEMENTED",
		Message: fmt.Sprintf("工具 %q 尚未实现（v1 构建期 stub）", tool),
		Details: map[string]any{"tool": tool},
	}
}

// Unauthorized 构造认证失败错误。
func Unauthorized(message string) *Error {
	return &Error{
		Kind:    KindUnauthorized,
		Code:    "DGW_UNAUTHORIZED",
		Message: message,
	}
}

// InvalidRequest 构造请求不合法错误（供后续票的语法/参数拒绝复用）。
func InvalidRequest(message string, details map[string]any) *Error {
	return &Error{
		Kind:    KindInvalidRequest,
		Code:    "DGW_INVALID_REQUEST",
		Message: message,
		Details: details,
	}
}

// PermissionDenied 构造无权限错误（供 02/03 票的表级授权拒绝复用）。
func PermissionDenied(message string, details map[string]any) *Error {
	return &Error{
		Kind:    KindPermission,
		Code:    "DGW_PERMISSION_DENIED",
		Message: message,
		Details: details,
	}
}

// RateLimited 构造并发超限错误（供 05 票的并发闸复用）。
func RateLimited(message string, details map[string]any) *Error {
	return &Error{
		Kind:    KindRateLimited,
		Code:    "DGW_RATE_LIMITED",
		Message: message,
		Details: details,
	}
}

// Internal 构造服务端内部错误。
func Internal(message string) *Error {
	return &Error{
		Kind:    KindInternal,
		Code:    "DGW_INTERNAL",
		Message: message,
	}
}
