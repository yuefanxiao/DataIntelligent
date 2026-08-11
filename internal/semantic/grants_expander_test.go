package semantic

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// storeOpenForTest 打开一个 SQLite 文件（备份恢复面测试用）。
func storeOpenForTest(t *testing.T, path string) (*store.Store, error) {
	t.Helper()
	return store.Open(path)
}

// 验收 6：指标/概念授权编译期展开为表授权（写入 02 权限表），无悬空。
// 流程 = 语义同步 → grants YAML（metric:/concept: 形态）→ grants-apply 展开。
func TestGrantExpansionMetricConcept(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync semantic: %v", err)
	}

	expand := NewGrantExpander(st)
	f, err := grants.Parse(strReader(`version: 1
grants:
  - user: dev-alice
    tables:
      - metric:payment_failure_rate
      - concept:payment_failure
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res, err := grants.Sync(ctx, st, f, expand)
	if err != nil {
		t.Fatalf("grants.Sync: %v", err)
	}
	if res.Added != 1 {
		t.Errorf("展开后授权 = Added %d, want 1（指标→payments + 概念→status 归表 = 同一张表去重）", res.Added)
	}

	// 指标展开 = 依赖表 payments；概念展开 = status 列归表 payments → 都落
	// dgw_table_grants（表 FQN 同一命名空间），无「指标有权底层没权」悬空。
	snap, err := grants.Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := map[string]bool{}
	for _, g := range snap {
		if g.User != "dev-alice" {
			continue
		}
		got[g.TableFQN] = true
	}
	if !got["payment-service.payment_db.payments"] {
		t.Errorf("展开应写入 payments 表授权: %+v", snap)
	}
	if len(got) != 1 {
		t.Errorf("两张展开应去重为 1 张表授权: %+v", snap)
	}
	// 权限 revision 已 bump（热重载信号）
	rev, err := st.PermissionRevision(ctx)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if rev < 1 {
		t.Errorf("revision = %d, want ≥1（授权展开应 bump 热重载信号）", rev)
	}
}

// 悬空拒绝：指标授权但语义库无该指标 → 编译拒绝（不写库）。
func TestGrantExpansionDanglingRejected(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync semantic: %v", err)
	}

	expand := NewGrantExpander(st)
	f, err := grants.Parse(strReader(`version: 1
grants:
  - user: dev-alice
    tables:
      - metric:no_such_metric
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := grants.Sync(ctx, st, f, expand); err == nil {
		t.Fatal("悬空指标授权应编译拒绝")
	}
	// 零写库
	snap, err := grants.Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Errorf("悬空拒绝应零写库: %+v", snap)
	}
}

