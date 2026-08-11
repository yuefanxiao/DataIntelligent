package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 测试 seam（spec §5 测试决策）：官方 go-sdk 客户端打自己的网关——
// 真实 MCP 协议往返（HTTP + stdio 双形态）。

func newTestGateway(t *testing.T) (*Gateway, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	g, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return g, st
}

func createKey(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	key, err := credentials.Create(context.Background(), st.DB(), userID)
	if err != nil {
		t.Fatalf("credentials.Create: %v", err)
	}
	return key
}

// keyIDForUser 查一把指定用户的 key 行 ID（每 key 并发闸的粒度标识，
// 测试断言 details.key 时使用）。
func keyIDForUser(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	keys, err := credentials.List(context.Background(), st.DB())
	if err != nil {
		t.Fatalf("credentials.List: %v", err)
	}
	for _, k := range keys {
		if k.UserID == userID {
			return strconv.FormatInt(k.ID, 10)
		}
	}
	t.Fatalf("找不到 %s 的 key", userID)
	return ""
}

// bearerTransport 给 SDK 客户端的每个请求附加 Authorization header。
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// connectHTTP 用官方 go-sdk 客户端连接网关（bearer 认证）。
// 注意：不注册 t.Cleanup——会话关闭必须在 httptest 服务关闭之前发生
// （Close 会等待包括 SSE 长连接在内的所有在途请求），调用方用
// defer session.Close() 紧跟 defer ts.Close() 保证 LIFO 顺序。
func connectHTTP(t *testing.T, endpoint, key string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "dgw-test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{token: key, base: http.DefaultTransport}},
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return session
}

func decodeErrorResult(t *testing.T, res *mcp.CallToolResult) *gwerr.Error {
	t.Helper()
	if res == nil || !res.IsError {
		t.Fatal("期望 isError=true 的工具结果")
	}
	if len(res.Content) == 0 {
		t.Fatal("错误结果缺 content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content 类型 = %T, want *TextContent", res.Content[0])
	}
	var e gwerr.Error
	if err := json.Unmarshal([]byte(text.Text), &e); err != nil {
		t.Fatalf("错误 content 非 gwerr JSON: %v（原文 %q）", err, text.Text)
	}
	return &e
}

// 六工具面固定（ADR-0003）；tools/list 返回按名称字母序（SDK 排序），
// 断言用集合比较。
var wantTools = []string{
	"search_entities",
	"get_entity",
	"traverse_relations",
	"get_metric_definition",
	"list_enum_values",
	"execute_sql",
}

func assertToolList(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	list, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make(map[string]bool, len(list.Tools))
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("工具 %q 未声明只读注解", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("工具 %q 缺输入 schema", tool.Name)
		}
	}
	if len(names) != len(wantTools) {
		t.Fatalf("工具数 = %d, want %d: %v", len(names), len(wantTools), names)
	}
	for _, want := range wantTools {
		if !names[want] {
			t.Errorf("缺工具 %q（全量 %v）", want, names)
		}
	}
}

// —— HTTP 形态 ——

// 无 token / 错 token → 401 + 结构化认证失败（kind=unauthorized）。
// 已吊销的 key 同样 401（吊销即时生效，AC5）。
func TestHTTPAuthRejectsMissingAndWrongToken(t *testing.T) {
	g, st := newTestGateway(t)
	ctx := context.Background()
	_ = createKey(t, st, "dev-alice")

	// 先吊销一把 key，验证吊销后的凭据立即失效（HTTP 每请求查库校验）。
	revokedKey := createKey(t, st, "dev-bob")
	keys, err := credentials.List(ctx, st.DB())
	if err != nil {
		t.Fatalf("credentials.List: %v", err)
	}
	for _, k := range keys {
		if k.UserID == "dev-bob" {
			if _, err := credentials.Revoke(ctx, st.DB(), k.ID); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
		}
	}

	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()

	for name, req := range map[string]*http.Request{
		"无 token":  mustRequest(t, http.MethodGet, ts.URL, ""),
		"错 token":  mustRequest(t, http.MethodGet, ts.URL, "Bearer dgw_wrong-token"),
		"非 bearer": mustRequest(t, http.MethodGet, ts.URL, "dgw_plain-token"),
		"已吊销 key":  mustRequest(t, http.MethodGet, ts.URL, "Bearer "+revokedKey),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			var e gwerr.Error
			if err := json.Unmarshal(body, &e); err != nil {
				t.Fatalf("401 body 非结构化 JSON: %v（原文 %q）", err, body)
			}
			if e.Kind != gwerr.KindUnauthorized || e.Code != "DGW_UNAUTHORIZED" {
				t.Errorf("401 body = kind %q code %q, want unauthorized/DGW_UNAUTHORIZED", e.Kind, e.Code)
			}
		})
	}
}

func mustRequest(t *testing.T, method, url, authHeader string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}

