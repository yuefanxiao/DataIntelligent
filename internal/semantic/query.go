package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// 查询接口：运行时只查 SQLite（ADR-0002），不读 YAML。
// 08 票（语义五工具）消费本文件；07 票的授权展开（grants 包）复用
// MetricTables / ConceptTables / TablesForService / TablesForDatabase。

// GetEntity 按 FQN 查实体（含墓碑过滤；墓碑实体返回 nil 实体 + nil 错误，
// 检索默认过滤墓碑，ADR-0002 墓碑语义）。
func GetEntity(ctx context.Context, st DBer, fqn string) (*Entity, error) {
	var e Entity
	var kind string
	var isTime, tombstone int
	err := st.DB().QueryRowContext(ctx, `
		SELECT fqn, kind, name, description, COALESCE(data_type,''), is_time,
		       COALESCE(pg_schema,''), COALESCE(expression,''),
		       COALESCE(aggregation,''), COALESCE(filter,''), tombstone
		FROM dgw_sem_entities WHERE fqn = ?`, fqn).
		Scan(&e.FQN, &kind, &e.Name, &e.Description, &e.DataType, &isTime,
			&e.PGSchema, &e.Expression, &e.Aggregation, &e.Filter, &tombstone)
	if err == sql.ErrNoRows || (err == nil && tombstone != 0) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get entity %q: %w", fqn, err)
	}
	e.Kind = Kind(kind)
	e.IsTime = isTime != 0
	return &e, nil
}

// ListTables 返回全部活跃表实体（服务.库.表），按 FQN 排序。
// 授权展开与 08 票检索共用。
func ListTables(ctx context.Context, st DBer) ([]Entity, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT fqn, name, description, COALESCE(pg_schema,'')
		FROM dgw_sem_entities WHERE kind = 'table' AND tombstone = 0
		ORDER BY fqn`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.FQN, &e.Name, &e.Description, &e.PGSchema); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		e.Kind = KindTable
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// relationTargets 返回某实体经指定类型边指向的目标 FQN 列表（去重、排序）。
func relationTargets(ctx context.Context, st DBer, typ RelationType, src string) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT dst_fqn FROM dgw_sem_relations
		WHERE type = ? AND src_fqn = ? AND tombstone = 0
		ORDER BY dst_fqn`, string(typ), src)
	if err != nil {
		return nil, fmt.Errorf("query %s edges: %w", typ, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fqn string
		if err := rows.Scan(&fqn); err != nil {
			return nil, err
		}
		out = append(out, fqn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// relationSources 返回经指定类型边指向某实体的源 FQN 列表（反向遍历，
// ADR-0001「双向可遍历」：沿 dst 索引查 src）。08 票 traverse_relations
// 的双向语义即由此支撑。
func relationSources(ctx context.Context, st DBer, typ RelationType, dst string) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT src_fqn FROM dgw_sem_relations
		WHERE type = ? AND dst_fqn = ? AND tombstone = 0
		ORDER BY src_fqn`, string(typ), dst)
	if err != nil {
		return nil, fmt.Errorf("query %s reverse edges: %w", typ, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fqn string
		if err := rows.Scan(&fqn); err != nil {
			return nil, err
		}
		out = append(out, fqn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MetricTables 返回指标依赖的底层表（describes 边 dst，指标授权展开依据）。
func MetricTables(ctx context.Context, st DBer, metricFQN string) ([]string, error) {
	return relationTargets(ctx, st, RelDescribes, metricFQN)
}

// ConceptTables 返回概念授权的展开表清单：概念 describes 的表直接计入，
// 列归到所属表，指标递归展开为其依赖表。空 = 概念不触任何表（授权展开为
// 空清单，按编译拒绝处理——「指标有权底层没权」的悬空同样适用于概念）。
func ConceptTables(ctx context.Context, st DBer, conceptFQN string) ([]string, error) {
	targets, err := relationTargets(ctx, st, RelDescribes, conceptFQN)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, t := range targets {
		e, err := GetEntity(ctx, st, t)
		if err != nil {
			return nil, err
		}
		if e == nil {
			continue // 墓碑目标跳过（边可能残留，检索过滤）
		}
		switch e.Kind {
		case KindTable:
			set[t] = true
		case KindColumn:
			set[tableOfColumn(t)] = true
		case KindMetric:
			tbls, err := MetricTables(ctx, st, t)
			if err != nil {
				return nil, err
			}
			for _, tb := range tbls {
				set[tb] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// tableOfColumn 从列 FQN（服务.库.表.列）取所属表 FQN（服务.库.表）。
func tableOfColumn(colFQN string) string {
	parts := strings.Split(colFQN, ".")
	if len(parts) != SegColumn {
		return colFQN // 非 4 段列 FQN：原样返回（调用方兜底拒绝）
	}
	return strings.Join(parts[:SegTable], ".")
}

// TablesForService 返回服务下全部活跃表（服务级通配展开）。
func TablesForService(ctx context.Context, st DBer, service string) ([]string, error) {
	prefix := service + "."
	return tablesByPrefix(ctx, st, prefix)
}

// TablesForDatabase 返回库下全部活跃表（库级通配展开）。
func TablesForDatabase(ctx context.Context, st DBer, database string) ([]string, error) {
	return tablesByPrefix(ctx, st, database+".")
}

// tablesByPrefix 精确前缀匹配：LIKE 的 %/_ 会被当通配符（服务名/库名含
// 下划线时授权展开会静默越界到变体服务——review 修复），改用 substr 精确
// 比较，前缀中的 %/_ 一律字面处理。
func tablesByPrefix(ctx context.Context, st DBer, prefix string) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT fqn FROM dgw_sem_entities
		WHERE kind = 'table' AND tombstone = 0
		  AND substr(fqn, 1, length(?)) = ? COLLATE BINARY
		ORDER BY fqn`, prefix, prefix)
	if err != nil {
		return nil, fmt.Errorf("tables by prefix %q: %w", prefix, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fqn string
		if err := rows.Scan(&fqn); err != nil {
			return nil, err
		}
		out = append(out, fqn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
