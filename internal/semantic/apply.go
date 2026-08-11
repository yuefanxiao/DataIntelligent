package semantic

import (
	"context"
	"database/sql"
	"fmt"
)

// Apply 把目标状态（编译产物）应用进运行时：幂等 upsert + 墓碑软删除。
//
// 幂等语义（ADR-0002「幂等 upsert」）：
//   - 实体/边/枚举按唯一键 upsert（ON CONFLICT DO UPDATE，无重复行）；
//   - 目标里消失的实体 → tombstone=1（软删除，检索默认过滤、历史可追溯）；
//   - 墓碑传播：实体墓碑化时，其关系边与枚举值一并墓碑化（孤儿清理）。
//
// 全部变更在单事务内完成：失败原子回滚（编译校验已在 Sync 里先行，此处
// 只做落库，失败即库级异常，不留半态）。
//
// 返回值 = 实际应用的行数（变更过的 upsert + 墓碑），供 CLI 摘要。
type ApplyStats struct {
	EntitiesUpserted    int
	EntitiesTombstoned  int
	RelationsUpserted   int
	RelationsTombstoned int
	EnumsUpserted       int
	EnumsTombstoned     int
}

// Apply 执行应用；cur 是应用前的快照（Sync 已取，避免重复查询）。
func Apply(ctx context.Context, st interface{ DB() *sql.DB }, target *Target, cur *Target) (*ApplyStats, error) {
	SortTarget(target)

	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stats, err := applyInTx(ctx, tx, target, cur)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return stats, nil
}

func applyInTx(ctx context.Context, tx *sql.Tx, target *Target, cur *Target) (*ApplyStats, error) {
	s := &ApplyStats{}
	curEnt := byEntityFQN(cur.Entities)
	curRel := byRelationKey(cur.Relations)
	curEnum := byEnumKey(cur.Enums)

	// 1) 实体 upsert（含墓碑复活：target 里存在 = 恢复）。
	for _, e := range target.Entities {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dgw_sem_entities
				(fqn, kind, name, description, data_type, is_time, pg_schema,
				 expression, aggregation, filter, tombstone)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT(fqn) DO UPDATE SET
				kind = excluded.kind, name = excluded.name,
				description = excluded.description, data_type = excluded.data_type,
				is_time = excluded.is_time, pg_schema = excluded.pg_schema,
				expression = excluded.expression, aggregation = excluded.aggregation,
				filter = excluded.filter, tombstone = 0,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			e.FQN, string(e.Kind), e.Name, e.Description, e.DataType,
			boolToInt(e.IsTime), e.PGSchema, e.Expression, e.Aggregation, e.Filter)
		if err != nil {
			return nil, fmt.Errorf("upsert entity %s: %w", e.FQN, err)
		}
		s.EntitiesUpserted++
	}

	// 2) 实体墓碑：目标里消失的（含当前墓碑但不在目标 = 保持墓碑）。
	for _, e := range cur.Entities {
		if _, ok := curEnt[e.FQN]; !ok {
			continue // 防御：cur 自身键必然存在
		}
		if hasEntity(target.Entities, e.FQN) {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_entities SET tombstone = 1,
			    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE fqn = ?`,
			e.FQN); err != nil {
			return nil, fmt.Errorf("tombstone entity %s: %w", e.FQN, err)
		}
		s.EntitiesTombstoned++
	}

	// 3) 关系边 upsert。
	for _, r := range target.Relations {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dgw_sem_relations (type, src_fqn, dst_fqn, meta, tombstone)
			VALUES (?, ?, ?, ?, 0)
			ON CONFLICT(type, src_fqn, dst_fqn) DO UPDATE SET
				meta = excluded.meta, tombstone = 0,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			string(r.Type), r.SrcFQN, r.DstFQN, r.Meta)
		if err != nil {
			return nil, fmt.Errorf("upsert relation %s %s→%s: %w", r.Type, r.SrcFQN, r.DstFQN, err)
		}
		s.RelationsUpserted++
	}

	// 4) 边墓碑：目标里消失的边；同时墓碑化「边指向的实体已墓碑」的残留边。
	for _, r := range cur.Relations {
		if _, ok := curRel[relationKey(r)]; !ok {
			continue
		}
		if _, ok := byRelationKey(target.Relations)[relationKey(r)]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_relations SET tombstone = 1 WHERE type = ? AND src_fqn = ? AND dst_fqn = ?`,
			string(r.Type), r.SrcFQN, r.DstFQN); err != nil {
			return nil, fmt.Errorf("tombstone relation %s %s→%s: %w", r.Type, r.SrcFQN, r.DstFQN, err)
		}
		s.RelationsTombstoned++
	}

	// 5) 枚举 upsert。
	for _, v := range target.Enums {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dgw_sem_enum_values (column_fqn, value, label, tombstone)
			VALUES (?, ?, ?, 0)
			ON CONFLICT(column_fqn, value) DO UPDATE SET
				label = excluded.label, tombstone = 0,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			v.ColumnFQN, v.Value, v.Label)
		if err != nil {
			return nil, fmt.Errorf("upsert enum %s=%s: %w", v.ColumnFQN, v.Value, err)
		}
		s.EnumsUpserted++
	}

	// 6) 枚举墓碑：目标里消失的枚举。
	for _, v := range cur.Enums {
		if _, ok := curEnum[enumKey(v)]; !ok {
			continue
		}
		if _, ok := byEnumKey(target.Enums)[enumKey(v)]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_enum_values SET tombstone = 1 WHERE column_fqn = ? AND value = ?`,
			v.ColumnFQN, v.Value); err != nil {
			return nil, fmt.Errorf("tombstone enum %s=%s: %w", v.ColumnFQN, v.Value, err)
		}
		s.EnumsTombstoned++
	}

	return s, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
