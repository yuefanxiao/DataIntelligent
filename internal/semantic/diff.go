package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Snapshot 读取运行时（SQLite）当前语义状态，供 diff 对比。
// 只查 dgw_sem_* 表，不读 YAML（ADR-0002「运行时只查运行时存储」）。
func Snapshot(ctx context.Context, st interface {
	DB() *sql.DB
}) (*Target, error) {
	s := &snapshotter{st: st}
	return s.snapshot(ctx)
}

type snapshotter struct {
	st interface{ DB() *sql.DB }
}

func (s *snapshotter) snapshot(ctx context.Context) (*Target, error) {
	ents, err := s.entities(ctx)
	if err != nil {
		return nil, err
	}
	rels, err := s.relations(ctx)
	if err != nil {
		return nil, err
	}
	enums, err := s.enums(ctx)
	if err != nil {
		return nil, err
	}
	return &Target{Entities: ents, Relations: rels, Enums: enums}, nil
}

func (s *snapshotter) entities(ctx context.Context) ([]Entity, error) {
	rows, err := s.st.DB().QueryContext(ctx, `
		SELECT fqn, kind, name, description, COALESCE(data_type,''),
		       is_time, COALESCE(pg_schema,''),
		       COALESCE(expression,''), COALESCE(aggregation,''), COALESCE(filter,''),
		       tombstone
		FROM dgw_sem_entities ORDER BY fqn`)
	if err != nil {
		return nil, fmt.Errorf("snapshot entities: %w", err)
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		var kind string
		var isTime, tombstone int
		if err := rows.Scan(&e.FQN, &kind, &e.Name, &e.Description, &e.DataType,
			&isTime, &e.PGSchema, &e.Expression, &e.Aggregation, &e.Filter, &tombstone); err != nil {
			return nil, fmt.Errorf("scan entity: %w", err)
		}
		e.Kind = Kind(kind)
		e.IsTime = isTime != 0
		if tombstone == 0 {
			out = append(out, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *snapshotter) relations(ctx context.Context) ([]Relation, error) {
	rows, err := s.st.DB().QueryContext(ctx, `
		SELECT type, src_fqn, dst_fqn, meta FROM dgw_sem_relations
		WHERE tombstone = 0 ORDER BY type, src_fqn, dst_fqn`)
	if err != nil {
		return nil, fmt.Errorf("snapshot relations: %w", err)
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var r Relation
		var typ string
		if err := rows.Scan(&typ, &r.SrcFQN, &r.DstFQN, &r.Meta); err != nil {
			return nil, fmt.Errorf("scan relation: %w", err)
		}
		r.Type = RelationType(typ)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *snapshotter) enums(ctx context.Context) ([]EnumValue, error) {
	rows, err := s.st.DB().QueryContext(ctx, `
		SELECT column_fqn, value, label FROM dgw_sem_enum_values
		WHERE tombstone = 0 ORDER BY column_fqn, value`)
	if err != nil {
		return nil, fmt.Errorf("snapshot enum values: %w", err)
	}
	defer rows.Close()
	var out []EnumValue
	for rows.Next() {
		var v EnumValue
		if err := rows.Scan(&v.ColumnFQN, &v.Value, &v.Label); err != nil {
			return nil, fmt.Errorf("scan enum: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Diff 是 dry-run 的增删改清单：相对当前运行时状态的净变化。
// 幂等重跑（同输入）时全部为空（同输出，§5.3 seam）。
type Diff struct {
	EntitiesAdded   []Entity
	EntitiesUpdated []Entity // FQN 相同但属性变化
	EntitiesDeleted []Entity // 目标里消失 → 墓碑

	RelationsAdded   []Relation
	RelationsUpdated []Relation // 键相同但 meta（references 的 join 条件）变化
	RelationsDeleted []Relation

	EnumsAdded   []EnumValue
	EnumsDeleted []EnumValue
}

// Empty 报告 diff 是否零变化（幂等 = 重跑空 diff）。
func (d *Diff) Empty() bool {
	return len(d.EntitiesAdded) == 0 && len(d.EntitiesUpdated) == 0 &&
		len(d.EntitiesDeleted) == 0 && len(d.RelationsAdded) == 0 &&
		len(d.RelationsUpdated) == 0 && len(d.RelationsDeleted) == 0 &&
		len(d.EnumsAdded) == 0 && len(d.EnumsDeleted) == 0
}

// Count 返回变更条目总数（CLI 摘要输出用）。
func (d *Diff) Count() int {
	return len(d.EntitiesAdded) + len(d.EntitiesUpdated) + len(d.EntitiesDeleted) +
		len(d.RelationsAdded) + len(d.RelationsUpdated) + len(d.RelationsDeleted) +
		len(d.EnumsAdded) + len(d.EnumsDeleted)
}

// Compare 计算目标（编译产物）相对当前快照的 diff。两输入都按 FQN/键排序
// 后比对，输出顺序确定（同输入同输出）。
func Compare(target *Target, cur *Target) *Diff {
	d := &Diff{}
	curEnt := byEntityFQN(cur.Entities)
	for _, e := range target.Entities {
		if prev, ok := curEnt[e.FQN]; ok {
			if entityChanged(prev, e) {
				d.EntitiesUpdated = append(d.EntitiesUpdated, e)
			}
		} else {
			d.EntitiesAdded = append(d.EntitiesAdded, e)
		}
	}
	for _, e := range cur.Entities {
		if !hasEntity(target.Entities, e.FQN) {
			d.EntitiesDeleted = append(d.EntitiesDeleted, e)
		}
	}

	curRel := byRelationKey(cur.Relations)
	targetRel := byRelationKey(target.Relations)
	for _, r := range target.Relations {
		if prev, ok := curRel[relationKey(r)]; ok {
			if prev.Meta != r.Meta {
				d.RelationsUpdated = append(d.RelationsUpdated, r)
			}
		} else {
			d.RelationsAdded = append(d.RelationsAdded, r)
		}
	}
	for _, r := range cur.Relations {
		if _, ok := targetRel[relationKey(r)]; !ok {
			d.RelationsDeleted = append(d.RelationsDeleted, r)
		}
	}

	curEnum := byEnumKey(cur.Enums)
	targetEnum := byEnumKey(target.Enums)
	for _, v := range target.Enums {
		if prev, ok := curEnum[enumKey(v)]; !ok || prev.Label != v.Label {
			d.EnumsAdded = append(d.EnumsAdded, v) // 新增或 label 变化都按新增（upsert 幂等）
		}
	}
	for _, v := range cur.Enums {
		if _, ok := targetEnum[enumKey(v)]; !ok {
			d.EnumsDeleted = append(d.EnumsDeleted, v)
		}
	}
	return d
}

// entityChanged 比较实体属性（FQN 是键；kind 变化 = 属性变化——同一 FQN
// 换类型必须进 diff，否则 apply 会改写 kind 而 dry-run 无感）。
func entityChanged(a, b Entity) bool {
	return a.Kind != b.Kind || a.Name != b.Name || a.Description != b.Description ||
		a.DataType != b.DataType || a.IsTime != b.IsTime ||
		a.PGSchema != b.PGSchema || a.Expression != b.Expression ||
		a.Aggregation != b.Aggregation || a.Filter != b.Filter
}

func hasEntity(ents []Entity, fqn string) bool {
	for _, e := range ents {
		if e.FQN == fqn {
			return true
		}
	}
	return false
}

func byEntityFQN(ents []Entity) map[string]Entity {
	m := make(map[string]Entity, len(ents))
	for _, e := range ents {
		m[e.FQN] = e
	}
	return m
}

func relationKey(r Relation) string { return string(r.Type) + "\x00" + r.SrcFQN + "\x00" + r.DstFQN }

func byRelationKey(rels []Relation) map[string]Relation {
	m := make(map[string]Relation, len(rels))
	for _, r := range rels {
		m[relationKey(r)] = r
	}
	return m
}

func enumKey(v EnumValue) string { return v.ColumnFQN + "\x00" + v.Value }

func byEnumKey(vs []EnumValue) map[string]EnumValue {
	m := make(map[string]EnumValue, len(vs))
	for _, v := range vs {
		m[enumKey(v)] = v
	}
	return m
}

// SortTarget 按确定性顺序排序 Target 的三个列表（diff/apply 的输入统一排序，
// 幂等重跑同输出）。
func SortTarget(t *Target) {
	sort.Slice(t.Entities, func(i, j int) bool { return t.Entities[i].FQN < t.Entities[j].FQN })
	sort.Slice(t.Relations, func(i, j int) bool {
		return relationKey(t.Relations[i]) < relationKey(t.Relations[j])
	})
	sort.Slice(t.Enums, func(i, j int) bool { return enumKey(t.Enums[i]) < enumKey(t.Enums[j]) })
}
