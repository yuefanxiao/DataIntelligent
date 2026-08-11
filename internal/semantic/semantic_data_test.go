package semantic

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 交付的语义仓库内容（samples/semantic = neo-cloud 全量 13 服务）回归测试
// （issue #30 验收：结构 YAML 全量落地、语义回写、主用例指标 dry-run、
// 全服务数据可经 08 工具抽查）。本测试是「仓库内容保持有效」的闸门：
// 结构采集/语义回写/指标口径任何一处回归到不可编译或不可检索都会失败。

// shippedDir 是交付的作者入口目录（相对 internal/semantic 包）。
func shippedDir() string { return filepath.Join("..", "..", "samples", "semantic") }

// shippedOnce 让全部 TestShipped* 共享一次全量 Sync（13 服务约 4600 行
// YAML 只编译一遍；每测试各自开全新 store 再 Load+Compile 是重复工作）。
var shippedOnce sync.Once
var shippedSt *store.Store
var shippedErr error

// syncShipped 把交付作者入口同步进运行时存储（编译校验原子门禁；
// 同步结果缓存，测试间共享——不注册 t.Cleanup，进程退出即清理）。
func syncShipped(t *testing.T) *store.Store {
	t.Helper()
	shippedOnce.Do(func() {
		st, err := store.Open(filepath.Join(t.TempDir(), "shipped.db"))
		if err != nil {
			shippedErr = fmt.Errorf("打开运行时存储: %w", err)
			return
		}
		if _, err := Sync(context.Background(), st, shippedDir()); err != nil {
			st.Close()
			shippedErr = fmt.Errorf("同步交付语义仓库失败: %w", err)
			return
		}
		shippedSt = st
	})
	if shippedErr != nil {
		t.Fatal(shippedErr)
	}
	return shippedSt
}

// kindFQNs 返回运行时存储里某类实体的 FQN 列表（升序；prefix 过滤可选）。
// prefix 用于「服务名.%」前缀匹配：服务名含 _ 时 LIKE 的 _ 是单字符通配
// （bss_wallet 会误匹配 bssXwallet），需 ESCAPE 转义。
func kindFQNs(t *testing.T, st *store.Store, kind Kind, prefix string) []string {
	t.Helper()
	q := "SELECT fqn FROM dgw_sem_entities WHERE kind = ? AND tombstone = 0"
	args := []any{string(kind)}
	if prefix != "" {
		q += " AND fqn LIKE ? ESCAPE '\\'"
		args = append(args, strings.ReplaceAll(prefix, "_", `\_`)+".%")
	}
	q += " ORDER BY fqn"
	rows, err := st.DB().QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("kindFQNs(%s): %v", kind, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fqn string
		if err := rows.Scan(&fqn); err != nil {
			t.Fatal(err)
		}
		out = append(out, fqn)
	}
	return out
}

// TestShippedSemanticCoverage 覆盖断言：13 服务 / 10 持库全覆盖 /
// 3 无持库服务仅服务实体 / 指标 + 概念就位（issue #30 AC1/AC2）。
func TestShippedSemanticCoverage(t *testing.T) {
	st := syncShipped(t)

	svcs := kindFQNs(t, st, KindService, "")
	if len(svcs) != 13 {
		t.Errorf("服务数 = %d，期望 13（neo-cloud 全量）", len(svcs))
	}
	dbs := kindFQNs(t, st, KindDatabase, "")
	if len(dbs) != 10 {
		t.Errorf("持库数 = %d，期望 10（10 个持库全覆盖）", len(dbs))
	}
	// 每个持库服务至少 1 张表；无持库服务 0 表（只服务实体）。
	for _, fqn := range svcs {
		tbls := kindFQNs(t, st, KindTable, fqn)
		switch fqn {
		case "ops-operation", "dashboard-backend", "usage-collection":
			if len(tbls) != 0 {
				t.Errorf("无持库服务 %s 不应有表（实际 %d）", fqn, len(tbls))
			}
		default:
			if len(tbls) == 0 {
				t.Errorf("持库服务 %s 应有表", fqn)
			}
		}
	}
	// 指标与概念就位。
	ctx := context.Background()
	for _, fqn := range []string{"payment_failure_rate"} {
		if _, err := GetEntity(ctx, st, fqn); err != nil {
			t.Errorf("指标 %s 缺失: %v", fqn, err)
		}
	}
	for _, fqn := range []string{"payment_failure", "payment_order", "bill", "organization"} {
		if _, err := GetEntity(ctx, st, fqn); err != nil {
			t.Errorf("概念 %s 缺失: %v", fqn, err)
		}
	}
}

