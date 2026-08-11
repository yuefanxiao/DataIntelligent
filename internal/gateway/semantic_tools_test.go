package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 五个语义工具的 MCP 级测试（08 票，spec §5「官方 go-sdk 客户端打自己的
// 网关」）：HTTP 形态真实协议往返——成功路径结构化 JSON、错误路径结构化
// gwerr、向量通道脚本化 fake、执行记录六工具全记。语义元数据面认证即读
// （spec §4.4：不需要表授权）。

// 语义 fixture（支付域：枚举/is_time/references/指标/概念）。
const semServices = `version: 1
service: payment-service
description: 支付服务
databases:
  - name: payment_db
    description: 支付库
    tables:
      - name: payments
        description: 支付单
        columns:
          - name: id
            type: uuid
            description: 支付单主键
          - name: status
            type: varchar
            description: 支付状态
            enum_values:
              - value: pending
                label: 待支付
              - value: succeeded
                label: 支付成功
              - value: failed
                label: 支付失败
          - name: created_at
            type: timestamptz
            is_time: true
        references:
          - to: order-service.order_db.orders
            on: "payments.order_id = orders.id"
`

const semOrders = `version: 1
service: order-service
description: 订单服务
databases:
  - name: order_db
    description: 订单库
    tables:
      - name: orders
        description: 订单
        columns:
          - name: id
            type: uuid
          - name: created_at
            type: timestamptz
            is_time: true
`

const semMetrics = `version: 1
metrics:
  - name: payment_failure_rate
    description: 支付失败率（支付状态为 failed 的单数占比）
    expression: "COUNT(*) FILTER (WHERE status = 'failed')::numeric / NULLIF(COUNT(*), 0)"
    aggregation: ratio
    filter: ""
    tables:
      - payment-service.payment_db.payments
`

const semConcepts = `version: 1
concepts:
  - name: payment_failure
    description: 支付失败业务概念
    describes:
      - payment-service.payment_db.payments.status
      - payment_failure_rate
`

