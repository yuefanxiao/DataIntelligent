package gateway

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

func TestWrapLimit(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		limit int
		want  string
	}{
		{"plain", `SELECT * FROM orders`, 500,
			`SELECT * FROM (SELECT * FROM orders) _q LIMIT 501`},
		{"trailing_semicolon", `SELECT * FROM orders;`, 500,
			`SELECT * FROM (SELECT * FROM orders) _q LIMIT 501`},
		{"own_limit", `SELECT * FROM orders LIMIT 10`, 100,
			`SELECT * FROM (SELECT * FROM orders LIMIT 10) _q LIMIT 101`},
		{"whitespace_padded", "  SELECT 1  ", 50,
			`SELECT * FROM (SELECT 1) _q LIMIT 51`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrapLimit(tc.sql, tc.limit); got != tc.want {
				t.Errorf("wrapLimit(%q, %d) = %q，期望 %q", tc.sql, tc.limit, got, tc.want)
			}
		})
	}
}

func TestNormalizeValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"nil", nil, nil},
		{"int", int64(42), int64(42)},
		{"float", 3.14, 3.14},
		{"bool", true, true},
		{"string", "x", "x"},
		{"time", time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC), "2026-08-12T10:00:00Z"},
		{"bytea", []byte{0xde, 0xad}, `\xdead`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeValue(tc.in); got != tc.want {
				t.Errorf("normalizeValue(%v) = %v (%T)，期望 %v", tc.in, got, got, tc.want)
			}
		})
	}
}

func TestPgError(t *testing.T) {
	t.Run("statement_timeout → invalid_request/timeout", func(t *testing.T) {
		e := pgError(&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"})
		if e.Kind != gwerr.KindInvalidRequest {
			t.Errorf("kind = %s，期望 %s", e.Kind, gwerr.KindInvalidRequest)
		}
		if e.Details["reason"] != "timeout" {
			t.Errorf("reason = %v，期望 timeout", e.Details["reason"])
		}
		if e.Details["pg_code"] != "57014" {
			t.Errorf("pg_code = %v", e.Details["pg_code"])
		}
	})

	t.Run("权限拒绝 → permission_denied（物理边界兜底）", func(t *testing.T) {
		e := pgError(&pgconn.PgError{Code: "42501", Message: "permission denied for table x"})
		if e.Kind != gwerr.KindPermission {
			t.Errorf("kind = %s，期望 %s", e.Kind, gwerr.KindPermission)
		}
	})

	t.Run("语法错误 → invalid_request/pg_error", func(t *testing.T) {
		e := pgError(&pgconn.PgError{Code: "42601", Message: "syntax error"})
		if e.Kind != gwerr.KindInvalidRequest {
			t.Errorf("kind = %s，期望 %s", e.Kind, gwerr.KindInvalidRequest)
		}
		if e.Details["reason"] != "pg_error" {
			t.Errorf("reason = %v，期望 pg_error", e.Details["reason"])
		}
	})

	t.Run("非 PgError → internal", func(t *testing.T) {
		e := pgError(errors.New("connection refused"))
		if e.Kind != gwerr.KindInternal {
			t.Errorf("kind = %s，期望 %s", e.Kind, gwerr.KindInternal)
		}
	})
}

func TestExecuteSQLLimitValidation(t *testing.T) {
	// 限额越界（spec §4.9：500-5000）= 启动失败 fail fast（校验在触库之前）。
	for _, bad := range []int{499, 0, -1, 5001} {
		if _, err := New(nil, nil, WithExecuteSQL(nil, bad)); err == nil {
			t.Errorf("limit=%d 应启动失败", bad)
		}
	}
	// 合法范围：注入依赖（New 后续需要 store 加载授权快照）。
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	for _, ok := range []int{500, 1000, 5000} {
		g, err := New(st, nil, WithExecuteSQL(nil, ok))
		if err != nil {
			t.Errorf("limit=%d 应合法: %v", ok, err)
		} else if g.execSQL == nil || g.execSQL.limit != ok {
			t.Errorf("limit=%d 应注入 execSQL 依赖", ok)
		}
	}
}

func TestSQLResultJSONShape(t *testing.T) {
	// 结构化结果的可 JSON 序列化形状（列名+类型+行数组+元信息，渲染交给 Agent）。
	res := &sqlResult{
		Columns: []sqlColumn{{Name: "id", Type: "int8"}, {Name: "ts", Type: "timestamptz"}},
		Rows:    [][]any{{int64(1), "2026-08-12T10:00:00Z"}},
		Meta:    sqlMeta{RowCount: 1, Truncated: false, DBName: "bss", PlanID: "p-1", ElapsedMS: 3},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	// 关键字段名契约（执行记录/Agent 解析依赖）
	for _, key := range []string{`"columns"`, `"rows"`, `"meta"`, `"row_count"`, `"truncated"`, `"dbname"`, `"plan_id"`, `"elapsed_ms"`, `"name"`, `"type"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("结果 JSON 缺字段 %s: %s", key, b)
		}
	}
}
