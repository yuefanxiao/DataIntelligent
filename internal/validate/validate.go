// Package validate 是校验层（ADR-0008）四段链的前两段：AST 分类 + 表提取与
// 授权比对，作为独立包交付（03 票），单测 seam 覆盖 §5.2 四类（分类/提取/
// 比对/拒绝语义）。
//
// 链的后两段（PG 物理边界 + LIMIT 包层）由 04 票在 execute_sql 上接线；本包
// 不触网、不触库、不依赖网关运行时——纯函数式校验：输入 SQL 与注入的
// 解析/白名单回调，输出 Report 或结构化错误（gwerr，kind + details.reason
// 机器可区分）。
//
// 立场（在 spec §4.5 枚举拒绝集之上的「只读网关」必然推论，均在拒绝集测试
// 覆盖内）：
//   - SELECT INTO（建表）与任何行锁子句（FOR UPDATE/SHARE/…，PG 只读事务
//     对 FOR SHARE 的语义版本间不统一，网关不赌）→ 按写副作用拒绝；
//   - EXPLAIN 一律按 utility 拒绝——「EXPLAIN 不作授权依据」落地为：授权只
//     发生在 AST 语法层，planner 层（EXPLAIN 输出）从不参与。
package validate

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	pgquery "github.com/pganalyze/pg_query_go/v6"
	wasm "github.com/wasilibs/go-pgquery"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// TableRef 是语法层表引用（AST 提取，未做 FQN 解析）：Schema 为空表示未限定
// （按连接 search_path 解析，解析权在注入方）。
type TableRef struct {
	Schema string
	Table  string
}

// Resolve 把语法层表引用映射为授权挂载点 FQN 候选（服务.库.表，ADR-0004）。
// 由网关注入（04 接线）：网关掌握 dbname 路由与服务归属拓扑（语义层）；
// 返回空切片 = 无法映射（未知表）→ 拒绝。v1 无通配展开——服务/库级通配是
// 作者入口语法糖，编译期展开依赖语义层表清单（07 票），落地前一律编译拒绝
// （grants.ValidateFQN）。
type Resolve func(ref TableRef) []string

// Allow 判定表 FQN 是否在白名单内：通常封装 authz.Allow(user, fqn)（02 票）；
// nil = 全拒（fail closed，ADR-0004 默认拒绝哲学）。
type Allow func(fqn string) bool

// Report 是 Check 通过的产出（执行记录/审计可用的表清单）。
type Report struct {
	Tables []TableRef // 语法层可见表引用，去重、按首次出现顺序
}

// Parse 解析 SQL 为 raw parse 语句列表。语法错误 → 结构化 invalid_request
// （details.reason=syntax_error），与权限拒绝机器可区分。
func Parse(sql string) ([]*pgquery.RawStmt, *gwerr.Error) {
	tree, err := wasm.Parse(sql)
	if err != nil {
		return nil, gwerr.InvalidRequest(
			fmt.Sprintf("SQL 语法错误: %s", err),
			map[string]any{"reason": "syntax_error", "error": err.Error()},
		)
	}
	return tree.Stmts, nil
}

// ClassifyStmt 段一（AST 分类）：仅 SELECT 放行；以下一律拒绝（结构化错误，
// 调用方凭 kind + details.reason 机器区分，网关不重试）：
//
//   - 非 SELECT 语句（DML/DDL/COPY/utility/事务）→ reason=non_select；
//   - SELECT INTO / 行锁子句 → reason=write_side_effect（只读网关推论）；
//   - 数据修改 CTE（WITH … INSERT/UPDATE/DELETE/MERGE，含嵌套在子查询内的）
//     → reason=data_modifying_cte。
func ClassifyStmt(stmt *pgquery.RawStmt) *gwerr.Error {
	n := stmt.Stmt
	if n == nil || n.Node == nil {
		return gwerr.InvalidRequest("空语句", map[string]any{"reason": "empty"})
	}
	if n.GetSelectStmt() == nil {
		return gwerr.InvalidRequest(
			fmt.Sprintf("非 SELECT 语句被拒绝（只读网关，ADR-0008）: %s", stmtKindName(n)),
			map[string]any{"reason": "non_select", "stmt": stmtKindName(n)},
		)
	}
	w := newWalker()
	w.walk(n)
	if w.intoClause {
		return gwerr.InvalidRequest(
			"SELECT INTO 被拒绝（建表是写副作用，只读网关）",
			map[string]any{"reason": "write_side_effect", "clause": "select_into"},
		)
	}
	if w.rowLock {
		return gwerr.InvalidRequest(
			"行锁子句被拒绝（FOR UPDATE/SHARE 等，只读网关）",
			map[string]any{"reason": "write_side_effect", "clause": "row_lock"},
		)
	}
	if w.modifyingCTE != "" {
		return gwerr.InvalidRequest(
			fmt.Sprintf("数据修改 CTE 被拒绝（只读网关）: %s", w.modifyingCTE),
			map[string]any{"reason": "data_modifying_cte", "cte": w.modifyingCTE, "stmt": w.modifyingKind},
		)
	}
	return nil
}

