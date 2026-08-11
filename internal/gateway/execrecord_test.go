package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/loadgate"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 执行记录（06 票）的网关侧测试：六工具全记 + 认证失败 + 分阶段打点 +
// JSONL 可重放（spec §4.6 / §6.4(b) 判定三件套前置）。execrecord 包的
// 轮转/保留期/聚合在包内单测覆盖；本文件验证网关真实打点。

// logRecords 读执行记录目录里今日原始 JSONL 的全部记录（按写入顺序）。
func logRecords(t *testing.T, logDir string) []map[string]any {
	t.Helper()
	day := time.Now().Format("2006-01-02")
	f, err := os.Open(filepath.Join(logDir, "raw-"+day+".jsonl"))
	if err != nil {
		t.Fatalf("打开执行记录: %v", err)
	}
	defer f.Close()
	var recs []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("执行记录行解析失败: %v\n%s", err, line)
			}
			recs = append(recs, m)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读执行记录: %v", err)
	}
	return recs
}

// ── 六工具全记（无 PG，HTTP 形态）───────────────────────────────────────

// spec §4.6「六工具全记」：六个工具每次调用都落一行（kind=tool_call），
// 身份（user/key）从认证上下文如实注入；execute_sql 未配置 → 结构化拒绝
// 记录（被拒原因 not_configured），其余五工具 stub → not_implemented。
func TestExecLogSixToolsRecorded(t *testing.T) {
	logDir := t.TempDir()
	g, st := newLoggedGateway(t, logDir)
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 六个工具各调一次（execute_sql 未配置 → 拒绝；其余 stub → 未实现）
	argsFor := map[string]map[string]any{
		"search_entities":       {"query": "支付失败"},
		"get_entity":            {"fqn": "bss.bss.orders"},
		"traverse_relations":    {"fqn": "bss.bss.orders", "relation": "contains"},
		"get_metric_definition": {"fqn": "bss.orders_amount"},
		"list_enum_values":      {"fqn": "bss.bss.orders.status"},
		"execute_sql":           {"sql": "SELECT 1", "dbname": "bss"},
	}
	for _, tool := range wantTools {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tool, Arguments: argsFor[tool],
		})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("stub/未配置工具 %s 应返回错误结果", tool)
		}
	}

	recs := logRecords(t, logDir)
	if len(recs) != len(wantTools) {
		t.Fatalf("记录数 = %d，期望 %d（六工具全记）", len(recs), len(wantTools))
	}
	byTool := map[string]map[string]any{}
	for _, r := range recs {
		if r["kind"] != "tool_call" {
			t.Errorf("kind = %v，期望 tool_call", r["kind"])
		}
		if r["user"] != "dev-alice" {
			t.Errorf("user = %v，期望 dev-alice", r["user"])
		}
		if r["key"] == "" {
			t.Errorf("key 缺失: %v", r)
		}
		if r["status"] != "rejected" {
			t.Errorf("%v status = %v，期望 rejected", r["tool"], r["status"])
		}
		byTool[r["tool"].(string)] = r
	}
	// execute_sql 未配置 → invalid_request/not_configured；其余 → not_implemented
	rejExec := byTool["execute_sql"]["reject"].(map[string]any)
	if rejExec["kind"] != "invalid_request" {
		t.Errorf("execute_sql 被拒原因 = %v", rejExec)
	}
	if d := rejExec["details"].(map[string]any); d["reason"] != "not_configured" {
		t.Errorf("execute_sql reason = %v，期望 not_configured", d["reason"])
	}
	for _, tool := range []string{"search_entities", "get_entity", "traverse_relations", "get_metric_definition", "list_enum_values"} {
		if rej := byTool[tool]["reject"].(map[string]any); rej["kind"] != "not_implemented" {
			t.Errorf("%s 被拒原因 = %v，期望 not_implemented", tool, rej)
		}
	}
}

// ── 认证失败记录（HTTP 401）────────────────────────────────────────────

