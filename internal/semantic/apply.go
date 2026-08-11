package semantic

import (
	"context"
	"database/sql"
	"fmt"
)

// Apply 把目标状态（编译产物）应用进运行时：幂等 upsert + 墓碑软删除。
//
// 幂等语义（ADR-0002「幂等 upsert」）：
//   - 应用按 diff 驱动：只有真实变化的实体/边/枚举才写库（Compare 先行）；
//     同输入重跑 → diff 全空 → 零写库、计数全 0（「同输入同输出」，§5.3 seam）；
//   - 实体/边/枚举按唯一键 upsert（ON CONFLICT DO UPDATE，无重复行）；
//   - 目标里消失的实体 → tombstone=1（软删除，检索默认过滤、历史可追溯）；
//   - 墓碑传播：实体墓碑化时，其关系边与枚举值一并墓碑化（孤儿清理）——
//     全量状态编译下，消失实体的边/枚举自然落入 diff 的删除面。
//
// 全部变更在单事务内完成：失败原子回滚（编译校验已在 Sync 里先行，此处
// 只做落库，失败即库级异常，不留半态）。
type ApplyStats struct {
	EntitiesUpserted    int
	EntitiesTombstoned  int
	RelationsUpserted   int
	RelationsTombstoned int
	EnumsUpserted       int
	EnumsTombstoned     int
}

// Apply 执行应用；cur 是应用前的快照（Sync 已取，避免重复查询）。
func Apply(ctx context.Context, st DBer, target *Target, cur *Target) (*ApplyStats, error) {
	SortTarget(target)
	d := Compare(target, cur) // diff 驱动：只写真实变化（幂等重跑 = 零写库）

	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stats, err := applyInTx(ctx, tx, d)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return stats, nil
}

func applyInTx(ctx context.Context, tx *sql.Tx, d *Diff) (*ApplyStats, error) {
	s := &ApplyStats{}

	// FTS 索引回填（08 票）：索引空而实体面非空 = 08 前的历史库升级（旧同步
	// 从未写过索引行），一次性全量回填活跃实体；此后按 diff 增量维护。空
	// 索引 + 空实体面 = 新库，无操作。与实体同事务：搜索面与实体面原子一致。
	if err := backfillFTSIfEmpty(ctx, tx); err != nil {
		return nil, err
	}

	// 1) 实体 upsert：diff 的新增 + 更新（含墓碑复活：目标里重新出现 = 恢复，
	//    墓碑实体不在快照 cur 里，故必然落在 EntitiesAdded）。
	for _, e := range append(append([]Entity{}, d.EntitiesAdded...), d.EntitiesUpdated...) {
		if err := upsertEntity(ctx, tx, e); err != nil {
			return nil, err
		}
		if err := upsertFTS(ctx, tx, e); err != nil {
			return nil, err
		}
		s.EntitiesUpserted++
	}

	// 2) 实体墓碑：diff 的删除面（目标里消失的）。
	for _, e := range d.EntitiesDeleted {
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_entities SET tombstone = 1,
			    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE fqn = ?`,
			e.FQN); err != nil {
			return nil, fmt.Errorf("tombstone entity %s: %w", e.FQN, err)
		}
		// 墓碑实体从关键词索引消失（检索默认过滤墓碑，索引删除 = 同一语义）。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM dgw_sem_fts WHERE fqn = ?`, e.FQN); err != nil {
			return nil, fmt.Errorf("drop fts row %s: %w", e.FQN, err)
		}
		s.EntitiesTombstoned++
	}

	// 3) 关系边 upsert（新增 + meta 变化）。
	for _, r := range append(append([]Relation{}, d.RelationsAdded...), d.RelationsUpdated...) {
		if err := upsertRelation(ctx, tx, r); err != nil {
			return nil, err
		}
		s.RelationsUpserted++
	}

	// 4) 边墓碑：diff 的删除面（目标里消失的边；含「实体已墓碑」的连带边）。
	for _, r := range d.RelationsDeleted {
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_relations SET tombstone = 1 WHERE type = ? AND src_fqn = ? AND dst_fqn = ?`,
			string(r.Type), r.SrcFQN, r.DstFQN); err != nil {
			return nil, fmt.Errorf("tombstone relation %s %s→%s: %w", r.Type, r.SrcFQN, r.DstFQN, err)
		}
		s.RelationsTombstoned++
	}

	// 5) 枚举 upsert（新增 + label 变化，diff 都记在 EnumsAdded）。
	for _, v := range d.EnumsAdded {
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

	// 6) 枚举墓碑：diff 的删除面。
	for _, v := range d.EnumsDeleted {
		if _, err := tx.ExecContext(ctx,
			`UPDATE dgw_sem_enum_values SET tombstone = 1 WHERE column_fqn = ? AND value = ?`,
			v.ColumnFQN, v.Value); err != nil {
			return nil, fmt.Errorf("tombstone enum %s=%s: %w", v.ColumnFQN, v.Value, err)
		}
		s.EnumsTombstoned++
	}

	return s, nil
}

func upsertEntity(ctx context.Context, tx *sql.Tx, e Entity) error {
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
		return fmt.Errorf("upsert entity %s: %w", e.FQN, err)
	}
	return nil
}

func upsertRelation(ctx context.Context, tx *sql.Tx, r Relation) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO dgw_sem_relations (type, src_fqn, dst_fqn, meta, tombstone)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(type, src_fqn, dst_fqn) DO UPDATE SET
			meta = excluded.meta, tombstone = 0,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
		string(r.Type), r.SrcFQN, r.DstFQN, r.Meta)
	if err != nil {
		return fmt.Errorf("upsert relation %s %s→%s: %w", r.Type, r.SrcFQN, r.DstFQN, err)
	}
	return nil
}

// backfillFTSIfEmpty 全量回填 FTS 索引（幂等）：索引空而实体面非空 =
// 历史库升级（08 之前的同步从未写索引），把活跃实体一次性写入；此后
// 增量维护。空索引 + 空实体面（新库）= 无操作。在 apply 事务内执行，
// 与实体面原子一致。
func backfillFTSIfEmpty(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM dgw_sem_fts`).Scan(&n); err != nil {
		return fmt.Errorf("count fts rows: %w", err)
	}
	if n > 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT fqn, kind, name, description FROM dgw_sem_entities WHERE tombstone = 0`)
	if err != nil {
		return fmt.Errorf("backfill fts snapshot: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e Entity
		var kind string
		if err := rows.Scan(&e.FQN, &kind, &e.Name, &e.Description); err != nil {
			return fmt.Errorf("scan fts backfill entity: %w", err)
		}
		e.Kind = Kind(kind)
		if err := upsertFTS(ctx, tx, e); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// upsertFTS 把一个实体的关键词索引行置为最新内容（幂等：先删后插，
// 描述变更时旧 trigram 不残留；fqn 是唯一键，删插即以 fqn 定位）。
// 六类实体都进索引（FTS 是通用检索面，检索时的 kind 过滤在查询侧）。
func upsertFTS(ctx context.Context, tx *sql.Tx, e Entity) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM dgw_sem_fts WHERE fqn = ?`, e.FQN); err != nil {
		return fmt.Errorf("delete fts row %s: %w", e.FQN, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO dgw_sem_fts (fqn, kind, name, description) VALUES (?, ?, ?, ?)`,
		e.FQN, string(e.Kind), e.Name, e.Description); err != nil {
		return fmt.Errorf("upsert fts row %s: %w", e.FQN, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
