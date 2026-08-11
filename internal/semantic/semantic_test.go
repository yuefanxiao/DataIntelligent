package semantic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// newStore 开临时库（测试用）。
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// writeSemantic 写一个语义作者入口目录（services/ + metrics + concepts）。
func writeSemantic(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if strings.Contains(name, "/") {
			os.MkdirAll(filepath.Dir(path), 0o755)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// 样例语义（多服务 + 全局指标/概念）：验收「样例 YAML 编译 → dry-run →
// 应用后 SQLite 全量可查（实体/边/枚举/is_time）」。镜像 neo-cloud 形态：
// 支付服务 + 订单服务，含枚举挂列、is_time、references、指标、概念。
const sampleServices = `version: 1
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

const sampleOrders = `version: 1
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
          - name: total_amount
            type: numeric
`

const sampleMetrics = `version: 1
metrics:
  - name: payment_failure_rate
    description: 支付失败率（支付状态为 failed 的单数占比）
    expression: "COUNT(*) FILTER (WHERE status = 'failed')::numeric / NULLIF(COUNT(*), 0)"
    aggregation: ratio
    filter: ""
    tables:
      - payment-service.payment_db.payments
`

const sampleConcepts = `version: 1
concepts:
  - name: payment_failure
    description: 支付失败业务概念
    describes:
      - payment-service.payment_db.payments.status
      - payment_failure_rate
`

func sampleDir(t *testing.T) string {
	return writeSemantic(t, map[string]string{
		"services/payment-service.yaml": sampleServices,
		"services/order-service.yaml":   sampleOrders,
		"metrics.yaml":                  sampleMetrics,
		"concepts.yaml":                 sampleConcepts,
	})
}

// 验收 1：样例语义 YAML（多服务 + 全局）编译 → dry-run diff 正确 → 应用后
// SQLite 全量可查（实体/边/枚举/is_time）。
func TestSyncSampleEndToEnd(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)

	// dry-run：全新增
	res, err := DryRun(ctx, st, dir)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if res.Applied {
		t.Error("dry-run 不应写库")
	}
	if res.Diff.Count() == 0 {
		t.Fatal("dry-run 应为全新增 diff")
	}
	// 实体数 = 2 服务 + 2 库 + 2 表 + 5 列 + 1 指标 + 1 概念 = 13
	if n := len(res.Diff.EntitiesAdded); n != 13 {
		t.Errorf("EntitiesAdded = %d, want 13（2 服务/2 库/2 表/5 列/1 指标/1 概念）", n)
	}

	// 应用
	res2, err := Sync(ctx, st, dir)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res2.Applied || res2.Stats == nil {
		t.Error("Sync 应写库并返回统计")
	}

	// 全量可查：实体
	if e, err := GetEntity(ctx, st, "payment-service.payment_db.payments.status"); err != nil || e == nil {
		t.Fatalf("GetEntity status: e=%v err=%v", e, err)
	} else if e.Kind != KindColumn || !e.IsTime && e.Name != "created_at" {
		t.Logf("status 实体: %+v", e)
	}

	// is_time 标注
	ct, err := GetEntity(ctx, st, "payment-service.payment_db.payments.created_at")
	if err != nil || ct == nil {
		t.Fatalf("GetEntity created_at: %v", err)
	}
	if !ct.IsTime {
		t.Error("created_at 应标注 is_time")
	}

	// 枚举挂列
	enum, err := listEnumsForTest(ctx, st, "payment-service.payment_db.payments.status")
	if err != nil {
		t.Fatalf("enum: %v", err)
	}
	if len(enum) != 3 || enum[0] != "failed" {
		t.Errorf("枚举 = %v, want [failed pending succeeded]", enum)
	}

	// 指标口径（machine-readable）
	m, err := GetEntity(ctx, st, "payment_failure_rate")
	if err != nil || m == nil {
		t.Fatalf("GetEntity metric: %v", err)
	}
	if !strings.Contains(m.Expression, "FILTER") {
		t.Errorf("指标 expression 缺失: %q", m.Expression)
	}

	// 关系边：references / describes / contains / connects_to
	if ok, err := hasRelation(t, st, RelReferences, "payment-service.payment_db.payments", "order-service.order_db.orders"); err != nil || !ok {
		t.Errorf("references 边缺失: ok=%v err=%v", ok, err)
	}
	if ok, err := hasRelation(t, st, RelDescribes, "payment_failure_rate", "payment-service.payment_db.payments"); err != nil || !ok {
		t.Errorf("指标 describes 边缺失: ok=%v err=%v", ok, err)
	}
	if ok, err := hasRelation(t, st, RelDescribes, "payment_failure", "payment-service.payment_db.payments.status"); err != nil || !ok {
		t.Errorf("概念 describes 边缺失: ok=%v err=%v", ok, err)
	}
}

// 验收 3 幂等 + 验收 8 dry-run 确定性：同输入重跑同输出。
func TestSyncIdempotentDeterministic(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)

	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("第 1 次 Sync: %v", err)
	}
	// 重跑：diff 全空（幂等，upsert 无重复）
	res, err := DryRun(ctx, st, dir)
	if err != nil {
		t.Fatalf("重跑 DryRun: %v", err)
	}
	if !res.Diff.Empty() {
		t.Errorf("幂等重跑 diff 应为空: %+v", res.Diff)
	}
	res2, err := Sync(ctx, st, dir)
	if err != nil {
		t.Fatalf("第 2 次 Sync: %v", err)
	}
	// 重跑不应产生墓碑/新增（同输入同输出）
	if res2.Diff.Count() != 0 {
		t.Errorf("幂等重跑 Sync diff = %d 项, want 0", res2.Diff.Count())
	}

	// 确定性：dry-run 的输出与首次一致（同输入同输出，§5.3 seam）
	first, err := DryRun(ctx, st, dir)
	if err != nil {
		t.Fatalf("确定性 DryRun: %v", err)
	}
	second, err := DryRun(ctx, st, dir)
	if err != nil {
		t.Fatalf("确定性 DryRun 2: %v", err)
	}
	if !equalDiff(first.Diff, second.Diff) {
		t.Error("同输入两次 dry-run 输出不一致（确定性被破坏）")
	}
}

// 墓碑传播：删除一个实体（YAML 里删掉），重跑后 tombstone=1 且检索过滤。
func TestSyncTombstonePropagation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	dir1 := sampleDir(t)
	if _, err := Sync(ctx, st, dir1); err != nil {
		t.Fatalf("Sync v1: %v", err)
	}

	// 删掉 order-service 文件（整个服务消失）；payment 的 references 指向它，
	// 作者入口必须同步自洽（删引用 + 删服务），否则编译校验拒绝——这正是
	// 引用完整性校验的职责（作者入口不一致 = 原子拒绝）。
	paymentsNoRef := strings.Replace(sampleServices,
		`        references:
          - to: order-service.order_db.orders
            on: "payments.order_id = orders.id"
`, "", 1)
	dir2 := writeSemantic(t, map[string]string{
		"services/payment-service.yaml": paymentsNoRef,
		"metrics.yaml":                  sampleMetrics,
		"concepts.yaml":                 sampleConcepts,
	})
	res, err := Sync(ctx, st, dir2)
	if err != nil {
		t.Fatalf("Sync v2: %v", err)
	}
	if len(res.Diff.EntitiesDeleted) == 0 {
		t.Fatal("删除服务应产生墓碑 diff")
	}

	// 墓碑实体检索过滤（GetEntity 返回 nil）
	if e, err := GetEntity(ctx, st, "order-service"); err != nil || e != nil {
		t.Errorf("墓碑服务应检索过滤: e=%v err=%v", e, err)
	}
	// 删除服务时其子实体（库/表/列）也应墓碑（墓碑传播）
	if e, err := GetEntity(ctx, st, "order-service.order_db.orders"); err != nil || e != nil {
		t.Errorf("墓碑传播缺失（订单表应不可检索）: e=%v err=%v", e, err)
	}
	// 引用它的边也墓碑（references 残留清理）
	if ok, err := hasRelation(t, st, RelReferences, "payment-service.payment_db.payments", "order-service.order_db.orders"); err != nil || ok {
		t.Errorf("references 边应随目标墓碑化: ok=%v err=%v", ok, err)
	}
}

// references 的 join 条件（meta）变化必须进 dry-run diff——Apply 的
// ON CONFLICT 会真的改写 meta，diff 与 apply 不一致 = 幂等重跑输出失真
// （review 修复：Compare 增加 RelationsUpdated）。
func TestSyncRelationMetaChangeInDiff(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	dir1 := sampleDir(t)
	if _, err := Sync(ctx, st, dir1); err != nil {
		t.Fatalf("Sync v1: %v", err)
	}

	// 改 join 条件（on 子句变化）
	changed := strings.Replace(sampleServices,
		`on: "payments.order_id = orders.id"`,
		`on: "payments.order_id = orders.id AND orders.status <> 'cancelled'"`, 1)
	dir2 := writeSemantic(t, map[string]string{
		"services/payment-service.yaml": changed,
		"services/order-service.yaml":   sampleOrders,
		"metrics.yaml":                  sampleMetrics,
		"concepts.yaml":                 sampleConcepts,
	})
	res, err := DryRun(ctx, st, dir2)
	if err != nil {
		t.Fatalf("DryRun v2: %v", err)
	}
	if len(res.Diff.RelationsUpdated) != 1 {
		t.Fatalf("join 条件变化应进 diff.RelationsUpdated, got %+v", res.Diff)
	}
	if len(res.Diff.RelationsAdded) != 0 || len(res.Diff.RelationsDeleted) != 0 {
		t.Errorf("join 条件变化不应产生增删边: %+v", res.Diff)
	}

	// 应用后重跑 = 空 diff（幂等收敛）
	if _, err := Sync(ctx, st, dir2); err != nil {
		t.Fatalf("Sync v2: %v", err)
	}
	res3, err := DryRun(ctx, st, dir2)
	if err != nil {
		t.Fatalf("DryRun v3: %v", err)
	}
	if !res3.Diff.Empty() {
		t.Errorf("应用后重跑应为空 diff: %+v", res3.Diff)
	}
}

// 验收 2：编译校验——FQN 重复 / 引用缺失 / 指标 SQL 不可解析 / 枚举非法 →
// 原子拒绝（错误返回且零写库）。
func TestCompileRejects(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string // 错误信息片段
	}{
		{
			name: "FQN 重复",
			files: map[string]string{
				"services/a.yaml": `version: 1
service: dup
databases:
  - name: db1
    tables:
      - name: t1
`,
				"services/b.yaml": `version: 1
service: dup
databases:
  - name: db2
    tables:
      - name: t2
`,
			},
			want: "FQN 重复",
		},
		{
			name: "引用缺失 references",
			files: map[string]string{
				"services/a.yaml": `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t1
        references:
          - to: svc.db.nonexistent
            on: "t1.id = nonexistent.id"
`,
			},
			want: "引用完整性",
		},
		{
			name: "指标 SQL 不可解析",
			files: map[string]string{
				"metrics.yaml": `version: 1
metrics:
  - name: broken
    expression: "COUNT( WHERE )"
    tables:
      - svc.db.t1
`,
				"services/a.yaml": `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t1
`,
			},
			want: "不可解析",
		},
		{
			name: "指标 filter 不可解析",
			files: map[string]string{
				"metrics.yaml": `version: 1
metrics:
  - name: badfilter
    expression: "COUNT(*)"
    filter: "status = AND ("
    tables:
      - svc.db.t1
`,
				"services/a.yaml": `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t1
`,
			},
			want: "filter 不可解析",
		},
		{
			name: "枚举非法（value 为空）",
			files: map[string]string{
				"services/a.yaml": `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t1
        columns:
          - name: c1
            type: varchar
            enum_values:
              - value: ""
`,
			},
			want: "value 为空",
		},
		{
			name: "枚举重复",
			files: map[string]string{
				"services/a.yaml": `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t1
        columns:
          - name: c1
            type: varchar
            enum_values:
              - value: a
              - value: a
`,
			},
			want: "重复",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			dir := writeSemantic(t, tc.files)
			_, err := Sync(ctx, st, dir)
			if err == nil {
				t.Fatal("Sync 应失败（编译拒绝）")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息 %q 缺片段 %q", err.Error(), tc.want)
			}
			// 原子拒绝：库零写
			snap, err := Snapshot(ctx, st)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(snap.Entities) != 0 {
				t.Errorf("原子拒绝应零写库, 实体 = %d", len(snap.Entities))
			}
		})
	}
}

// 验收 4：运行时只查 SQLite 不读 YAML——同步后删除 YAML 源，查询照常。
func TestRuntimeDoesNotReadYAML(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)

	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// 删掉整个作者入口（模拟仓库被移除/离线）
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove semantic dir: %v", err)
	}
	e, err := GetEntity(ctx, st, "payment-service")
	if err != nil {
		t.Fatalf("YAML 删除后查询失败（运行时不应读 YAML）: %v", err)
	}
	if e == nil || e.Kind != KindService {
		t.Errorf("服务实体缺失: %+v", e)
	}
	// 表清单查询照常（授权展开依赖）
	tbls, err := ListTables(ctx, st)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tbls) != 2 {
		t.Errorf("ListTables = %d, want 2", len(tbls))
	}
}

// 验收 1 补充：关系边双向可遍历（ADR-0001「四类关系边双向可遍历」）——
// 反向查询（dst → src）与正向（src → dst）都可用。
func TestRelationBidirectionalTraversal(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// 正向：服务 → 库（connects_to）
	fwd, err := relationTargets(ctx, st, RelConnectsTo, "payment-service")
	if err != nil {
		t.Fatalf("正向 connects_to: %v", err)
	}
	if len(fwd) != 1 || fwd[0] != "payment-service.payment_db" {
		t.Errorf("正向 connects_to = %v, want [payment-service.payment_db]", fwd)
	}
	// 反向：库 → 服务（dst 查 src）
	rev, err := relationSources(ctx, st, RelConnectsTo, "payment-service.payment_db")
	if err != nil {
		t.Fatalf("反向 connects_to: %v", err)
	}
	if len(rev) != 1 || rev[0] != "payment-service" {
		t.Errorf("反向 connects_to = %v, want [payment-service]", rev)
	}

	// 反向 contains：表 → 库
	rev2, err := relationSources(ctx, st, RelContains, "payment-service.payment_db.payments")
	if err != nil {
		t.Fatalf("反向 contains: %v", err)
	}
	if len(rev2) != 1 || rev2[0] != "payment-service.payment_db" {
		t.Errorf("反向 contains = %v, want [payment-service.payment_db]", rev2)
	}

	// references 反向：被引用表 → 引用它的表（samples 里 orders 被 payments 引用）
	rev3, err := relationSources(ctx, st, RelReferences, "order-service.order_db.orders")
	if err != nil {
		t.Fatalf("反向 references: %v", err)
	}
	if len(rev3) != 1 || rev3[0] != "payment-service.payment_db.payments" {
		t.Errorf("反向 references = %v, want [payment-service.payment_db.payments]", rev3)
	}
}

// --- helpers ---

func listEnumsForTest(ctx context.Context, st *store.Store, colFQN string) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx,
		`SELECT value FROM dgw_sem_enum_values WHERE column_fqn = ? AND tombstone = 0 ORDER BY value`, colFQN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func hasRelation(t *testing.T, st *store.Store, typ RelationType, src, dst string) (bool, error) {
	t.Helper()
	var n int
	err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM dgw_sem_relations WHERE type = ? AND src_fqn = ? AND dst_fqn = ? AND tombstone = 0`,
		string(typ), src, dst).Scan(&n)
	return n > 0, err
}

func equalDiff(a, b *Diff) bool {
	if len(a.EntitiesAdded) != len(b.EntitiesAdded) ||
		len(a.EntitiesUpdated) != len(b.EntitiesUpdated) ||
		len(a.EntitiesDeleted) != len(b.EntitiesDeleted) ||
		len(a.RelationsAdded) != len(b.RelationsAdded) ||
		len(a.RelationsDeleted) != len(b.RelationsDeleted) ||
		len(a.EnumsAdded) != len(b.EnumsAdded) ||
		len(a.EnumsDeleted) != len(b.EnumsDeleted) {
		return false
	}
	for i := range a.EntitiesAdded {
		if a.EntitiesAdded[i].FQN != b.EntitiesAdded[i].FQN {
			return false
		}
	}
	return true
}

// sql.DB 接口断言（防止签名漂移）：semantic 包的查询函数接受任何 DB() *sql.DB。
var _ DBer = (*store.Store)(nil)
