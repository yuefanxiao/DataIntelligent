package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 检索域测试（08 票）：五个语义原语——search_entities（FTS5 主通道 +
// vec0 向量兜底 RRF）、get_entity（枚举挂列/is_time/关系摘要）、
// traverse_relations（双向多跳有界）、get_metric_definition（dry-run
// 展开）、list_enum_values。测试哲学：只测外部行为（spec §5），向量
// 通道用脚本化 fake（确定性可预期）。

// scriptedEmbedder 是脚本化 fake embedding：文本 → 预置向量（缺失文本
// 报错——测试向量通道降级路径）。与 SaveEmbeddings 配合可精确控制
// 实体向量与查询向量的相似度关系。
type scriptedEmbedder struct {
	vecs  map[string][]float32
	calls []string // 记录查询侧嵌入输入（断言用）
}

func (s *scriptedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := s.vecs[t]
		if !ok {
			return nil, errors.New("scriptedEmbedder: 未预置文本 " + t)
		}
		out[i] = v
		s.calls = append(s.calls, t)
	}
	return out, nil
}

// failingEmbedder 恒失败的 embedder（向量通道降级路径的测试对象）。
type failingEmbedder struct{}

func (failingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, errors.New("embedding 服务不可用")
}

// vec 构造维度 4 的测试向量（脚本化向量的公共形状）。
func vec(a, b, c, d float32) []float32 {
	return []float32{a, b, c, d}
}

// 检索 fixture：支付域 + 退款域，含枚举/is_time/references/指标/概念
// （比 semantic_test 的样例多一个退款概念——RRF 向量兜底命中需要
// 「关键词不中但向量近」的实体）。
const searchServices = `version: 1
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

const searchOrders = `version: 1
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

const searchMetrics = `version: 1
metrics:
  - name: payment_failure_rate
    description: 支付失败率（支付状态为 failed 的单数占比）
    expression: "COUNT(*) FILTER (WHERE status = 'failed')::numeric / NULLIF(COUNT(*), 0)"
    aggregation: ratio
    filter: ""
    tables:
      - payment-service.payment_db.payments
`

const searchConcepts = `version: 1
concepts:
  - name: payment_failure
    description: 支付失败业务概念
    describes:
      - payment-service.payment_db.payments.status
      - payment_failure_rate
  - name: refund_flow
    description: 退款流程
    describes:
      - payment-service.payment_db.payments
`