// TestShippedSemanticTools 08 工具抽查（issue #30 AC4）：
// search_entities 双入口定位 → get_entity 枚举含义 → traverse_relations
// 引用边 → list_enum_values。
func TestShippedSemanticTools(t *testing.T) {
	st := syncShipped(t)
	ctx := context.Background()

	// search_entities：「支付失败」双入口定位到概念 + 指标。
	hits, total, err := SearchEntities(ctx, st, "支付失败", "", nil, SearchLimit, nil)
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if total == 0 {
		t.Fatal("搜「支付失败」应命中语义实体")
	}
	found := map[string]bool{}
	for _, h := range hits {
		found[h.FQN] = true
	}
	if !found["payment_failure"] || !found["payment_failure_rate"] {
		t.Errorf("「支付失败」应命中 payment_failure 概念与 payment_failure_rate 指标，实际: %v", hits)
	}
	// 无持库服务也可检索（服务实体在语义层）。
	if _, _, err := SearchEntities(ctx, st, "用量采集", "", nil, SearchLimit, nil); err != nil {
		t.Fatalf("SearchEntities(用量采集): %v", err)
	}

	// get_entity：支付单状态列枚举含义已回写（US-15/16 确认后语义）。
	col, err := GetEntityDetail(ctx, st, "bss-wallet.wallet.payment_orders.status")
	if err != nil {
		t.Fatalf("GetEntityDetail(status): %v", err)
	}
	labels := map[string]string{}
	for _, e := range col.Enums {
		labels[e.Value] = e.Label
	}
	if labels["4"] != "支付失败" || labels["2"] != "支付成功" {
		t.Errorf("status 枚举 label 未回写: %v", labels)
	}

	// traverse_relations：支付单 → 钱包账户（references 边）。
	tr, err := TraverseRelations(ctx, st, "bss-wallet.wallet.payment_orders", RelReferences, "out", 1, MaxTraverseNodes)
	if err != nil {
		t.Fatalf("TraverseRelations: %v", err)
	}
	reachable := map[string]bool{}
	for _, n := range tr.Nodes {
		reachable[n.FQN] = true
	}
	if !reachable["bss-wallet.wallet.wallet_accounts"] {
		t.Errorf("支付单应经 references 到达钱包账户，实际节点: %v", tr.Nodes)
	}

	// list_enum_values：退款单状态枚举有界返回。
	vals, totalEnums, _, err := ListEnumValues(ctx, st, "bss-wallet.wallet.refund_orders.status", 100)
	if err != nil {
		t.Fatalf("ListEnumValues: %v", err)
	}
	if totalEnums != 7 || len(vals) != 7 {
		t.Errorf("退款状态枚举数 = %d/%d，期望 7", len(vals), totalEnums)
	}
}

// TestShippedMetricPaymentFailureRate 主用例指标口径 machine-readable +
// dry-run 展开正确（issue #30 AC3）：表达式/聚合/过滤/依赖表可读，
// 带时间参数按 payment_orders.created_at 半开区间展开（不执行）。
func TestShippedMetricPaymentFailureRate(t *testing.T) {
	st := syncShipped(t)
	ctx := context.Background()

	d, err := MetricDefinition(ctx, st, "payment_failure_rate", nil, nil)
	if err != nil {
		t.Fatalf("MetricDefinition: %v", err)
	}
	if d.Expression != "COUNT(*) FILTER (WHERE status = 4)::numeric / NULLIF(COUNT(*), 0)" {
		t.Errorf("口径表达式 = %q", d.Expression)
	}
	if d.Aggregation != "ratio" || d.Filter != "" {
		t.Errorf("聚合/过滤 = %q/%q", d.Aggregation, d.Filter)
	}
	if len(d.Tables) != 1 || d.Tables[0] != "bss-wallet.wallet.payment_orders" {
		t.Errorf("依赖表 = %v", d.Tables)
	}
	if d.TimeApplied || d.DryRunSQL != "SELECT COUNT(*) FILTER (WHERE status = 4)::numeric / NULLIF(COUNT(*), 0) AS payment_failure_rate FROM payment_orders" {
		t.Errorf("无时间参数 dry-run = %q（TimeApplied=%v）", d.DryRunSQL, d.TimeApplied)
	}

	start, _ := time.Parse(time.RFC3339, "2026-08-11T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-08-12T00:00:00Z")
	d, err = MetricDefinition(ctx, st, "payment_failure_rate", &start, &end)
	if err != nil {
		t.Fatalf("MetricDefinition(时间): %v", err)
	}
	want := "SELECT COUNT(*) FILTER (WHERE status = 4)::numeric / NULLIF(COUNT(*), 0) AS payment_failure_rate FROM payment_orders" +
		" WHERE payment_orders.created_at >= '2026-08-11T00:00:00Z' AND payment_orders.created_at < '2026-08-12T00:00:00Z'"
	if d.DryRunSQL != want {
		t.Errorf("带时间 dry-run SQL =\n%q\n期望\n%q", d.DryRunSQL, want)
	}
	if !d.TimeApplied || d.Note != "" {
		t.Errorf("TimeApplied=%v Note=%q", d.TimeApplied, d.Note)
	}
	// 展开 SQL 本身可被 PG 解析（口径在编译期已校验，这里再确证一次）。
	if err := parseProbe(d.DryRunSQL); err != nil {
		t.Errorf("dry-run SQL 应可解析: %v", err)
	}
}

// TestShippedSemanticContent 语义内容抽查：服务/表描述与 is_time 标注
// 已回写（structure-only 草稿不满足本票验收）。
func TestShippedSemanticContent(t *testing.T) {
	st := syncShipped(t)
	ctx := context.Background()

	svc, err := GetEntity(ctx, st, "bss-wallet")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Description == "" || !strings.Contains(svc.Description, "钱包") {
		t.Errorf("bss-wallet 服务描述缺失: %q", svc.Description)
	}
	tbl, err := GetEntity(ctx, st, "bss-wallet.wallet.payment_orders")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tbl.Description, "支付单") {
		t.Errorf("payment_orders 表描述缺失: %q", tbl.Description)
	}
	// is_time 标注（dry-run 时间展开的挂载点）。
	col, err := GetEntity(ctx, st, "bss-wallet.wallet.payment_orders.created_at")
	if err != nil {
		t.Fatal(err)
	}
	if !col.IsTime {
		t.Error("payment_orders.created_at 应标注 is_time")
	}
	// 主用例下钻相关表（账单/结算/组织）都在语义层。
	for _, fqn := range []string{"bss-bill.bill.bills", "bss-bill.bill.settlement_batches", "iam.iam.organizations"} {
		if _, err := GetEntity(ctx, st, fqn); err != nil {
			t.Errorf("下钻表 %s 缺失: %v", fqn, err)
		}
	}
}
