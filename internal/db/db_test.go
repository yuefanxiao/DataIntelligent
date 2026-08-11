package db

import (
	"context"
	"time"

	"strings"
	"testing"
)

func TestParseEntries(t *testing.T) {
	t.Run("空配置", func(t *testing.T) {
		got, err := ParseEntries("")
		if err != nil {
			t.Fatalf("空配置应解析成功: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("空配置条目 = %d，期望 0", len(got))
		}
	})

	t.Run("合法多库", func(t *testing.T) {
		raw := `[
			{"dbname": "bss", "service": "bss", "dsn": "postgres://dgw_ro@localhost:5432/bss"},
			{"dbname": "iam", "service": "iam", "dsn": "postgres://dgw_ro@localhost:5432/iam"}
		]`
		got, err := ParseEntries(raw)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(got) != 2 || got[0].DBName != "bss" || got[1].Service != "iam" {
			t.Fatalf("解析结果 = %+v", got)
		}
	})

	t.Run("非 JSON 拒绝", func(t *testing.T) {
		if _, err := ParseEntries("not-json"); err == nil {
			t.Fatal("非法 JSON 应拒绝")
		}
	})

	t.Run("dbname 重复拒绝", func(t *testing.T) {
		raw := `[
			{"dbname": "bss", "service": "a", "dsn": "postgres://x"},
			{"dbname": "bss", "service": "b", "dsn": "postgres://y"}
		]`
		if _, err := ParseEntries(raw); err == nil {
			t.Fatal("dbname 重复应拒绝")
		}
	})

	t.Run("FQN 段含点拒绝", func(t *testing.T) {
		for _, e := range []Entry{
			{DBName: "b.ss", Service: "bss", DSN: "postgres://x"},
			{DBName: "bss", Service: "b.ss", DSN: "postgres://x"},
		} {
			if err := validateEntry(e); err == nil {
				t.Fatalf("含点段应拒绝: %+v", e)
			}
		}
	})

	t.Run("空段拒绝", func(t *testing.T) {
		for _, e := range []Entry{
			{DBName: "", Service: "bss", DSN: "postgres://x"},
			{DBName: "bss", Service: "", DSN: "postgres://x"},
			{DBName: "bss", Service: "bss", DSN: ""},
		} {
			if err := validateEntry(e); err == nil {
				t.Fatalf("空段应拒绝: %+v", e)
			}
		}
	})
}

func TestRouterLookupAndSingle(t *testing.T) {
	// 纯逻辑分支（不建真实连接）：
	// Single：空路由 → ""
	if got := (&Router{}).Single(); got != "" {
		t.Fatalf("空路由 Single() = %q，期望空串", got)
	}
	// Lookup：未知 dbname → ok=false
	if _, _, ok := (&Router{}).Lookup("nope"); ok {
		t.Fatal("未知 dbname 应 ok=false")
	}
	// Lookup 的 service 段透传（不触连接；池为 nil 只验路由命中）
	r2 := &Router{routes: map[string]route{"bss": {service: "bss"}}}
	if _, svc, ok := r2.Lookup("bss"); !ok || svc != "bss" {
		t.Fatalf("Lookup(bss) = (%v, %q)，期望 (true, bss)", ok, svc)
	}
}

// 确保错误消息含可读的配置指引（运维者按消息即可修正 env）。
func TestParseEntriesErrorMessage(t *testing.T) {
	_, err := ParseEntries(`[{"dbname": "bss", "service": "bss"}]`)
	if err == nil {
		t.Fatal("缺 dsn 应拒绝")
	}
	if !strings.Contains(err.Error(), "dsn") {
		t.Fatalf("错误消息应指明缺失字段: %v", err)
	}
}

// statement_timeout 非法值（0 = PG 关闭超时 / 负值 = 连接建立即失败）=
// 启动失败 fail fast（负载防护不能被静默绕过）。
func TestNewRouterStatementTimeoutValidation(t *testing.T) {
	entries := []Entry{{DBName: "bss", Service: "bss", DSN: "postgres://dgw_ro@127.0.0.1:1/bss"}}
	for _, bad := range []time.Duration{0, -1, -30 * time.Second} {
		if _, err := NewRouter(context.Background(), entries, bad); err == nil {
			t.Errorf("statementTimeout=%v 应启动失败", bad)
		}
	}
	if _, err := NewRouter(context.Background(), entries, time.Millisecond); err != nil {
		t.Errorf("statementTimeout=1ms 应合法: %v", err)
	}
}