// AC：认证失败落记录（kind=auth_failure），身份未知（不伪造 user/key），
// 被拒原因如实（unauthorized）。
func TestExecLogAuthFailure(t *testing.T) {
	logDir := t.TempDir()
	g, _ := newLoggedGateway(t, logDir)
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL) // 无 token
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", resp.StatusCode)
	}

	recs := logRecords(t, logDir)
	if len(recs) != 1 {
		t.Fatalf("记录数 = %d，期望 1（认证失败一行）", len(recs))
	}
	r := recs[0]
	if r["kind"] != "auth_failure" || r["status"] != "rejected" {
		t.Fatalf("认证失败记录 = %v", r)
	}
	if _, ok := r["user"]; ok {
		t.Errorf("认证失败身份未知，不应有 user: %v", r)
	}
	if rej := r["reject"].(map[string]any); rej["kind"] != "unauthorized" {
		t.Errorf("被拒原因 = %v，期望 unauthorized", rej["kind"])
	}
}

// ── execute_sql 全调用链（docker PG）────────────────────────────────────

// AC1：execute_sql 全调用链落 JSONL —— SQL 原文（不脱敏）、分阶段耗时
// （认证→权限→解析→执行→返回）、状态、行数、truncated、plan_id、被拒原因。
func TestExecLogExecuteSQLChain(t *testing.T) {
	requirePG(t)
	logDir := t.TempDir()
	g, st := e2eGatewayLogged(t, logDir, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
	}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	sql := "SELECT id, status FROM orders ORDER BY id LIMIT 2"
	res := callSQL(t, session, map[string]any{"sql": sql, "dbname": "bss", "plan_id": "plan-42"})
	if res.Meta.RowCount != 2 {
		t.Fatalf("行数 = %d", res.Meta.RowCount)
	}

	recs := logRecords(t, logDir)
	if len(recs) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(recs))
	}
	r := recs[0]
	if r["kind"] != "tool_call" || r["tool"] != "execute_sql" || r["user"] != "dev-alice" {
		t.Fatalf("基础字段 = %v", r)
	}
	// SQL 原文入库（不脱敏，宿主机权限即访问边界）
	params := r["params"].(map[string]any)
	if params["sql"] != sql {
		t.Errorf("params.sql = %v，期望 SQL 原文", params["sql"])
	}
	if params["dbname"] != "bss" {
		t.Errorf("params.dbname = %v", params["dbname"])
	}
	// 状态/行数/truncated/plan_id
	if r["status"] != "success" || r["rows"] != float64(2) || r["truncated"] != false || r["plan_id"] != "plan-42" {
		t.Errorf("结果字段 = %v", r)
	}
	// 分阶段耗时：认证（HTTP verifyToken 实测）→权限→解析→执行→返回
	stages := r["stages_ms"].(map[string]any)
	for _, s := range []string{"auth", "perm", "parse", "exec", "return"} {
		if v, ok := stages[s]; !ok || v.(float64) < 0 {
			t.Errorf("阶段 %s 缺失或非法: %v", s, stages)
		}
	}
}

// 截断查询 → truncated=true 如实落记录。
func TestExecLogTruncatedFlag(t *testing.T) {
	requirePG(t)
	logDir := t.TempDir()
	g, st := e2eGatewayLogged(t, logDir, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
	}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	callSQL(t, session, map[string]any{"sql": "SELECT id FROM orders ORDER BY id", "dbname": "bss"})
	r := logRecords(t, logDir)[0]
	if r["truncated"] != true || r["rows"] != float64(500) {
		t.Errorf("截断记录 = rows %v truncated %v，期望 500/true", r["rows"], r["truncated"])
	}
}

