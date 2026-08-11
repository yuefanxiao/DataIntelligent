package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseTypeExtraction 覆盖类型原文提取：schema 限定、typmods、
// 多词内建类型、数组、关键字边界。
func TestParseTypeExtraction(t *testing.T) {
	cases := []struct {
		name, ddl, want string
	}{
		{"varchar mods", "CREATE TABLE t (a varchar(64));", "varchar(64)"},
		{"numeric mods", "CREATE TABLE t (a numeric(20,6) NOT NULL);", "numeric(20,6)"},
		{"char mods", "CREATE TABLE t (a char(3) DEFAULT 'CNY');", "char(3)"},
		{"bigint", "CREATE TABLE t (a bigint NOT NULL);", "bigint"},
		{"timestamptz", "CREATE TABLE t (a timestamptz DEFAULT NOW());", "timestamptz"},
		{"jsonb default", "CREATE TABLE t (a jsonb NOT NULL DEFAULT '[]');", "jsonb"},
		{"multiword", "CREATE TABLE t (a timestamp with time zone NOT NULL);", "timestamp with time zone"},
		{"multiword2", "CREATE TABLE t (a double precision);", "double precision"},
		{"array", "CREATE TABLE t (a text[]);", "text[]"},
		{"qualified", "CREATE TABLE s.t (a varchar(16) NOT NULL, b uuid);", "varchar(16)"},
		{"collate", `CREATE TABLE t (a varchar(16) COLLATE "C");`, "varchar(16)"},
		{"generated", "CREATE TABLE t (a varchar(64) GENERATED ALWAYS AS (b) STORED);", "varchar(64)"},
		{"reference default", "CREATE TABLE t (a varchar(64) DEFAULT 'x' REFERENCES o (id));", "varchar(64)"},
		{"pgcrypto ext type", "CREATE TABLE t (a bytea);", "bytea"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := parseOne(t, tc.ddl)
			col := st.findTable("t").findColumn("a")
			if col == nil {
				t.Fatalf("列 a 未解析: %s", tc.ddl)
			}
			if col.Type != tc.want {
				t.Errorf("类型 = %q, want %q", col.Type, tc.want)
			}
		})
	}
}

// TestParseEnumExtraction CHECK IN 枚举提取与形态跳过。
func TestParseEnumExtraction(t *testing.T) {
	ddl := `CREATE TABLE t (
		status smallint NOT NULL,
		kind varchar(16) NOT NULL,
		amount numeric NOT NULL,
		CONSTRAINT ck_status CHECK (status IN (1, 2, 3)),
		CONSTRAINT ck_kind CHECK (kind IN ('a', 'b', 'a')),
		CONSTRAINT ck_amount CHECK (amount >= 0),
		CONSTRAINT ck_compound CHECK (status <> 2 OR amount > 0)
	);`
	st, _ := parseOne(t, ddl)
	tbl := st.findTable("t")
	status := tbl.findColumn("status")
	want := []string{"1", "2", "3"}
	if !eqStrings(status.EnumValues, want) {
		t.Errorf("status 枚举 = %v, want %v", status.EnumValues, want)
	}
	kind := tbl.findColumn("kind")
	if !eqStrings(kind.EnumValues, []string{"a", "b"}) {
		t.Errorf("kind 枚举 = %v（应去重排序）, want [a b]", kind.EnumValues)
	}
	if len(tbl.findColumn("amount").EnumValues) != 0 {
		t.Error("CHECK (amount >= 0) 不是枚举，不应提取")
	}
	if !eqStrings(status.EnumValues, []string{"1", "2", "3"}) {
		t.Error("复合 CHECK 不应覆盖已提取的枚举")
	}
}

// TestParseEmptyEnumValue 空字符串枚举值跳过 + warn（07 编译校验要求非空）。
func TestParseEmptyEnumValue(t *testing.T) {
	ddl := `CREATE TABLE t (
		actor varchar(16) NOT NULL DEFAULT '',
		CONSTRAINT ck_actor CHECK (actor IN ('', 'user', 'admin'))
	);`
	st, findings := parseOne(t, ddl)
	actor := st.findTable("t").findColumn("actor")
	if !eqStrings(actor.EnumValues, []string{"admin", "user"}) {
		t.Errorf("空字符串枚举值应跳过: %v", actor.EnumValues)
	}
	found := false
	for _, f := range findings {
		if f.Severity == "warn" && strings.Contains(f.Message, "空字符串") {
			found = true
		}
	}
	if !found {
		t.Error("应有空字符串枚举跳过的 warn 发现")
	}
}

