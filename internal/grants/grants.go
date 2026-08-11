// Package grants 是权限维护方（ADR-0004）：表级授权的写入域。
//
// 维护面 = grants YAML + 权限 CLI（仅宿主机可运行）：YAML 是「可 git review
// 的授权事实源」，apply 全量编译进 SQLite 权限表；CLI 的增删授权是临时调整
// （下次 apply 会被 YAML 覆盖）。任何写入都 bump 权限 revision——网关侧的热
// 重载轮询据此感知（02 票）。凭据（key）不属本包：明文仅创建时打印一次，
// 永不进 YAML，key 生命周期在 credentials 包。
//
// 授权对象（07 票扩展，ADR-0004「指标/概念授权编译期展开为表授权」）：
//   - 表 FQN（服务.库.表）→ 直接写入 dgw_table_grants；
//   - metric:xxx / concept:xxx → 经 Expander 展开为底层表清单写入；
//   - service:xxx / database:xxx.yyy → 通配语法糖，展开为当前表清单快照
//     写入，并把声明记入 dgw_grant_patterns——同步管线据此告警「新表
//     未覆盖」（新表默认拒绝，重展开 = 重跑 grants-apply）。`*` 不开放。
//
// 展开失败（指标/概念不存在、通配展开出空清单）= 编译拒绝（杜绝悬空授权）。
package grants

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// FQNSegments 是表 FQN 的段数：服务.库.表（与本体 Table 实体同一命名空间）。
const FQNSegments = 3

// ErrWildcard 表示授权对象含未支持的 `*` 通配——全库通配不开放（ADR-0004：
// 「全授权」应是显式写出的少数情况；服务/库级通配走 service:/database: 语法）。
var ErrWildcard = errors.New("wildcard `*` not supported: use service:/database: targets")

// Target 前缀（授权对象形态，07 票）。
const (
	PrefixMetric   = "metric:"
	PrefixConcept  = "concept:"
	PrefixService  = "service:"
	PrefixDatabase = "database:"
)

// Expander 是把授权对象展开为具体表 FQN 清单的注入接口（由 semantic 包实现，
// 07 票同步管线落地后接线）：grants 保持授权写入域，不依赖语义查询实现。
type Expander interface {
	// Expand 返回授权对象的展开表清单（按 FQN 排序、去重）。
	// 对象不存在或展开为空 → 返回错误（编译拒绝，杜绝悬空授权）。
	Expand(ctx context.Context, target string) ([]string, error)
}

// ValidateGrantTarget 校验授权对象：表 FQN（服务.库.表）或带前缀形态
// （metric:/concept:/service:/database:）。`*` 一律拒绝。
func ValidateGrantTarget(target string) error {
	if strings.ContainsAny(target, "*") {
		return fmt.Errorf("%w（授权对象 %q 含 *；服务/库级通配请写 service:服务名 或 database:服务.库）", ErrWildcard, target)
	}
	switch {
	case strings.HasPrefix(target, PrefixMetric):
		return validateName(target, PrefixMetric, "指标")
	case strings.HasPrefix(target, PrefixConcept):
		return validateName(target, PrefixConcept, "概念")
	case strings.HasPrefix(target, PrefixService):
		return validateName(target, PrefixService, "服务")
	case strings.HasPrefix(target, PrefixDatabase):
		return validateDB(target)
	default:
		return ValidateFQN(target)
	}
}

func validateName(target, prefix, what string) error {
	name := strings.TrimPrefix(target, prefix)
	if name == "" {
		return fmt.Errorf("%s授权对象 %q 缺名字（%s名字）", what, target, what)
	}
	// \x00 是内部复合键分隔符（expandKey），名字含它会导致授权/告警键错位
	// （review 修复：与点/空白同组拒绝）。
	if strings.ContainsAny(name, ".\x00 \t\r\n") {
		return fmt.Errorf("%s授权对象 %q 名字不能含点/空白/控制字符", what, target)
	}
	return nil
}

func validateDB(target string) error {
	name := strings.TrimPrefix(target, PrefixDatabase)
	parts := strings.Split(name, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("库级通配 %q 应为 服务.库 两段", target)
	}
	return nil
}

// ValidateFQN 校验授权挂载点：恰好 服务.库.表 三段、每段非空。
// 返回的错误信息面向 CLI 运维者，可直接展示。
func ValidateFQN(fqn string) error {
	if strings.ContainsAny(fqn, "*") {
		return fmt.Errorf("%w: %q（全库通配不开放；服务/库级通配请写 service:/database: 目标）", ErrWildcard, fqn)
	}
	parts := strings.Split(fqn, ".")
	if len(parts) != FQNSegments {
		return fmt.Errorf("表 FQN 应为 服务.库.表 三段: %q（收到 %d 段）", fqn, len(parts))
	}
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("表 FQN 第 %d 段为空: %q", i+1, fqn)
		}
		// \x00 是内部复合键分隔符（expandKey），与 validateName/EntityFQN
		// 同一拒绝组（review 修复：防授权键错位）。
		if strings.ContainsAny(p, "\x00 \t\r\n") {
			return fmt.Errorf("表 FQN 段不能含控制字符或空白: %q", fqn)
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
	return mutate(ctx, st, "insert grant", func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO dgw_table_grants (user_id, table_fqn) VALUES (?, ?)`,
			user, tableFQN)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// RemoveGrant 撤掉用户对一张表的授权（幂等：不存在视为成功）。
func RemoveGrant(ctx context.Context, st *store.Store, user, tableFQN string) error {
	if err := ValidateFQN(tableFQN); err != nil {
		return err
	}
	return mutate(ctx, st, "delete grant", func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM dgw_table_grants WHERE user_id = ? AND table_fqn = ?`,
			user, tableFQN)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// mutate 是授权写入的公共事务骨架：单语句变更 + 热重载信号。
// rowsAffected == 0 = 幂等 no-op（不 bump revision，避免无谓热重载）；
// 否则 bump 后提交——数据与版本号同一事务，网关不会看到中间态。
func mutate(ctx context.Context, st *store.Store, op string, fn func(tx *sql.Tx) (int64, error)) error {
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	n, err := fn(tx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if n == 0 {
		return tx.Commit()
	}
	if err := st.BumpPermissionRevisionTx(ctx, tx); err != nil {
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
