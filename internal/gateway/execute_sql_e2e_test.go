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
	"github.com/yuefanxiao/DataIntelligent/internal/loadgate"
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

// e2eGateway 建一个带 PG 路由的网关（注入 execute_sql，并发闸取 spec 默认
// 2/8）+ 测试身份授权。
func e2eGateway(t *testing.T, entries []db.Entry, limit int, timeout time.Duration, grants_ ...string) (*Gateway, *store.Store) {
	t.Helper()
	return e2eGatewayWith(t, entries, limit, timeout, 2, 8, grants_...)
}

// e2eGatewayWith 是 e2eGateway 的闸数值注入形态（并发闸测试用短配额）。
func e2eGatewayWith(t *testing.T, entries []db.Entry, limit int, timeout time.Duration, gatePerKey, gateTotal int, grants_ ...string) (*Gateway, *store.Store) {
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
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), WithExecuteSQL(router, limit), WithLoadGate(gatePerKey, gateTotal))
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

// 物理边界默认值：连接级 statement_timeout 默认 30s 生效（spec §4.9；
// 可配路径 = TestExecuteSQLE2ETimeout 的 200ms 注入）。
func TestExecuteSQLE2EStatementTimeoutDefault(t *testing.T) {
	requirePG(t)
	g, st := e2eGateway(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	res := callSQL(t, session, map[string]any{"sql": "SELECT current_setting('statement_timeout')", "dbname": "bss"})
	if len(res.Rows) != 1 {
		t.Fatalf("结果行数 = %d，期望 1", len(res.Rows))
	}
	if got, ok := res.Rows[0][0].(string); !ok || got != "30s" {
		t.Fatalf("statement_timeout = %v (%T)，期望 \"30s\"（默认 30s 连接级生效）", res.Rows[0][0], res.Rows[0][0])
	}
}

// callOutcome 是并发闸 e2e 里 goroutine 调用的结果（不能在 goroutine 里
// 用 t.Fatalf，结果经 channel 回传主测试）。
type callOutcome struct {
	res     *sqlResult
	e       *gwerr.Error
	elapsed time.Duration
}

// holdCall 发一个长查询并占用并发位：CallTool 发出后立刻通知 issued，
// 完成后把结果送回 out。
func holdCall(session *mcp.ClientSession, sql string, issued chan<- struct{}, out chan<- callOutcome) {
	issued <- struct{}{}
	start := time.Now()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql", Arguments: map[string]any{"sql": sql, "dbname": "bss"},
	})
	elapsed := time.Since(start)
	out <- outcomeOf(res, err, elapsed)
}

// outcomeOf 把 CallTool 的返回转成 callOutcome（成功/结构化错误统一收口）。
func outcomeOf(res *mcp.CallToolResult, err error, elapsed time.Duration) callOutcome {
	if err != nil {
		return callOutcome{e: &gwerr.Error{Kind: gwerr.KindInternal, Message: err.Error()}, elapsed: elapsed}
	}
	// 成功路径优先（平铺，避免错误解析的深层嵌套）
	if res != nil && !res.IsError {
		var out sqlResult
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			_ = json.Unmarshal([]byte(tc.Text), &out)
		}
		return callOutcome{res: &out, elapsed: elapsed}
	}
	// 错误路径：content 应为 gwerr JSON
	if res != nil && len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			var e gwerr.Error
			if json.Unmarshal([]byte(tc.Text), &e) == nil {
				return callOutcome{e: &e, elapsed: elapsed}
			}
		}
	}
	return callOutcome{e: &gwerr.Error{Kind: gwerr.KindInternal, Message: "error result"}, elapsed: elapsed}
}

// assertFastReject 断言一次调用被并发闸快速拒绝（<1s 不排队）且 reason 正确。
func assertFastReject(t *testing.T, session *mcp.ClientSession, wantReason string) *gwerr.Error {
	t.Helper()
	start := time.Now()
	e := callSQLErr(t, session, map[string]any{"sql": "SELECT pg_sleep(3)", "dbname": "bss"})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("并发超限请求耗时 %v，期望快速失败（不排队）", elapsed)
	}
	if e.Kind != gwerr.KindRateLimited || e.Details["reason"] != wantReason {
		t.Fatalf("请求 = %s reason=%v，期望 rate_limited/%s", e.Kind, e.Details["reason"], wantReason)
	}
	return e
}

// drainHolders 收齐在途查询的结果并断言全部成功（闸位不误伤在途请求）。
func drainHolders(t *testing.T, out chan callOutcome, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if oc := <-out; oc.e != nil {
			t.Fatalf("在途查询应成功，得到错误: %v", oc.e)
		}
	}
}

