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
  - name: c
    dir: c-service
`)
	m, err := LoadManifest(ok)
	if err != nil {
		t.Fatalf("合法清单应通过: %v", err)
	}
	if len(m.Services) != 3 {
		t.Fatalf("服务数 = %d", len(m.Services))
	}
	if m.Services[1].ModelsDir != "internal/repo" {
		t.Error("models_dir 应保留")
	}
	if m.Services[0].ModelsDir != "internal/data" {
		t.Error("models_dir 缺省应为 internal/data")
	}
	// db 可选：无库服务（纯编排/聚合类）db 为空，不报错。
	if m.Services[2].DB != "" {
		t.Errorf("无 db 的服务 DB 应为空，得到 %q", m.Services[2].DB)
	}

	missingName := write("missing-name.yaml", "version: 1\nservices:\n  - dir: a-service\n    db: d\n")
	if _, err := LoadManifest(missingName); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("缺 name 应报错: %v", err)
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

// TestRenderDraftNoDB 无持库服务草稿：只有服务实体、无 database 条目
// （纯编排/聚合类服务），且可过编译校验（语义层覆盖全部后端服务）。
func TestRenderDraftNoDB(t *testing.T) {
	st := &Structure{Service: "ops-operation"}
	out, err := RenderDraft(st)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "service: ops-operation") {
		t.Errorf("草稿缺服务名:\n%s", s)
	}
	if strings.Contains(s, "\n    name:") || strings.Contains(s, "tables:") {
		t.Errorf("无库服务草稿不应有 database 条目:\n%s", s)
	}
	dir := t.TempDir()
	if _, err := WriteDraft(dir, st); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompile(dir); err != nil {
		t.Fatalf("无库服务草稿应过 07 编译校验: %v", err)
	}
}

// TestMergeSemantics 采集重跑保留人工语义（描述/is_time/枚举 label），
// 结构以新采集为准（表/列/枚举值变化语义随之丢弃）。
func TestMergeSemantics(t *testing.T) {
	// 现有作者入口：已回写语义（描述/is_time/label）。
	existing := `version: 1
service: wallet
description: 钱包服务（已确认描述）
databases:
  - name: w
    description: 钱包库（已确认）
    tables:
      - name: accounts
        description: 账户表（已确认）
        schema: wallet
        columns:
          - name: id
            type: bigint
          - name: status
            type: smallint
            description: 账户状态（已确认）
            is_time: false
            enum_values:
              - value: "1"
                label: 正常（已确认）
              - value: "2"
                label: 冻结
          - name: created_at
            type: timestamptz
            is_time: true
      - name: dropped_table
        description: 已删除表的描述（应随结构丢弃）
        schema: wallet
        columns:
          - name: x
            type: bigint
`
	// 新草稿：结构变化（dropped_table 没了、accounts 多了 paid_at 列、
	// status 枚举少了 "2"）。
	newDraft := `version: 1
service: wallet
databases:
  - name: w
    tables:
      - name: accounts
        schema: wallet
        columns:
          - name: id
            type: bigint
          - name: status
            type: smallint
            enum_values:
              - value: "1"
          - name: created_at
            type: timestamptz
          - name: paid_at
            type: timestamptz
`
	out, err := MergeSemantics([]byte(newDraft), []byte(existing))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"description: 钱包服务（已确认描述）", // 服务描述保留
		"description: 钱包库（已确认）",     // 库描述保留
		"description: 账户表（已确认）",     // 表描述保留
		"description: 账户状态（已确认）",    // 列描述保留
		"label: 正常（已确认）",            // 枚举 label 保留
		"is_time: true",                // is_time 保留
		"name: paid_at",                // 新列进来（无语义字段）
	} {
		if !strings.Contains(s, want) {
			t.Errorf("合并结果缺 %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{
		"label: 冻结",        // 枚举值被移除 → label 随结构丢弃
		"dropped_table",    // 表被移除 → 描述随结构丢弃
		"description: 已删除表的描述（应随结构丢弃）",
	} {
		if strings.Contains(s, bad) {
			t.Errorf("合并结果不应含 %q:\n%s", bad, s)
		}
	}
	// 服务名不一致 → 拒绝（防串文件覆盖）。
	if _, err := MergeSemantics([]byte(newDraft), []byte(strings.ReplaceAll(existing, "service: wallet", "service: other"))); err == nil {
		t.Error("服务名不一致应拒绝合并")
	}
	// 现有文件解析失败 → 拒绝（防语义丢失）。
	if _, err := MergeSemantics([]byte(newDraft), []byte("not: [valid: yaml")); err == nil {
		t.Error("现有文件解析失败应拒绝合并")
	}
}

// TestWriteDraftPreservesSemantics WriteDraft 覆盖写前先合并：重采不丢
// 语义（采集 → 回写语义 → 重采 的闭环）。
func TestWriteDraftPreservesSemantics(t *testing.T) {
	out := t.TempDir()
	st := &Structure{
		Service: "svc", DB: "db",
		Tables: []*Table{{
			Name: "t", Schema: "s",
			Columns: []*Column{
				{Name: "id", Type: "bigint"},
				{Name: "status", Type: "smallint", EnumValues: []string{"1", "2"}},
			},
		}},
	}
	if _, err := WriteDraft(out, st); err != nil {
		t.Fatal(err)
	}
	// 人工回写语义（模拟 review 后合入的作者入口）。
	path := filepath.Join(out, "services", "svc.yaml")
	enriched, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	enriched = []byte(strings.ReplaceAll(string(enriched),
		"service: svc", "service: svc\ndescription: 服务描述（已确认）"))
	enriched = []byte(strings.ReplaceAll(string(enriched),
		"- name: status", "- name: status\n            description: 状态列（已确认）"))
	enriched = []byte(strings.ReplaceAll(string(enriched),
		"value: \"1\"", "value: \"1\"\n                label: 正常（已确认）"))
	if err := os.WriteFile(path, enriched, 0o644); err != nil {
		t.Fatal(err)
	}
	// 重跑采集（结构不变）→ 语义保留。
	if _, err := WriteDraft(out, st); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"服务描述（已确认）", "状态列（已确认）", "label: 正常（已确认）"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("重采后语义丢失 %q:\n%s", want, data)
		}
	}
}
// TestCollectNoDBService 全量采集含无库服务：产出服务实体草稿 + warn
// 发现，不进迁移解析（目录不存在也不报错）。
func TestCollectNoDBService(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte(`version: 1
services:
  - name: svc-no-db
    dir: svc-b
`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	res, err := Collect(CollectConfig{
		Repo:     dir, // 目录不存在也不影响：无库服务不读文件系统
		Manifest: m,
		GORM:     false,
		OutDir:   out,
	})
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(res.Services) != 1 || res.Services[0].Name != "svc-no-db" {
		t.Fatalf("服务数/名称异常: %+v", res.Services)
	}
	noDB := res.Services[0]
	if noDB.Tables != 0 || noDB.Enums != 0 || noDB.Refs != 0 {
		t.Errorf("无库服务不应有结构: %+v", noDB)
	}
	if noDB.Errors() != 0 || len(noDB.Findings) != 1 {
		t.Errorf("无库服务应有 1 条 warn 发现: %+v", noDB.Findings)
	}
	data, err := os.ReadFile(filepath.Join(out, "services", "svc-no-db.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "service: svc-no-db") {
		t.Errorf("无库服务草稿内容异常:\n%s", data)
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
