package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
)

// 分阶段耗时的上下文累积器（spec §4.6：认证→权限→解析→执行→返回，
// 键见 execrecord.Stage*——执行记录字段契约的一部分）。
//
// logged 包装器创建累积器并注入上下文，execute_sql 在链的相应位置打点
// （权限 = allow 回调耗时；解析 = Parse+Check 总耗时减权限；执行 = 查询+
// 编码）；认证耗时由 HTTP 层 verifyToken 实测并经 TokenInfo.Extra 注入
// （stdio 形态无 per-call 认证，该阶段缺失——如实不伪造）。返回耗时由
// 包装器在记录组装/落盘处打点。

type stageTimerKey struct{}

// stageTimer 是分阶段耗时的并发安全累积器（一次调用一个实例；MCP 调用
// 频率低，互斥锁开销可忽略）。
type stageTimer struct {
	mu     sync.Mutex
	stages map[string]time.Duration
}

func withStageTimer(ctx context.Context, t *stageTimer) context.Context {
	return context.WithValue(ctx, stageTimerKey{}, t)
}

// stageTimerFrom 取上下文里的累积器（logged 注入）；不存在时新建——防御
// 分支是真实路径：测试直接调用 handleExecuteSQL 不经过 logged（阶段打点
// 依旧工作，只是没有 wrapper 的 return/auth）。
func stageTimerFrom(ctx context.Context) *stageTimer {
	if t, ok := ctx.Value(stageTimerKey{}).(*stageTimer); ok {
		return t
	}
	return &stageTimer{stages: map[string]time.Duration{}}
}

// newStageTimer 取调用上下文的耗时累积器并注入 HTTP 形态的认证耗时：
// verifyToken 把 VerifyKey 实测耗时写进 TokenInfo.Extra["auth_ms"]（SDK
// 中间件把同一 TokenInfo 挂进调用上下文，handler 侧可读——与 key_id 同一
// 机制）。stdio 形态无 per-call 认证，该阶段不打点（如实缺失）。
func newStageTimer(ctx context.Context) *stageTimer {
	t := stageTimerFrom(ctx)
	if ti := auth.TokenInfoFromContext(ctx); ti != nil {
		if ms, ok := ti.Extra["auth_ms"].(int64); ok {
			t.Add(execrecord.StageAuth, time.Duration(ms)*time.Millisecond)
		}
	}
	return t
}

// Add 累计一个阶段的耗时（同名多次打点相加）。
func (t *stageTimer) Add(name string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stages[name] += d
}

// Get 返回某阶段的累计耗时（parse 打点 = 链总耗时 - perm 时用）。
func (t *stageTimer) Get(name string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stages[name]
}

// ms 返回毫秒快照：打过点的阶段全部进记录（毫秒取整，<1ms 记 0——如实；
// 认证在 HTTP 形态下恒打点）；未打点的阶段（stdio 无 auth、语义工具无
// perm/parse/exec）不进记录。
func (t *stageTimer) ms() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int64, len(t.stages))
	for k, v := range t.stages {
		out[k] = v.Milliseconds()
	}
	return out
}