// TestParseAlterSequence 增量迁移：ADD/DROP COLUMN、ALTER TYPE、
// ADD/DROP CONSTRAINT（枚举与外键可撤）、DROP TABLE。
func TestParseAlterSequence(t *testing.T) {
	ddl := `BEGIN;
	CREATE TABLE t (
		id bigint NOT NULL,
		status smallint NOT NULL,
		other_id bigint,
		CONSTRAINT pk_t PRIMARY KEY (id),
		CONSTRAINT ck_t_status CHECK (status IN (1, 2)),
		CONSTRAINT fk_t_other FOREIGN KEY (other_id) REFERENCES u (id)
	);
	CREATE TABLE u (id bigint NOT NULL);
	ALTER TABLE t ADD COLUMN note varchar(64);
	ALTER TABLE t DROP COLUMN note;
	ALTER TABLE t ALTER COLUMN status TYPE varchar(4) USING status::text;
	ALTER TABLE t ADD CONSTRAINT ck_t_status2 CHECK (status IN ('ok', 'bad'));
	ALTER TABLE t DROP CONSTRAINT ck_t_status;
	ALTER TABLE t DROP CONSTRAINT fk_t_other;
	ALTER TABLE u DROP COLUMN id;
	COMMIT;`
	st, findings := parseOne(t, ddl)
	tbl := st.findTable("t")
	status := tbl.findColumn("status")
	if status.Type != "varchar(4)" {
		t.Errorf("ALTER TYPE 后类型 = %q, want varchar(4)", status.Type)
	}
	if tbl.findColumn("note") != nil {
		t.Error("DROP COLUMN 后 note 应不存在")
	}
	if !eqStrings(status.EnumValues, []string{"bad", "ok"}) {
		t.Errorf("撤旧约束后枚举 = %v, want [bad ok]", status.EnumValues)
	}
	if len(tbl.References) != 0 {
		t.Errorf("撤外键后 references = %v, want 空", tbl.References)
	}
	if st.findTable("u").findColumn("id") != nil {
		t.Error("DROP COLUMN u.id 后应不存在")
	}
	if len(findings) != 0 {
		t.Errorf("本用例不应有发现: %v", findings)
	}
}

// TestParseDropTable DROP TABLE 移除表。
func TestParseDropTable(t *testing.T) {
	ddl := `CREATE TABLE keep (id bigint);
	CREATE TABLE gone (id bigint);
	DROP TABLE gone;`
	st, _ := parseOne(t, ddl)
	if st.findTable("gone") != nil {
		t.Error("DROP TABLE 后 gone 应不存在")
	}
	if st.findTable("keep") == nil {
		t.Error("keep 应保留")
	}
}

// TestParseSchemaAndTable 生产形态：schema 前缀进表、public 缺省空。
func TestParseSchemaAndTable(t *testing.T) {
	ddl := `CREATE TABLE wallet.accounts (id bigint);
	CREATE TABLE plain (id bigint);`
	st, _ := parseOne(t, ddl)
	if st.findTable("accounts").Schema != "wallet" {
		t.Error("schema 前缀应记录")
	}
	if st.findTable("plain").Schema != "" {
		t.Error("public 表 schema 应为空")
	}
}

// TestParseForeignReference 外键 → 引用边（join 条件确定性生成）。
func TestParseForeignReference(t *testing.T) {
	ddl := `CREATE TABLE orders (id bigint);
	CREATE TABLE payments (
		id bigint,
		order_id bigint,
		shop_id bigint,
		CONSTRAINT fk_pay_order FOREIGN KEY (order_id, shop_id) REFERENCES orders (id, shop_id)
	);`
	st, _ := parseOne(t, ddl)
	refs := st.findTable("payments").References
	if len(refs) != 1 {
		t.Fatalf("references = %d, want 1", len(refs))
	}
	on := refs[0].On("payments")
	want := "payments.order_id = orders.id AND payments.shop_id = orders.shop_id"
	if on != want {
		t.Errorf("on = %q, want %q", on, want)
	}
}

// TestParseInvalidSQL 解析失败 = error 发现（不 panic）。
func TestParseInvalidSQL(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "0001_broken.up.sql")
	os.WriteFile(f, []byte("CREATE TABLE t ("), 0o644)
	st, findings := ParseMigrations("s", "d", []string{f})
	if st.findTable("t") != nil {
		t.Error("语法错误文件不应产出表")
	}
	hasErr := false
	for _, ff := range findings {
		if ff.Severity == "error" {
			hasErr = true
		}
	}
	if !hasErr {
		t.Error("语法错误应产生 error 发现")
	}
}

