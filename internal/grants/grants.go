// Package grants 是权限维护方（ADR-0004）：表级授权的写入域。
//
// 维护面 = grants YAML + 权限 CLI（仅宿主机可运行）：YAML 是「可 git review
// 的授权事实源」，apply 全量编译进 SQLite 权限表；CLI 的增删授权是临时调整
// （下次 apply 会被 YAML 覆盖）。任何写入都 bump 权限 revision——网关侧的热
// 重载轮询据此感知（02 票）。凭据（key）不属本包：明文仅创建时打印一次，
// 永不进 YAML，key 生命周期在 credentials 包。
//
// v1 只支持显式表 FQN（服务.库.表）；服务/库级通配是作者入口语法糖，编译期
// 展开依赖语义层表清单（07 同步管线），语义层落地前通配一律编译拒绝。
package grants

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// FQNSegments 是表 FQN 的段数：服务.库.表（与本体 Table 实体同一命名空间）。
const FQNSegments = 3

// ErrWildcard 表示 FQN 含通配模式——v1 不开放，展开归 07。
var ErrWildcard = errors.New("wildcard FQN not supported in v1")

// ValidateFQN 校验授权挂载点：恰好 服务.库.表 三段、每段非空。
// 返回的错误信息面向 CLI 运维者，可直接展示。
func ValidateFQN(fqn string) error {
	if strings.ContainsAny(fqn, "*") {
		return fmt.Errorf("%w: %q（服务/库级通配是作者入口语法糖，编译期展开依赖语义层表清单（07 票），v1 请写具体表 FQN）", ErrWildcard, fqn)
	}
	parts := strings.Split(fqn, ".")
	if len(parts) != FQNSegments {
		return fmt.Errorf("表 FQN 应为 服务.库.表 三段: %q（收到 %d 段）", fqn, len(parts))
	}
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("表 FQN 第 %d 段为空: %q", i+1, fqn)
		}
		if strings.ContainsAny(p, " \t\r\n") {
			return fmt.Errorf("表 FQN 段不能含空白: %q", fqn)
		}
	}
	return nil
}

// Grant 是一条授权（快照视图的一行）。
type Grant struct {
	User      string
	TableFQN  string
	GrantedAt string
}

// AddGrant 给用户加一张表的授权（幂等：已存在视为成功），并 bump 权限
// revision——热重载信号与数据写入同一事务，网关不会看到「数据变了但版本没变」。
func AddGrant(ctx context.Context, st *store.Store, user, tableFQN string) error {
	if err := ValidateFQN(tableFQN); err != nil {
		return err
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO dgw_table_grants (user_id, table_fqn) VALUES (?, ?)`,
		user, tableFQN)
	if err != nil {
		return fmt.Errorf("insert grant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert grant: %w", err)
	}
	if n == 0 {
		return tx.Commit() // 已存在：幂等 no-op，不 bump
	}
	if err := st.BumpPermissionRevision(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveGrant 撤掉用户对一张表的授权（幂等：不存在视为成功）。
func RemoveGrant(ctx context.Context, st *store.Store, user, tableFQN string) error {
	if err := ValidateFQN(tableFQN); err != nil {
		return err
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM dgw_table_grants WHERE user_id = ? AND table_fqn = ?`,
		user, tableFQN)
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete grant: %w", err)
	}
	if n == 0 {
		return tx.Commit() // 不存在：幂等 no-op，不 bump
	}
	if err := st.BumpPermissionRevision(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Snapshot 返回全部表授权（按用户、FQN 排序），供 CLI 快照查看。
func Snapshot(ctx context.Context, st *store.Store) ([]Grant, error) {
	rows, err := st.DB().QueryContext(ctx,
		`SELECT user_id, table_fqn, granted_at FROM dgw_table_grants
		 ORDER BY user_id, table_fqn`)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.User, &g.TableFQN, &g.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}
