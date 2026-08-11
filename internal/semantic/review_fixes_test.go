package semantic

import (
	"context"
	"os"
	"testing"
)

// 探针单语句强制：多语句输入（含 DML/DDL 注入形态）必须被编译期拒绝
// （review 修复：parseProbe 校验恰好 1 条语句）。
func TestParseProbeRejectsMultiStatement(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1; DROP TABLE t",
		"SELECT 1; SELECT 2",
		"COUNT(*) ; DELETE FROM t",
	} {
		if err := parseProbe(sql); err == nil {
			t.Errorf("多语句探针应拒绝: %q", sql)
		}
	}
	// 单表达式仍放行（探针的正当用途）。
	for _, sql := range []string{
		"SELECT COUNT(*) FILTER (WHERE status = 'failed')",
		"SELECT COALESCE(SUM(amount), 0)",
	} {
		if err := parseProbe(sql); err != nil {
			t.Errorf("合法单语句探针误拒: %q: %v", sql, err)
		}
	}
}

// 通配展开的精确前缀语义：服务/库名含 %/_ 时不得越界匹配变体服务
// （review 修复：LIKE → substr 精确比较；安全评审实测过 LIKE 通配泄漏）。
func TestTablesByPrefixNoWildcardLeak(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	dir := writeSemantic(t, map[string]string{
		"services/billing_service.yaml": `version: 1
service: billing_service
databases:
  - name: db1
    tables:
      - name: t1
`,
		"services/billingXservice.yaml": `version: 1
service: billingXservice
databases:
  - name: db1
    tables:
      - name: t2
`,
	})
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// service: 通配展开必须只命中本服务（billing_service 不能拿到 billingXservice 的表）。
	tbls, err := TablesForService(ctx, st, "billing_service")
	if err != nil {
		t.Fatalf("TablesForService: %v", err)
	}
	if len(tbls) != 1 || tbls[0] != "billing_service.db1.t1" {
		t.Errorf("精确前缀应只命中本服务: %v", tbls)
	}
	tbls, err = TablesForService(ctx, st, "billing%")
	if err != nil {
		t.Fatalf("TablesForService %%: %v", err)
	}
	if len(tbls) != 0 {
		t.Errorf("%% 是字面量，不应命中任何服务: %v", tbls)
	}
}

// 备份目标 == 源库文件必须拒绝（review 修复：先截断再拷贝会清库）。
func TestBackupRejectsSameFile(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	path, err := mainDBPath(ctx, st)
	if err != nil {
		t.Fatalf("mainDBPath: %v", err)
	}
	if err := Backup(ctx, st, path); err == nil {
		t.Fatal("备份到源库自身应拒绝")
	}
	// 库文件未被清零（同文件防护生效）。
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("源库被清空（同文件防护失效）")
	}
}
