package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	wasm "github.com/wasilibs/go-pgquery"
)

// migrationFile 是 golang-migrate 命名约定（NNNN_YYYYMMDDHHMMSS_name.up.sql），
// 文件名即版本序。目录名统一、纯 SQL up/down、可全量重建（ADR-0007）。
const migrationsDirName = "migrations"

// DiscoverMigrations 找服务的迁移文件（migrations/ 顶层 *.up.sql，
// 不含子目录——golang-migrate 忽略子目录，deprecated/ 同理不算数）。
// 按文件名排序 = golang-migrate 的版本序（确定性）。
func DiscoverMigrations(serviceDir string) ([]string, error) {
	dir := filepath.Join(serviceDir, migrationsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("服务目录 %s 没有迁移文件（期望 %s/*.up.sql）", serviceDir, migrationsDirName)
	}
	sort.Strings(files)
	return files, nil
}

// ParseMigrations 按版本序解析全部迁移文件 → 结构中间态。
// 确定性：同语料同输出（pg_query WASM 解析 + 固定遍历顺序）。
// 解析失败的发现以 error 级别返回（采集不可靠 = 门禁失败），
// 但已解析的部分仍然返回（草稿照写，问题列表给人看）。
func ParseMigrations(service, db string, files []string) (*Structure, []Finding) {
	st := &Structure{Service: service, DB: db}
	var findings []Finding
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			findings = append(findings, Finding{"migration", "error", fmt.Sprintf("读取 %s: %v", f, err)})
			continue
		}
		tree, err := wasm.Parse(string(data))
		if err != nil {
			findings = append(findings, Finding{"migration", "error",
				fmt.Sprintf("解析 %s 失败（PG 语法错误 = 迁移文件本身不可解析，采集停在此文件）: %v", f, err)})
			continue
		}
		for _, stmt := range tree.Stmts {
			applyStmt(st, stmt, string(data), f, &findings)
		}
	}
	st.sortTables()
	return st, findings
}

// applyStmt 应用一条语句到结构中间态；不识别的语句类型跳过
// （采集只关心结构：表/列/类型/枚举/引用边）。src 是文件全文，
// 类型原文提取在语句边界 [StmtLocation, StmtLocation+StmtLen) 内扫描。
func applyStmt(st *Structure, stmt *pg_query.RawStmt, src, file string, findings *[]Finding) {
	beg := int(stmt.StmtLocation)
	end := beg + int(stmt.StmtLen)
	if beg < 0 || end > len(src) {
		beg, end = 0, len(src)
	}
	node := stmt.GetStmt().GetNode()
	switch n := node.(type) {
	case *pg_query.Node_CreateStmt:
		applyCreateTable(st, n.CreateStmt, src, beg, end, findings)
	case *pg_query.Node_AlterTableStmt:
		applyAlterTable(st, n.AlterTableStmt, src, beg, end, file, findings)
	case *pg_query.Node_DropStmt:
		if n.DropStmt.RemoveType == pg_query.ObjectType_OBJECT_TABLE {
			for _, obj := range n.DropStmt.Objects {
				// 对象是 List 包装 + String 表名（PG 解析器形态：
				// DROP TABLE a, b → List(List(String(a)), List(String(b)))）。
				for _, item := range unwrapList(obj) {
					if s := item.GetString_(); s != nil && s.Sval != "" {
						st.Tables = removeTable(st.Tables, s.Sval)
					}
				}
			}
		}
	}
	// IndexStmt/CreateSchemaStmt/CreateExtensionStmt/CreateSeqStmt/
	// CreateFunctionStmt/DoStmt/TransactionStmt/InsertStmt/UpdateStmt/
	// DeleteStmt/SelectStmt/LockStmt/CommentStmt/RenameStmt(仅索引)
	// 与结构中间态无关，跳过（索引/序列不进作者入口 YAML 模型）。
}

func applyCreateTable(st *Structure, cs *pg_query.CreateStmt, src string, beg, end int, findings *[]Finding) {
	t := &Table{
		Name:        cs.Relation.Relname,
		Schema:      cs.Relation.Schemaname,
		constraints: map[string]constraintState{},
	}
	for _, el := range cs.TableElts {
		switch e := el.GetNode().(type) {
		case *pg_query.Node_ColumnDef:
			col := &Column{
				Name: e.ColumnDef.Colname,
				Type: columnType(src, beg, end, e.ColumnDef),
			}
			// 列级内联约束（NOT NULL/UNIQUE 等不进模型；列级外键/枚举
			// 在 corpus 里走表级命名约束，此处登记防御）。
			for _, c := range e.ColumnDef.Constraints {
				if cn := c.GetConstraint(); cn != nil && cn.Conname != "" {
					applyConstraint(t, cn, findings)
				}
			}
			t.Columns = append(t.Columns, col)
		case *pg_query.Node_Constraint:
			applyConstraint(t, e.Constraint, findings)
		}
	}
	// 重建语义：同表名 CREATE（IF NOT EXISTS 等）以最后定义为准。
	for i, prev := range st.Tables {
		if prev.Name == t.Name {
			st.Tables[i] = t
			return
		}
	}
	st.Tables = append(st.Tables, t)
}

