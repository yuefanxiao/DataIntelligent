package grants

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestValidateFQN(t *testing.T) {
	valid := []string{
		"bss.payment_db.t_payment",
		"iam.iam_db.t_user",
		"a.b.c",
	}
	for _, fqn := range valid {
		if err := ValidateFQN(fqn); err != nil {
			t.Errorf("ValidateFQN(%q) = %v, want nil", fqn, err)
		}
	}

	invalid := []struct {
		fqn  string
		want string // 错误信息应包含的关键片段（面向 CLI 运维者）
	}{
		{"bss.payment_db", "服务.库.表"},
		{"a.b.c.d", "服务.库.表"},
		{"a..c", "第 2 段为空"},
		{"a.b.", "第 3 段为空"},
		{".b.c", "第 1 段为空"},
		{"a b.c.d", "空白"},
		{"bss.*", "通配"},
		{"*", "通配"},
		{"bss.payment_db.*.extra", "通配"},
	}
	for _, tc := range invalid {
		err := ValidateFQN(tc.fqn)
		if err == nil {
			t.Errorf("ValidateFQN(%q) = nil, want error（%q）", tc.fqn, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateFQN(%q) 错误 = %q, 应含 %q", tc.fqn, err, tc.want)
		}
		if strings.Contains(tc.fqn, "*") && !errors.Is(err, ErrWildcard) {
			t.Errorf("ValidateFQN(%q) 通配错误应可被 errors.Is 识别为 ErrWildcard", tc.fqn)
		}
	}
}

// 增删授权落库 + bump revision（热重载信号）；幂等 no-op 不 bump。
func TestAddRemoveGrantAndRevision(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	rev := func() int64 {
		r, err := s.PermissionRevision(ctx)
		if err != nil {
			t.Fatalf("PermissionRevision: %v", err)
		}
		return r
	}

	if err := AddGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if rev() != 1 {
		t.Errorf("AddGrant 后 revision = %d, want 1", rev())
	}
	if err := AddGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("重复 AddGrant: %v", err)
	}
	if rev() != 1 {
		t.Errorf("幂等 AddGrant 不应 bump：revision = %d, want 1", rev())
	}

	if err := RemoveGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if rev() != 2 {
		t.Errorf("RemoveGrant 后 revision = %d, want 2", rev())
	}
	if err := RemoveGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("重复 RemoveGrant: %v", err)
	}
	if rev() != 2 {
		t.Errorf("幂等 RemoveGrant 不应 bump：revision = %d, want 2", rev())
	}
}

// 非法 FQN 的增删一律拒绝，且不产生任何写入。
func TestAddRemoveRejectsInvalidFQN(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, fqn := range []string{"bss.payment_db", "bss.*"} {
		if err := AddGrant(ctx, s, "dev-alice", fqn); err == nil {
			t.Errorf("AddGrant(%q) = nil, want error", fqn)
		}
		if err := RemoveGrant(ctx, s, "dev-alice", fqn); err == nil {
			t.Errorf("RemoveGrant(%q) = nil, want error", fqn)
		}
	}
	if r, err := s.PermissionRevision(ctx); err != nil || r != 0 {
		t.Errorf("非法 FQN 后 revision = %d, %v；want 0, nil", r, err)
	}
}

func TestSnapshotOrdering(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := AddGrant(ctx, s, "dev-bob", "iam.iam_db.t_user"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := AddGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := AddGrant(ctx, s, "dev-alice", "bss.payment_db.t_order"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	got, err := Snapshot(ctx, s)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := []string{
		"dev-alice bss.payment_db.t_order",
		"dev-alice bss.payment_db.t_payment",
		"dev-bob iam.iam_db.t_user",
	}
	if len(got) != len(want) {
		t.Fatalf("快照行数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].User+" "+got[i].TableFQN != w {
			t.Errorf("快照[%d] = %q, want %q", i, got[i].User+" "+got[i].TableFQN, w)
		}
	}
}

const validYAML = `version: 1
grants:
  - user: dev-alice
    tables:
      - bss.payment_db.t_payment
      - iam.iam_db.t_user
  - user: dev-bob
    tables:
      - bss.payment_db.t_payment
`

func TestParseValid(t *testing.T) {
	f, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Version != 1 || len(f.Grants) != 2 {
		t.Fatalf("解析结果 = %+v", f)
	}
	if f.Grants[0].User != "dev-alice" || len(f.Grants[0].Tables) != 2 {
		t.Errorf("第一条 = %+v, want dev-alice 两张表", f.Grants[0])
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"版本不符": {`version: 2
grants: []`, "version = 2"},
		"缺 version": {`grants: []`, "version"},
		"未知字段": {`version: 1
typo: x
grants: []`, "not found in type grants.File"},
		"缺 user": {`version: 1
grants:
  - tables: [a.b.c]`, "user"},
		"空 tables": {`version: 1
grants:
  - user: dev-alice
    tables: []`, "tables 为空"},
		"非法 FQN": {`version: 1
grants:
  - user: dev-alice
    tables: [bss.payment_db]`, "服务.库.表"},
		"通配 FQN": {`version: 1
grants:
  - user: dev-alice
    tables: [bss.*]`, "通配"},
		"重复表": {`version: 1
grants:
  - user: dev-alice
    tables: [a.b.c, a.b.c]`, "重复授权"},
		"空文件": {``, "EOF"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.yaml))
			if err == nil {
				t.Fatal("Parse = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误 = %q, 应含 %q", err, tc.want)
			}
		})
	}
}

// Sync 全量编译：库里原有授权被 YAML 状态覆盖（YAML 是事实源），revision bump。
func TestSyncFullReplace(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if err := AddGrant(ctx, s, "dev-legacy", "bss.old_db.t_old"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := AddGrant(ctx, s, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	f, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res, err := Sync(ctx, s, f, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Added != 2 || res.Removed != 1 {
		t.Errorf("Sync 摘要 = %+v, want Added=2 Removed=1（净 diff：新增 dev-alice×iam、dev-bob×bss；移除 dev-legacy×bss.old）", res)
	}
	if res.Revision != 3 { // AddGrant ×2 + Sync ×1
		t.Errorf("revision = %d, want 3", res.Revision)
	}

	got, err := Snapshot(ctx, s)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Sync 后快照行数 = %d, want 3（%+v）", len(got), got)
	}
	for _, g := range got {
		if g.User == "dev-legacy" {
			t.Errorf("旧授权应被 YAML 状态覆盖: %+v", g)
		}
	}
}

// Sync 幂等：同文件反复 apply 结果一致；零变更不 bump（与 AddGrant/RemoveGrant
// 的 no-op 纪律一致，不触发无谓热重载）。
func TestSyncIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	f, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res1, err := Sync(ctx, s, f, nil)
	if err != nil {
		t.Fatalf("第 1 次 Sync: %v", err)
	}
	if res1.Added != 3 || res1.Removed != 0 || res1.Revision != 1 {
		t.Errorf("第 1 次 Sync = %+v, want Added=3 Removed=0 Revision=1", res1)
	}

	res2, err := Sync(ctx, s, f, nil)
	if err != nil {
		t.Fatalf("第 2 次 Sync: %v", err)
	}
	if res2.Added != 0 || res2.Removed != 0 {
		t.Errorf("第 2 次 Sync 净 diff = %+v, want 零变更", res2)
	}
	if res2.Revision != 1 {
		t.Errorf("零变更不应 bump：revision = %d, want 1", res2.Revision)
	}
}