func searchFixture(t *testing.T) *store.Store {
	t.Helper()
	st := newStore(t)
	dir := writeSemantic(t, map[string]string{
		"services/payment-service.yaml": searchServices,
		"services/order-service.yaml":   searchOrders,
		"metrics.yaml":                  searchMetrics,
		"concepts.yaml":                 searchConcepts,
	})
	if _, err := Sync(context.Background(), st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return st
}

// ── search_entities ────────────────────────────────────────────────────

// 关键词主通道：CJK 短语命中概念/指标；type 限定单入口；≤limit + total。
func TestSearchEntitiesKeyword(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// 「支付失败」（4 字符 ≥3 → FTS5 trigram 短语子串匹配）：概念 + 指标都命中。
	hits, total, err := SearchEntities(ctx, st, "支付失败", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d，期望 2（概念 + 指标）", total)
	}
	got := map[string]bool{}
	for _, h := range hits {
		got[h.FQN] = true
		if h.Kind != KindConcept && h.Kind != KindMetric {
			t.Errorf("命中 %s 的 kind = %s，期望 concept/metric", h.FQN, h.Kind)
		}
	}
	if !got["payment_failure"] || !got["payment_failure_rate"] {
		t.Errorf("命中 = %v，期望含 payment_failure 与 payment_failure_rate", got)
	}

	// type 限定：metric 单入口只出指标。
	hits, total, err = SearchEntities(ctx, st, "支付失败", "metric", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(metric): %v", err)
	}
	if total != 1 || hits[0].FQN != "payment_failure_rate" {
		t.Errorf("metric 单入口 = %+v total=%d，期望仅 payment_failure_rate", hits, total)
	}

	// 短查询（2 字符 <3 → LIKE 兜底）：同样命中。
	hits, total, err = SearchEntities(ctx, st, "支付", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(短查询): %v", err)
	}
	if total != 2 {
		t.Errorf("短查询 total = %d，期望 2", total)
	}

	// 英文标识符：payment_failure_rate 的 fqn/name 命中。
	hits, _, err = SearchEntities(ctx, st, "failure_rate", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(英文): %v", err)
	}
	if len(hits) == 0 || hits[0].FQN != "payment_failure_rate" {
		t.Errorf("英文查询命中 = %+v，期望 payment_failure_rate 第一", hits)
	}

	// 零命中：total = 0、hits 空（非错误）。
	hits, total, err = SearchEntities(ctx, st, "不存在的领域词xyz", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(零命中): %v", err)
	}
	if len(hits) != 0 || total != 0 {
		t.Errorf("零命中 = %+v total=%d，期望空", hits, total)
	}
}

// RRF 融合：关键词命中（即使弱相似）恒排在纯向量命中之前（ADR-0002
// 「关键词命中排向量命中之前」）；向量通道缺失（无 embedder）不报错。
func TestSearchEntitiesRRFOrdering(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// 实体向量（写入索引面）：payment_failure 与查询同向、refund_flow 高度
	// 同向但关键词不中（「支付失败」不出现于其文本）、payment_failure_rate
	// 正交。
	if err := SaveEmbeddings(ctx, st, "test-model", []string{
		"payment_failure", "payment_failure_rate", "refund_flow",
	}, [][]float32{
		vec(1, 0, 0, 0), vec(0, 1, 0, 0), vec(0.99, 0.01, 0, 0),
	}); err != nil {
		t.Fatalf("SaveEmbeddings: %v", err)
	}
	emb := &scriptedEmbedder{vecs: map[string][]float32{"支付失败": vec(1, 0, 0, 0)}}

	hits, total, err := SearchEntities(ctx, st, "支付失败", "", emb, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d，期望 3（关键词 2 + 向量兜底 1）", total)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d，期望 3", len(hits))
	}
	// 前两名 = 关键词命中的概念与指标（refund_flow 向量最近但关键词不中，
	// 只能排在其后——「关键词主通道优先」由权重保证）。
	if hits[0].FQN != "payment_failure" && hits[0].FQN != "payment_failure_rate" {
		t.Errorf("首位 = %s，期望关键词命中的概念/指标", hits[0].FQN)
	}
	if hits[1].FQN != "payment_failure" && hits[1].FQN != "payment_failure_rate" {
		t.Errorf("次位 = %s，期望关键词命中的概念/指标", hits[1].FQN)
	}
	if hits[2].FQN != "refund_flow" {
		t.Errorf("末位 = %s，期望 refund_flow（纯向量兜底）", hits[2].FQN)
	}
	// 查询文本确实被嵌入（向量通道真实参与）。
	if len(emb.calls) != 1 || emb.calls[0] != "支付失败" {
		t.Errorf("查询嵌入输入 = %v，期望 [支付失败]", emb.calls)
	}

	// 无 embedder（未配置）：纯关键词检索，不报错、只少向量兜底命中。
	hits, total, err = SearchEntities(ctx, st, "支付失败", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(nil embedder): %v", err)
	}
	if total != 2 || len(hits) != 2 {
		t.Errorf("nil embedder: total=%d hits=%d，期望 2/2", total, len(hits))
	}
}

// 向量通道故障（embedder 报错）→ 降级为纯关键词（不阻断主通道）。
func TestSearchEntitiesVectorDegrades(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()
	hits, total, err := SearchEntities(ctx, st, "支付失败", "", failingEmbedder{}, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(向量故障): %v", err)
	}
	if total != 2 || len(hits) != 2 {
		t.Errorf("向量故障降级: total=%d hits=%d，期望 2/2（纯关键词）", total, len(hits))
	}
}