// AC2：权限拒绝 / 解析失败均落记录（被拒原因如实）。
func TestExecLogRejections(t *testing.T) {
	requirePG(t)
	logDir := t.TempDir()
	g, st := e2eGatewayLogged(t, logDir, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
	}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 权限拒绝（白名单外真实存在的表）
	e := callSQLErr(t, session, map[string]any{"sql": "SELECT * FROM secret", "dbname": "bss"})
	if e.Kind != gwerr.KindPermission {
		t.Fatalf("未授权表错误 = %s", e.Kind)
	}
	// 解析失败（语法错误）
	callSQLErr(t, session, map[string]any{"sql": "SELEC 1", "dbname": "bss"})

	recs := logRecords(t, logDir)
	if len(recs) != 2 {
		t.Fatalf("记录数 = %d，期望 2", len(recs))
	}
	perm := recs[0]
	if perm["status"] != "rejected" {
		t.Errorf("权限拒绝状态 = %v，期望 rejected", perm["status"])
	}
	if rej := perm["reject"].(map[string]any); rej["kind"] != "permission_denied" {
		t.Errorf("权限拒绝原因 = %v", rej)
	}
	if d := perm["reject"].(map[string]any)["details"].(map[string]any); d["reason"] != "not_granted" {
		t.Errorf("权限拒绝 reason = %v，期望 not_granted", d["reason"])
	}
	parse := recs[1]
	if parse["status"] != "parse_error" {
		t.Errorf("语法错误状态 = %v，期望 parse_error", parse["status"])
	}
	if d := parse["reject"].(map[string]any)["details"].(map[string]any); d["reason"] != "syntax_error" {
		t.Errorf("语法错误 reason = %v，期望 syntax_error", d["reason"])
	}
}

// AC2：限流拒绝落记录（被拒原因 key_concurrency_limit 如实；快速失败）。
func TestExecLogRateLimited(t *testing.T) {
	requirePG(t)
	logDir := t.TempDir()
	g, st := e2eGatewayOpts(t, logDir, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
	}, 500, 30*time.Second, 1, 2, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 占满每 key 并发位（1），第二发快速拒绝
	issued := make(chan struct{}, 1)
	out := make(chan callOutcome, 1)
	go holdCall(session, "SELECT pg_sleep(2)", issued, out)
	<-issued
	time.Sleep(700 * time.Millisecond) // 等查询在途（闸位已占用）

	assertFastReject(t, session, loadgate.ReasonKeyConcurrency)
	drainHolders(t, out, 1)

	recs := logRecords(t, logDir)
	if len(recs) != 2 {
		t.Fatalf("记录数 = %d，期望 2（限流拒绝 + 在途成功）", len(recs))
	}
	// 顺序：限流拒绝立即落盘（t≈0），在途 pg_sleep 完成才落盘（t≈2s）——
	// 第一行是被拒记录，第二行是在途成功。
	r := recs[0]
	if r["status"] != "rejected" {
		t.Errorf("限流状态 = %v，期望 rejected", r["status"])
	}
	rej, ok := r["reject"].(map[string]any)
	if !ok {
		t.Fatalf("限流记录缺被拒原因: %v", r)
	}
	if d := rej["details"].(map[string]any); d["reason"] != "key_concurrency_limit" {
		t.Errorf("限流 reason = %v，期望 key_concurrency_limit（被拒原因如实）", d["reason"])
	}
	if recs[1]["status"] != "success" {
		t.Errorf("在途成功记录 = %v，期望 success", recs[1]["status"])
	}
}

