package grants

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// expandResult 是单个授权对象的展开产出。
type expandResult struct {
	tables  []string // 展开出的具体表 FQN（排序、去重）
	pattern string   // 通配声明（service:/database: 形态；非通配为空）
}

// expandTarget 把授权对象展开为具体表清单：
//
//	表 FQN（服务.库.表）        → 原样单表
//	metric:xxx / concept:xxx   → Expander.Expand（底层表清单）
//	service:xxx / database:xxx → Expander.Expand（表清单快照）+ pattern 声明
//
// expand == nil：只接受表 FQN（grants-apply 未接语义层时的保守形态）。
func expandTarget(ctx context.Context, st *store.Store, expand Expander, user, target string) (expandResult, error) {
	// 通配与指标/概念形态都走 Expander；表 FQN 直接落库。
	if !strings.Contains(target, ":") {
		if err := ValidateFQN(target); err != nil {
			return expandResult{}, err
		}
		return expandResult{tables: []string{target}}, nil
	}
	if expand == nil {
		return expandResult{}, fmt.Errorf("授权对象 %q 需要语义层展开（grants-apply 未注入 Expander，07 同步管线落地后可用）", target)
	}
	tables, err := expand.Expand(ctx, target)
	if err != nil {
		return expandResult{}, fmt.Errorf("展开授权对象 %q（用户 %s）: %w", target, user, err)
	}
	if len(tables) == 0 {
		return expandResult{}, fmt.Errorf("展开授权对象 %q（用户 %s）为空清单——指标/概念/通配必须有底层表（杜绝悬空授权）", target, user)
	}
	res := expandResult{tables: tables}
	if strings.HasPrefix(target, PrefixService) || strings.HasPrefix(target, PrefixDatabase) {
		res.pattern = target // 通配声明入 pattern 表（同步管线告警用）
	}
	return res, nil
}

// expandAll 展开整个 grants 文件，产出目标表授权集合与通配声明集合。
// 任一对象展开失败 = 整体失败（原子拒绝：编译期拒绝，不写库）。
func expandAll(ctx context.Context, st *store.Store, expand Expander, f File) (map[string]struct{}, map[string]struct{}, error) {
	targets := map[string]struct{}{}  // user\x00tableFQN
	patterns := map[string]struct{}{} // user\x00pattern
	for _, g := range f.Grants {
		for _, t := range g.Tables {
			res, err := expandTarget(ctx, st, expand, g.User, t)
			if err != nil {
				return nil, nil, err
			}
			if res.pattern != "" {
				patterns[g.User+"\x00"+res.pattern] = struct{}{}
			}
			for _, tbl := range res.tables {
				targets[g.User+"\x00"+tbl] = struct{}{}
			}
		}
	}
	return targets, patterns, nil
}

// SyncPatterns 返回全部通配声明（user × pattern，排序），供同步管线告警
// 「新表未覆盖」（07 票语义同步应用后调用）。
func SyncPatterns(ctx context.Context, st *store.Store) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx,
		`SELECT user_id, pattern FROM dgw_grant_patterns ORDER BY user_id, pattern`)
	if err != nil {
		return nil, fmt.Errorf("list grant patterns: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u, p string
		if err := rows.Scan(&u, &p); err != nil {
			return nil, fmt.Errorf("scan pattern: %w", err)
		}
		out = append(out, u+"\x00"+p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// expandKey 是展开结果的稳定排序键。
func expandKey(user, fqn string) string { return user + "\x00" + fqn }

// sortTargets 排序展开目标（确定性输出，测试断言稳定）。
func sortTargets(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
