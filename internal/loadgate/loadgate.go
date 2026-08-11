// Package loadgate 实现网关的负载防护并发闸（05 票，spec §4.9 参数表 /
// §6.3 负向例 4、ADR-0010）：每 key 并发 + 进程级总并发双信号量。
//
// 语义：
//   - 不排队：占用采用 try 语义，任一闸满 → 结构化拒绝（gwerr rate_limited，
//     §6.3 负向例 4「快速失败」），网关不重试、无自愈循环；
//   - 守护进程语义：进程级闸在网关进程内全 key 共享（HTTP 守护形态）；
//     stdio 调试形态每进程一个 Gateway/Gate 实例，自然退化为每进程闸
//     （spec §4.9「stdio 调试形态下退化为每进程闸」）；
//   - 每 key 闸以凭据 key 为粒度（ADR-0004 key→用户扁平映射）：同一把 key
//     的并发查询共享每 key 配额，一用户多 key 各占配额（§6.3「不影响
//     其他 key」）；stdio 形态单 key 单进程，每 key 闸即每进程闸。
package loadgate

import (
	"fmt"
	"sync"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 结构化拒绝的 details.reason 稳定值（调用方按 reason 区分是哪个闸超限）。
const (
	ReasonKeyConcurrency     = "key_concurrency_limit"
	ReasonProcessConcurrency = "process_concurrency_limit"
)

// Gate 是双信号量并发闸：perKey = 每 key 并发上限，total = 进程级总并发
// 上限。计数在互斥锁下维护（调用频率低——每 SQL 一次 acquire/release，
// 锁开销可忽略；try 语义保证无等待）。
type Gate struct {
	perKey int
	total  int

	mu       sync.Mutex
	totalCur int
	keyCur   map[string]int
}

// New 构造并发闸。perKey/total 必须 ≥1 且 total ≥ perKey（否则单个 key
// 永远无法达到自身上限，进程级闸先触发，配置无意义）——非法 = 构造错误，
// 调用方按配置错误 fail fast（spec 参数表默认 2/8，env 可覆盖）。
func New(perKey, total int) (*Gate, error) {
	if perKey < 1 {
		return nil, fmt.Errorf("每 key 并发上限 %d < 1", perKey)
	}
	if total < 1 {
		return nil, fmt.Errorf("进程级并发上限 %d < 1", total)
	}
	if total < perKey {
		return nil, fmt.Errorf("进程级并发上限 %d < 每 key 上限 %d（单 key 无法达到自身配额）", total, perKey)
	}
	return &Gate{perKey: perKey, total: total, keyCur: make(map[string]int)}, nil
}

// TryAcquire 尝试为 key 占用一个并发位（不阻塞、不排队）：
//   - 每 key 闸满 → rate_limited/key_concurrency_limit；
//   - 进程级闸满 → rate_limited/process_concurrency_limit。
//
// 成功返回 nil，调用方必须配对 Release（defer）。
func (g *Gate) TryAcquire(key string) *gwerr.Error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if kc := g.keyCur[key]; kc >= g.perKey {
		return gwerr.RateLimited(
			fmt.Sprintf("并发超限：key %q 同时查询数已达上限 %d（不排队，请稍后重试）", key, g.perKey),
			map[string]any{"reason": ReasonKeyConcurrency, "key": key, "limit": g.perKey, "current": kc})
	}
	if g.totalCur >= g.total {
		return gwerr.RateLimited(
			fmt.Sprintf("并发超限：网关同时查询数已达进程级上限 %d（不排队，请稍后重试）", g.total),
			map[string]any{"reason": ReasonProcessConcurrency, "limit": g.total, "current": g.totalCur})
	}
	g.keyCur[key]++
	g.totalCur++
	return nil
}

// Release 释放 key 的一个并发位（与 TryAcquire 配对，defer 调用）。
// 幂等：key 未占用时调用则静默（防御，不动任何计数——进程级计数与
// 各 key 计数之和的恒等式必须保持，否则闸会欠计数）。
func (g *Gate) Release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.keyCur[key] <= 0 {
		return
	}
	g.keyCur[key]--
	if g.keyCur[key] == 0 {
		delete(g.keyCur, key)
	}
	g.totalCur--
}

// Limits 返回闸的数值（每 key / 进程级上限），供工具描述等观测面。
func (g *Gate) Limits() (perKey, total int) {
	return g.perKey, g.total
}