// applyConstraint 登记一个约束（命名约束；匿名约束不登记——
// 匿名 CHECK 无法被 DROP CONSTRAINT 点名，只做一次性枚举提取）。
func applyConstraint(t *Table, cn *pg_query.Constraint, findings *[]Finding) {
	if cn.Conname == "" {
		if st, ok := enumState(t, cn); ok {
			attachEnum(t, st, findings)
		}
		return
	}
	switch cn.Contype {
	case pg_query.ConstrType_CONSTR_CHECK:
		st, ok := enumState(t, cn)
		if !ok {
			t.addConstraint(cn.Conname, constraintState{kind: "other"})
			return
		}
		t.addConstraint(cn.Conname, st)
		attachEnum(t, st, findings)
	case pg_query.ConstrType_CONSTR_FOREIGN:
		st := constraintState{
			kind:         "fk",
			targetSchema: fkSchema(cn.Pktable),
			targetTable:  cn.Pktable.Relname,
			cols:         stringList(cn.FkAttrs),
			pkCols:       stringList(cn.PkAttrs),
		}
		t.addConstraint(cn.Conname, st)
		t.References = append(t.References, &Reference{
			TargetSchema: st.targetSchema,
			TargetTable:  st.targetTable,
			Cols:         st.cols,
			PkCols:       st.pkCols,
		})
	default:
		t.addConstraint(cn.Conname, constraintState{kind: "other"})
	}
}

// enumState 从 CHECK 约束提取枚举态：raw_expr 必须是
// `列 IN (常量列表)`（AEXPR_IN，A_Const 全字面量）。其他形态
// （= 单值、BETWEEN、复合条件）不是枚举（ADR-0007「CHECK 枚举」），
// 提取失败返回 ok=false。
func enumState(t *Table, cn *pg_query.Constraint) (constraintState, bool) {
	col, values, ok := enumExpr(t, cn.RawExpr)
	if !ok {
		return constraintState{}, false
	}
	sort.Strings(values)
	return constraintState{kind: "enum", column: col, values: values}, true
}

// enumExpr 走 CHECK 表达式树；返回（列名, 值列表, 是否可提取）。
func enumExpr(t *Table, n *pg_query.Node) (string, []string, bool) {
	a := nodeAExpr(n)
	if a == nil || a.Kind != pg_query.A_Expr_Kind_AEXPR_IN {
		return "", nil, false
	}
	colRef := a.Lexpr.GetColumnRef()
	if colRef == nil || len(colRef.Fields) == 0 {
		return "", nil, false
	}
	colName := lastString(colRef.Fields)
	if t.findColumn(colName) == nil {
		return "", nil, false
	}
	var values []string
	if list := a.Rexpr.GetList(); list != nil {
		for _, item := range list.Items {
			v, ok := constValue(item)
			if !ok {
				return "", nil, false
			}
			values = append(values, v)
		}
	} else if arr := a.Rexpr.GetArrayExpr(); arr != nil {
		for _, item := range arr.Elements {
			v, ok := constValue(item)
			if !ok {
				return "", nil, false
			}
			values = append(values, v)
		}
	} else {
		return "", nil, false
	}
	if len(values) == 0 {
		return "", nil, false
	}
	return colName, values, true
}

// constValue 提取 A_Const 的字面量字符串（ival/sval/fval/boolval）。
func constValue(n *pg_query.Node) (string, bool) {
	c := n.GetAConst()
	if c == nil {
		return "", false
	}
	switch v := c.Val.(type) {
	case *pg_query.A_Const_Ival:
		return strconv.FormatInt(int64(v.Ival.Ival), 10), true
	case *pg_query.A_Const_Sval:
		return v.Sval.Sval, true
	case *pg_query.A_Const_Fval:
		return v.Fval.Fval, true
	case *pg_query.A_Const_Boolval:
		return strconv.FormatBool(v.Boolval.Boolval), true
	}
	return "", false
}

