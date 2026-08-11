package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 本机 PostgreSQL e2e（spec §5 测试决策主 seam 的 DB 侧）：Docker 一次性 PG
// 容器（postgres:17，trust 认证、127.0.0.1 随机端口映射）+ 官方 go-sdk 客户端
// 打自己的网关（HTTP 形态，bearer 认证）。docker 不可用（CI 等）→ 整文件测试
// 跳过，不影响其他包。

var (
	pgOK   bool   // Docker PG 可用（TestMain 探测）
	pgCont string // 测试用容器名
	pgPort int
	bssDSN string
	iamDSN string
)

func TestMain(m *testing.M) {
	if err := startTestPG(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e PostgreSQL（Docker）不可用，跳过 execute_sql 真实实例测试: %v\n", err)
	} else {
		pgOK = true
	}
	code := m.Run()
	if pgOK {
		stopTestPG()
	}
	os.Exit(code)
}

// startTestPG 起一个一次性 postgres:17 容器（trust 认证、随机宿主端口映射），
// 建两个库（bss/iam）+ 只读角色 + 测试表/数据/授权。
func startTestPG() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("缺少 docker 可执行文件")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		return fmt.Errorf("docker daemon 不可用: %v\n%s", err, out)
	}
	pgPort = freePort()
	pgCont = fmt.Sprintf("dgw-e2e-pg-%d-%d", os.Getpid(), pgPort)
	run := exec.Command("docker", "run", "-d", "--rm", "--name", pgCont,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", pgPort),
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust",
		"postgres:17")
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run 失败: %v\n%s", err, out)
	}

	// 就绪轮询（容器首次启动 ~几秒；docker exec 失败 = 未就绪）。
	ready := false
	for i := 0; i < 120; i++ {
		if out, err := exec.Command("docker", "exec", "-u", "postgres", pgCont,
			"pg_isready", "-U", "postgres", "-h", "127.0.0.1").CombinedOutput(); err == nil {
			_ = out
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		stopTestPG()
		return fmt.Errorf("容器 %s 就绪超时", pgCont)
	}

	psql := func(dbname string, stmts ...string) error {
		args := []string{"exec", "-u", "postgres", pgCont, "psql",
			"-U", "postgres", "-h", "127.0.0.1", "-d", dbname, "-v", "ON_ERROR_STOP=1", "-q"}
		for _, s := range stmts {
			args = append(args, "-c", s)
		}
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("psql(%s) 失败: %v\n%s", dbname, err, out)
		}
		return nil
	}

	if err := psql("postgres",
		"CREATE ROLE dgw_ro LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT",
		"CREATE DATABASE bss",
		"CREATE DATABASE iam",
	); err != nil {
		stopTestPG()
		return err
	}
	if err := psql("bss",
		"CREATE TABLE orders (id bigint PRIMARY KEY, status text NOT NULL, amount numeric(12,2), paid_at timestamptz, note bytea, meta jsonb, uid uuid)",
		"INSERT INTO orders SELECT g, CASE WHEN g % 10 = 0 THEN 'refunded' ELSE 'paid' END, (g * 1.5)::numeric(12,2), now() - make_interval(mins => g), NULL, jsonb_build_object('a', g), md5(g::text)::uuid FROM generate_series(1, 600) g",
		"CREATE TABLE secret (id int)",
		"GRANT SELECT ON orders TO dgw_ro",
	); err != nil {
		stopTestPG()
		return err
	}
	if err := psql("iam",
		"CREATE TABLE users (id bigint PRIMARY KEY, name text NOT NULL)",
		"INSERT INTO users VALUES (1, 'alice'), (2, 'bob')",
		"GRANT SELECT ON users TO dgw_ro",
	); err != nil {
		stopTestPG()
		return err
	}

	bssDSN = fmt.Sprintf("postgres://dgw_ro@127.0.0.1:%d/bss?sslmode=disable", pgPort)
	iamDSN = fmt.Sprintf("postgres://dgw_ro@127.0.0.1:%d/iam?sslmode=disable", pgPort)
	return nil
}

