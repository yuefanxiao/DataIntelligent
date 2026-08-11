package main

// 断言核心的单元测试（「测测试者」：compareCell 的类型归一化是验收闸最
// 微妙的代码——假失败是响的，错误归一化导致的静默假通过才是危险方向）。
// 不依赖网关/数据库：纯函数测试。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompareCellInt(t *testing.T) {
	for _, tc := range []struct {
		typ, psql string
		gw        any
		wantOK    bool
	}{
		{"int8", "600", json.Number("600"), true},
		{"int4", "42", json.Number("42"), true},
		{"int8", "600", json.Number("601"), false},
		{"int8", "abc", json.Number("1"), false},
		{"int8", "600", "600", false}, // 非数字值
	} {
		got := compareCell(tc.typ, tc.psql, tc.gw)
		if (got == "") != tc.wantOK {
			t.Errorf("compareCell(%s, %q, %v) = %q, wantOK=%v", tc.typ, tc.psql, tc.gw, got, tc.wantOK)
		}
	}
}

func TestCompareCellNumeric(t *testing.T) {
	// numeric：big.Rat 精确比较（1.50 == 1.5，字符串形式不同也一致）
	if got := compareCell("numeric", "1.50", json.Number("1.5")); got != "" {
		t.Errorf("numeric 1.50 vs 1.5: %q", got)
	}
	if got := compareCell("numeric", "1.50", json.Number("1.51")); got == "" {
		t.Error("numeric 1.50 vs 1.51 应不一致")
	}
	if got := compareCell("numeric", "1.50", "1.50"); got != "" {
		t.Errorf("numeric 字符串形态: %q", got)
	}
}

func TestCompareCellBool(t *testing.T) {
	if got := compareCell("bool", "t", true); got != "" {
		t.Errorf("bool t/true: %q", got)
	}
	if got := compareCell("bool", "f", false); got != "" {
		t.Errorf("bool f/false: %q", got)
	}
	if got := compareCell("bool", "t", false); got == "" {
		t.Error("bool t/false 应不一致")
	}
}

func TestCompareCellText(t *testing.T) {
	if got := compareCell("text", "paid", "paid"); got != "" {
		t.Errorf("text: %q", got)
	}
	if got := compareCell("varchar", "a", "b"); got == "" {
		t.Error("varchar a/b 应不一致")
	}
}

func TestCompareCellTimestamptz(t *testing.T) {
	// psql 侧可变小数位 + 无冒号时区；网关侧 RFC3339Nano——同一时刻的
	// 多种渲染都要判一致。
	for _, tc := range []struct {
		psql, gw string
	}{
		{"2026-08-12 09:58:00.123456+00", "2026-08-12T09:58:00.123456Z"},
		{"2026-08-12 09:58:00+00", "2026-08-12T09:58:00Z"},
		{"2026-08-12 17:58:00.123+08", "2026-08-12T09:58:00.123Z"},
		{"2026-08-12 09:58:00.123456+00", "2026-08-12T09:58:00.123456+00:00"},
	} {
		if got := compareCell("timestamptz", tc.psql, tc.gw); got != "" {
			t.Errorf("timestamptz %q vs %q: %q", tc.psql, tc.gw, got)
		}
	}
	if got := compareCell("timestamptz", "2026-08-12 09:58:00+00", "2026-08-12T10:00:00Z"); got == "" {
		t.Error("timestamptz 不同时刻应不一致")
	}
}

func TestCompareCellDate(t *testing.T) {
	// PG date：psql 渲染 YYYY-MM-DD，网关 pgx 解码为 time.Time → JSON
	// RFC3339（验收环境 UTC）——日历日归一化比较（矩阵的按天聚合用例
	// date_trunc('day', …)::date 全走此类型）。
	for _, tc := range []struct {
		psql, gw string
		wantOK   bool
	}{
		{"2026-08-04", "2026-08-04T00:00:00Z", true},
		{"2026-08-04", "2026-08-04T00:00:00+00:00", true},
		{"2026-08-04", "2026-08-05T00:00:00Z", false},
		{"2026-08-04", "2026-08-04T00:00:00", false}, // 非 RFC3339（缺时区）
		{"2026-08-04", "2026-08-04", false},           // 网关侧不应出现 date-only 渲染
	} {
		got := compareCell("date", tc.psql, tc.gw)
		if (got == "") != tc.wantOK {
			t.Errorf("date %q vs %q = %q, wantOK=%v", tc.psql, tc.gw, got, tc.wantOK)
		}
	}
}

func TestCompareCellNull(t *testing.T) {
	if got := compareCell("text", "", nil); got != "" {
		t.Errorf("null 对照: %q", got)
	}
	// NULL vs 空字符串：psql CSV 无法区分（都渲染空字段）→ 假失败方向报不一致
	if got := compareCell("text", "", ""); got == "" {
		t.Error("psql 空 vs 网关空串应报不一致（已知边界：安全方向）")
	}
}

func TestCompareCellUUIDBytea(t *testing.T) {
	if got := compareCell("uuid", "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"); got != "" {
		t.Errorf("uuid 大小写: %q", got)
	}
	if got := compareCell("bytea", `\x4142`, `\x4142`); got != "" {
		t.Errorf("bytea: %q", got)
	}
}

func TestCompareCellJSON(t *testing.T) {
	// jsonb 语义相等：键序/空白不敏感
	if got := compareCell("jsonb", `{"a": 1, "b": [1, 2]}`, `{"b":[1,2],"a":1}`); got != "" {
		t.Errorf("jsonb 语义相等: %q", got)
	}
	if got := compareCell("json", `{"a": 1}`, `{"a": 2}`); got == "" {
		t.Error("json 不同值应不一致")
	}
	if got := compareCell("jsonb", `{"a": 1}`, `not json`); got == "" {
		t.Error("网关侧非法 JSON 应不一致")
	}
}