// attachEnum 把枚举值挂到列上（去重 + 排序，确定性）。
// 空字符串值（corpus 先例：” 表示「未指定」）在 07 编译校验里不合法
// （枚举取值必须非空），语义层无法表达——跳过并显式 warn，不留静默丢失。
func attachEnum(t *Table, st constraintState, findings *[]Finding) {
	c := t.findColumn(st.column)
	if c == nil {
		return
	}
	seen := map[string]bool{}
	for _, v := range st.values {
		if v == "" {
			*findings = append(*findings, Finding{"migration", "warn",
				fmt.Sprintf("列 %s.%s 的枚举含空字符串值（'' 表示未指定），07 编译校验要求枚举非空，已跳过该值", t.Name, c.Name)})
			continue
		}
		if !seen[v] {
			seen[v] = true
			c.EnumValues = append(c.EnumValues, v)
		}
	}
	sort.Strings(c.EnumValues)
}

func applyAlterTable(st *Structure, at *pg_query.AlterTableStmt, src string, beg, end int, file string, findings *[]Finding) {
	t := st.findTable(at.Relation.Relname)
	if t == nil {
		// ALTER 目标表不存在 = 迁移序列与结构推断不一致（可能是跨
		// 库 schema 前缀错配或语料残缺）——警告但不中断。
		*findings = append(*findings, Finding{"migration", "warn",
			fmt.Sprintf("%s: ALTER TABLE %s 目标表不在采集结构里（跳过该语句）", file, at.Relation.Relname)})
		return
	}
	for _, cmd := range at.Cmds {
		c := cmd.GetAlterTableCmd()
		switch c.Subtype {
		case pg_query.AlterTableType_AT_AddColumn:
			if cd := c.Def.GetColumnDef(); cd != nil {
				col := &Column{Name: cd.Colname, Type: columnType(src, beg, end, cd)}
				for _, cn := range cd.Constraints {
					if con := cn.GetConstraint(); con != nil && con.Conname != "" {
						applyConstraint(t, con, findings)
					}
				}
				if t.findColumn(col.Name) != nil {
					// ADD COLUMN 同名列已存在（IF NOT EXISTS 语义）——
					// 保留原列（不覆盖类型，追加语义正确）。
					*findings = append(*findings, Finding{"migration", "warn",
						fmt.Sprintf("%s: ADD COLUMN %s.%s 重复（已存在，跳过）", file, t.Name, col.Name)})
					continue
				}
				t.Columns = append(t.Columns, col)
			}
		case pg_query.AlterTableType_AT_DropColumn:
			t.Columns = removeColumn(t.Columns, c.Name)
		case pg_query.AlterTableType_AT_AlterColumnType:
			if cd := c.Def.GetColumnDef(); cd != nil && cd.TypeName != nil {
				if col := t.findColumn(c.Name); col != nil {
					col.Type = columnType(src, beg, end, cd)
				} else {
					*findings = append(*findings, Finding{"migration", "warn",
						fmt.Sprintf("%s: ALTER COLUMN %s.%s 目标列不在结构里（跳过）", file, t.Name, c.Name)})
				}
			} else {
				*findings = append(*findings, Finding{"migration", "warn",
					fmt.Sprintf("%s: ALTER COLUMN %s.%s TYPE 无类型信息（跳过）", file, t.Name, c.Name)})
			}
		case pg_query.AlterTableType_AT_AddConstraint:
			if cn := c.Def.GetConstraint(); cn != nil {
				applyConstraint(t, cn, findings)
			}
		case pg_query.AlterTableType_AT_DropConstraint:
			t.dropConstraint(c.Name)
		}
		// AT_ColumnDefault/AT_DropNotNull/AT_SetNotNull/
		// AT_ValidateConstraint 不进 YAML 模型，跳过。
	}
}