// 正确 key → 放行：SDK 客户端完整往返（tools/list + 六工具 stub 调用）。
func TestHTTPEndToEnd(t *testing.T) {
	g, st := newTestGateway(t)
	key := createKey(t, st, "dev-alice")
	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()

	session := connectHTTP(t, ts.URL, key)
	defer session.Close() // 必须先于 ts.Close()（SSE 长连接）
	assertToolList(t, session)

	for _, tool := range wantTools {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tool,
			Arguments: sampleArgs(tool),
		})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
		e := decodeErrorResult(t, res)
		if tool == "execute_sql" {
			// execute_sql 已实装（04）：未注入 PG 路由时返回结构化「未配置」。
			if e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "not_configured" {
				t.Errorf("CallTool(execute_sql) 错误 = %s [%s], want invalid_request/not_configured", e.Kind, e.Code)
			}
			continue
		}
		if e.Kind != gwerr.KindNotImplemented || e.Code != "DGW_NOT_IMPLEMENTED" {
			t.Errorf("CallTool(%s) 错误 = %s [%s], want not_implemented/DGW_NOT_IMPLEMENTED", tool, e.Kind, e.Code)
		}
		if e.Details["tool"] != tool {
			t.Errorf("CallTool(%s) details.tool = %v, want %s", tool, e.Details["tool"], tool)
		}
	}
}

// sampleArgs 给各 stub 工具一个形状合法的调用参数（schema 校验应放行，
// 然后返回 not_implemented）。
func sampleArgs(tool string) map[string]any {
	switch tool {
	case "search_entities":
		return map[string]any{"query": "支付失败"}
	case "get_entity":
		return map[string]any{"fqn": "payment.service_db.t_payment"}
	case "traverse_relations":
		return map[string]any{"fqn": "payment", "relation": "connects_to"}
	case "get_metric_definition":
		return map[string]any{"fqn": "payment.支付失败率"}
	case "list_enum_values":
		return map[string]any{"fqn": "payment.service_db.t_payment.status"}
	default: // execute_sql
		return map[string]any{"sql": "SELECT 1"}
	}
}

// HTTP 形态下身份（auth.TokenInfo.UserID + Extra.key_id）应流达工具调用层。
func TestHTTPIdentityPropagates(t *testing.T) {
	g, st := newTestGateway(t)
	key := createKey(t, st, "dev-alice")
	wantKeyID := keyIDForUser(t, st, "dev-alice")

	var mu sync.Mutex
	var seenUser, seenKey string
	g.Server().AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if u, ok := UserFromContext(ctx); ok {
					mu.Lock()
					seenUser = u
					mu.Unlock()
				}
				if k, ok := KeyFromContext(ctx); ok {
					mu.Lock()
					seenKey = k
					mu.Unlock()
				}
			}
			return next(ctx, method, req)
		}
	})

	ts := httptest.NewServer(g.HTTPHandler())
	defer ts.Close()
	session := connectHTTP(t, ts.URL, key)
	defer session.Close() // 必须先于 ts.Close()（SSE 长连接）
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_sql", Arguments: map[string]any{"sql": "SELECT 1"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seenUser != "dev-alice" {
		t.Errorf("工具调用层用户身份 = %q, want %q", seenUser, "dev-alice")
	}
	if seenKey != wantKeyID {
		t.Errorf("工具调用层 key 身份 = %q, want %q（每 key 并发闸粒度）", seenKey, wantKeyID)
	}
}

// —— stdio 调试形态 ——

// env 传 key：无效 key 拒绝启动（fail fast）。
func TestStdioRejectsInvalidKey(t *testing.T) {
	g, st := newTestGateway(t)
	_ = createKey(t, st, "dev-alice")

	err := g.ServeStdio(context.Background(), "dgw_not-a-valid-key")
	if err == nil {
		t.Fatal("无效 key 应拒绝启动")
	}
}

// 有效 key：经 env 校验后整条连接以该身份服务（工具面 + stub 错误同上）。
// 用 InMemoryTransport 走 serveKeyed 同一条路径——真实 StdioTransport 绑定
// os.Stdin/Stdout，进程内不可注入；校验与身份逻辑在本路径全覆盖。
func TestStdioFormEndToEnd(t *testing.T) {
	g, st := newTestGateway(t)
	key := createKey(t, st, "dev-alice")
	wantKeyID := keyIDForUser(t, st, "dev-alice")

	var mu sync.Mutex
	var seenUser, seenKey string
	g.Server().AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if u, ok := UserFromContext(ctx); ok {
					mu.Lock()
					seenUser = u
					mu.Unlock()
				}
				if k, ok := KeyFromContext(ctx); ok {
					mu.Lock()
					seenKey = k
					mu.Unlock()
				}
			}
			return next(ctx, method, req)
		}
	})

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- g.serveKeyed(ctx, key, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "dgw-test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	assertToolList(t, session)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "execute_sql", Arguments: map[string]any{"sql": "SELECT 1"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if e := decodeErrorResult(t, res); e.Kind != gwerr.KindInvalidRequest || e.Details["reason"] != "not_configured" {
		t.Errorf("错误 = %s [%s] reason=%v, want invalid_request/not_configured", e.Kind, e.Code, e.Details["reason"])
	}

	mu.Lock()
	if seenUser != "dev-alice" {
		t.Errorf("stdio 用户身份 = %q, want %q", seenUser, "dev-alice")
	}
	if seenKey != wantKeyID {
		t.Errorf("stdio key 身份 = %q, want %q（每 key 并发闸粒度）", seenKey, wantKeyID)
	}
	mu.Unlock()

	session.Close()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("serveKeyed 退出错误: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("服务端未随会话关闭退出")
	}
}
