package collector

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// gormModel 是从 Go AST 提取的一个 GORM 模型（表 + 列）。
type gormModel struct {
	Table   string
	Struct  string
	Schema  string // TableName() 带 schema 前缀时记录（bill.bills → bill）
	Columns []string
	Types   map[string]string // 列 → gorm tag 显式 type（无则缺省，不做类型比较）
}

// ExtractGormModels 从模型目录提取 GORM 模型（交叉验证第二道闸的输入）。
// 提取规则（与 GORM 映射语义对齐，但只做静态面，不做完整类型检查）：
//   - 只认显式 TableName() 的模型——没有 TableName 的结构（查询扫描
//     结构/入参 DTO 等）不是 GORM 模型，静默跳过；
//   - 列 = 具名字段（无 gorm 标签的字段 GORM 默认也映射，snake_case 命名）；
//   - `gorm:"-"` 跳过；嵌入 gorm.Model/gorm.DeletedAt 展开标准列；
//   - 同包结构嵌入递归展开（防环）；
//   - TableName() 允许 schema 限定（bill.bills），与迁移结构比对时
//     归一为表名（schema 记到 Schema 字段做提示）。
//
// 目录不存在 = error 发现（模型目录配错要显式暴露，不静默）。
func ExtractGormModels(dir string) ([]gormModel, []Finding) {
	var findings []Finding
	if _, err := os.Stat(dir); err != nil {
		return nil, []Finding{{"gorm", "error", fmt.Sprintf("读取模型目录 %s: %v", dir, err)}}
	}
	files := map[string]*ast.File{}
	collectGoFiles(dir, files, &findings)
	// 第一遍：登记全部结构（含嵌入解析的候选）。
	structs := map[string]*ast.StructType{}
	tables := map[string]string{} // 结构名 → TableName()
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					structs[ts.Name.Name] = st
				}
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// 显式 TableName() 必须返回字符串字面量（常量），
				// 非字面量（拼接/变量）无法静态提取，跳过。
				if fn := findTableName(f, ts.Name.Name); fn != "" {
					tables[ts.Name.Name] = fn
				}
			}
		}
	}
	// 第二遍：按结构名排序展开列（确定性）。
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)
	var models []gormModel
	for _, name := range names {
		table, schema := splitQualified(tables[name])
		m := gormModel{Table: table, Struct: name, Schema: schema, Types: map[string]string{}}
		cols := collectColumns(name, structs, map[string]bool{}, &findings)
		for col, typ := range cols {
			m.Columns = append(m.Columns, col)
			if typ != "" {
				m.Types[col] = typ
			}
		}
		sort.Strings(m.Columns)
		models = append(models, m)
	}
	return models, findings
}

// splitQualified 拆 TableName() 的 schema 限定（bill.bills → bills, bill）。
func splitQualified(table string) (string, string) {
	if i := strings.LastIndex(table, "."); i >= 0 {
		return table[i+1:], table[:i]
	}
	return table, ""
}

// collectColumns 展开一个结构的列（含嵌入递归）。返回 列名→显式类型。
func collectColumns(structName string, structs map[string]*ast.StructType, seen map[string]bool, findings *[]Finding) map[string]string {
	// 防环：嵌入图里回环（A 嵌入 B，B 嵌入 A）在 Go 编译期不可能，
	// 但静态解析不报错，seen 兜底。
	if seen[structName] {
		return map[string]string{}
	}
	seen[structName] = true
	defer delete(seen, structName)

	st, ok := structs[structName]
	if !ok {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// 嵌入字段。
			typ := embeddedTypeName(f.Type)
			switch typ {
			case "gorm.Model":
				for _, c := range []string{"id", "created_at", "updated_at", "deleted_at"} {
					out[c] = ""
				}
			case "gorm.DeletedAt", "DeletedAt", "gorm.Model_DeletedAt":
				out["deleted_at"] = ""
			default:
				// 同包结构嵌入 → 递归展开。
				base := baseIdent(typ)
				if base != "" {
					if _, ok := structs[base]; ok {
						for c, t := range collectColumns(base, structs, seen, findings) {
							out[c] = t
						}
					}
				}
			}
			continue
		}
		// 具名字段。
		// 关联字段（slice/map 等 = has-many/多态关联，不是列）跳过。
		if isAssociationType(f.Type) {
			continue
		}
		tag := ""
		if f.Tag != nil {
			tag = f.Tag.Value
		}
		dirs := parseGormTag(tag)
		if dirs["-"] != "" {
			continue
		}
		name := snakeCase(f.Names[0].Name)
		if c, ok := dirs["column"]; ok && c != "" {
			name = c
		}
		out[name] = dirs["type"]
	}
	return out
}