// newSemanticGateway 建一个带已同步语义数据的网关（HTTP 形态测试夹具）。
func newSemanticGateway(t *testing.T, emb semantic.Embedder) (*Gateway, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	for name, content := range map[string]string{
		"services/payment-service.yaml": semServices,
		"services/order-service.yaml":   semOrders,
		"metrics.yaml":                  semMetrics,
		"concepts.yaml":                 semConcepts,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := semantic.Sync(context.Background(), st, dir); err != nil {
		t.Fatalf("semantic.Sync: %v", err)
	}

	opts := []Option{}
	if emb != nil {
		opts = append(opts, WithSearchEmbed(emb))
	}
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return g, st
}

// callSemTool 经真实 MCP 会话调用语义工具，返回结构化结果 JSON（文本）。
func callSemTool(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: tool, Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if res == nil || res.IsError {
		t.Fatalf("期望成功结果，得到 error result: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content 类型 = %T，期望 TextContent", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("结果 JSON 解析失败: %v\n%s", err, tc.Text)
	}
	return out
}

// callSemErr 经真实 MCP 会话调用语义工具，返回结构化错误。
func callSemErr(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *gwerr.Error {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: tool, Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	return decodeErrorResult(t, res)
}

// ── 成功路径：五工具全流程 ──────────────────────────────────────────────

// 主用例前置形态：搜「支付失败」双入口定位 → 取口径 dry-run → 枚举 →
// 关系遍历——全部结构化 JSON，经真实 MCP HTTP 往返。
func TestSemanticToolsE2E(t *testing.T) {
	g, st := newSemanticGateway(t, nil)
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// search_entities：双入口命中概念 + 指标（关键词通道，无向量也工作）。
	out := callSemTool(t, session, "search_entities", map[string]any{"query": "支付失败"})
	hits := out["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits = %d，期望 2（概念 + 指标）: %+v", len(hits), out)
	}
	if out["total"] != float64(2) {
		t.Errorf("total = %v，期望 2", out["total"])
	}
	got := map[string]bool{}
	for _, h := range hits {
		m := h.(map[string]any)
		got[m["fqn"].(string)] = true
		if m["kind"] == nil {
			t.Errorf("命中缺 kind: %+v", m)
		}
	}
	if !got["payment_failure"] || !got["payment_failure_rate"] {
		t.Errorf("命中 = %v，期望含 payment_failure 与 payment_failure_rate", got)
	}

	// search_entities：type 限定单入口。
	out = callSemTool(t, session, "search_entities", map[string]any{"query": "支付失败", "type": "metric"})
	if len(out["hits"].([]any)) != 1 {
		t.Errorf("metric 单入口 hits = %+v", out["hits"])
	}

	// get_entity：列实体带枚举挂列。
	out = callSemTool(t, session, "get_entity", map[string]any{"fqn": "payment-service.payment_db.payments.status"})
	if out["kind"] != "column" || out["data_type"] != "varchar" {
		t.Errorf("get_entity(列) = %+v", out)
	}
	if enums := out["enums"].([]any); len(enums) != 3 {
		t.Errorf("枚举挂列 = %v", enums)
	}
	// is_time 标注。
	out = callSemTool(t, session, "get_entity", map[string]any{"fqn": "payment-service.payment_db.payments.created_at"})
	if out["is_time"] != true {
		t.Errorf("created_at is_time = %v，期望 true", out["is_time"])
	}

	// traverse_relations：references 出边到 orders。
	out = callSemTool(t, session, "traverse_relations", map[string]any{
		"fqn": "payment-service.payment_db.payments", "relation": "references", "direction": "out",
	})
	nodes := out["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("traverse nodes = %d，期望 2（payments + orders）: %+v", len(nodes), out)
	}

	// get_metric_definition：口径 machine-readable + 带时间 dry-run 展开。
	out = callSemTool(t, session, "get_metric_definition", map[string]any{
		"fqn": "payment_failure_rate",
		"time_range": map[string]any{
			"start": "2026-08-11T00:00:00Z", "end": "2026-08-12T00:00:00Z",
		},
	})
	if out["expression"] == "" || out["aggregation"] != "ratio" {
		t.Errorf("口径 = %+v", out)
	}
	sqlText := out["dry_run_sql"].(string)
	if sqlText == "" || !strings.Contains(sqlText, "payments.created_at >= '2026-08-11T00:00:00Z'") ||
		!strings.Contains(sqlText, "payments.created_at < '2026-08-12T00:00:00Z'") {
		t.Errorf("dry_run_sql = %q（应含半开区间时间谓词）", sqlText)
	}
	if out["time_applied"] != true {
		t.Errorf("time_applied = %v", out["time_applied"])
	}

	// list_enum_values：状态枚举 3 条（CHECK 约束语义）。
	out = callSemTool(t, session, "list_enum_values", map[string]any{"fqn": "payment-service.payment_db.payments.status"})
	values := out["values"].([]any)
	if len(values) != 3 || out["total"] != float64(3) || out["truncated"] != nil {
		t.Errorf("枚举 = %+v total=%v", values, out["total"])
	}
}

// ── 错误路径：结构化 gwerr ─────────────────────────────────────────────

func TestSemanticToolsErrors(t *testing.T) {
	g, st := newSemanticGateway(t, nil)
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 参数错误：空查询 / 未知类型 / 未知关系 / 非法时间窗口。
	e := callSemErr(t, session, "search_entities", map[string]any{"query": "  "})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "empty" {
		t.Errorf("空查询 = %+v", e)
	}
	e = callSemErr(t, session, "search_entities", map[string]any{"query": "支付", "type": "table"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "bad_type" {
		t.Errorf("未知类型 = %+v", e)
	}
	e = callSemErr(t, session, "traverse_relations", map[string]any{"fqn": "x", "relation": "owns"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "bad_relation" {
		t.Errorf("未知关系 = %+v", e)
	}
	e = callSemErr(t, session, "get_metric_definition", map[string]any{
		"fqn":        "payment_failure_rate",
		"time_range": map[string]any{"start": "2026-08-12T00:00:00Z", "end": "2026-08-11T00:00:00Z"},
	})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "bad_time" {
		t.Errorf("非法时间窗口 = %+v", e)
	}

	// 实体不存在 → not_found；类型不符 → wrong_kind。
	e = callSemErr(t, session, "get_entity", map[string]any{"fqn": "no-such-service.db.t"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "not_found" {
		t.Errorf("not_found = %+v", e)
	}
	e = callSemErr(t, session, "get_metric_definition", map[string]any{"fqn": "payment-service.payment_db.payments"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "wrong_kind" {
		t.Errorf("wrong_kind(指标) = %+v", e)
	}
	e = callSemErr(t, session, "list_enum_values", map[string]any{"fqn": "payment-service.payment_db.payments"})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "wrong_kind" {
		t.Errorf("wrong_kind(列) = %+v", e)
	}

	// 长度上限（对抗评审 P2）：超长查询/FQN 边界处结构化拒绝（不触发
	// FTS5 分词 CPU 放大与执行记录落盘放大）。
	e = callSemErr(t, session, "search_entities", map[string]any{"query": strings.Repeat("支", 300)})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "too_long" {
		t.Errorf("超长查询 = %+v", e)
	}
	e = callSemErr(t, session, "get_entity", map[string]any{"fqn": strings.Repeat("a", 600)})
	if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "too_long" {
		t.Errorf("超长 FQN = %+v", e)
	}
}

// ── 向量通道（脚本化 fake，MCP 全链路）─────────────────────────────────

// search_entities 向量兜底：查询经 embedder 嵌入 → vec0 KNN 候选进入 RRF；
// 关键词命中仍排向量命中之前。
func TestSemanticToolsSearchVector(t *testing.T) {
	ctx := context.Background()
	_, st := newSemanticGateway(t, nil)
	// 预置实体向量（写索引面），再以带 embedder 的网关服务。
	if err := semantic.SaveEmbeddings(ctx, st, "test-model", []string{"payment_failure"},
		[][]float32{{1, 0, 0, 0}}); err != nil {
		t.Fatalf("SaveEmbeddings: %v", err)
	}
	g2, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithSearchEmbed(&scriptedEmb{vecs: map[string][]float32{"矢量检索词": {1, 0, 0, 0}}}))
	if err != nil {
		t.Fatalf("gateway.New(向量): %v", err)
	}
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g2.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	// 关键词零命中 + 向量命中：纯向量兜底路径。
	out := callSemTool(t, session, "search_entities", map[string]any{"query": "矢量检索词"})
	hits := out["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["fqn"] != "payment_failure" {
		t.Errorf("向量兜底命中 = %+v，期望 payment_failure", out["hits"])
	}
}

// scriptedEmb 是网关测试的脚本化 embedder（确定性向量查找）。
type scriptedEmb struct {
	vecs map[string][]float32
}

func (s *scriptedEmb) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := s.vecs[t]
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		out[i] = v
	}
	return out, nil
}

// ── 执行记录：五工具调用均落记录（复用 06 写入器）──────────────────────

// AC「五工具调用均落执行记录」：同步数据上的成功调用五条全记（六工具
// 全记的语义工具侧；execute_sql 侧由 execrecord_test 覆盖）。
func TestSemanticToolsLogged(t *testing.T) {
	logDir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range map[string]string{
		"services/payment-service.yaml": semServices,
		"services/order-service.yaml":   semOrders,
		"metrics.yaml":                  semMetrics,
		"concepts.yaml":                 semConcepts,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := semantic.Sync(context.Background(), st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	lg, err := execrecord.New(execrecord.Config{Dir: logDir, RawRetentionDays: 7, SummaryRetentionDays: 30})
	if err != nil {
		t.Fatalf("execrecord.New: %v", err)
	}
	t.Cleanup(func() { lg.Close() })
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), WithExecLog(lg))
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close()

	argsFor := map[string]map[string]any{
		"search_entities":       {"query": "支付失败"},
		"get_entity":            {"fqn": "payment-service.payment_db.payments.status"},
		"traverse_relations":    {"fqn": "payment-service.payment_db.payments", "relation": "references"},
		"get_metric_definition": {"fqn": "payment_failure_rate"},
		"list_enum_values":      {"fqn": "payment-service.payment_db.payments.status"},
	}
	for _, tool := range []string{"search_entities", "get_entity", "traverse_relations", "get_metric_definition", "list_enum_values"} {
		if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tool, Arguments: argsFor[tool],
		}); err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
	}

	recs := logRecords(t, logDir)
	if len(recs) != 5 {
		t.Fatalf("记录数 = %d，期望 5（五工具全记）", len(recs))
	}
	byTool := map[string]map[string]any{}
	for _, r := range recs {
		if r["kind"] != "tool_call" {
			t.Errorf("kind = %v，期望 tool_call", r["kind"])
		}
		if r["status"] != "success" {
			t.Errorf("%v status = %v，期望 success", r["tool"], r["status"])
		}
		if r["user"] != "dev-alice" {
			t.Errorf("user = %v，期望 dev-alice", r["user"])
		}
		byTool[r["tool"].(string)] = r
	}
	for _, tool := range []string{"search_entities", "get_entity", "traverse_relations", "get_metric_definition", "list_enum_values"} {
		if byTool[tool] == nil {
			t.Errorf("缺 %s 的执行记录", tool)
		}
	}
	// 参数原文入库（搜索关键词喂聚合摘要，06 票契约）。
	if p := byTool["search_entities"]["params"].(map[string]any); p["query"] != "支付失败" {
		t.Errorf("search_entities params = %v，期望 query 原文", p)
	}
}