func stopTestPG() {
	if pgCont != "" {
		exec.Command("docker", "rm", "-f", pgCont).Run()
		pgCont = ""
	}
}

// freePort 拿一个随机空闲端口（listen :0 后关闭——竞态可接受，测试专用）。
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 54329
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// requirePG 跳过无 PG 环境（CI 等）。
func requirePG(t *testing.T) {
	t.Helper()
	if !pgOK {
		t.Skip("本机无 docker/PG 容器，跳过真实实例 e2e")
	}
}

// e2eGateway 建一个带 PG 路由的网关（注入 execute_sql）+ 测试身份授权。
func e2eGateway(t *testing.T, entries []db.Entry, limit int, timeout time.Duration, grants_ ...string) (*Gateway, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	router, err := db.NewRouter(context.Background(), entries, timeout)
	if err != nil {
		t.Fatalf("db.NewRouter: %v", err)
	}
	t.Cleanup(router.Close)
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), WithExecuteSQL(router, limit))
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	for _, fqn := range grants_ {
		if err := grants.AddGrant(context.Background(), st, "dev-alice", fqn); err != nil {
			t.Fatalf("AddGrant(%s): %v", fqn, err)
		}
	}
	// 授权加在 New 之后：快照重载（生产 = 热重载轮询，测试 = 显式 Load）。
	if err := g.authz.Load(context.Background()); err != nil {
		t.Fatalf("authz.Load: %v", err)
	}
	return g, st
}

// callSQL 经真实 MCP 会话调用 execute_sql 并返回结构化结果。
func callSQL(t *testing.T, session *mcp.ClientSession, args map[string]any) *sqlResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(execute_sql): %v", err)
	}
	if res == nil || res.IsError {
		text := ""
		if res != nil && len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("期望成功结果，得到 error result: %+v\ncontent: %s", res, text)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content 类型 = %T，期望 TextContent", res.Content[0])
	}
	var out sqlResult
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("结果 JSON 解析失败: %v\n%s", err, tc.Text)
	}
	return &out
}

// callSQLErr 经真实 MCP 会话调用 execute_sql 并返回结构化错误。
func callSQLErr(t *testing.T, session *mcp.ClientSession, args map[string]any) *gwerr.Error {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "execute_sql",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(execute_sql): %v", err)
	}
	return decodeErrorResult(t, res)
}

// ── 用例 ──────────────────────────────────────────────────────────────────

// 主路径：白名单表查询成功，结果结构化 JSON（列名+类型+行+元信息）。
func TestExecuteSQLE2EHappyPath(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
		{DBName: "iam", Service: "iam", DSN: iamDSN},
	}, 500, 30*time.Second, "bss.bss.orders", "iam.iam.users")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	res := callSQL(t, session, map[string]any{
		"sql":     "SELECT id, status, amount, paid_at, meta, uid FROM orders ORDER BY id LIMIT 2",
		"dbname":  "bss",
		"plan_id": "plan-42",
	})
	if len(res.Columns) != 6 {
		t.Fatalf("列数 = %d，期望 6: %+v", len(res.Columns), res.Columns)
	}
	if res.Columns[0].Name != "id" || res.Columns[0].Type != "int8" {
		t.Errorf("列[0] = %+v，期望 id/int8", res.Columns[0])
	}
	if res.Columns[2].Type != "numeric" {
		t.Errorf("amount 类型 = %s，期望 numeric", res.Columns[2].Type)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(res.Rows))
	}
	if res.Meta.RowCount != 2 || res.Meta.Truncated {
		t.Errorf("meta = %+v，期望 row_count=2 truncated=false", res.Meta)
	}
	if res.Meta.DBName != "bss" {
		t.Errorf("meta.dbname = %q，期望 bss", res.Meta.DBName)
	}
	if res.Meta.PlanID != "plan-42" {
		t.Errorf("meta.plan_id = %q，期望 plan-42（透传）", res.Meta.PlanID)
	}
	// 数值归一化：numeric → 文本形态（与 psql 一致）；jsonb → 原生 JSON；uuid → 文本
	row := res.Rows[0]
	if row[2] != "1.50" {
		t.Errorf("amount = %v (%T)，期望 \"1.50\"", row[2], row[2])
	}
	if row[4] == nil {
		t.Errorf("meta(jsonb) 应解析为 JSON 值: %v", row[4])
	}
	if s, ok := row[5].(string); !ok || len(s) != 36 {
		t.Errorf("uid = %v (%T)，期望 36 字符 uuid 文本", row[5], row[5])
	}
	if _, ok := row[3].(string); !ok {
		t.Errorf("paid_at = %v (%T)，期望 RFC3339 文本", row[3], row[3])
	}
}

