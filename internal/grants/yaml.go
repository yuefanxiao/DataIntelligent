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
// 约定：grants YAML 只声明「用户 → 授权对象」，不含凭据——key 明文仅
// 创建时打印一次，永不进任何文件。YAML 全量覆盖表授权（apply 即编译）。
//
// Tables 列表的条目形态（07 票扩展，ADR-0004「指标/概念授权编译期展开为
// 表授权」「服务/库级通配 = 语法糖」）：
//
//	服务.库.表           表 FQN（直接授权，03 票原样）
//	metric:指标名         指标授权 → 展开为其依赖表（describes 边）
//	concept:概念名        概念授权 → 展开为其描述的实体（表/列归表、指标递归）
//	service:服务名        服务级通配 → 展开为该服务全部表清单快照
//	database:服务.库      库级通配 → 展开为该库全部表清单快照
//
// `*` 全库通配不开放（ValidateGrantTarget 拒绝）。
type File struct {
	Version int `yaml:"version"`
	Grants  []struct {
		User   string   `yaml:"user"`
		Tables []string `yaml:"tables"`
	} `yaml:"grants"`
}

// Parse 解析并校验 grants YAML（结构 + 授权对象合法性，不触库）。
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
			if err := ValidateGrantTarget(t); err != nil {
				return File{}, err
			}
			if seen[g.User][t] {
				return File{}, fmt.Errorf("用户 %q 重复授权对象 %q", g.User, t)
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
	Patterns int // 通配声明数（service:/database:，快照展开 + 告警依据）
	Revision int64
}

// Sync 把 grants YAML 全量编译进 SQLite 权限表（ADR-0004「编译进权限表」；
// 存储载体经 ADR-0005 修正为 SQLite）：单事务 = 清空旧表授权与通配声明 →
// 写入 YAML 声明（表 FQN 直接落库，指标/概念/服务/库级经 expand 展开为
// 具体表，通配声明记入 dgw_grant_patterns）→ bump revision。
// 零变更（净 diff 为空）不写库不 bump。
//
// expand == nil：只接受显式表 FQN（未接语义层时的保守形态）；遇指标/概念/
// 通配目标返回错误（编译拒绝）。
//
// YAML 是授权的唯一事实源：CLI 的 grant-add/remove 是临时调整，apply 后
// 回到 YAML 状态（git review 即权限变更评审闸门）。
func Sync(ctx context.Context, st *store.Store, f File, expand Expander) (SyncResult, error) {
	// 展开阶段先行：任一对象展开失败 = 整体原子拒绝（不写库）。
	targets, patterns, err := expandAll(ctx, expand, f)
	if err != nil {
		return SyncResult{}, err
	}

	// 净 diff：removed = 库里存在但 YAML 没声明；added = YAML 声明但库里没有。
	existing, err := Snapshot(ctx, st)
	if err != nil {
		return SyncResult{}, err
	}
	var res SyncResult
	for _, g := range existing {
		if _, ok := targets[expandKey(g.User, g.TableFQN)]; !ok {
			res.Removed++
		}
	}
	have := map[string]bool{}
	for _, g := range existing {
		have[expandKey(g.User, g.TableFQN)] = true
	}
	for k := range targets {
		if !have[k] {
			res.Added++
		}
	}
	res.Patterns = len(patterns)

	// 通配声明也参与净 diff：pattern 表与目标一致则无需写。
	curPatterns, err := SyncPatterns(ctx, st)
	if err != nil {
		return SyncResult{}, err
	}
	patternChanged := false
	if len(curPatterns) != len(patterns) {
		patternChanged = true
	} else {
		got := map[string]bool{}
		for _, p := range curPatterns {
			got[p] = true
		}
		for p := range patterns {
			if !got[p] {
				patternChanged = true
				break
			}
		}
	}

	if res.Added == 0 && res.Removed == 0 && !patternChanged {
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM dgw_grant_patterns"); err != nil {
		return SyncResult{}, fmt.Errorf("clear grant patterns: %w", err)
	}
	for _, k := range sortTargets(targets) {
		user, fqn := splitKey(k)
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO dgw_table_grants (user_id, table_fqn) VALUES (?, ?)`,
			user, fqn); err != nil {
			return SyncResult{}, fmt.Errorf("insert grant %s × %s: %w", user, fqn, err)
		}
	}
	for _, k := range sortTargets(patterns) {
		user, p := splitKey(k)
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO dgw_grant_patterns (user_id, pattern) VALUES (?, ?)`,
			user, p); err != nil {
			return SyncResult{}, fmt.Errorf("insert pattern %s × %s: %w", user, p, err)
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

// splitKey 拆开 user\x00fqn 复合键（与 expandKey 互逆）。
func splitKey(k string) (user, rest string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}
