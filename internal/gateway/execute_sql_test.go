package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/loadgate"
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

	t.Run("连接失败 → internal（调用方不可自愈）", func(t *testing.T) {
		for _, code := range []string{"08006", "08001"} {
			e := pgError(&pgconn.PgError{Code: code, Message: "connection failed"})
			if e.Kind != gwerr.KindInternal {
				t.Errorf("code %s kind = %s，期望 %s", code, e.Kind, gwerr.KindInternal)
			}
		}
	})

	t.Run("实例停机 → internal", func(t *testing.T) {
		e := pgError(&pgconn.PgError{Code: "57P01", Message: "admin shutdown"})
		if e.Kind != gwerr.KindInternal {
			t.Errorf("kind = %s，期望 %s", e.Kind, gwerr.KindInternal)
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
	// 合法范围：注入依赖（New 后续需要 store 加载授权快照；router 用不触
	// 连接的假 DSN——pgxpool 惰性建连）。
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	router, err := db.NewRouter(context.Background(), []db.Entry{
		{DBName: "bss", Service: "bss", DSN: "postgres://dgw_ro@127.0.0.1:1/bss"},
	}, time.Second)
	if err != nil {
		t.Fatalf("db.NewRouter: %v", err)
	}
	defer router.Close()
	for _, ok := range []int{500, 1000, 5000} {
		g, err := New(st, nil, WithExecuteSQL(router, ok))
		if err != nil {
			t.Errorf("limit=%d 应合法: %v", ok, err)
		} else if g.execSQL == nil || g.execSQL.limit != ok {
			t.Errorf("limit=%d 应注入 execSQL 依赖", ok)
		}
	}
}

// 并发闸数值非法（<1 / 进程级 < 每 key）= 启动失败 fail fast（spec §4.9）。
func TestLoadGateValidation(t *testing.T) {
	for name, args := range map[string]struct{ perKey, total int }{
		"perKey 零":       {0, 8},
		"perKey 负":       {-1, 8},
		"total 零":        {2, 0},
		"total < perKey": {5, 3},
	} {
		if _, err := New(nil, nil, WithLoadGate(args.perKey, args.total)); err == nil {
			t.Errorf("%s: WithLoadGate(%d, %d) 应启动失败", name, args.perKey, args.total)
		}
	}
	// 合法值（含 total == perKey 边界）：默认值注入。
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	for _, ok := range []struct{ perKey, total int }{{1, 1}, {2, 8}, {8, 8}} {
		g, err := New(st, nil, WithLoadGate(ok.perKey, ok.total))
		if err != nil {
			t.Errorf("WithLoadGate(%d, %d) 应合法: %v", ok.perKey, ok.total, err)
		} else if g.loadGate == nil {
			t.Error("应注入并发闸")
		}
	}
	// 未注入 = spec 默认 2/8（负载防护恒启用）。
	g, err := New(st, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if g.loadGate == nil {
		t.Fatal("并发闸应默认启用")
	}
}

// 并发闸在触库之前生效：key 饱和 → 结构化 rate_limited（不排队）；
// 释放后恢复 → 请求穿过闸到达 DB（不可达 DSN → internal，证明闸已放行）。
func TestExecuteSQLGateRejectsBeforeDB(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	// 假 DSN（127.0.0.1:1 拒绝连接）：闸饱和时请求不应触达 DB。
	router, err := db.NewRouter(context.Background(), []db.Entry{
		{DBName: "bss", Service: "bss", DSN: "postgres://dgw_ro@127.0.0.1:1/bss"},
	}, time.Second)
	if err != nil {
		t.Fatalf("db.NewRouter: %v", err)
	}
	defer router.Close()
	g, err := New(st, nil, WithExecuteSQL(router, 500), WithLoadGate(1, 8))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := withUserID(context.Background(), "dev-alice")
	req := &mcp.CallToolRequest{}
	in := executeSQLInput{SQL: "SELECT 1", DBName: "bss"}

	// 占满 dev-alice 的每 key 配额（1/1）→ 调用被闸拒，不触 DB。
	if e := g.loadGate.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("TryAcquire: %v", e)
	}
	res, _, _ := g.handleExecuteSQL(ctx, req, in)
	if res == nil || !res.IsError {
		t.Fatal("饱和时应返回错误结果")
	}
	e := decodeError(t, res)
	if e.Kind != gwerr.KindRateLimited || e.Details["reason"] != loadgate.ReasonKeyConcurrency {
		t.Fatalf("拒绝 = %s reason=%v，期望 rate_limited/%s", e.Kind, e.Details["reason"], loadgate.ReasonKeyConcurrency)
	}

	// 释放 → 闸放行 → 请求触达 DB（不可达 DSN → internal 连接错误）。
	g.loadGate.Release("dev-alice")
	res, _, _ = g.handleExecuteSQL(ctx, req, in)
	if res == nil || !res.IsError {
		t.Fatal("触库失败应返回错误结果")
	}
	if e := decodeError(t, res); e.Kind != gwerr.KindInternal {
		t.Fatalf("触库错误 = %s，期望 internal（连接失败，非闸拒）", e.Kind)
	}
}

// 进程级闸：跨 key 共享总配额，进程级饱和 → 结构化拒绝（key 本身未满）。
func TestExecuteSQLProcessGateRejects(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	router, err := db.NewRouter(context.Background(), []db.Entry{
		{DBName: "bss", Service: "bss", DSN: "postgres://dgw_ro@127.0.0.1:1/bss"},
	}, time.Second)
	if err != nil {
		t.Fatalf("db.NewRouter: %v", err)
	}
	defer router.Close()
	// perKey=2, total=2：两个 key 各占 1 → 进程级满，第三 key 被拒（其 key 配额未满）。
	g, err := New(st, nil, WithExecuteSQL(router, 500), WithLoadGate(2, 2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e := g.loadGate.TryAcquire("dev-bob"); e != nil {
		t.Fatalf("TryAcquire(bob): %v", e)
	}
	if e := g.loadGate.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("TryAcquire(alice): %v", e)
	}

	res, _, _ := g.handleExecuteSQL(withUserID(context.Background(), "dev-carol"),
		&mcp.CallToolRequest{}, executeSQLInput{SQL: "SELECT 1", DBName: "bss"})
	if res == nil || !res.IsError {
		t.Fatal("进程级饱和时应返回错误结果")
	}
	if e := decodeError(t, res); e.Kind != gwerr.KindRateLimited || e.Details["reason"] != loadgate.ReasonProcessConcurrency {
		t.Fatalf("拒绝 = %s reason=%v，期望 rate_limited/%s", e.Kind, e.Details["reason"], loadgate.ReasonProcessConcurrency)
	}
}

// decodeError 从 errResult 产物中解出 gwerr（handleExecuteSQL 直调形态）。
func decodeError(t *testing.T, res *mcp.CallToolResult) *gwerr.Error {
	t.Helper()
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content 类型 = %T，期望 TextContent", res.Content[0])
	}
	var e gwerr.Error
	if err := json.Unmarshal([]byte(text.Text), &e); err != nil {
		t.Fatalf("错误 content 非 gwerr JSON: %v（原文 %q）", err, text.Text)
	}
	return &e
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
