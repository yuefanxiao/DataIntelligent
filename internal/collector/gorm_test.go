package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModel 写一个模型文件（helper）。
func writeModel(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGormExtract 基本提取：TableName、column 覆盖、snake_case、
// 无标签字段、`-` 跳过、slice 关联跳过、嵌入 gorm.Model。
func TestGormExtract(t *testing.T) {
	dir := t.TempDir()
	writeModel(t, dir, "models.go", `package data

import "time"

type userPO struct {
	ID       int64     `+"`gorm:\"primaryKey;autoIncrement\"`"+`
	UserID   string    `+"`gorm:\"column:user_id;type:varchar(64);not null;uniqueIndex\"`"+`
	Email    *string   `+"`gorm:\"size:255\"`"+`
	Status   string    `+"`gorm:\"size:32;not null;default:active\"`"+`
	CreatedAt time.Time `+"`gorm:\"autoCreateTime\"`"+`
	Raw      string    `+"`gorm:\"-\"`"+`
	Items    []itemPO  // 关联，不是列
	Meta     map[string]string // 关联，不是列
}

func (userPO) TableName() string { return "users" }

type itemPO struct {
	ID int64 `+"`gorm:\"primaryKey\"`"+`
}

func (itemPO) TableName() string { return "items" }

type auditPO struct {
	gorm.Model
	Action string `+"`gorm:\"type:varchar(16)\"`"+`
}

func (auditPO) TableName() string { return "audit_logs" }
`)
	writeModel(t, dir, "qualified.go", `package data

type qualifiedPO struct {
	ID int64 `+"`gorm:\"primaryKey\"`"+`
}

func (qualifiedPO) TableName() string { return "bill.bills" }
`)
	writeModel(t, dir, "notmodel.go", `package data

type NotAModel struct {
	X string `+"`gorm:\"type:varchar(4)\"`"+`
}
`)

	models, findings := ExtractGormModels(dir)
	if len(findings) != 0 {
		t.Fatalf("不应有发现: %v", findings)
	}
	if len(models) != 4 {
		t.Fatalf("模型数 = %d, want 4（NotAModel 无 TableName 应跳过）", len(models))
	}
	byTable := map[string]gormModel{}
	for _, m := range models {
		byTable[m.Table] = m
	}
	users := byTable["users"]
	if users.Struct != "userPO" {
		t.Errorf("users 模型 = %s", users.Struct)
	}
	if !containsStr(users.Columns, "user_id") || !containsStr(users.Columns, "email") ||
		containsStr(users.Columns, "raw") || containsStr(users.Columns, "items") ||
		containsStr(users.Columns, "meta") {
		t.Errorf("users 列 = %v", users.Columns)
	}
	if users.Types["user_id"] != "varchar(64)" {
		t.Errorf("user_id 显式类型 = %q", users.Types["user_id"])
	}
	if _, ok := users.Types["email"]; ok {
		t.Error("email 只有 size 标签，不应产生类型比较")
	}
	audit := byTable["audit_logs"]
	for _, c := range []string{"id", "created_at", "updated_at", "deleted_at", "action"} {
		if !containsStr(audit.Columns, c) {
			t.Errorf("audit_logs 嵌入 gorm.Model 应含 %s，实际 %v", c, audit.Columns)
		}
	}
	if _, ok := byTable["bills"]; !ok {
		t.Error("schema 限定 TableName（bill.bills）应归一为 bills")
	}
}

// TestGormExtractEmbeddedRecurse 同包嵌入结构递归展开 + 防环。
func TestGormExtractEmbeddedRecurse(t *testing.T) {
	dir := t.TempDir()
	writeModel(t, dir, "embed.go", `package data

type basePO struct {
	ID   int64 `+"`gorm:\"primaryKey\"`"+`
	Note string
}

type childPO struct {
	basePO
	Name string `+"`gorm:\"column:name;type:varchar(32)\"`"+`
}

func (childPO) TableName() string { return "children" }
`)
	models, findings := ExtractGormModels(dir)
	if len(findings) != 0 {
		t.Fatalf("不应有发现: %v", findings)
	}
	if len(models) != 1 {
		t.Fatalf("模型数 = %d, want 1", len(models))
	}
	m := models[0]
	for _, c := range []string{"id", "note", "name"} {
		if !containsStr(m.Columns, c) {
			t.Errorf("嵌入展开应含 %s，实际 %v", c, m.Columns)
		}
	}
}

// TestGormExtractMissingDir 模型目录缺失 = error 发现。
func TestGormExtractMissingDir(t *testing.T) {
	_, findings := ExtractGormModels(filepath.Join(t.TempDir(), "nope"))
	hasErr := false
	for _, f := range findings {
		if f.Severity == "error" {
			hasErr = true
		}
	}
	if !hasErr {
		t.Error("目录缺失应有 error 发现")
	}
}

// TestCrossCheckSeverity 交叉验证严重度语义：
// 模型有而迁移没有 = error；迁移有而模型没有 = warn。
func TestCrossCheckSeverity(t *testing.T) {
	st := &Structure{
		Service: "s", DB: "d",
		Tables: []*Table{
			{Name: "shared", Columns: []*Column{{Name: "id", Type: "bigint"}, {Name: "mig_only", Type: "text"}}},
			{Name: "mig_table", Columns: []*Column{{Name: "id", Type: "bigint"}}},
		},
	}
	models := []gormModel{
		{Table: "shared", Struct: "SharedPO", Columns: []string{"id", "model_only"}, Types: map[string]string{"id": "bigint"}},
		{Table: "model_table", Struct: "ModelPO", Columns: []string{"id"}},
	}
	fs := CrossCheck(st, models)
	var errs, warns int
	for _, f := range fs {
		switch f.Severity {
		case "error":
			errs++
		case "warn":
			warns++
		}
	}
	if errs != 2 {
		t.Errorf("error 发现 = %d, want 2（model_only 列 + model_table 表）: %v", errs, fs)
	}
	if warns != 2 {
		t.Errorf("warn 发现 = %d, want 2（mig_only 列 + mig_table 表）: %v", warns, fs)
	}
}

// TestGormTypeMismatch 类型不一致 = warn。
func TestGormTypeMismatch(t *testing.T) {
	st := &Structure{
		Service: "s", DB: "d",
		Tables: []*Table{{Name: "t", Columns: []*Column{{Name: "v", Type: "varchar(64)"}}}},
	}
	models := []gormModel{
		{Table: "t", Struct: "TPO", Columns: []string{"v"}, Types: map[string]string{"v": "numeric(20,6)"}},
	}
	fs := CrossCheck(st, models)
	found := false
	for _, f := range fs {
		if f.Severity == "warn" && strings.Contains(f.Message, "类型不一致") {
			found = true
		}
	}
	if !found {
		t.Errorf("类型不一致应有 warn: %v", fs)
	}
}

// TestSnakeCase GORM 默认命名。
func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"UserID": "user_id", "APIKey": "api_key", "URL": "url",
		"ID": "id", "CreatedAt": "created_at", "XID": "xid",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestGormTableNameWeirdForms 非常规 TableName() 形态（无返回值/
// 空函数体/裸 return）不 panic，静默跳过。
func TestGormTableNameWeirdForms(t *testing.T) {
	dir := t.TempDir()
	writeModel(t, dir, "weird.go", `package data

type NoReturnPO struct {
	ID int64
}

func (NoReturnPO) TableName() {}

type EmptyBodyPO struct {
	ID int64
}

func (EmptyBodyPO) TableName() string {}

type BareReturnPO struct {
	ID int64
}

func (BareReturnPO) TableName() string { return }

type GoodPO struct {
	ID int64 `+"`gorm:\"primaryKey\"`"+`
}

func (GoodPO) TableName() string { return "good" }
`)
	models, findings := ExtractGormModels(dir)
	if len(findings) != 0 {
		t.Fatalf("不应有发现: %v", findings)
	}
	if len(models) != 1 || models[0].Table != "good" {
		t.Fatalf("只应提取 GoodPO, got %v", models)
	}
}