// parseOne 解析单条 DDL 文本（helper）。
func parseOne(t *testing.T, ddl string) (*Structure, []Finding) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "0001_test.up.sql")
	if err := os.WriteFile(f, []byte(ddl), 0o644); err != nil {
		t.Fatal(err)
	}
	return ParseMigrations("svc", "db", []string{f})
}

// TestParseFkNoTargetColumns REFERENCES 简写（未指定目标列）——
// 曾经触发 On() 越界 panic 的形态；on 条件留空 + warn。
func TestParseFkNoTargetColumns(t *testing.T) {
	ddl := `CREATE TABLE u (id bigint);
	CREATE TABLE t (u_id bigint REFERENCES u);`
	st, findings := parseOne(t, ddl)
	refs := st.findTable("t").References
	if len(refs) != 1 {
		t.Fatalf("references = %d, want 1", len(refs))
	}
	if refs[0].On("t") != "" {
		t.Errorf("未指定目标列的外键 on 应留空, got %q", refs[0].On("t"))
	}
	warn := false
	for _, f := range findings {
		if f.Severity == SeverityWarn && strings.Contains(f.Message, "未指定目标列") {
			warn = true
		}
	}
	if !warn {
		t.Errorf("应有「未指定目标列」warn: %v", findings)
	}
}

// TestParseFkAutoNameDrop 内联匿名外键按 PG 自动名登记：
// DROP CONSTRAINT <表>_<列>_fkey 能撤掉引用边。
func TestParseFkAutoNameDrop(t *testing.T) {
	ddl := `CREATE TABLE u (id bigint);
	CREATE TABLE t (u_id bigint REFERENCES u(id));
	ALTER TABLE t DROP CONSTRAINT t_u_id_fkey;`
	st, _ := parseOne(t, ddl)
	if len(st.findTable("t").References) != 0 {
		t.Error("按 PG 自动名 DROP CONSTRAINT 后引用边应被撤掉")
	}
}

// TestParseAddColumnIfNotExistsNoop ADD COLUMN IF NOT EXISTS 对已存在
// 列整体 no-op（该语句自带的列级约束不生效；同一语句里独立的
// ADD CONSTRAINT 命令照常生效——那是另一条命令，PG 语义如此）。
func TestParseAddColumnIfNotExistsNoop(t *testing.T) {
	ddl := `CREATE TABLE t (status smallint NOT NULL);
	ALTER TABLE t ADD COLUMN IF NOT EXISTS status smallint CHECK (status IN (9, 8));`
	st, _ := parseOne(t, ddl)
	col := st.findTable("t").findColumn("status")
	if len(col.EnumValues) != 0 {
		t.Errorf("IF NOT EXISTS no-op 语句不应注入枚举: %v", col.EnumValues)
	}
	// 同语句的独立 ADD CONSTRAINT 命令照常生效。
	ddl2 := `CREATE TABLE t (status smallint NOT NULL);
	ALTER TABLE t ADD COLUMN IF NOT EXISTS status smallint,
		ADD CONSTRAINT ck_status2 CHECK (status IN (9, 8));`
	st2, _ := parseOne(t, ddl2)
	if !eqStrings(st2.findTable("t").findColumn("status").EnumValues, []string{"8", "9"}) {
		t.Errorf("独立 ADD CONSTRAINT 应生效: %v", st2.findTable("t").findColumn("status").EnumValues)
	}
}

// TestParseRenameWarn RENAME COLUMN/TABLE 未处理 → warn 发现。
func TestParseRenameWarn(t *testing.T) {
	ddl := `CREATE TABLE t (a bigint);
	ALTER TABLE t RENAME COLUMN a TO b;`
	_, findings := parseOne(t, ddl)
	warn := false
	for _, f := range findings {
		if f.Severity == SeverityWarn && strings.Contains(f.Message, "改名") {
			warn = true
		}
	}
	if !warn {
		t.Errorf("RENAME COLUMN 应有 warn: %v", findings)
	}
}

// TestParseUnhandledAlterWarn 未处理的结构影响型 ALTER 子类型 → warn。
func TestParseUnhandledAlterWarn(t *testing.T) {
	ddl := `CREATE TABLE t (a bigint);
	ALTER TABLE t RENAME TO t2;`
	st, findings := parseOne(t, ddl)
	if st.findTable("t2") != nil {
		t.Error("RENAME TABLE 未处理，表名不应变")
	}
	warn := false
	for _, f := range findings {
		if f.Severity == SeverityWarn && strings.Contains(f.Message, "未处理") {
			warn = true
		}
	}
	if !warn {
		t.Errorf("RENAME TABLE 应有 warn: %v", findings)
	}
}