// ExtractTables 段二前半（表提取）：返回语法层可见的全部表引用——CTE 定义体、
// 子查询（FROM/WHERE/目标列表/函数实参内）、join、集合运算全可见；CTE 名
// 引用（含递归/互引）不视为表。定义域 = 已通过 ClassifyStmt 的语句（对
// 非 SELECT 语句的提取结果无保证）。
func ExtractTables(stmt *pgquery.RawStmt) []TableRef {
	w := newWalker()
	w.walk(stmt.Stmt)
	return w.refs
}

// AuthorizeTables 段二后半（授权比对）：对提取出的表引用逐表解析 + 白名单
// 比对，任一表未通过即拒绝（默认拒绝，ADR-0004）。details 区分：
//
//   - unknown_table：引用无法映射到任何 FQN（白名单/语义层里没有这张表）；
//   - not_granted：有候选 FQN 但全部未授权（错误信息可让 Agent 自我修正）。
func AuthorizeTables(refs []TableRef, resolve Resolve, allow Allow) *gwerr.Error {
	if resolve == nil {
		resolve = func(TableRef) []string { return nil }
	}
	if allow == nil {
		allow = func(string) bool { return false }
	}
	for _, ref := range refs {
		fqns := resolve(ref)
		if len(fqns) == 0 {
			return gwerr.PermissionDenied(
				fmt.Sprintf("未知表：%s 无法映射到任何表 FQN（AST 为唯一授权通道，ADR-0008）", refString(ref)),
				map[string]any{"reason": "unknown_table", "schema": ref.Schema, "table": ref.Table},
			)
		}
		granted := false
		for _, fqn := range fqns {
			if allow(fqn) {
				granted = true
				break
			}
		}
		if !granted {
			return gwerr.PermissionDenied(
				fmt.Sprintf("未授权表：%s 的全部候选 FQN 均无读取授权（默认拒绝，ADR-0004）", refString(ref)),
				map[string]any{"reason": "not_granted", "schema": ref.Schema, "table": ref.Table, "candidate_fqns": fqns},
			)
		}
	}
	return nil
}

// Check 是校验层前两段链的组合：解析 → 逐语句分类 → 提取 → 逐表比对。
// 任一失败返回结构化错误（网关不重试、无自愈循环，ADR-0008）：
//
//	语法错误         → invalid_request（reason=syntax_error）
//	非 SELECT/写副作用 → invalid_request（reason=non_select / data_modifying_cte / write_side_effect）
//	未知/未授权表    → permission_denied（reason=unknown_table / not_granted）
func Check(sql string, resolve Resolve, allow Allow) (*Report, *gwerr.Error) {
	stmts, err := Parse(sql)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	for _, stmt := range stmts {
		if err := ClassifyStmt(stmt); err != nil {
			return nil, err
		}
		rep.Tables = append(rep.Tables, ExtractTables(stmt)...)
	}
	if err := AuthorizeTables(rep.Tables, resolve, allow); err != nil {
		return nil, err
	}
	return rep, nil
}

// ── 分类辅助 ──────────────────────────────────────────────────────────────

// dataModifyingKind 判断节点是否为数据修改语句（CTE 体），返回语句类型机器名
// （insert_stmt/update_stmt/delete_stmt/merge_stmt）；非数据修改返回空串。
func dataModifyingKind(n *pgquery.Node) string {
	if n == nil || n.Node == nil {
		return ""
	}
	switch n.Node.(type) {
	case *pgquery.Node_InsertStmt:
		return "insert_stmt"
	case *pgquery.Node_UpdateStmt:
		return "update_stmt"
	case *pgquery.Node_DeleteStmt:
		return "delete_stmt"
	case *pgquery.Node_MergeStmt:
		return "merge_stmt"
	}
	return ""
}

// stmtKindName 返回语句类型的机器名（insert_stmt/explain_stmt/vacuum_stmt…），
// 供非 SELECT 拒绝的结构化详情使用。命名取 oneof 包装类型（Node_<Message>），
// 反射可得全部语句类型，无需逐一枚举。
func stmtKindName(n *pgquery.Node) string {
	t := reflect.TypeOf(n.Node)
	if t == nil || t.Kind() != reflect.Pointer {
		return "unknown"
	}
	return camelToSnake(strings.TrimPrefix(t.Elem().Name(), "Node_"))
}

// camelToSnake 把 CamelCase 转下划线小写（InsertStmt → insert_stmt）。
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// refString 是表引用的展示形态（schema 限定时不省略）。
func refString(r TableRef) string {
	if r.Schema == "" {
		return r.Table
	}
	return r.Schema + "." + r.Table
}