// dbname 路由：iam 库的表只能经 iam 路由查到；缺省推断（单库配置）。
func TestExecuteSQLE2EDBNameRouting(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
		{DBName: "iam", Service: "iam", DSN: iamDSN},
	}, 500, 30*time.Second, "bss.bss.orders", "iam.iam.users")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 多库配置缺省 dbname → 结构化拒绝（不猜测目标库）
	e := callSQLErr(t, session, map[string]any{"sql": "SELECT 1"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "dbname_required" {
		t.Fatalf("缺省 dbname 错误 = %s reason=%v，期望 dbname_required", e.Kind, e.Details["reason"])
	}
	// 未知 dbname → 结构化拒绝
	e = callSQLErr(t, session, map[string]any{"sql": "SELECT 1", "dbname": "nope"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "unknown_dbname" {
		t.Fatalf("未知 dbname 错误 = %s reason=%v，期望 unknown_dbname", e.Kind, e.Details["reason"])
	}
	// iam 路由正确
	res := callSQL(t, session, map[string]any{"sql": "SELECT name FROM users ORDER BY id", "dbname": "iam"})
	if len(res.Rows) != 2 || res.Rows[0][0] != "alice" || res.Meta.DBName != "iam" {
		t.Fatalf("iam 路由结果 = %+v", res)
	}
	// 经 bss 路由查 iam 的表 → 未授权（FQN 组装按路由库）
	e = callSQLErr(t, session, map[string]any{"sql": "SELECT * FROM users", "dbname": "bss"})
	if e.Kind != gwerr.KindPermission {
		t.Fatalf("跨库查询错误 = %s，期望 permission_denied", e.Kind)
	}

	// 单库配置：缺省 dbname 推断到唯一库
	g2, st2 := e2eGateway(t, []db.Entry{{DBName: "iam", Service: "iam", DSN: iamDSN}}, 500, 30*time.Second, "iam.iam.users")
	key2 := createKey(t, st2, "dev-alice")
	ts2 := httptest.NewServer(g2.HTTPHandler())
	defer ts2.Close()
	session2 := connectHTTP(t, ts2.URL, key2)
	defer session2.Close()
	res2 := callSQL(t, session2, map[string]any{"sql": "SELECT count(*) FROM users"})
	if res2.Meta.DBName != "iam" || len(res2.Rows) != 1 {
		t.Fatalf("单库缺省路由结果 = %+v", res2)
	}
}

// 限额包层：>500 行截断 + truncated 标记；用户 SQL 自带 LIMIT/尾分号不受影响。
func TestExecuteSQLE2ETruncation(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	res := callSQL(t, session, map[string]any{"sql": "SELECT id FROM orders ORDER BY id;", "dbname": "bss"})
	if res.Meta.RowCount != 500 || !res.Meta.Truncated {
		t.Fatalf("600 行表应截断为 500 + truncated: %+v", res.Meta)
	}
	if len(res.Rows) != 500 {
		t.Fatalf("返回行数 = %d，期望 500", len(res.Rows))
	}
	if res.Rows[0][0] != float64(1) || res.Rows[499][0] != float64(500) {
		t.Fatalf("LIMIT 包层应保留 ORDER BY 语义: 首行=%v 末行=%v", res.Rows[0][0], res.Rows[499][0])
	}

	// 用户 SQL 自带 LIMIT：包层叠加，不破坏
	res2 := callSQL(t, session, map[string]any{"sql": "SELECT id FROM orders ORDER BY id LIMIT 3", "dbname": "bss"})
	if res2.Meta.RowCount != 3 || res2.Meta.Truncated {
		t.Fatalf("自带 LIMIT 结果 = %+v", res2.Meta)
	}
}

// 拒绝语义（端到端）：未授权表 / 非 SELECT / 语法错误 / 未配置。
func TestExecuteSQLE2ERejections(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 未授权表（白名单外，含真实存在的 secret 表）→ permission_denied/not_granted
	e := callSQLErr(t, session, map[string]any{"sql": "SELECT * FROM secret", "dbname": "bss"})
	if e.Kind != gwerr.KindPermission || e.Details["reason"] != "not_granted" {
		t.Fatalf("未授权表错误 = %s reason=%v，期望 permission_denied/not_granted", e.Kind, e.Details["reason"])
	}
	// 非 public schema 引用 → 无法映射 → 未知表（unknown_table 路径）
	e = callSQLErr(t, session, map[string]any{"sql": "SELECT * FROM audit.logs", "dbname": "bss"})
	if e.Kind != gwerr.KindPermission || e.Details["reason"] != "unknown_table" {
		t.Fatalf("未知表错误 = %s reason=%v，期望 permission_denied/unknown_table", e.Kind, e.Details["reason"])
	}
	// 非 SELECT → invalid_request/non_select
	e = callSQLErr(t, session, map[string]any{"sql": "DELETE FROM orders WHERE id = 1", "dbname": "bss"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "non_select" {
		t.Fatalf("非 SELECT 错误 = %s reason=%v，期望 invalid_request/non_select", e.Kind, e.Details["reason"])
	}
	// 语法错误 → invalid_request/syntax_error
	e = callSQLErr(t, session, map[string]any{"sql": "SELEC 1", "dbname": "bss"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "syntax_error" {
		t.Fatalf("语法错误 = %s reason=%v，期望 invalid_request/syntax_error", e.Kind, e.Details["reason"])
	}
	// 空 SQL / 仅注释 → invalid_request/empty
	for _, sql := range []string{"   ", "-- 只有注释"} {
		e = callSQLErr(t, session, map[string]any{"sql": sql, "dbname": "bss"})
		if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "empty" {
			t.Fatalf("空 SQL %q = %s reason=%v，期望 invalid_request/empty", sql, e.Kind, e.Details["reason"])
		}
	}
	// 批处理（多语句）→ invalid_request/multi_statement（不落到误导性 42601）
	e = callSQLErr(t, session, map[string]any{"sql": "SELECT 1; SELECT 2", "dbname": "bss"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "multi_statement" {
		t.Fatalf("批处理 = %s reason=%v，期望 invalid_request/multi_statement", e.Kind, e.Details["reason"])
	}
}

// 物理边界：statement_timeout 生效（PG 层兜底，超时结构化拒绝）。
func TestExecuteSQLE2ETimeout(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 200*time.Millisecond)
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	e := callSQLErr(t, session, map[string]any{"sql": "SELECT pg_sleep(2)", "dbname": "bss"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "timeout" {
		t.Fatalf("超时错误 = %s reason=%v details=%v msg=%q，期望 invalid_request/timeout", e.Kind, e.Details["reason"], e.Details, e.Message)
	}
}