// findTableName 找 `func (x *T) TableName() string` 且返回字符串字面量。
func findTableName(f *ast.File, structName string) string {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "TableName" || len(fd.Recv.List) == 0 {
			continue
		}
		recv := fd.Recv.List[0].Type
		name := ""
		switch t := recv.(type) {
		case *ast.Ident:
			name = t.Name
		case *ast.StarExpr:
			if id, ok := t.X.(*ast.Ident); ok {
				name = id.Name
			}
		}
		if name != structName {
			continue
		}
		if ret, ok := fd.Type.Results.List[0].Type.(*ast.Ident); !ok || ret.Name != "string" {
			continue
		}
		// 返回单个字符串字面量。
		if rs, ok := fd.Body.List[0].(*ast.ReturnStmt); ok && len(rs.Results) == 1 {
			if lit, ok := rs.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					return s
				}
			}
		}
	}
	return ""
}

// parseGormTag 解析 gorm:"..." 标签 → 指令表（`-`/`column:x`/`type:x` 等）。
// 只提取交叉验证需要的指令；其余指令（index/uniqueIndex/not null…
// 进数据库 schema，不进语义结构）忽略。
func parseGormTag(tag string) map[string]string {
	out := map[string]string{}
	if !strings.Contains(tag, "gorm:") {
		return out
	}
	rest := tag
	for {
		idx := strings.Index(rest, `gorm:"`)
		if idx < 0 {
			break
		}
		rest = rest[idx+len(`gorm:"`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			break
		}
		body := rest[:end]
		rest = rest[end+1:]
		for _, part := range strings.Split(body, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "-" {
				out["-"] = "true"
				continue
			}
			kv := strings.SplitN(part, ":", 2)
			if len(kv) == 2 {
				out[kv[0]] = kv[1]
			} else {
				out[part] = ""
			}
		}
	}
	return out
}

func embeddedTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name + "." + t.Sel.Name
		}
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedTypeName(t.X)
	}
	return ""
}

func baseIdent(typ string) string {
	if i := strings.Index(typ, "."); i >= 0 {
		return typ[i+1:]
	}
	return typ
}

// snakeCase 是 GORM 默认命名（CamelCase → snake_case）：
// UserID → user_id；APIKey → api_key；URL → url。
func snakeCase(s string) string {
	runes := []rune(s)
	var out []rune
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				prevLower := unicode.IsLower(prev) || unicode.IsDigit(prev)
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if prevLower || nextLower {
					out = append(out, '_')
				}
			}
			out = append(out, unicode.ToLower(r))
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

// collectGoFiles 递归收集目录树里的 .go 文件（排除 _test.go），
// 子目录（如 iam 的 internal/data/invite/）也是服务模型的一部分。
func collectGoFiles(dir string, files map[string]*ast.File, findings *[]Finding) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // 顶层目录缺失已在调用方报 error；子目录缺失静默
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			collectGoFiles(path, files, findings)
			continue
		}
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			*findings = append(*findings, Finding{"gorm", "warn",
				fmt.Sprintf("解析 %s 失败（跳过该文件）: %v", path, perr)})
			continue
		}
		files[path] = f
	}
}

// isAssociationType 判断字段类型是否为 GORM 关联（slice/map/chan/
// func/interface——关联不是列，不能进交叉验证）。
func isAssociationType(t ast.Expr) bool {
	switch t.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType:
		return true
	case *ast.StarExpr:
		return isAssociationType(t.(*ast.StarExpr).X)
	}
	return false
}
