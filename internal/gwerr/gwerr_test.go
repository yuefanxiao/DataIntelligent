package gwerr

import (
	"encoding/json"
	"strings"
	"testing"
)

// 错误形状契约：Kind + 稳定 Code + Message + Details，调用方可机器判读。
func TestNotImplementedShape(t *testing.T) {
	e := NotImplemented("execute_sql")

	if e.Kind != KindNotImplemented {
		t.Errorf("Kind = %q, want %q", e.Kind, KindNotImplemented)
	}
	if e.Code != "DGW_NOT_IMPLEMENTED" {
		t.Errorf("Code = %q, want %q", e.Code, "DGW_NOT_IMPLEMENTED")
	}
	if !strings.Contains(e.Message, "execute_sql") {
		t.Errorf("Message 应含工具名: %q", e.Message)
	}
	if got := e.Details["tool"]; got != "execute_sql" {
		t.Errorf("Details[tool] = %v, want %q", got, "execute_sql")
	}

	// JSON 面：工具调用的 content 就是它。
	var back map[string]any
	if err := json.Unmarshal(e.JSON(), &back); err != nil {
		t.Fatalf("JSON 非法: %v", err)
	}
	for _, k := range []string{"kind", "code", "message", "details"} {
		if _, ok := back[k]; !ok {
			t.Errorf("JSON 缺字段 %q: %v", k, back)
		}
	}
	if back["kind"] != "not_implemented" || back["code"] != "DGW_NOT_IMPLEMENTED" {
		t.Errorf("JSON 内容不符: %v", back)
	}
}

// Error() 至少可读且含类别与 code。
func TestErrorString(t *testing.T) {
	for _, e := range []*Error{
		Unauthorized("missing or invalid bearer key"),
		InvalidRequest("bad sql", map[string]any{"sql": "DELETE x"}),
		PermissionDenied("no grant", map[string]any{"table": "a.b.c"}),
		RateLimited("too many concurrent", map[string]any{"limit": 2}),
		Internal("boom"),
	} {
		s := e.Error()
		if s == "" || !strings.Contains(s, string(e.Kind)) || !strings.Contains(s, e.Code) {
			t.Errorf("Error() = %q, 应含 kind %q 与 code %q", s, e.Kind, e.Code)
		}
	}
}

// 类别区分性：不同构造器产出不同 kind（调用方重试决策的依据）。
func TestKindsDistinct(t *testing.T) {
	got := map[Kind]bool{
		NotImplemented("x").Kind:        true,
		Unauthorized("x").Kind:          true,
		InvalidRequest("x", nil).Kind:   true,
		PermissionDenied("x", nil).Kind: true,
		RateLimited("x", nil).Kind:      true,
		Internal("x").Kind:              true,
	}
	for _, k := range []Kind{KindNotImplemented, KindUnauthorized, KindInvalidRequest, KindPermission, KindRateLimited, KindInternal} {
		if !got[k] {
			t.Errorf("缺 kind %q", k)
		}
	}
	if len(got) != 6 {
		t.Errorf("应有 6 种互异 kind，实际 %d", len(got))
	}
}
