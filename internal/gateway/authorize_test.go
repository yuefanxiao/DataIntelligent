package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 业务面授权入口（AuthorizeBusinessTable）：默认拒绝——未授权表、未授权
// 用户、无身份一律 permission_denied；白名单表放行。
func TestAuthorizeBusinessTableDefaultDeny(t *testing.T) {
	g, st := newTestGateway(t)
	ctx := context.Background()

	// 无身份调用 → 拒绝。
	if e := g.AuthorizeBusinessTable(ctx, "bss.payment_db.t_payment"); e == nil {
		t.Fatal("无身份调用应拒绝")
	} else if e.Kind != gwerr.KindPermission {
		t.Errorf("kind = %q, want %q", e.Kind, gwerr.KindPermission)
	}

	aliceCtx := withUserID(ctx, "dev-alice")
	// 无任何授权 → 拒绝（默认拒绝的核心路径）。
	if e := g.AuthorizeBusinessTable(aliceCtx, "bss.payment_db.t_payment"); e == nil {
		t.Fatal("未授权表应拒绝")
	}

	// 授权后 → 放行（白名单命中）。
	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := g.authz.Load(ctx); err != nil {
		t.Fatalf("authz.Load: %v", err)
	}
	if e := g.AuthorizeBusinessTable(aliceCtx, "bss.payment_db.t_payment"); e != nil {
		t.Errorf("白名单表应放行, got %v", e)
	}

	// 未授权表仍拒绝（白名单外不误放）。
	if e := g.AuthorizeBusinessTable(aliceCtx, "bss.payment_db.t_order"); e == nil {
		t.Error("白名单外表应拒绝")
	}
}

// 双表面分界的行为证据：语义元数据面（认证即读）不因「无表授权」受影响——
// 语义工具在零授权环境下调用返回 stub 的 not_implemented，而不是
// permission_denied（表级授权只在 execute_sql 路径上）。
func TestSemanticSurfaceSkipsTableAuthz(t *testing.T) {
	g, st := newTestGateway(t)
	key := createKey(t, st, "dev-alice")

	semanticTools := []string{
		"search_entities", "get_entity", "traverse_relations",
		"get_metric_definition", "list_enum_values",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverT, clientT := mcp.NewInMemoryTransports()
	serveErr := make(chan error, 1)
	go func() { serveErr <- g.serveKeyed(ctx, key, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "dgw-test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	for _, tool := range semanticTools {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      tool,
			Arguments: sampleArgs(tool),
		})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
		e := decodeErrorResult(t, res)
		if e.Kind != gwerr.KindNotImplemented {
			t.Errorf("语义工具 %s 零授权下错误 = %s, want not_implemented（不得出现 permission_denied）", tool, e.Kind)
		}
	}
}

// 热重载端到端（网关进程内）：CLI 侧写入（模拟）→ revision 变化 →
// ReloadLoop 感知 → 业务面授权立即生效。
func TestAuthorizeHotReloadEndToEnd(t *testing.T) {
	g, st := newTestGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.StartAuthzReloadEvery(ctx, 20*time.Millisecond)

	aliceCtx := withUserID(ctx, "dev-alice")
	if e := g.AuthorizeBusinessTable(aliceCtx, "bss.payment_db.t_payment"); e == nil {
		t.Fatal("初始应拒绝（无授权）")
	}

	// CLI 侧写入（grants.AddGrant 即 CLI 的底层路径）。
	if err := grants.AddGrant(context.Background(), st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	// 轮询周期内授权应自动生效（兜底 5s 上限）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e := g.AuthorizeBusinessTable(aliceCtx, "bss.payment_db.t_payment"); e == nil {
			return // 热重载已生效
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("5s 内授权未生效（热重载链路断裂）")
}
