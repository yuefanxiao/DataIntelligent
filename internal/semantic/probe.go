package semantic

import (
	"fmt"

	wasm "github.com/wasilibs/go-pgquery"
)

// parseProbe 用 PG 解析器（wasilibs/go-pgquery，校验层同款，cgo-free）验证
// 一段 SQL 可解析。指标 expression/filter 与 references on 条件的编译期
// 校验入口。
//
// 单语句强制：探针必须恰好解析出 1 条语句——多语句输入（如
// "SELECT 1; DROP TABLE t"）虽然语法合法，但口径校验只验证单个表达式/
// 条件，放行多语句会让不可预期语句绕过编译期闸门（review 修复）。
func parseProbe(sql string) error {
	tree, err := wasm.Parse(sql)
	if err != nil {
		return fmt.Errorf("SQL 不可解析: %w", err)
	}
	if n := len(tree.Stmts); n != 1 {
		return fmt.Errorf("SQL 应为单条语句，收到 %d 条（多语句不参与口径校验）", n)
	}
	return nil
}
