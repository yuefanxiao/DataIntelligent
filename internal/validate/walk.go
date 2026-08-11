package validate

import (
	"reflect"

	pgquery "github.com/pganalyze/pg_query_go/v6"
)

// walker 是语法树遍历器：一次深度遍历同时服务两个消费者——分类检查
// （数据修改 CTE/写副作用）与表引用提取（去重、按首次出现顺序）。
//
// CTE 作用域：WITH 的 CTE 名在 walkSelect 入口先于整棵语句注册（语句体与
// CTE 定义体共享同一作用域，递归/互引 CTE 自引用可见）；RangeVar 命中
// 作用域内 CTE 名时视为 CTE 引用而非表引用——CTE 引用最终解析到其定义体
// 里的真实表，避免「WITH x AS (...) SELECT * FROM x」把 x 当外部表。
//
// 遍历用反射下钻 pg_query 生成的消息结构：任何持 *Node / []*Node / 嵌套
// 消息的字段都会继续走，无需逐一枚举表达式类型——子查询（SubLink /
// RangeSubselect）、join（JoinExpr）、集合运算（SelectStmt.Op）、分析后
// 形态（Query，防御性）全部被覆盖，函数实参 / CASE 分支内的隐藏子查询
// 也不会漏。生成树无环，反射遍历安全。
type walker struct {
	refs []TableRef
	seen map[string]struct{} // 引用去重键：schema + "\x00" + table

	ctes []map[string]struct{} // CTE 名作用域栈（近者优先）

	// 分类收集器（首个违规即停，供 ClassifyStmt 读取）。
	modifyingCTE  string // 数据修改 CTE 名
	modifyingKind string // 其语句类型机器名（insert_stmt 等）
	intoClause    bool   // SELECT INTO（建表写副作用）
	rowLock       bool   // 任何行锁子句（FOR UPDATE/SHARE 等）
}

func newWalker() *walker {
	return &walker{seen: map[string]struct{}{}}
}

// walk 遍历以 n 为根的语法子树。只有四个节点类型需要显式处理：
// RangeVar（CTE 作用域过滤后收集）、CommonTableExpr（数据修改 CTE 检查）、
// SelectStmt / Query（CTE 作用域注册）；其余一律反射下钻。
func (w *walker) walk(n *pgquery.Node) {
	if n == nil || n.Node == nil {
		return
	}
	switch v := n.Node.(type) {
	case *pgquery.Node_RangeVar:
		rv := v.RangeVar
		if !w.cteInScope(rv.Relname) {
			w.addRef(rv.Schemaname, rv.Relname)
		}
	case *pgquery.Node_CommonTableExpr:
		cte := v.CommonTableExpr
		if cte != nil && w.modifyingCTE == "" {
			if kind := dataModifyingKind(cte.Ctequery); kind != "" {
				w.modifyingCTE = cte.Ctename
				w.modifyingKind = kind
			}
		}
		w.walk(cte.Ctequery)
	case *pgquery.Node_SelectStmt:
		w.walkSelect(v.SelectStmt)
	case *pgquery.Node_Query:
		w.walkQuery(v.Query)
	default:
		w.walkStruct(n.Node)
	}
}

// walkSelect 处理 SELECT 语句：先注册其 WITH 的 CTE 名（语句体与定义体共享
// 作用域），再检查写副作用，最后反射下钻全部字段（FromClause/WhereClause/
// TargetList/集合运算 Larg/Rarg 等）。嵌套 SELECT（子查询/CTE 体）递归进入
// 本函数，各自注册自己的 WITH 作用域。
func (w *walker) walkSelect(s *pgquery.SelectStmt) {
	if s == nil {
		return
	}
	if s.WithClause != nil {
		w.pushCTE(s.WithClause)
		defer w.popCTE()
	}
	if s.IntoClause != nil {
		w.intoClause = true
	}
	if len(s.LockingClause) > 0 {
		w.rowLock = true
	}
	w.walkStruct(s)
}

// walkQuery 处理分析后形态的 Query（防御性：libpg_query raw parse 不产生
// Query，但递归 CTE 展开 / 未来接线可能遇到）。CTE 作用域取自 CteList。
func (w *walker) walkQuery(q *pgquery.Query) {
	if q == nil {
		return
	}
	if len(q.CteList) > 0 {
		w.pushCTEList(q.CteList)
		defer w.popCTE()
	}
	w.walkStruct(q)
}

// ── CTE 作用域 ────────────────────────────────────────────────────────────

func (w *walker) pushCTE(wc *pgquery.WithClause) {
	w.pushCTEList(wc.Ctes)
}

func (w *walker) pushCTEList(list []*pgquery.Node) {
	scope := map[string]struct{}{}
	for _, c := range list {
		if cte := c.GetCommonTableExpr(); cte != nil {
			scope[cte.Ctename] = struct{}{}
		}
	}
	w.ctes = append(w.ctes, scope)
}

func (w *walker) popCTE() {
	w.ctes = w.ctes[:len(w.ctes)-1]
}

func (w *walker) cteInScope(name string) bool {
	for i := len(w.ctes) - 1; i >= 0; i-- {
		if _, ok := w.ctes[i][name]; ok {
			return true
		}
	}
	return false
}

// ── 表引用收集 ────────────────────────────────────────────────────────────

func (w *walker) addRef(schema, table string) {
	key := schema + "\x00" + table
	if _, ok := w.seen[key]; ok {
		return
	}
	w.seen[key] = struct{}{}
	w.refs = append(w.refs, TableRef{Schema: schema, Table: table})
}

// ── 反射下钻 ──────────────────────────────────────────────────────────────

var (
	nodeType       = reflect.TypeOf((*pgquery.Node)(nil))
	selectStmtType = reflect.TypeOf((*pgquery.SelectStmt)(nil))
	queryType      = reflect.TypeOf((*pgquery.Query)(nil))
)

// walkStruct 反射下钻任意消息结构：遍历全部导出字段；*Node / []*Node 字段
// 重新进入类型派发（发现嵌套子查询/CTE/表引用），SelectStmt/Query 走各自
// 的作用域处理，其余消息结构递归下钻。protoimpl 内部字段不可导出，天然跳过。
func (w *walker) walkStruct(v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := rv.Field(i)
		switch f.Type.Kind() {
		case reflect.Pointer:
			switch f.Type {
			case nodeType:
				w.walk(fv.Interface().(*pgquery.Node))
			case selectStmtType:
				w.walkSelect(fv.Interface().(*pgquery.SelectStmt))
			case queryType:
				w.walkQuery(fv.Interface().(*pgquery.Query))
			default:
				w.walkStruct(fv.Interface())
			}
		case reflect.Slice:
			if f.Type.Elem().Kind() != reflect.Pointer {
				continue // []string / []enum 等叶子
			}
			switch f.Type.Elem() {
			case nodeType:
				for j := 0; j < fv.Len(); j++ {
					w.walk(fv.Index(j).Interface().(*pgquery.Node))
				}
			case selectStmtType:
				for j := 0; j < fv.Len(); j++ {
					w.walkSelect(fv.Index(j).Interface().(*pgquery.SelectStmt))
				}
			case queryType:
				for j := 0; j < fv.Len(); j++ {
					w.walkQuery(fv.Index(j).Interface().(*pgquery.Query))
				}
			default:
				for j := 0; j < fv.Len(); j++ {
					w.walkStruct(fv.Index(j).Interface())
				}
			}
		}
	}
}
