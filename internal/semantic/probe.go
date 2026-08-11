package semantic

import (
	"fmt"

	wasm "github.com/wasilibs/go-pgquery"
)

// parseProbe 用 PG 解析器（wasilibs/go-pgquery，校验层同款，cgo-free）验证
// 一段 SQL 可解析。指标 expression 校验的入口（compile.checkMetricSQL）。
func parseProbe(sql string) error {
	if _, err := wasm.Parse(sql); err != nil {
		return fmt.Errorf("SQL 不可解析: %w", err)
	}
	return nil
}
