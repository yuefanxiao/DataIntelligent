package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GrantExpander 实现 grants.Expander（授权对象 → 底层表清单），供
// grants-apply 注入（07 票落地后接线）。语义域是授权展开的权威：指标/概念
// 的依赖表来自 describes 边，服务/库级通配来自实体表清单——「指标有权底层
// 没权」的悬空在此杜绝（展开为空 → grants.Sync 编译拒绝）。
type GrantExpander struct {
	st interface{ DB() *sql.DB }
}

// NewGrantExpander 构造授权展开器（grants-apply 注入点）。
func NewGrantExpander(st interface{ DB() *sql.DB }) *GrantExpander {
	return &GrantExpander{st: st}
}

// Expand 展开授权对象：
//
//	metric:xxx    → MetricTables（describes 边，指标依赖表）
//	concept:xxx   → ConceptTables（描述的表/列归表、指标递归展开）
//	service:xxx   → TablesForService（服务级通配快照）
//	database:x.y  → TablesForDatabase（库级通配快照）
//
// 对象不存在 / 语义库未同步 → 空清单或错误（grants.Sync 编译拒绝）。
func (g *GrantExpander) Expand(ctx context.Context, target string) ([]string, error) {
	switch {
	case strings.HasPrefix(target, "metric:"):
		name := strings.TrimPrefix(target, "metric:")
		e, err := GetEntity(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if e == nil || e.Kind != KindMetric {
			return nil, fmt.Errorf("指标 %q 不存在（语义库未同步或 FQN 错误）", name)
		}
		tbls, err := MetricTables(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if len(tbls) == 0 {
			return nil, fmt.Errorf("指标 %q 未声明依赖表（YAML metrics.tables 为空——悬空授权，拒绝）", name)
		}
		return tbls, nil
	case strings.HasPrefix(target, "concept:"):
		name := strings.TrimPrefix(target, "concept:")
		e, err := GetEntity(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if e == nil || e.Kind != KindConcept {
			return nil, fmt.Errorf("概念 %q 不存在（语义库未同步或 FQN 错误）", name)
		}
		tbls, err := ConceptTables(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if len(tbls) == 0 {
			return nil, fmt.Errorf("概念 %q 未描述任何表（悬空授权，拒绝）", name)
		}
		return tbls, nil
	case strings.HasPrefix(target, "service:"):
		name := strings.TrimPrefix(target, "service:")
		tbls, err := TablesForService(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if len(tbls) == 0 {
			return nil, fmt.Errorf("服务 %q 无表（语义库未同步或服务不存在——通配展开为空，拒绝）", name)
		}
		return tbls, nil
	case strings.HasPrefix(target, "database:"):
		name := strings.TrimPrefix(target, "database:")
		tbls, err := TablesForDatabase(ctx, g.st, name)
		if err != nil {
			return nil, err
		}
		if len(tbls) == 0 {
			return nil, fmt.Errorf("库 %q 无表（语义库未同步或库不存在——通配展开为空，拒绝）", name)
		}
		return tbls, nil
	default:
		// 表 FQN：grants.Sync 直接落库，不经过展开器。
		return nil, fmt.Errorf("授权对象 %q 不是展开形态（表 FQN 直接落库）", target)
	}
}
