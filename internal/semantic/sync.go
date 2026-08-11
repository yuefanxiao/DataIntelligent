package semantic

import (
	"context"
)

// Result 是一次同步管线（或 dry-run）的完整产出。
type Result struct {
	Diff    *Diff       // dry-run 的增删改清单（应用后重跑 = 空）
	Stats   *ApplyStats // 应用统计（dry-run 为 nil）
	Applied bool        // 是否已应用（false = 只算 diff，未写库）
}

// Sync 是同步管线主入口（ADR-0002）：编译校验 → dry-run diff → 应用。
//
// 失败语义：
//   - 作者入口缺失/解析失败/编译校验失败 → 错误返回，**零写库**（原子拒绝）；
//   - 应用阶段失败 → 事务回滚，零写库。
//
// 幂等：同输入重跑 → diff 为空、库无变化。
func Sync(ctx context.Context, st DBer, dir string) (*Result, error) {
	return run(ctx, st, dir, true)
}

// DryRun 只做 编译 + diff，不写库（§5.3 seam：dry-run 确定性测试对象）。
func DryRun(ctx context.Context, st DBer, dir string) (*Result, error) {
	return run(ctx, st, dir, false)
}

func run(ctx context.Context, st DBer, dir string, apply bool) (*Result, error) {
	in, err := Load(dir)
	if err != nil {
		return nil, err
	}
	target, err := Compile(in)
	if err != nil {
		return nil, err
	}
	cur, err := Snapshot(ctx, st)
	if err != nil {
		return nil, err
	}
	res := &Result{Diff: Compare(target, cur), Applied: apply}
	if !apply {
		return res, nil
	}
	stats, err := Apply(ctx, st, target, cur)
	if err != nil {
		return nil, err
	}
	res.Stats = stats
	return res, nil
}
