package credentials

import (
	"context"
	"errors"
	"fmt"
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

func TestGenerateShape(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(k, Prefix) {
		t.Errorf("缺前缀 %q: %q", Prefix, k)
	}
	if len(k) != 47 {
		t.Errorf("长度 = %d, want 47（32 字节 base64url 无填充 + 前缀）", len(k))
	}
	if strings.ContainsAny(k, "+/=") {
		t.Errorf("出现 base64 填充/不兼容字符: %q", k)
	}

	k2, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if k == k2 {
		t.Error("两次生成相同——随机源失效？")
	}
}

func TestHashDeterministic(t *testing.T) {
	h1 := Hash("dgw_secret")
	h2 := Hash("dgw_secret")
	if h1 != h2 || len(h1) != 64 {
		t.Errorf("Hash 应稳定输出 64 hex: %q vs %q", h1, h2)
	}
}

// 明文仅打印一次契约：库里只有哈希，没有任何列存明文。
func TestPlaintextNeverStored(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	plain, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rows, err := s.DB().QueryContext(ctx,
		"SELECT key_hash, user_id, role, created_at, revoked_at FROM dgw_credentials")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var fields []string
	for rows.Next() {
		var hash, userID, createdAt string
		var role, revokedAt *string
		if err := rows.Scan(&hash, &userID, &role, &createdAt, &revokedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fields = append(fields, hash, userID, createdAt)
		if role != nil {
			fields = append(fields, *role)
		}
		if revokedAt != nil {
			fields = append(fields, *revokedAt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, f := range fields {
		if strings.Contains(f, plain) {
			t.Fatalf("明文 %q 出现在存储字段 %q 中", plain, f)
		}
	}
	if !strings.Contains(fields[0], Hash(plain)) {
		t.Errorf("应存哈希而非明文: %q", fields[0])
	}
}

func TestCreateVerifyRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	plain, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	userID, err := Verify(ctx, s.DB(), plain)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if userID != "dev-alice" {
		t.Errorf("userID = %q, want %q", userID, "dev-alice")
	}
}

// 一用户多 key（ADR-0004）：设备/工具维度，全部解析到同一身份。
func TestMultipleKeysSameUser(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	k1, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	k2, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, k := range []string{k1, k2} {
		if userID, err := Verify(ctx, s.DB(), k); err != nil || userID != "dev-alice" {
			t.Errorf("Verify(%q) = %q, %v；want dev-alice, nil", k, userID, err)
		}
	}
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := Verify(ctx, s.DB(), Prefix+"not-a-real-key"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("错误 = %v, want ErrInvalidKey", err)
	}
}

// 重启持久性：哈希落盘，跨进程仍可校验。
func TestVerifyAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dgw.db")
	ctx := context.Background()

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plain, err := Create(ctx, s1.DB(), "dev-bob")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s1.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if userID, err := Verify(ctx, s2.DB(), plain); err != nil || userID != "dev-bob" {
		t.Errorf("重开后 Verify = %q, %v；want dev-bob, nil", userID, err)
	}
}

// 吊销即时生效（ADR-0004）：revoke 后同一明文立即校验失败；重复吊销幂等。
func TestRevokeImmediateAndIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	plain, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keys, err := List(ctx, s.DB())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0].UserID != "dev-alice" || keys[0].RevokedAt != "" {
		t.Fatalf("快照 = %+v, want 一条未吊销的 dev-alice", keys)
	}

	revoked, err := Revoke(ctx, s.DB(), keys[0].ID)
	if err != nil || !revoked {
		t.Fatalf("首次吊销 = %v, %v；want true, nil", revoked, err)
	}
	if _, err := Verify(ctx, s.DB(), plain); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("吊销后 Verify = %v, want ErrInvalidKey", err)
	}

	// 幂等：重复吊销成功但不报错（自动化脚本可安全重跑）。
	again, err := Revoke(ctx, s.DB(), keys[0].ID)
	if err != nil || again {
		t.Errorf("重复吊销 = %v, %v；want false, nil", again, err)
	}

	keys, err = List(ctx, s.DB())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if keys[0].RevokedAt == "" {
		t.Error("快照应显示吊销时间")
	}
}

// 吊销后重开库仍失效（revoked_at 落盘，不是进程内状态）。
func TestRevokePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dgw.db")
	ctx := context.Background()

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plain, err := Create(ctx, s1.DB(), "dev-bob")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keys, err := List(ctx, s1.DB())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := Revoke(ctx, s1.DB(), keys[0].ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	s1.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if _, err := Verify(ctx, s2.DB(), plain); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("重开后 Verify = %v, want ErrInvalidKey", err)
	}
}

// 快照视图不含明文：哈希可读（宿主机即访问边界），明文无处可现。
func TestListNeverExposesPlaintext(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	plain, err := Create(ctx, s.DB(), "dev-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keys, err := List(ctx, s.DB())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k.UserID+fmt.Sprint(k.ID)+k.CreatedAt+k.RevokedAt, plain) {
			t.Fatalf("快照泄露明文 %q: %+v", plain, k)
		}
	}
}