// 验收 7：服务/库级通配 = 语法糖，展开为具体表清单快照。
func TestGrantExpansionWildcard(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync semantic: %v", err)
	}

	expand := NewGrantExpander(st)
	f, err := grants.Parse(strReader(`version: 1
grants:
  - user: dev-bob
    tables:
      - service:payment-service
      - database:order-service.order_db
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := grants.Sync(ctx, st, f, expand); err != nil {
		t.Fatalf("grants.Sync: %v", err)
	}

	snap, err := grants.Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := map[string]bool{}
	for _, g := range snap {
		if g.User != "dev-bob" {
			continue
		}
		got[g.TableFQN] = true
	}
	// payment-service 只有 1 张表；order-service.order_db 只有 1 张表 → 共 2 张
	if len(got) != 2 {
		t.Errorf("通配展开快照 = %d 张表, want 2: %+v", len(got), got)
	}
	if !got["payment-service.payment_db.payments"] || !got["order-service.order_db.orders"] {
		t.Errorf("通配展开缺表: %+v", got)
	}

	// 通配声明已记录（同步管线告警依据）
	patterns, err := grants.SyncPatterns(ctx, st)
	if err != nil {
		t.Fatalf("SyncPatterns: %v", err)
	}
	if len(patterns) != 2 {
		t.Errorf("通配声明 = %d, want 2: %v", len(patterns), patterns)
	}
}

// 新表默认拒绝 + 管线告警：通配快照后新增表 → 不在授权里（默认拒绝）。
func TestWildcardSnapshotNewTableDefaultDeny(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync semantic v1: %v", err)
	}

	expand := NewGrantExpander(st)
	f, err := grants.Parse(strReader(`version: 1
grants:
  - user: dev-bob
    tables:
      - service:payment-service
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := grants.Sync(ctx, st, f, expand); err != nil {
		t.Fatalf("grants.Sync: %v", err)
	}

	// 语义新增一张表（payments_refunds）并同步——通配声明仍在，但新表
	// 不在展开快照里（新表默认拒绝）。
	dir2 := writeSemantic(t, map[string]string{
		"services/payment-service.yaml": sampleServices + `      - name: payment_refunds
        description: 支付退款单
`,
		"services/order-service.yaml": sampleOrders,
		"metrics.yaml":                sampleMetrics,
		"concepts.yaml":               sampleConcepts,
	})
	if _, err := Sync(ctx, st, dir2); err != nil {
		t.Fatalf("Sync semantic v2: %v", err)
	}

	// 新表不在授权快照 = 默认拒绝（零未授权访问的基线）
	snap, err := grants.Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, g := range snap {
		if g.TableFQN == "payment-service.payment_db.payment_refunds" {
			t.Error("新表不应自动进授权快照（新表默认拒绝，重展开确认）")
		}
	}
	// 语义库里有新表（管线可据此告警）
	tbls, err := ListTables(ctx, st)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	found := false
	for _, tb := range tbls {
		if tb.FQN == "payment-service.payment_db.payment_refunds" {
			found = true
		}
	}
	if !found {
		t.Error("新表应存在于语义库（告警的对照面）")
	}
}

// 验收 5：embedding 生成写入向量（失败降级不阻塞同步）。
func TestEmbeddingDegradeNotBlock(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)

	// 失败 embedder：同步必须继续（降级不阻塞）
	failing := &fakeEmbedder{err: errors.New("api down")}
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	target, err := Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	n, err := EmbedEntityTexts(ctx, st, target, failing, "text-embedding-3", nil)
	if err == nil {
		t.Error("失败 embedder 应返回错误（调用方据此记录降级提示）")
	}
	if n != 0 {
		t.Errorf("失败 embedder 应降级为 0 写入, got %d", n)
	}

	// 成功 embedder：向量写入可查（确定性 fake：按文本 hash 生成）
	good := &fakeEmbedder{}
	n, err = EmbedEntityTexts(ctx, st, target, good, "text-embedding-3", nil)
	if err != nil {
		t.Fatalf("embedding 成功路径: %v", err)
	}
	if n != len(target.Entities) {
		t.Errorf("写入实体数 = %d, want %d", n, len(target.Entities))
	}
	var cnt int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dgw_sem_embeddings WHERE model = 'text-embedding-3'`).Scan(&cnt); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if cnt != len(target.Entities) {
		t.Errorf("向量表行数 = %d, want %d", cnt, len(target.Entities))
	}
	// 幂等重写不报错
	if _, err := EmbedEntityTexts(ctx, st, target, good, "text-embedding-3", nil); err != nil {
		t.Fatalf("embedding 重写: %v", err)
	}
}

// fakeEmbedder：确定性向量（按文本 hash），err 非 nil 时失败（降级测试）。
type fakeEmbedder struct{ err error }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, s := range texts {
		v := make([]float32, 4)
		h := hashString(s)
		for j := range v {
			v[j] = float32((h>>(j*8))&0xff) / 255
		}
		out[i] = v
	}
	return out, nil
}

func hashString(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// 向量编解码 round-trip。
func TestVectorEncodeDecode(t *testing.T) {
	v := []float32{0.1, -0.2, math.MaxFloat32, math.SmallestNonzeroFloat32, 1}
	buf := encodeFloats(v)
	got, err := decodeFloats(buf)
	if err != nil {
		t.Fatalf("decodeFloats: %v", err)
	}
	if len(got) != len(v) {
		t.Fatalf("长度 %d != %d", len(got), len(v))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("第 %d 个值 %v != %v", i, got[i], v[i])
		}
	}
}

// 备份：WAL checkpoint + 文件拷贝（ADR-0005；10 部署接入）。
func TestBackupRoundTrip(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(ctx, st, dst); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	// 备份文件存在且非空
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("backup 文件为空")
	}

	// 打开备份可查询（恢复面）
	restore, err := storeOpenForTest(t, dst)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restore.Close()
	tbls, err := ListTables(ctx, restore)
	if err != nil {
		t.Fatalf("ListTables on backup: %v", err)
	}
	if len(tbls) != 2 {
		t.Errorf("备份可查 = %d 张表, want 2", len(tbls))
	}
}

func strReader(s string) io.Reader { return strings.NewReader(s) }