// 参数校验：空查询/未知类型报错。
func TestSearchEntitiesValidation(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()
	if _, _, err := SearchEntities(ctx, st, "  ", "", nil, SearchLimit); err == nil {
		t.Error("空查询应报错")
	}
	if _, _, err := SearchEntities(ctx, st, "支付", "table", nil, SearchLimit); err == nil {
		t.Error("未知类型应报错")
	}
}

// ── get_entity ─────────────────────────────────────────────────────────

// FQN 精确：列实体带枚举挂列；表实体带 is_time 列与关系摘要（出/入边）。
func TestGetEntityDetail(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// 列实体：枚举挂列齐全（value + label）。
	d, err := GetEntityDetail(ctx, st, "payment-service.payment_db.payments.status")
	if err != nil {
		t.Fatalf("GetEntityDetail(列): %v", err)
	}
	if d.Kind != KindColumn || d.DataType != "varchar" {
		t.Errorf("列实体 = %+v", d.Entity)
	}
	if len(d.Enums) != 3 || d.Enums[0].Value != "failed" || d.Enums[0].Label != "支付失败" {
		t.Errorf("枚举挂列 = %+v，期望 3 条含 failed/支付失败", d.Enums)
	}

	// 时间列：is_time 标注。
	d, err = GetEntityDetail(ctx, st, "payment-service.payment_db.payments.created_at")
	if err != nil {
		t.Fatalf("GetEntityDetail(时间列): %v", err)
	}
	if !d.IsTime {
		t.Error("created_at 应 is_time=true")
	}

	// 表实体：关系摘要含 contains（出：列）、references（出：orders——
	// payments 引用 orders，边 src=payments；orders 侧查入边才见 payments）、
	// describes（入：指标/概念）。
	d, err = GetEntityDetail(ctx, st, "payment-service.payment_db.payments")
	if err != nil {
		t.Fatalf("GetEntityDetail(表): %v", err)
	}
	byType := map[RelationType]*RelationSummary{}
	for i := range d.Relations {
		byType[d.Relations[i].Type] = &d.Relations[i]
	}
	if r := byType[RelContains]; r == nil || len(r.Outgoing) != 3 {
		t.Errorf("contains 摘要 = %+v，期望 3 条出边（列）", r)
	}
	if r := byType[RelReferences]; r == nil || len(r.Outgoing) != 1 || r.Outgoing[0] != "order-service.order_db.orders" {
		t.Errorf("references 出边 = %+v", r)
	}
	if r := byType[RelReferences]; r == nil || len(r.Incoming) != 0 {
		t.Errorf("references 入边 = %+v，期望空（orders 侧才有入边）", r)
	}
	if r := byType[RelDescribes]; r == nil || len(r.Incoming) != 2 {
		t.Errorf("describes 入边 = %+v，期望 2（指标 + 概念）", r)
	}

	// 不存在的 FQN → ErrNotFound。
	if _, err := GetEntityDetail(ctx, st, "no-such-service.db.t"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在 FQN 错误 = %v，期望 ErrNotFound", err)
	}
}

// ── traverse_relations ─────────────────────────────────────────────────

