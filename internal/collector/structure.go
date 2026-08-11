// Package collector 是知识采集器（ADR-0007）的域：结构知识自动、
// 语义知识人工。本包负责确定性解析服务仓库 migration 文件 → 结构
// 中间态 → 语义作者入口 YAML 草稿（services/<service>.yaml，与
// 07 同步管线的 Load/Compile 兼容），并以 GORM 模型交叉验证为第二道闸。
//
// 分工（ADR-0007「结构自动、语义人工」）：
//   - 结构：表/列/类型/主外键/CHECK 枚举/引用边 = 自动采集（本包）；
//   - 语义：描述/is_time/枚举 label/口径 = 人工（采集草稿留空）；
//   - calibrate 子命令 = 按需连只读从库做生产校准（v1 低优先）。
//
// 确定性契约：同输入（迁移语料 + manifest）同输出（草稿字节 + 交叉
// 验证发现）——neo-cloud 真实迁移语料即 golden test 集。
package collector

import (
	"fmt"
	"sort"
)

// Structure 是一个服务采集后的结构中间态（一个服务一个库的生产形态，
// ADR-0007「每服务一库/schema 前缀」）。
type Structure struct {
	Service string
	DB      string
	Tables  []*Table // 按名字典序（草稿写出前排序，diff 稳定）
}

// Table 是采集到的一张表（列保持 DDL 出现顺序：CREATE 顺序 + ALTER 追加）。
type Table struct {
	Name       string // 表名（不含 schema 前缀）
	Schema     string // PG schema；"" = public（缺省按 dbname 路由推断）
	Columns    []*Column
	References []*Reference

	// constraints 是约束登记表（约束名 → 状态）：ALTER DROP CONSTRAINT
	// 需要撤掉对应的枚举/引用边；YAML 草稿本身不承载约束名，登记表只
	// 存在于采集过程。
	constraints map[string]constraintState
}

// Column 是采集到的一列。
type Column struct {
	Name       string
	Type       string // 迁移源文本的原始类型写法（如 varchar(64)）
	EnumValues []string
}

// Reference 是一条外键推导的引用边（表↔表 join 条件）。
type Reference struct {
	TargetSchema string
	TargetTable  string
	Cols         []string // 本表外键列
	PkCols       []string // 目标表被引用列
}

// On 生成 join 条件（确定性：列对按外键声明顺序拼接）。
// 列对不齐（REFERENCES 简写未指定目标列，PG 合法形态）时返回 ""
// ——目标主键列静态不可知，留空由人工补（编译校验跳过空 on）。
func (r *Reference) On(srcTable string) string {
	if len(r.Cols) == 0 || len(r.Cols) != len(r.PkCols) {
		return ""
	}
	out := ""
	for i := range r.Cols {
		if i > 0 {
			out += " AND "
		}
		out += srcTable + "." + r.Cols[i] + " = " + r.TargetTable + "." + r.PkCols[i]
	}
	return out
}

// constraintState 是约束登记状态（枚举/外键两类需要增量撤除）。
type constraintState struct {
	kind         string // "enum" | "fk" | "other"
	column       string
	values       []string
	targetSchema string
	targetTable  string
	cols         []string
	pkCols       []string
}

// findTable 按名找表（s 为 "" 时忽略 schema 匹配——同库内表名唯一）。
func (s *Structure) findTable(name string) *Table {
	for _, t := range s.Tables {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// findColumn 按名找列。
func (t *Table) findColumn(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// addConstraint 登记约束（同名列覆盖——重建语义，最后定义生效）。
func (t *Table) addConstraint(name string, st constraintState) {
	if t.constraints == nil {
		t.constraints = map[string]constraintState{}
	}
	t.constraints[name] = st
}

// dropConstraint 撤掉约束及其结构效果（枚举值/引用边）。
// 枚举值从剩余枚举约束重建（同列可能挂多个枚举 CHECK，撤一个不能
// 清掉另一个加的取值——增量语义）。
func (t *Table) dropConstraint(name string) {
	st, ok := t.constraints[name]
	if !ok {
		return
	}
	switch st.kind {
	case "enum":
		if c := t.findColumn(st.column); c != nil {
			c.EnumValues = nil
		}
	case "fk":
		t.References = removeReference(t.References, st)
	}
	delete(t.constraints, name)
	if st.kind == "enum" {
		// 重建该列的枚举值（保留的约束按名字典序，确定性）。
		names := make([]string, 0, len(t.constraints))
		for n, s := range t.constraints {
			if s.kind == "enum" && s.column == st.column {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		if c := t.findColumn(st.column); c != nil {
			for _, n := range names {
				attachEnumValues(c, t.constraints[n].values)
			}
		}
	}
}

// removeReference 移除指向同一目标表/列对的引用边（DROP CONSTRAINT 幂等）。
func removeReference(refs []*Reference, st constraintState) []*Reference {
	out := refs[:0]
	for _, r := range refs {
		if r.TargetTable == st.targetTable && r.TargetSchema == st.targetSchema &&
			eqStrings(r.Cols, st.cols) && eqStrings(r.PkCols, st.pkCols) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortTables 按名字典序排表（确定性 + diff 稳定）。
func (s *Structure) sortTables() {
	for i := 1; i < len(s.Tables); i++ {
		for j := i; j > 0 && s.Tables[j].Name < s.Tables[j-1].Name; j-- {
			s.Tables[j], s.Tables[j-1] = s.Tables[j-1], s.Tables[j]
		}
	}
}

// stats 返回结构统计（CLI 输出用）。
func (s *Structure) stats() (tables, columns, enums, refs int) {
	for _, t := range s.Tables {
		tables++
		refs += len(t.References)
		for _, c := range t.Columns {
			columns++
			enums += len(c.EnumValues)
		}
	}
	return
}

// attachEnumValues 把值并入列（去重 + 排序，确定性）。
func attachEnumValues(c *Column, values []string) {
	seen := map[string]bool{}
	for _, v := range c.EnumValues {
		seen[v] = true
	}
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		c.EnumValues = append(c.EnumValues, v)
	}
	sort.Strings(c.EnumValues)
}

// Severity 是发现级别（error = 门禁失败 / warn = 提示 / info = 信息）。
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
	SeverityInfo  Severity = "info"
)

// Source 是发现来源（迁移解析 / GORM 交叉验证 / 生产校准）。
type Source string

const (
	SourceMigration Source = "migration"
	SourceGORM      Source = "gorm"
	SourceCalibrate Source = "calibrate"
)

// Finding 是一条采集/交叉验证/校准发现（门禁与报告共用）。
type Finding struct {
	Source   Source
	Severity Severity
	Message  string
}

func (f Finding) String() string {
	return fmt.Sprintf("[%s/%s] %s", f.Severity, f.Source, f.Message)
}
