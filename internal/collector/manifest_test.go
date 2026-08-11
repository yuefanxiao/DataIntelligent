package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadManifest 清单校验：version、必填字段、服务名唯一。
func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write("ok.yaml", `version: 1
services:
  - name: a
    dir: a-service
    db: a_db
  - name: b
    dir: b-service
    db: b_db
    models_dir: internal/repo
`)
	m, err := LoadManifest(ok)
	if err != nil {
		t.Fatalf("合法清单应通过: %v", err)
	}
	if len(m.Services) != 2 {
		t.Fatalf("服务数 = %d", len(m.Services))
	}
	if m.Services[1].ModelsDir != "internal/repo" {
		t.Error("models_dir 应保留")
	}
	if m.Services[0].ModelsDir != "internal/data" {
		t.Error("models_dir 缺省应为 internal/data")
	}

	bad := write("bad.yaml", "version: 1\nservices:\n  - name: a\n    dir: a-service\n")
	if _, err := LoadManifest(bad); err == nil || !strings.Contains(err.Error(), "db") {
		t.Errorf("缺 db 应报错: %v", err)
	}

	dup := write("dup.yaml", "version: 1\nservices:\n  - name: a\n    dir: x\n    db: d\n  - name: a\n    dir: y\n    db: d\n")
	if _, err := LoadManifest(dup); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Errorf("服务名重复应报错: %v", err)
	}

	ver := write("ver.yaml", "version: 99\nservices: []\n")
	if _, err := LoadManifest(ver); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("version 不符应报错: %v", err)
	}

	missing := filepath.Join(dir, "missing.yaml")
	if _, err := LoadManifest(missing); err == nil {
		t.Error("清单缺失应报错（防路径笔误当空清单）")
	}
}

// TestRenderDraft 草稿渲染：schema 前缀、引用 FQN、枚举排序、
// 跨服务引用跳过（目标不在本服务结构内）。
func TestRenderDraft(t *testing.T) {
	st := &Structure{
		Service: "wallet", DB: "w",
		Tables: []*Table{
			{
				Name: "accounts", Schema: "wallet",
				Columns: []*Column{
					{Name: "id", Type: "bigint"},
					{Name: "status", Type: "smallint", EnumValues: []string{"3", "1", "2"}},
				},
			},
			{
				Name: "payments", Schema: "wallet",
				Columns:    []*Column{{Name: "id", Type: "bigint"}, {Name: "account_id", Type: "bigint"}},
				References: []*Reference{{TargetSchema: "wallet", TargetTable: "accounts", Cols: []string{"account_id"}, PkCols: []string{"id"}}},
			},
			{
				Name: "external", Schema: "wallet",
				Columns:    []*Column{{Name: "id", Type: "bigint"}},
				References: []*Reference{{TargetSchema: "bill", TargetTable: "bills", Cols: []string{"bill_id"}, PkCols: []string{"id"}}},
			},
		},
	}
	out, err := RenderDraft(st)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"service: wallet",
		"- name: w",
		"schema: wallet",
		"to: wallet.w.accounts",
		"\"on\": payments.account_id = accounts.id",
		"value: \"1\"", "value: \"2\"", "value: \"3\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("草稿缺 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "bill.bills") {
		t.Error("跨服务引用（目标不在结构内）应跳过")
	}
	// 列顺序 = DDL 顺序（id 在前）。
	if strings.Index(s, "name: id") > strings.Index(s, "name: account_id") {
		t.Error("列顺序应为 DDL 顺序")
	}
}

// TestRenderDraftCompile 草稿可被 07 编译校验接受（单服务最小闭环）。
func TestRenderDraftCompile(t *testing.T) {
	st := &Structure{
		Service: "svc", DB: "db",
		Tables: []*Table{
			{
				Name: "orders", Schema: "s",
				Columns: []*Column{
					{Name: "id", Type: "bigint"},
					{Name: "status", Type: "smallint", EnumValues: []string{"1", "2"}},
				},
			},
		},
	}
	out := t.TempDir()
	if _, err := WriteDraft(out, st); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompile(out); err != nil {
		t.Fatalf("草稿应过 07 编译校验: %v", err)
	}
}

// TestCheckCompileRejects 编译不兼容产出必须原子拒绝。
func TestCheckCompileRejects(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 空枚举 value（07 编译校验要求非空）。
	bad := `version: 1
service: svc
databases:
  - name: db
    tables:
      - name: t
        columns:
          - name: status
            type: smallint
            enum_values:
              - value: ""
`
	if err := os.WriteFile(filepath.Join(dir, "svc.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompile(out); err == nil {
		t.Error("空枚举值应编译失败（原子拒绝）")
	}
}

// TestLoadManifestIdentValidation 服务名/库名标识符校验（防路径穿越）。
func TestLoadManifestIdentValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct{ name, content string }{
		{"traversal.yaml", "version: 1\nservices:\n  - name: ../../etc/passwd\n    dir: x\n    db: d\n"},
		{"dot.yaml", "version: 1\nservices:\n  - name: a.b\n    dir: x\n    db: d\n"},
		{"upper.yaml", "version: 1\nservices:\n  - name: AService\n    dir: x\n    db: d\n"},
		{"dashlead.yaml", "version: 1\nservices:\n  - name: -svc\n    dir: x\n    db: d\n"},
		{"baddb.yaml", "version: 1\nservices:\n  - name: ok\n    dir: x\n    db: ../evil\n"},
		{"absdir.yaml", "version: 1\nservices:\n  - name: ok\n    dir: /etc\n    db: d\n"},
		{"dotdotdir.yaml", "version: 1\nservices:\n  - name: ok\n    dir: a/../b\n    db: d\n"},
	}
	for _, tc := range cases {
		p := write(tc.name, tc.content)
		if _, err := LoadManifest(p); err == nil {
			t.Errorf("%s 应被拒绝", tc.name)
		}
	}
	// 合法形态放行。
	ok := write("ok.yaml", "version: 1\nservices:\n  - name: bss-wallet\n    dir: bss/bss-wallet-service\n    db: wallet\n")
	if _, err := LoadManifest(ok); err != nil {
		t.Errorf("合法清单应通过: %v", err)
	}
}

// TestLoadManifestModelsDirValidation models_dir 拒绝绝对路径与 .. 逃逸。
func TestLoadManifestModelsDirValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, md := range []string{"../../../../etc", "/etc", "a/../b"} {
		p := write("m.yaml", "version: 1\nservices:\n  - name: ok\n    dir: svc\n    db: d\n    models_dir: "+md+"\n")
		if _, err := LoadManifest(p); err == nil {
			t.Errorf("models_dir %q 应被拒绝", md)
		}
	}
	ok := write("ok.yaml", "version: 1\nservices:\n  - name: ok\n    dir: svc\n    db: d\n    models_dir: internal/data/invite\n")
	if _, err := LoadManifest(ok); err != nil {
		t.Errorf("合法 models_dir 应通过: %v", err)
	}
}
