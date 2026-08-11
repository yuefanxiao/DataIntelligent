package grants

import (
	"context"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

// 当前 grants YAML 的 schema 版本；解析时强制校验，防旧/新格式混用。
const yamlVersion = 1

// File 是 grants YAML 的结构（ADR-0004：可 git review 的授权事实源）。
//
// 约定：grants YAML 只声明「用户 → 表 FQN 白名单」，不含凭据——key 明文仅
// 创建时打印一次，永不进任何文件。YAML 全量覆盖表授权（apply 即编译）。
type File struct {
	Version int `yaml:"version"`
	Grants  []struct {
		User   string   `yaml:"user"`
		Tables []string `yaml:"tables"`
	} `yaml:"grants"`
}

// Parse 解析并校验 grants YAML（结构 + FQN 合法性，不触库）。
func Parse(r io.Reader) (File, error) {
	var f File
	dec := yaml.NewDecoder(r)
	// 未知字段直接报错：授权文件里拼错字段名必须失败，不能静默忽略（漏授权）。
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("解析 grants YAML: %w", err)
	}
	if f.Version != yamlVersion {
		return File{}, fmt.Errorf("grants YAML version = %d, want %d", f.Version, yamlVersion)
	}
	seen := map[string]map[string]bool{}
	for _, g := range f.Grants {
		if g.User == "" {
			return File{}, fmt.Errorf("grants 条目缺 user 字段")
		}
		if len(g.Tables) == 0 {
			return File{}, fmt.Errorf("用户 %q 的 tables 为空", g.User)
		}
		if seen[g.User] == nil {
			seen[g.User] = map[string]bool{}
		}
		for _, t := range g.Tables {
			if err := ValidateFQN(t); err != nil {
				return File{}, err
			}
			if seen[g.User][t] {
				return File{}, fmt.Errorf("用户 %q 重复授权表 %q", g.User, t)
			}
			seen[g.User][t] = true
		}
	}
	return f, nil
}

// SyncResult 是 apply 的变更摘要（CLI 输出用）：Added/Removed 是相对上次
// 状态的净变化，幂等重跑两者均为 0。
type SyncResult struct {
	Added    int // 净新增的表授权数
	Removed  int // 净清掉的旧授权数
	Revision int64
}

// Sync 把 grants YAML 全量编译进 SQLite 权限表（ADR-0004「编译进权限表」；
// 存储载体经 ADR-0005 修正为 SQLite）：单事务 = 清空旧表授权 → 写入 YAML
// 声明 → bump revision。零变更（净 diff 为空）不写库不 bump。
//
// YAML 是表授权的事实源：CLI 的 grant-add/remove 是临时调整，apply 后回到
// YAML 状态（git review 即权限变更评审闸门）。
func Sync(ctx context.Context, st *store.Store, f File) (SyncResult, error) {
	// 先算净 diff：removed = 库里存在但 YAML 没声明；added = YAML 声明但库里没有。
	existing, err := Snapshot(ctx, st)
	if err != nil {
		return SyncResult{}, err
	}
	target := map[string]bool{}
	for _, g := range f.Grants {
		for _, t := range g.Tables {
			target[g.User+"\x00"+t] = true
		}
	}
	var res SyncResult
	for _, g := range existing {
		if !target[g.User+"\x00"+g.TableFQN] {
			res.Removed++
		}
	}
	have := map[string]bool{}
	for _, g := range existing {
		have[g.User+"\x00"+g.TableFQN] = true
	}
	for k := range target {
		if !have[k] {
			res.Added++
		}
	}
	if res.Added == 0 && res.Removed == 0 {
		// 零变更：不写库也不 bump（与 AddGrant/RemoveGrant 的 no-op 纪律一致）。
		res.Revision, err = st.PermissionRevision(ctx)
		return res, err
	}

	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return SyncResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM dgw_table_grants"); err != nil {
		return SyncResult{}, fmt.Errorf("clear grants: %w", err)
	}
	for _, g := range f.Grants {
		for _, t := range g.Tables {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO dgw_table_grants (user_id, table_fqn) VALUES (?, ?)`,
				g.User, t); err != nil {
				return SyncResult{}, fmt.Errorf("insert grant %s × %s: %w", g.User, t, err)
			}
		}
	}
	if err := st.BumpPermissionRevisionTx(ctx, tx); err != nil {
		return SyncResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncResult{}, fmt.Errorf("commit: %w", err)
	}

	res.Revision, err = st.PermissionRevision(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	return res, nil
}