// columnType 从解析树 + 源文本提取列类型的作者原始写法。
// 解析树把类型规范化为 pg_catalog 名（varchar→pg_catalog.varchar），
// 草稿要给人 review，保留源文本（bigint/numeric(20,6)/char(3)）。
// 提取失败回退解析树规范化名（fallbackType）。
func columnType(src string, beg, end int, cd *pg_query.ColumnDef) string {
	if cd.TypeName == nil {
		return ""
	}
	start := int(cd.TypeName.Location)
	// 类型起始必须落在语句边界内（防跨语句误读）。
	if start < beg || start >= end {
		return fallbackType(cd.TypeName)
	}
	// 类型 = 标识符序列（允许 schema. 限定与多词内建类型：
	// double precision / timestamp with time zone…）+ 可选 (typmods)
	// + 可选 [] 数组后缀。标识符扫描在 SQL 关键字前停下。
	i := start
	for i < end && isSpace(src[i]) {
		i++
	}
	begType := i
	for i < end {
		// 跳过空白；若下一个字符不是标识符起始，停止。
		j := i
		for j < end && isSpace(src[j]) {
			j++
		}
		if j >= end || !isIdentStart(src[j]) {
			break
		}
		// 收集一个标识符。
		k := j
		for k < end && isIdentChar(src[k]) {
			k++
		}
		if typeStopWords[strings.ToLower(src[j:k])] {
			break
		}
		i = k
		// 标识符后：空白/点 → 继续（多词内建类型 / schema 限定）；
		// '(' → typmods 开始，标识符序列结束。
		k2 := i
		for k2 < end && isSpace(src[k2]) {
			k2++
		}
		if k2 >= end || src[k2] == '(' {
			break
		}
		if src[k2] == '.' {
			i = k2 + 1
			continue
		}
		i = k2
	}
	endType := i
	// 可选 typmods：(…) 平衡括号。
	if i < end && src[i] == '(' {
		depth := 0
		for ; i < end; i++ {
			if src[i] == '(' {
				depth++
			} else if src[i] == ')' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
		}
		endType = i
	}
	// 可选数组后缀 []。
	j := endType
	for j < end && isSpace(src[j]) {
		j++
	}
	if j+1 < end && src[j] == '[' && src[j+1] == ']' {
		endType = j + 2
	}
	typ := strings.TrimSpace(src[begType:endType])
	if typ == "" {
		return fallbackType(cd.TypeName)
	}
	return typ
}

// fallbackType 解析树规范化名回退（仅当源文本提取失败时用）：
// pg_catalog.varchar → varchar；int8 → bigint 等映射保持作者习惯。
func fallbackType(tn *pg_query.TypeName) string {
	if tn == nil {
		return ""
	}
	parts := []string{}
	for _, n := range tn.Names {
		if s := n.GetString_(); s != nil {
			parts = append(parts, s.Sval)
		}
	}
	// 去掉 pg_catalog 前缀；核心类型映射回常见写法。
	name := strings.Join(parts, ".")
	switch name {
	case "pg_catalog.varchar":
		name = "varchar"
	case "pg_catalog.bpchar":
		name = "char"
	case "pg_catalog.int2":
		name = "smallint"
	case "pg_catalog.int4":
		name = "integer"
	case "pg_catalog.int8":
		name = "bigint"
	case "pg_catalog.bool":
		name = "boolean"
	case "pg_catalog.numeric":
		name = "numeric"
	case "pg_catalog.timestamptz":
		name = "timestamptz"
	}
	mods := []string{}
	for _, m := range tn.Typmods {
		if ac := m.GetAConst(); ac != nil {
			if v := ac.GetIval(); v != nil {
				mods = append(mods, strconv.FormatInt(int64(v.Ival), 10))
			}
		}
	}
	if len(mods) > 0 {
		return name + "(" + strings.Join(mods, ",") + ")"
	}
	return name
}

var typeStopWords = map[string]bool{
	"not": true, "null": true, "default": true, "constraint": true,
	"primary": true, "unique": true, "check": true, "references": true,
	"collate": true, "generated": true, "exclude": true,
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$'
}

func removeTable(tables []*Table, name string) []*Table {
	out := tables[:0]
	for _, t := range tables {
		if t.Name != name {
			out = append(out, t)
		}
	}
	return out
}

func removeColumn(cols []*Column, name string) []*Column {
	out := cols[:0]
	for _, c := range cols {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// stringList 提取 String_ 节点列表（列名列表）。
func stringList(nodes []*pg_query.Node) []string {
	out := []string{}
	for _, n := range nodes {
		if s := n.GetString_(); s != nil {
			out = append(out, s.Sval)
		}
	}
	return out
}

func lastString(nodes []*pg_query.Node) string {
	l := stringList(nodes)
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

func fkSchema(rv *pg_query.RangeVar) string {
	if rv == nil {
		return ""
	}
	return rv.Schemaname
}

func nodeAExpr(n *pg_query.Node) *pg_query.A_Expr {
	if n == nil {
		return nil
	}
	if a := n.GetAExpr(); a != nil {
		return a
	}
	return nil
}

// unwrapList 解包一层 Node_List 包装（DROP TABLE 的对象是
// List(List(String...)) 形态，解一层即得 String 表名）。
func unwrapList(n *pg_query.Node) []*pg_query.Node {
	if l := n.GetList(); l != nil {
		return l.Items
	}
	return []*pg_query.Node{n}
}