// 双向多跳：connects_to 服务→库→表（多跳）；references 双向；深度/节点
// 界触发 truncated。
func TestTraverseRelations(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// out 两跳：payment-service → payment_db → payments（+ 列不展开——只
	// 沿 connects_to 边，contains 不属于该类型）。
	res, err := TraverseRelations(ctx, st, "payment-service", RelConnectsTo, "out", 2, MaxTraverseNodes)
	if err != nil {
		t.Fatalf("TraverseRelations: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("connects_to 两跳节点 = %+v，期望 2（服务 + 库）", res.Nodes)
	}

	// contains 多跳：payment_db → payments → 3 列（深度 3）。
	res, err = TraverseRelations(ctx, st, "payment-service.payment_db", RelContains, "out", 3, MaxTraverseNodes)
	if err != nil {
		t.Fatalf("TraverseRelations(contains): %v", err)
	}
	if len(res.Nodes) != 5 {
		t.Errorf("contains 多跳节点数 = %d，期望 5（库 + 表 + 3 列）", len(res.Nodes))
	}
	if len(res.Edges) != 4 {
		t.Errorf("contains 边数 = %d，期望 4", len(res.Edges))
	}

	// references 双向：从 orders 反向（in）回到 payments。
	res, err = TraverseRelations(ctx, st, "order-service.order_db.orders", RelReferences, "in", 1, MaxTraverseNodes)
	if err != nil {
		t.Fatalf("TraverseRelations(references in): %v", err)
	}
	if len(res.Nodes) != 2 || res.Nodes[0].FQN != "order-service.order_db.orders" {
		t.Errorf("references in 节点 = %+v", res.Nodes)
	}
	found := false
	for _, n := range res.Nodes {
		if n.FQN == "payment-service.payment_db.payments" {
			found = true
		}
	}
	if !found {
		t.Error("references in 应经入边到达 payments")
	}

	// 缺省深度 = 1：payment_db 的 contains 只到表，不到列。
	res, err = TraverseRelations(ctx, st, "payment-service.payment_db", RelContains, "out", 0, MaxTraverseNodes)
	if err != nil {
		t.Fatalf("TraverseRelations(缺省深度): %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("缺省深度节点数 = %d，期望 2（库 + 表）", len(res.Nodes))
	}

	// 深度 0 显式（语义 = 缺省 1）；起点不存在 → ErrNotFound。
	if _, err := TraverseRelations(ctx, st, "ghost", RelContains, "out", 0, MaxTraverseNodes); !errors.Is(err, ErrNotFound) {
		t.Errorf("起点不存在错误 = %v，期望 ErrNotFound", err)
	}
	// 非法方向报错。
	if _, err := TraverseRelations(ctx, st, "payment-service", RelContains, "sideways", 1, MaxTraverseNodes); err == nil {
		t.Error("非法方向应报错")
	}
}

// ── get_metric_definition ──────────────────────────────────────────────

// 口径 machine-readable + dry-run 展开（时间参数代入 is_time 列，不执行）；
// 无时间参数 = 不展开时间谓词；非指标/不存在 = 哨兵错误。
func TestMetricDefinitionDryRun(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// 无时间参数：口径原样 + 依赖表 + 基础展开 SQL（无 WHERE）。
	d, err := MetricDefinition(ctx, st, "payment_failure_rate", nil, nil)
	if err != nil {
		t.Fatalf("MetricDefinition: %v", err)
	}
	if d.Expression == "" || d.Aggregation != "ratio" {
		t.Errorf("口径 = %+v", d)
	}
	if len(d.Tables) != 1 || d.Tables[0] != "payment-service.payment_db.payments" {
		t.Errorf("依赖表 = %v", d.Tables)
	}
	if d.DryRunSQL != "SELECT COUNT(*) FILTER (WHERE status = 'failed')::numeric / NULLIF(COUNT(*), 0) AS payment_failure_rate FROM payments" {
		t.Errorf("dry-run SQL = %q", d.DryRunSQL)
	}
	if d.TimeApplied {
		t.Error("无时间参数不应 TimeApplied")
	}

	// 带时间参数：半开区间 [start, end) 应用到 payments.created_at。
	start, _ := time.Parse(time.RFC3339, "2026-08-11T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-08-12T00:00:00Z")
	d, err = MetricDefinition(ctx, st, "payment_failure_rate", &start, &end)
	if err != nil {
		t.Fatalf("MetricDefinition(时间): %v", err)
	}
	want := "SELECT COUNT(*) FILTER (WHERE status = 'failed')::numeric / NULLIF(COUNT(*), 0) AS payment_failure_rate FROM payments" +
		" WHERE payments.created_at >= '2026-08-11T00:00:00Z' AND payments.created_at < '2026-08-12T00:00:00Z'"
	if d.DryRunSQL != want {
		t.Errorf("带时间 dry-run SQL =\n%q\n期望\n%q", d.DryRunSQL, want)
	}
	if !d.TimeApplied || d.Note != "" {
		t.Errorf("TimeApplied=%v Note=%q", d.TimeApplied, d.Note)
	}

	// 非指标实体 → ErrNotMetric；不存在 → ErrNotFound；时间参数不完整报错。
	if _, err := MetricDefinition(ctx, st, "payment-service.payment_db.payments", nil, nil); !errors.Is(err, ErrNotMetric) {
		t.Errorf("非指标错误 = %v，期望 ErrNotMetric", err)
	}
	if _, err := MetricDefinition(ctx, st, "ghost_metric", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在指标错误 = %v，期望 ErrNotFound", err)
	}
	if _, err := MetricDefinition(ctx, st, "payment_failure_rate", &start, nil); err == nil {
		t.Error("时间参数不完整应报错")
	}
	if _, err := MetricDefinition(ctx, st, "payment_failure_rate", &end, &start); err == nil {
		t.Error("start >= end 应报错")
	}
}

// ── list_enum_values ───────────────────────────────────────────────────

// 枚举取值（CHECK 约束语义）：排序 + 有界 + total；非列/不存在 = 哨兵。
func TestListEnumValues(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	values, total, truncated, err := ListEnumValues(ctx, st, "payment-service.payment_db.payments.status", EnumValuesLimit)
	if err != nil {
		t.Fatalf("ListEnumValues: %v", err)
	}
	if total != 3 || truncated || len(values) != 3 {
		t.Errorf("枚举 = %d 条 total=%d truncated=%v，期望 3/3/false", len(values), total, truncated)
	}
	if values[0].Value != "failed" || values[0].Label != "支付失败" {
		t.Errorf("排序首位 = %+v，期望 failed/支付失败（按 value 排序）", values[0])
	}

	// 非列实体 → ErrNotColumn；不存在 → ErrNotFound。
	if _, _, _, err := ListEnumValues(ctx, st, "payment-service.payment_db.payments", EnumValuesLimit); !errors.Is(err, ErrNotColumn) {
		t.Errorf("非列错误 = %v，期望 ErrNotColumn", err)
	}
	if _, _, _, err := ListEnumValues(ctx, st, "ghost.table.col", EnumValuesLimit); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在列错误 = %v，期望 ErrNotFound", err)
	}
}

// ── 索引维护（Apply 的 FTS 面 + vec0 面）───────────────────────────────

// FTS 与实体同事务：墓碑实体从关键词索引消失；历史库（索引空 + 实体面
// 非空）全量回填。
func TestFTSMaintenance(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()

	// 墓碑传播：第二次同步删掉 refund_flow 概念 → 搜索不再命中它。
	dir := writeSemantic(t, map[string]string{
		"services/payment-service.yaml": searchServices,
		"services/order-service.yaml":   searchOrders,
		"metrics.yaml":                  searchMetrics,
		"concepts.yaml":                 strings.Replace(searchConcepts, "  - name: refund_flow\n    description: 退款流程\n    describes:\n      - payment-service.payment_db.payments\n", "", 1),
	})
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync(删 refund_flow): %v", err)
	}
	hits, _, err := SearchEntities(ctx, st, "退款流程", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(墓碑): %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("墓碑概念仍命中 = %+v", hits)
	}

	// 历史库升级：索引行被外部清空（模拟 08 前的库）→ 下次同步幂等回填。
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM dgw_sem_fts`); err != nil {
		t.Fatalf("清空 FTS: %v", err)
	}
	if _, err := Sync(ctx, st, dir); err != nil { // 无 diff 的幂等重跑
		t.Fatalf("Sync(回填): %v", err)
	}
	hits, total, err := SearchEntities(ctx, st, "支付失败", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(回填后): %v", err)
	}
	if total != 2 || len(hits) != 2 {
		t.Errorf("回填后检索 total=%d hits=%d，期望 2/2", total, len(hits))
	}
}

// vec0 索引维护：SaveEmbeddings 双写；维度变化（模型切换）重建 + 回填
// 同维存量；RemoveEmbeddings 双删。查询词用「关键词零命中」的专用词
// （zzz_vectest），隔离向量通道的行为。
func TestVecIndexMaintenance(t *testing.T) {
	st := searchFixture(t)
	ctx := context.Background()
	const q = "zzz_vectest" // 任何实体文本都不含此词（关键词通道必然零命中）

	// 写入 4 维向量 → vec0 建为 4 维（Open 时默认 1536，按实际维度重建）。
	if err := SaveEmbeddings(ctx, st, "test-model", []string{"payment_failure", "payment_failure_rate"},
		[][]float32{vec(1, 0, 0, 0), vec(0, 1, 0, 0)}); err != nil {
		t.Fatalf("SaveEmbeddings: %v", err)
	}
	emb4 := &scriptedEmbedder{vecs: map[string][]float32{q: vec(1, 0, 0, 0)}}
	hits, total, err := SearchEntities(ctx, st, q, "", emb4, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(向量): %v", err)
	}
	if total != 2 || len(hits) != 2 {
		t.Fatalf("向量通道 total=%d hits=%d，期望 2（两实体都入候选并集）", total, len(hits))
	}
	// RRF 按余弦距离排序：payment_failure（dist 0）在前。
	if hits[0].FQN != "payment_failure" || hits[1].FQN != "payment_failure_rate" {
		t.Errorf("向量通道顺序 = %+v，期望 payment_failure → payment_failure_rate（按距离）", hits)
	}

	// 维度变化（模拟模型切换 4→8）：重建索引，旧维行丢弃、新维写入。
	if err := SaveEmbeddings(ctx, st, "test-model-large", []string{"payment_failure"},
		[][]float32{{1, 0, 0, 0, 0, 0, 0, 0}}); err != nil {
		t.Fatalf("SaveEmbeddings(维度切换): %v", err)
	}
	emb8 := &scriptedEmbedder{vecs: map[string][]float32{q: {1, 0, 0, 0, 0, 0, 0, 0}}}
	hits, total, err = SearchEntities(ctx, st, q, "", emb8, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(重建后): %v", err)
	}
	if total != 1 || len(hits) != 1 || hits[0].FQN != "payment_failure" {
		t.Errorf("重建后 total=%d hits=%+v，期望仅 payment_failure（旧维行已弃）", total, hits)
	}

	// RemoveEmbeddings 双删：向量通道不再命中（关键词通道本就零命中）。
	if err := RemoveEmbeddings(ctx, st, []string{"payment_failure"}); err != nil {
		t.Fatalf("RemoveEmbeddings: %v", err)
	}
	hits, total, err = SearchEntities(ctx, st, q, "", emb8, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(双删后): %v", err)
	}
	if total != 0 || len(hits) != 0 {
		t.Errorf("双删后 total=%d hits=%+v，期望空", total, hits)
	}
}

// ── 确定性/边界 ────────────────────────────────────────────────────────

// 有界返回：超过 limit 的候选只取前 limit（构造 >limit 的候选集）。
func TestSearchEntitiesBounded(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	// 直插 30 个概念（绕过 YAML，检索边界测试聚焦）：名字共享「bounded」。
	for i := 0; i < 30; i++ {
		fqn := "concept_bounded_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO dgw_sem_entities (fqn, kind, name, description, tombstone)
			VALUES (?, 'concept', ?, 'bounded 概念 ' || ?, 0)`, fqn, fqn, fqn); err != nil {
			t.Fatalf("直插概念: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO dgw_sem_fts (fqn, kind, name, description) VALUES (?, 'concept', ?, 'bounded 概念 ' || ?)`,
			fqn, fqn, fqn); err != nil {
			t.Fatalf("直插 FTS: %v", err)
		}
	}
	hits, total, err := SearchEntities(ctx, st, "bounded", "", nil, SearchLimit)
	if err != nil {
		t.Fatalf("SearchEntities(有界): %v", err)
	}
	if len(hits) != SearchLimit {
		t.Errorf("hits = %d，期望 ≤%d（有界）", len(hits), SearchLimit)
	}
	if total != 30 {
		t.Errorf("total = %d，期望 30（并集总数如实）", total)
	}
}