func TestCompareCellUnknownType(t *testing.T) {
	// 未覆盖类型：严格文本对照（宁可失败不静默放行）
	if got := compareCell("geometry", "POINT(1 1)", "POINT(1 1)"); got != "" {
		t.Errorf("未知类型文本一致应通过: %q", got)
	}
	if got := compareCell("geometry", "POINT(1 1)", "POINT(2 2)"); got == "" {
		t.Error("未知类型文本不一致应失败")
	}
}

func TestParsePSQLTime(t *testing.T) {
	for _, s := range []string{
		"2026-08-12 09:58:00.123456+00",
		"2026-08-12 09:58:00+00",
		"2026-08-12 09:58:00.123456789+00",
		"2026-08-12 17:58:00.123+08",
		"2026-08-12 09:58:00.123456+08:00",
	} {
		if _, err := parsePSQLTime(s); err != nil {
			t.Errorf("parsePSQLTime(%q) 失败: %v", s, err)
		}
	}
	if _, err := parsePSQLTime("not-a-time"); err == nil {
		t.Error("非法时间应失败")
	}
}

func TestResolvePath(t *testing.T) {
	doc := parseJSONDoc(`{"total": 0, "meta": {"row_count": 2}, "hits": [{"fqn": "a.b.c", "kind": "table"}]}`)
	for _, tc := range []struct {
		path   string
		want   any
		wantOK bool
	}{
		{"total", 0, true},
		{"meta.row_count", 2, true},
		{"hits[0].fqn", "a.b.c", true},
		{"hits[0].kind", "table", true},
		{"missing", nil, false},
		{"meta.missing", nil, false},
		{"hits[5]", nil, false},
	} {
		got, ok := resolvePath(doc, tc.path)
		if ok != tc.wantOK {
			t.Errorf("resolvePath(%q) ok=%v want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if ok && !eqValue(got, tc.want) {
			t.Errorf("resolvePath(%q) = %v want %v", tc.path, got, tc.want)
		}
	}
}

func TestEqValue(t *testing.T) {
	cases := []struct {
		got, want any
		wantOK    bool
	}{
		{json.Number("0"), 0, true},
		{json.Number("2"), 2, true},
		{json.Number("2.5"), 2.5, true},
		{json.Number("3"), 2, false},
		{"refunded", "refunded", true},
		{"a", "b", false},
		{true, true, true},
		{true, false, false},
		{nil, nil, true},
		{"x", nil, false},
	}
	for _, tc := range cases {
		if got := eqValue(tc.got, tc.want); got != tc.wantOK {
			t.Errorf("eqValue(%v, %v) = %v want %v", tc.got, tc.want, got, tc.wantOK)
		}
	}
}

func TestJSONSemanticEqual(t *testing.T) {
	if !jsonSemanticEqual(`{"a": 1, "b": [1, 2]}`, `{"b":[1,2],"a":1}`) {
		t.Error("键序不同应语义相等")
	}
	if jsonSemanticEqual(`{"a": 1}`, `{"a": 2}`) {
		t.Error("值不同应不等")
	}
	if jsonSemanticEqual(`{"a": 1}`, `not json`) {
		t.Error("非法 JSON 应不等")
	}
}

func TestCanonEqual(t *testing.T) {
	a := map[string]any{"sql": "SELECT 1", "dbname": "bill"}
	b := map[string]any{"dbname": "bill", "sql": "SELECT 1"}
	if !canonEqual(a, b) {
		t.Error("键序不同的 map 应规范相等")
	}
	b["sql"] = "SELECT 2"
	if canonEqual(a, b) {
		t.Error("值不同应不等")
	}
}

func TestParseSQLResult(t *testing.T) {
	// execute_sql 结果：解析成功
	sqlRes := `{"columns":[{"name":"n","type":"int8"}],"rows":[[600]],"meta":{"row_count":1,"truncated":false,"dbname":"bill"}}`
	r, ok := parseSQLResult(sqlRes)
	if !ok || r.Meta.RowCount != 1 || r.Meta.Truncated {
		t.Fatalf("SQL 结果解析失败: ok=%v r=%+v", ok, r)
	}
	// 语义工具结果（无 columns）：必须判为「非 SQL 结果」
	// ——否则 search 结果被误判成 0 行成功，破坏记录对照。
	if _, ok := parseSQLResult(`{"hits":[],"total":0}`); ok {
		t.Error("非 SQL 结果不应解析成功")
	}
	// 0 行 SQL 结果仍有列清单
	empty := `{"columns":[{"name":"n","type":"int8"}],"rows":[],"meta":{"row_count":0,"truncated":false}}`
	if _, ok := parseSQLResult(empty); !ok {
		t.Error("0 行 SQL 结果应解析成功")
	}
}

func TestParseJSONLLine(t *testing.T) {
	line := `{"kind":"tool_call","ts":"2026-08-12T09:58:00+00:00","user":"dev-alice","tool":"execute_sql","params":{"sql":"SELECT 1"},"stages_ms":{"exec":1},"status":"success","rows":1,"truncated":false}`
	rec := parseJSONLLine([]byte(line))
	if rec == nil || rec.Kind != "tool_call" || rec.Tool != "execute_sql" || rec.User != "dev-alice" {
		t.Fatalf("解析失败: %+v", rec)
	}
	if rec.Rows == nil || *rec.Rows != 1 {
		t.Error("rows 解析失败")
	}
	if parseJSONLLine([]byte("not json")) != nil {
		t.Error("坏行应返回 nil")
	}
	if parseJSONLLine([]byte(strings.TrimSpace(""))) != nil {
		t.Error("空行应返回 nil")
	}
}
