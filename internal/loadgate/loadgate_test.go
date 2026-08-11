package loadgate

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

func TestNewValidation(t *testing.T) {
	// 非法上限（<1 / total < perKey）= 构造错误，配置 fail fast。
	for name, args := range map[string]struct{ perKey, total int }{
		"perKey 零":       {0, 8},
		"perKey 负":       {-1, 8},
		"total 零":        {2, 0},
		"total 负":        {2, -3},
		"total < perKey": {5, 3},
	} {
		if _, err := New(args.perKey, args.total); err == nil {
			t.Errorf("%s: New(%d, %d) 应报错", name, args.perKey, args.total)
		}
	}
	// 合法组合（含 total == perKey 的边界）。
	for _, args := range []struct{ perKey, total int }{{1, 1}, {2, 8}, {8, 8}} {
		if _, err := New(args.perKey, args.total); err != nil {
			t.Errorf("New(%d, %d) 应合法: %v", args.perKey, args.total, err)
		}
	}
}

// 同 key 并发 >2 → 结构化拒绝（不排队）；不影响其他 key。
func TestPerKeyLimit(t *testing.T) {
	g, err := New(2, 8)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e := g.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("第 1 次占用应成功: %v", e)
	}
	if e := g.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("第 2 次占用应成功: %v", e)
	}
	e := g.TryAcquire("dev-alice")
	if e == nil {
		t.Fatal("第 3 次占用应被拒绝")
	}
	if e.Kind != gwerr.KindRateLimited || e.Details["reason"] != ReasonKeyConcurrency {
		t.Fatalf("拒绝 = %s reason=%v，期望 rate_limited/%s", e.Kind, e.Details["reason"], ReasonKeyConcurrency)
	}
	if e.Details["limit"] != 2 || e.Details["current"] != 2 {
		t.Errorf("details = %v，期望 limit=2 current=2", e.Details)
	}
	if e.Details["key"] != "dev-alice" {
		t.Errorf("details.key = %v，期望 dev-alice", e.Details["key"])
	}

	// 其他 key 不受影响（超限不阻塞别的 key）
	if e := g.TryAcquire("dev-bob"); e != nil {
		t.Fatalf("其他 key 占用应成功（key 隔离）: %v", e)
	}
}

// 进程级并发 >8 → 结构化拒绝（跨 key 共享一个总闸）。
func TestProcessLimit(t *testing.T) {
	g, err := New(2, 2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e := g.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("占用 alice 应成功: %v", e)
	}
	if e := g.TryAcquire("dev-bob"); e != nil {
		t.Fatalf("占用 bob 应成功: %v", e)
	}
	// 进程级满（2/2），carol 的每 key 配额未满（0/2）→ 进程级闸拒绝。
	e := g.TryAcquire("dev-carol")
	if e == nil {
		t.Fatal("第 3 个并发（跨 key）应被进程级闸拒绝")
	}
	if e.Kind != gwerr.KindRateLimited || e.Details["reason"] != ReasonProcessConcurrency {
		t.Fatalf("拒绝 = %s reason=%v，期望 rate_limited/%s", e.Kind, e.Details["reason"], ReasonProcessConcurrency)
	}
	if e.Details["limit"] != 2 || e.Details["current"] != 2 {
		t.Errorf("details = %v，期望 limit=2 current=2", e.Details)
	}

	// 释放后恢复可用；carol 自身 key 配额未满，进程级放行即成功。
	g.Release("dev-alice")
	if e := g.TryAcquire("dev-carol"); e != nil {
		t.Fatalf("释放后占用应成功: %v", e)
	}
}

// Release 恢复容量：释放后同 key 可再次占用；计数归零后 key 从表里清理。
func TestReleaseRestores(t *testing.T) {
	g, err := New(1, 8)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e := g.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("占用应成功: %v", e)
	}
	if e := g.TryAcquire("dev-alice"); e == nil {
		t.Fatal("满位时应拒绝")
	}
	g.Release("dev-alice")
	if e := g.TryAcquire("dev-alice"); e != nil {
		t.Fatalf("释放后应可再占用: %v", e)
	}
	g.Release("dev-alice")
	if _, ok := g.keyCur["dev-alice"]; ok {
		t.Error("计数归零后 key 应从表里清理（防泄漏）")
	}
	// 幂等释放：未占用 key 也调 Release 不 panic 不破坏计数。
	g.Release("dev-alice")
	g.Release("dev-nonexistent")
	if g.totalCur != 0 {
		t.Errorf("totalCur = %d，期望 0", g.totalCur)
	}
}

// 并发压力：多 goroutine 随机占用/释放，占用数永不超过双闸上限
// （race detector 同时验证锁的正确性；本测试用 -race 跑）。
func TestConcurrentStress(t *testing.T) {
	const (
		perKey   = 2
		total    = 8
		workers  = 24
		perRound = 60
	)
	g, err := New(perKey, total)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	keys := make([]string, 6)
	for i := range keys {
		keys[i] = fmt.Sprintf("dev-u%d", i)
	}

	// 外部计数跟踪成功占用的在途峰值（不读闸内部状态，仅验证行为）。
	// 计数时点：++ 紧跟 acquire，-- 在 Release 之前——保证外部计数 ≤
	// 闸真实占用（测量不虚高），峰值超限断言才可靠。
	var mu sync.Mutex
	var totalInFlight, maxTotal, maxKey int
	keyInFlight := make(map[string]int, len(keys))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			key := keys[w%len(keys)]
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w))) // 每 goroutine 独立 rng（*rand.Rand 非并发安全）
			for i := 0; i < perRound; i++ {
				if e := g.TryAcquire(key); e == nil {
					mu.Lock()
					totalInFlight++
					if totalInFlight > maxTotal {
						maxTotal = totalInFlight
					}
					keyInFlight[key]++
					if keyInFlight[key] > maxKey {
						maxKey = keyInFlight[key]
					}
					mu.Unlock()

					time.Sleep(time.Duration(rng.Intn(50)) * time.Microsecond)
					mu.Lock()
					totalInFlight--
					keyInFlight[key]--
					mu.Unlock()
					g.Release(key)
				} else if e.Kind != gwerr.KindRateLimited {
					t.Errorf("拒绝 kind = %s，期望 rate_limited", e.Kind)
				}
			}
		}(w)
	}
	wg.Wait()

	if maxTotal > total {
		t.Errorf("进程级峰值并发 %d > 上限 %d", maxTotal, total)
	}
	if maxKey > perKey {
		t.Errorf("每 key 峰值并发 %d > 上限 %d", maxKey, perKey)
	}
	// Wait 之后无并发写，读闸状态验证无泄漏。
	if g.totalCur != 0 {
		t.Errorf("全部释放后 totalCur = %d，期望 0（防泄漏）", g.totalCur)
	}
	if len(g.keyCur) != 0 {
		t.Errorf("全部释放后 keyCur = %v，期望空（防泄漏）", g.keyCur)
	}
}
