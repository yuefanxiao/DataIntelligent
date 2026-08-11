package authz

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "dgw.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return svc, s
}

// 默认拒绝（ADR-0004）：未加载快照、未授权表、未知用户一律 deny。
func TestAllowDefaultDeny(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()

	// 未加载 → 拒绝（fail closed）。
	if svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Error("未加载快照时 Allow 应为 false")
	}

	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		user, fqn string
		want      bool
	}{
		{"dev-alice", "bss.payment_db.t_payment", true}, // 白名单命中
		{"dev-alice", "bss.payment_db.t_order", false},  // 未授权表
		{"dev-bob", "bss.payment_db.t_payment", false},  // 未授权用户
		{"dev-alice", "svc.db.tbl", false},              // 未知表
		{"", "bss.payment_db.t_payment", false},         // 无身份
	}
	for _, tc := range cases {
		if got := svc.Allow(tc.user, tc.fqn); got != tc.want {
			t.Errorf("Allow(%q, %q) = %v, want %v", tc.user, tc.fqn, got, tc.want)
		}
	}
}

// 一用户多 key 语义在授权侧 = 多 key 解析到同一 user，授权按 user 生效
// （跨包链路：credentials 归一到 user → authz 只看 user）。
func TestGrantByUserCoversMultipleKeys(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()

	if err := grants.AddGrant(ctx, st, "dev-alice", "iam.iam_db.t_user"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 任何解析到 dev-alice 的 key 都享有同一授权集（key 是设备维度）。
	if !svc.Allow("dev-alice", "iam.iam_db.t_user") {
		t.Error("按用户授权应覆盖该用户全部 key")
	}
}

// 热重载：CLI 侧（另一进程视角）增删授权 + bump revision 后，Reload 即生效。
func TestHotReloadViaRevision(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()

	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// grant 后（revision 1）：内存还是旧快照，Reload 后放行。
	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Fatal("未热重载前不应放行新授权")
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Error("热重载后应放行新授权")
	}

	// revoke 后（revision 2）：Reload 后立即拒绝。
	if err := grants.RemoveGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Error("revoke 热重载后应立即拒绝")
	}
}

// 轮询循环：revision 变化在 interval 内被感知（网关无需重启的验收路径）。
func TestReloadLoopPicksUpChanges(t *testing.T) {
	svc, st := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	go svc.ReloadLoop(ctx, 20*time.Millisecond)

	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Allow("dev-alice", "bss.payment_db.t_payment") {
			return // 轮询已感知
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("轮询 5s 未感知 grant（revision 热重载失效）")
}

// 热重载失败 = fail closed（零未授权访问底线）：Load 报错后快照置为
// 未加载（Allow 全拒）+ revision 归 -1（下一轮轮询必重试，恢复后自愈）。
func TestLoadFailureFailsClosed(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()

	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Fatal("初始快照应放行")
	}

	// 模拟存储故障：把库关掉后 Load 必失败。
	st.Close()
	if err := svc.Load(ctx); err == nil {
		t.Fatal("库已关闭，Load 应失败")
	}
	// 快照已置未加载：连之前授权的表也一律拒绝（不保留旧快照）。
	if svc.Allow("dev-alice", "bss.payment_db.t_payment") {
		t.Error("Load 失败后应 fail closed（全拒），不得保留旧快照放行")
	}
	if rev := svc.Revision(); rev != -1 {
		t.Errorf("失败后 revision = %d, want -1（保证轮询必然重试）", rev)
	}
}

// 并发安全：Allow 读路径与 Load 写路径可并发（热重载时查询不阻塞不撕裂）。
func TestConcurrentAllowAndLoad(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()

	if err := grants.AddGrant(ctx, st, "dev-alice", "bss.payment_db.t_payment"); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// 只断言不 panic/不撕裂（-race 负责检测数据竞争）。
					_ = svc.Allow("dev-alice", "bss.payment_db.t_payment")
					_ = svc.Revision()
				}
			}
		}()
	}

	for i := 0; i < 50; i++ {
		if err := svc.Load(ctx); err != nil {
			t.Fatalf("Load: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}
