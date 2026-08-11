package collector

import (
	"fmt"
	"sort"
	"strings"
)

// CrossCheck 是 GORM 交叉验证（第二道闸）：迁移推导的结构（主干真相，
// ADR-0007「migration 文件为主干」）vs GORM 模型（代码侧真相）。
//
// 严重度语义：
//   - error = 模型有而迁移没有（代码查的表/列在采集结构里不存在：
//     要么模型漂移，要么迁移语料漏采——门禁必须失败，交给人确认）；
//   - warn  = 迁移有而模型没有（GORM 不覆盖全部表/列属正常，提示性）。
//
// 返回按（severity, message）排序的发现，确定性输出。
func CrossCheck(st *Structure, models []gormModel) []Finding {
	var findings []Finding

	// 模型表索引（表名 → 模型）。
	modelTables := map[string]*gormModel{}
	var modelNames []string
	for i := range models {
		m := &models[i]
		modelTables[m.Table] = m
		modelNames = append(modelNames, m.Table)
	}

	// 迁移表索引。
	structTables := map[string]*Table{}
	for _, t := range st.Tables {
		structTables[t.Name] = t
	}

	for _, name := range modelNames {
		m := modelTables[name]
		t, ok := structTables[name]
		if !ok {
			findings = append(findings, Finding{"gorm", "error",
				fmt.Sprintf("模型表 %s（%s）不在迁移结构里（模型漂移或迁移语料漏采）", name, m.Struct)})
			continue
		}
		// 列比对。
		structCols := map[string]*Column{}
		for _, c := range t.Columns {
			structCols[c.Name] = c
		}
		for _, col := range m.Columns {
			sc, ok := structCols[col]
			if !ok {
				findings = append(findings, Finding{"gorm", "error",
					fmt.Sprintf("模型列 %s.%s（%s）不在迁移结构里（模型漂移或迁移漏采）", name, col, m.Struct)})
				continue
			}
			if mt, ok := m.Types[col]; ok && sc.Type != "" && !typeEquivalent(sc.Type, mt) {
				findings = append(findings, Finding{"gorm", "warn",
					fmt.Sprintf("列 %s.%s 类型不一致：迁移=%s 模型=%s", name, col, sc.Type, mt)})
			}
		}
		for _, c := range t.Columns {
			if !containsStr(m.Columns, c.Name) {
				findings = append(findings, Finding{"gorm", "warn",
					fmt.Sprintf("迁移列 %s.%s 无模型映射（模型未使用该列，属正常）", name, c.Name)})
			}
		}
	}

	for _, t := range st.Tables {
		if _, ok := modelTables[t.Name]; !ok {
			findings = append(findings, Finding{"gorm", "warn",
				fmt.Sprintf("迁移表 %s 无 GORM 模型覆盖（种子/纯迁移表属正常）", t.Name)})
		}
	}

	sortFindings(findings)
	return findings
}

// typeEquivalent 做宽松类型等价比较（去掉空白、大小写），
// 用于迁移类型 vs 模型 gorm type 标签。
func typeEquivalent(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
	return norm(a) == norm(b)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sortFindings 按（severity, message）排序——确定性输出契约。
func sortFindings(fs []Finding) {
	sev := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.Slice(fs, func(i, j int) bool {
		if sev[fs[i].Severity] != sev[fs[j].Severity] {
			return sev[fs[i].Severity] < sev[fs[j].Severity]
		}
		return fs[i].Message < fs[j].Message
	})
}

// countSeverity 统计某严重度数量。
func countSeverity(fs []Finding, sev string) int {
	n := 0
	for _, f := range fs {
		if f.Severity == sev {
			n++
		}
	}
	return n
}