// 负向例 4：同 key 并发 >2 → 结构化拒绝（不排队、不影响其他 key）。
// 每 key 配额 2：两并发在途时第三发快速失败；同用户其他 key / 其他用户
// 照常放行（key 粒度隔离）；在途结束后配额恢复。
func TestExecuteSQLE2EConcurrencyGate(t *testing.T) {
	requirePG(t)
	g, st := e2eGatewayWith(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 30*time.Second, 2, 8, "bss.bss.orders")
	keyA := createKey(t, st, "dev-alice")
	keyB := createKey(t, st, "dev-bob")
	// bob 也需要表授权（跨用户隔离断言用）
	if err := grants.AddGrant(context.Background(), st, "dev-bob", "bss.bss.orders"); err != nil {
		t.Fatalf("AddGrant(bob): %v", err)
	}
	if err := g.authz.Load(context.Background()); err != nil {
		t.Fatalf("authz.Load: %v", err)
	}
	// keyA 的行 ID = 每 key 闸的粒度标识（details.key 应等于它）
	keyAID := keyIDForUser(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()

	sa1 := connectHTTP(t, ts.URL, keyA)
	defer sa1.Close()
	sa2 := connectHTTP(t, ts.URL, keyA)
	defer sa2.Close()
	sa3 := connectHTTP(t, ts.URL, keyA)
	defer sa3.Close()
	sb := connectHTTP(t, ts.URL, keyB)
	defer sb.Close()

	// 同 key 两个在途长查询（pg_sleep(4) 留足断言窗口）
	issued := make(chan struct{}, 2)
	out := make(chan callOutcome, 2)
	go holdCall(sa1, "SELECT pg_sleep(4)", issued, out)
	go holdCall(sa2, "SELECT pg_sleep(4)", issued, out)
	for i := 0; i < 2; i++ {
		<-issued
	}
	time.Sleep(700 * time.Millisecond) // 等两个查询真正在途（闸位已占用）

	// 第三发（同 key）→ 快速失败：rate_limited/key_concurrency_limit
	e := assertFastReject(t, sa3, loadgate.ReasonKeyConcurrency)
	// 经 JSON 解码的数值是 float64（闸内是 int，契约以值比较）
	if e.Details["key"] != keyAID || e.Details["limit"] != float64(2) {
		t.Errorf("details = %v，期望 key=%s limit=2", e.Details, keyAID)
	}

	// 同用户、另一把 key 不受影响（每 key 粒度，§6.3「不影响其他 key」）：
	// dev-alice 的第二把 key 并发查询成功
	keyA2 := createKey(t, st, "dev-alice")
	saOther := connectHTTP(t, ts.URL, keyA2)
	defer saOther.Close()
	resA2 := callSQL(t, saOther, map[string]any{"sql": "SELECT count(*) FROM orders", "dbname": "bss"})
	if len(resA2.Rows) != 1 {
		t.Fatalf("同用户另一 key 查询应成功: %+v", resA2)
	}

	// 其他用户也不受影响（跨用户隔离）：bob 照常查询成功
	resB := callSQL(t, sb, map[string]any{"sql": "SELECT count(*) FROM orders", "dbname": "bss"})
	if len(resB.Rows) != 1 {
		t.Fatalf("bob 查询应成功: %+v", resB)
	}

	// 两个在途查询正常完成（闸位不误伤在途请求）
	drainHolders(t, out, 2)

	// 在途结束 → 配额恢复：同 key 再查成功
	callSQL(t, sa3, map[string]any{"sql": "SELECT count(*) FROM orders", "dbname": "bss"})
}

// 负向例 4：进程级并发 >8 → 结构化拒绝（守护进程语义，跨 key 共享总闸）。
// perKey=2, total=2：两个不同 key 各占 1 → 进程级满，第三 key 被拒
// （其每 key 配额未满，证明是进程级闸触发）。
func TestExecuteSQLE2EProcessGate(t *testing.T) {
	requirePG(t)
	g, st := e2eGatewayWith(t, []db.Entry{{DBName: "bss", Service: "bss", DSN: bssDSN}}, 500, 30*time.Second, 2, 2, "bss.bss.orders")
	keyA := createKey(t, st, "dev-alice")
	keyB := createKey(t, st, "dev-bob")
	keyC := createKey(t, st, "dev-carol")
	for _, u := range []string{"dev-bob", "dev-carol"} {
		if err := grants.AddGrant(context.Background(), st, u, "bss.bss.orders"); err != nil {
			t.Fatalf("AddGrant(%s): %v", u, err)
		}
	}
	if err := g.authz.Load(context.Background()); err != nil {
		t.Fatalf("authz.Load: %v", err)
	}
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()

	sa := connectHTTP(t, ts.URL, keyA)
	defer sa.Close()
	sb := connectHTTP(t, ts.URL, keyB)
	defer sb.Close()
	sc := connectHTTP(t, ts.URL, keyC)
	defer sc.Close()

	issued := make(chan struct{}, 2)
	out := make(chan callOutcome, 2)
	go holdCall(sa, "SELECT pg_sleep(4)", issued, out)
	go holdCall(sb, "SELECT pg_sleep(4)", issued, out)
	for i := 0; i < 2; i++ {
		<-issued
	}
	time.Sleep(700 * time.Millisecond)

	// 第三 key（每 key 配额未满）→ 进程级闸拒绝，快速失败
	e := assertFastReject(t, sc, loadgate.ReasonProcessConcurrency)
	if e.Details["limit"] != float64(2) {
		t.Errorf("details = %v，期望 limit=2", e.Details)
	}

	// 在途查询正常完成（闸位不误伤在途请求）
	drainHolders(t, out, 2)

	// 进程级配额恢复：carol 再查成功
	callSQL(t, sc, map[string]any{"sql": "SELECT count(*) FROM orders", "dbname": "bss"})
}
