package collector

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestCalibrateIntegration 校准集成测试：连测试 PG（docker 起的只读
// 形态库，neo-cloud 业务形态）验证漂移报告。
// 默认跳过；本机验证：
//
//	docker run -d --name dgw-calibrate-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=wallet -p 54329:5432 postgres:17-alpine
//	DGW_TEST_PG_DSN="postgres://postgres:test@127.0.0.1:54329/wallet?sslmode=disable" \
//	  go test ./internal/collector/ -run TestCalibrate -v
func TestCalibrateIntegration(t *testing.T) {
	dsn := os.Getenv("DGW_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("设 DGW_TEST_PG_DSN 指向测试 PG 后运行（calibrate 集成验证）")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接测试 PG: %v", err)
	}
	defer conn.Close(ctx)

	// 建业务形态 schema（wallet 库 + wallet schema，镜像 neo-cloud）。
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS wallet CASCADE;
		CREATE SCHEMA wallet;
		CREATE TABLE wallet.wallet_accounts (
			id bigint NOT NULL, org_id varchar(36) NOT NULL,
			balance numeric(30,12) NOT NULL, status smallint NOT NULL,
			created_at timestamptz NOT NULL);
		CREATE TABLE wallet.wallet_transactions (
			id bigint NOT NULL, transaction_id varchar(128) NOT NULL,
			org_id varchar(36) NOT NULL, tx_type smallint NOT NULL,
			created_at timestamptz NOT NULL);`); err != nil {
		t.Fatalf("建测试 schema: %v", err)
	}
	t.Cleanup(func() { conn.Exec(ctx, "DROP SCHEMA IF EXISTS wallet CASCADE") })

	// 草稿侧结构（带 1 张漂移表 + 1 个漂移列 + 1 个类型漂移）。
	st := &Structure{
		Service: "wallet", DB: "wallet",
		Tables: []*Table{
			{Name: "wallet_accounts", Schema: "wallet", Columns: []*Column{
				{Name: "id", Type: "bigint"},
				{Name: "org_id", Type: "varchar(36)"},
				{Name: "balance", Type: "numeric(30,12)"},
				{Name: "status", Type: "smallint"},
				{Name: "created_at", Type: "timestamptz"},
				{Name: "drift_col", Type: "varchar(8)"}, // 草稿有、生产无 = error
			}},
			{Name: "wallet_transactions", Schema: "wallet", Columns: []*Column{
				{Name: "id", Type: "bigint"},
				{Name: "transaction_id", Type: "varchar(128)"},
				{Name: "org_id", Type: "varchar(36)"},
				{Name: "tx_type", Type: "varchar(16)"}, // 类型漂移：生产 smallint = warn
				{Name: "created_at", Type: "timestamptz"},
			}},
			{Name: "ghost_table", Schema: "wallet", Columns: []*Column{{Name: "id", Type: "bigint"}}}, // 草稿有、生产无 = error
		},
	}
	findings, err := Calibrate(ctx, dsn, st)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	var errs, warns int
	for _, f := range findings {
		switch f.Severity {
		case "error":
			errs++
		case "warn":
			warns++
		}
		t.Logf("%s", f.String())
	}
	if errs != 2 {
		t.Errorf("error 发现 = %d, want 2（drift_col 列 + ghost_table 表）: %v", errs, findings)
	}
	if warns != 1 {
		t.Errorf("warn 发现 = %d, want 1（tx_type 类型漂移）: %v", warns, findings)
	}
	// 确定性：连跑第二次输出一致。
	again, err := Calibrate(ctx, dsn, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(findings) {
		t.Errorf("两次校准发现数不一致（确定性破坏）")
	}
	for i := range findings {
		if findings[i].String() != again[i].String() {
			t.Errorf("两次校准发现不一致: %s vs %s", findings[i], again[i])
		}
	}
}

// TestDraftTypeToInfoSchema 草稿类型 → information_schema 形态归一。
func TestDraftTypeToInfoSchema(t *testing.T) {
	cases := map[string]string{
		"varchar(128)":   "character varying(128)",
		"VARCHAR(64)":    "character varying(64)",
		"char(3)":        "character(3)",
		"timestamptz":    "timestamp with time zone",
		"timestamp":      "timestamp without time zone",
		"numeric(30,12)": "numeric(30,12)",
		"numeric":        "numeric",
		"bigint":         "bigint",
		"smallint":       "smallint",
		"jsonb":          "jsonb",
		"text":           "text",
	}
	for in, want := range cases {
		if got := draftTypeToInfoSchema(in); got != want {
			t.Errorf("draftTypeToInfoSchema(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeInfoSchemaType 归一化往返等价。
func TestNormalizeInfoSchemaType(t *testing.T) {
	len128 := 128
	if got := normalizeInfoSchemaType("character varying", &len128, nil, nil); got != "character varying(128)" {
		t.Errorf("varchar 带长度归一 = %q", got)
	}
	prec, scale := 30, 12
	if got := normalizeInfoSchemaType("numeric", nil, &prec, &scale); got != "numeric(30,12)" {
		t.Errorf("numeric 带精度归一 = %q", got)
	}
	if got := normalizeInfoSchemaType("bigint", nil, nil, nil); got != "bigint" {
		t.Errorf("bigint 归一 = %q", got)
	}
	if got := normalizeInfoSchemaType("timestamp with time zone", nil, nil, nil); !strings.Contains(got, "timestamp") {
		t.Errorf("timestamptz 归一 = %q", got)
	}
}

// TestDraftTypeToInfoSchemaSerial serial 族归一（自增主键不报假漂移）。
func TestDraftTypeToInfoSchemaSerial(t *testing.T) {
	for in, want := range map[string]string{
		"bigserial": "bigint", "serial": "integer", "smallserial": "smallint",
	} {
		if got := draftTypeToInfoSchema(in); got != want {
			t.Errorf("draftTypeToInfoSchema(%q) = %q, want %q", in, got, want)
		}
	}
}