// AC5：从 JSONL 可完整重放一次调用链（§6.4(b) 判定三件套前置）——
// 工具/参数原文/状态/行数/truncated/plan_id/分阶段耗时/被拒原因按序可重建。
func TestExecLogReplayChain(t *testing.T) {
	requirePG(t)
	logDir := t.TempDir()
	g, st := e2eGatewayLogged(t, logDir, []db.Entry{
		{DBName: "bss", Service: "bss", DSN: bssDSN},
	}, 500, 30*time.Second, "bss.bss.orders")
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 调用链：语义定位（stub，拒绝）→ execute_sql 成功 → execute_sql 拒绝
	session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_entities", Arguments: map[string]any{"query": "支付失败"}})
	callSQL(t, session, map[string]any{
		"sql": "SELECT count(*) FROM orders WHERE status = 'paid'", "dbname": "bss", "plan_id": "p9"})
	callSQLErr(t, session, map[string]any{"sql": "SELECT * FROM secret", "dbname": "bss"})

	recs := logRecords(t, logDir)
	if len(recs) != 3 {
		t.Fatalf("记录数 = %d，期望 3", len(recs))
	}
	// [0] search_entities stub 拒绝
	if recs[0]["tool"] != "search_entities" || recs[0]["status"] != "rejected" {
		t.Errorf("重放[0] = %v", recs[0])
	}
	// [1] execute_sql 成功：SQL 原文/状态/行数/plan_id/阶段/用户
	r1 := recs[1]
	if r1["tool"] != "execute_sql" || r1["status"] != "success" ||
		r1["plan_id"] != "p9" || r1["user"] != "dev-alice" {
		t.Errorf("重放[1] = %v", r1)
	}
	if p := r1["params"].(map[string]any); p["sql"] != "SELECT count(*) FROM orders WHERE status = 'paid'" {
		t.Errorf("重放[1] SQL 原文 = %v", p["sql"])
	}
	if _, ok := r1["rows"]; !ok {
		t.Errorf("重放[1] 缺行数")
	}
	if st, ok := r1["stages_ms"].(map[string]any); !ok || len(st) == 0 {
		t.Errorf("重放[1] 缺分阶段耗时")
	}
	// [2] execute_sql 拒绝：被拒原因如实
	r2 := recs[2]
	if r2["status"] != "rejected" {
		t.Errorf("重放[2] 状态 = %v", r2["status"])
	}
	if d := r2["reject"].(map[string]any)["details"].(map[string]any); d["reason"] != "not_granted" {
		t.Errorf("重放[2] 被拒原因 = %v", d["reason"])
	}
}

// ── stdio 形态（serveKeyed 路径）───────────────────────────────────────

// stdio 形态同样六工具全记：身份为连接级预置（user/key），无 per-call
// 认证阶段（阶段契约如实缺失）。
func TestExecLogStdioForm(t *testing.T) {
	logDir := t.TempDir()
	g, st := newLoggedGateway(t, logDir)
	key := createKey(t, st, "dev-alice")

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- g.serveKeyed(ctx, key, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "dgw-test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql", Arguments: map[string]any{"sql": "SELECT 1"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if e := decodeErrorResult(t, res); e.Kind != gwerr.KindInvalidRequest {
		t.Fatalf("错误 = %s，期望 invalid_request/not_configured", e.Kind)
	}
	session.Close()
	if err := <-serveErr; err != nil {
		t.Fatalf("serveKeyed 退出错误: %v", err)
	}

	recs := logRecords(t, logDir)
	if len(recs) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(recs))
	}
	r := recs[0]
	if r["kind"] != "tool_call" || r["tool"] != "execute_sql" || r["user"] != "dev-alice" || r["key"] == "" {
		t.Fatalf("stdio 记录 = %v", r)
	}
	// stdio 无 per-call 认证：stages_ms 不含 auth（如实缺失，不伪造 0）
	if stages, ok := r["stages_ms"].(map[string]any); ok {
		if _, hasAuth := stages["auth"]; hasAuth {
			t.Errorf("stdio 形态不应有 auth 阶段: %v", stages)
		}
	}
}

// ── 助手 ─────────────────────────────────────────────────────────────────

// newLoggedGateway 建一个注入执行记录的网关（无 PG 路由；日志写入失败
// 不影响调用——本测试只断言成功路径）。
func newLoggedGateway(t *testing.T, logDir string) (*Gateway, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	lg, err := execrecord.New(execrecord.Config{Dir: logDir, RawRetentionDays: 7, SummaryRetentionDays: 30})
	if err != nil {
		t.Fatalf("execrecord.New: %v", err)
	}
	t.Cleanup(func() { lg.Close() })
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), WithExecLog(lg))
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return g, st
}
