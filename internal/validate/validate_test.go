package validate

import (
	"strings"
	"testing"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// testResolve 是测试用解析器：已知表（orders/users/items/payments/events）→
// "svc.db.<table>"；schema 限定 public 时同样映射（production 形态 search_path
// 通常为 public）；其余引用 → 空（未知表）。形如真实网关（04 接线）的
// dbname 路由 + 服务归属解析，但固定为测试拓扑。
func testResolve(ref TableRef) []string {
	if ref.Schema != "" && ref.Schema != "public" {
		return nil
	}
	switch ref.Table {
	case "orders", "users", "items", "payments", "events", "region_sales",
		"t1", "t2", "orders_backup", "users_backup":
		return []string{"svc.db." + ref.Table}
	}
	return nil
}

// testAllow 是测试用白名单判定：fqn 属于 allowed 集合才放行。
func testAllow(allowed ...string) Allow {
	set := map[string]struct{}{}
	for _, f := range allowed {
		set[f] = struct{}{}
	}
	return func(fqn string) bool {
		_, ok := set[fqn]
		return ok
	}
}

// assertReject 断言 Check 以指定 kind + reason 拒绝，并返回错误供进一步检查。
func assertReject(t *testing.T, sql string, kind gwerr.Kind, reason string) *gwerr.Error {
	t.Helper()
	_, err := Check(sql, testResolve, testAllow("svc.db.orders"))
	if err == nil {
		t.Fatalf("Check(%q) 应被拒绝，却通过了", sql)
	}
	if err.Kind != kind {
		t.Errorf("Check(%q) kind = %s，期望 %s（错误: %s）", sql, err.Kind, kind, err.Message)
	}
	if got := err.Details["reason"]; got != reason {
		t.Errorf("Check(%q) details.reason = %v，期望 %q", sql, got, reason)
	}
	return err
}

// assertTables 断言 Check 通过且提取出的表引用恰好等于 want。
func assertTables(t *testing.T, sql string, want []string) {
	t.Helper()
	rep, err := Check(sql, testResolve, testAllow("svc.db.orders", "svc.db.users", "svc.db.items", "svc.db.payments", "svc.db.events", "svc.db.region_sales"))
	if err != nil {
		t.Fatalf("Check(%q) 不应被拒绝: %s", sql, err)
	}
	var got []string
	for _, r := range rep.Tables {
		got = append(got, strings.TrimPrefix(r.Schema+"."+r.Table, "."))
	}
	if len(got) != len(want) {
		t.Fatalf("Check(%q) 提取表 = %v，期望 %v", sql, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Check(%q) 提取表 = %v，期望 %v", sql, got, want)
		}
	}
}

// ── 段一：AST 分类——非 SELECT 拒绝集（AC 1/5，§6.3 负向例 2）───────────────────
//
// reason 列声明各拒绝类别的机器可区分数值：DML/DDL/COPY/utility → non_select；
// SELECT INTO/行锁 → write_side_effect；数据修改 CTE → data_modifying_cte。

func TestClassifyRejectsNonSelect(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		reason string
	}{
		{"insert", `INSERT INTO orders (id) VALUES (1)`, "non_select"},
		{"update", `UPDATE orders SET status = 'paid' WHERE id = 1`, "non_select"},
		{"delete", `DELETE FROM orders WHERE id = 1`, "non_select"},
		{"merge", `MERGE INTO orders o USING payments p ON o.id = p.order_id WHEN MATCHED THEN UPDATE SET o.status = p.status`, "non_select"},
		{"copy", `COPY orders FROM STDIN`, "non_select"},
		{"create", `CREATE TABLE t (id int)`, "non_select"},
		{"create_index", `CREATE INDEX idx ON orders (id)`, "non_select"},
		{"drop", `DROP TABLE orders`, "non_select"},
		{"alter", `ALTER TABLE orders ADD COLUMN note text`, "non_select"},
		{"truncate", `TRUNCATE orders`, "non_select"},
		{"vacuum", `VACUUM orders`, "non_select"},
		{"explain", `EXPLAIN SELECT * FROM orders`, "non_select"},
		{"explain_analyze", `EXPLAIN ANALYZE SELECT * FROM orders`, "non_select"},
		{"grant", `GRANT SELECT ON orders TO reader`, "non_select"},
		{"transaction", `BEGIN`, "non_select"},
		{"set", `SET statement_timeout = 1000`, "non_select"},
		{"do", `DO $$ BEGIN NULL; END $$`, "non_select"},
		{"select_into", `SELECT * INTO orders_backup FROM orders`, "write_side_effect"},
		{"for_update", `SELECT * FROM orders FOR UPDATE`, "write_side_effect"},
		{"for_no_key_update", `SELECT * FROM orders FOR NO KEY UPDATE`, "write_side_effect"},
		{"for_share", `SELECT * FROM orders FOR SHARE`, "write_side_effect"},
		{"for_key_share", `SELECT * FROM orders FOR KEY SHARE`, "write_side_effect"},
		{"modifying_cte_delete", `WITH d AS (DELETE FROM orders WHERE id = 1 RETURNING *) SELECT * FROM d`, "data_modifying_cte"},
		{"modifying_cte_insert", `WITH i AS (INSERT INTO orders (id) VALUES (1) RETURNING *) SELECT * FROM i`, "data_modifying_cte"},
		{"modifying_cte_update", `WITH u AS (UPDATE orders SET status = 'x' RETURNING *) SELECT * FROM u`, "data_modifying_cte"},
		{"modifying_cte_nested", `SELECT * FROM (WITH u AS (UPDATE orders SET status = 'x' RETURNING *) SELECT * FROM u) s`, "data_modifying_cte"},
		{"multi_statement_mixed", `SELECT 1; DELETE FROM orders`, "non_select"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertReject(t, tc.sql, gwerr.KindInvalidRequest, tc.reason)
		})
	}
}

func TestClassifyAcceptsReadOnlySelect(t *testing.T) {
	cases := []string{
		`SELECT 1`,
		`SELECT * FROM orders`,
		`SELECT * FROM orders o JOIN users u ON o.uid = u.id`,
		`WITH x AS (SELECT * FROM orders) SELECT * FROM x`,
		`SELECT count(*) AS n FROM orders GROUP BY status HAVING count(*) > 1`,
		`SELECT * FROM t1 UNION ALL SELECT * FROM t2`,
		`-- 只有注释`,
		``,
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			if _, err := Parse(sql); err != nil {
				// 空串/纯注释：解析器可能给 0 语句结果——分类层面仍应通过。
				_ = err
			}
			rep, err := Check(sql, testResolve, testAllow("svc.db.orders", "svc.db.users", "svc.db.t1", "svc.db.t2"))
			if err != nil {
				t.Fatalf("Check(%q) 不应被拒绝: %s", sql, err)
			}
			_ = rep
		})
	}
}

// ── 段二前半：表提取——CTE/子查询/join 语法层引用全可见（AC 2）────────────────

func TestExtractTables(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"simple", `SELECT * FROM orders`, []string{"orders"}},
		{"schema_qualified", `SELECT * FROM public.orders`, []string{"public.orders"}},
		{"alias_only", `SELECT * FROM orders o WHERE o.id = 1`, []string{"orders"}},
		{"join_inner", `SELECT * FROM orders o JOIN users u ON o.uid = u.id`, []string{"orders", "users"}},
		{"join_left_using", `SELECT * FROM orders LEFT JOIN users USING (id)`, []string{"orders", "users"}},
		{"join_full", `SELECT * FROM orders a FULL OUTER JOIN users b ON a.id = b.id`, []string{"orders", "users"}},
		{"join_chain", `SELECT * FROM orders o JOIN users u ON o.uid = u.id JOIN payments p ON p.uid = u.id`, []string{"orders", "users", "payments"}},
		{"subquery_from", `SELECT * FROM (SELECT * FROM events) e`, []string{"events"}},
		{"subquery_where_in", `SELECT * FROM orders WHERE uid IN (SELECT id FROM users)`, []string{"orders", "users"}},
		{"subquery_where_exists", `SELECT * FROM orders o WHERE EXISTS (SELECT 1 FROM payments p WHERE p.uid = o.uid)`, []string{"orders", "payments"}},
		{"subquery_scalar_target", `SELECT (SELECT max(amount) FROM payments) FROM orders`, []string{"payments", "orders"}},
		{"subquery_nested", `SELECT * FROM (SELECT * FROM (SELECT * FROM events) e1) e2`, []string{"events"}},
		{"cte_basic", `WITH x AS (SELECT * FROM orders) SELECT * FROM x`, []string{"orders"}},
		{"cte_join", `WITH a AS (SELECT * FROM orders), b AS (SELECT * FROM users) SELECT * FROM a JOIN b ON true`, []string{"orders", "users"}},
		{"cte_recursive", `WITH RECURSIVE n AS (SELECT 1 UNION ALL SELECT n + 1 FROM n WHERE n < 10) SELECT * FROM n`, []string{}},
		{"cte_shadowing", `WITH x AS (SELECT * FROM orders) SELECT * FROM (WITH x AS (SELECT * FROM users) SELECT * FROM x) s`, []string{"users", "orders"}},
		{"cte_collision_schema_qualified", `WITH x AS (SELECT 1) SELECT * FROM public.x`, []string{"public.x"}},
		{"cte_collision_unqualified", `WITH x AS (SELECT 1) SELECT * FROM x`, []string{}},
		// 非递归 CTE 自引用是 PG 分析期拒绝的非法 SQL；抑制是安全的过近似。
		{"cte_self_reference_invalid_sql", `WITH x AS (SELECT * FROM x) SELECT * FROM x`, []string{}},
		{"cte_mutual_ref", `WITH a AS (SELECT * FROM b), b AS (SELECT * FROM orders) SELECT * FROM a`, []string{"orders"}},
		{"setop_union", `SELECT * FROM orders UNION ALL SELECT * FROM orders_backup`, []string{"orders", "orders_backup"}},
		{"setop_intersect", `SELECT * FROM users INTERSECT SELECT * FROM users_backup`, []string{"users", "users_backup"}},
		{"lateral", `SELECT * FROM orders o, LATERAL (SELECT * FROM payments p WHERE p.uid = o.uid) s`, []string{"orders", "payments"}},
		{"case_expression", `SELECT CASE WHEN EXISTS (SELECT 1 FROM payments) THEN 1 ELSE 0 END FROM orders`, []string{"payments", "orders"}},
		{"func_arg_subquery", `SELECT coalesce((SELECT max(amount) FROM payments), 0) FROM orders`, []string{"payments", "orders"}},
		{"self_join_dedupe", `SELECT * FROM orders a JOIN orders b ON a.id = b.id`, []string{"orders"}},
		{"values_no_table", `SELECT * FROM (VALUES (1, 2)) AS v`, []string{}},
		{"table_shorthand", `TABLE orders`, []string{"orders"}},
		{"materialized_cte", `WITH x AS MATERIALIZED (SELECT * FROM orders) SELECT * FROM x`, []string{"orders"}},
		{"tablesample", `SELECT * FROM orders TABLESAMPLE BERNOULLI (10)`, []string{"orders"}},
		{"only_inheritance", `SELECT * FROM ONLY orders`, []string{"orders"}},
		{"function_from", `SELECT * FROM generate_series(1, 10)`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) 失败: %s", tc.sql, err)
			}
			if len(stmts) != 1 {
				t.Fatalf("Parse(%q) 语句数 = %d，期望 1", tc.sql, len(stmts))
			}
			if c := ClassifyStmt(stmts[0]); c != nil {
				t.Fatalf("ClassifyStmt(%q) 应通过: %s", tc.sql, c)
			}
			var got []string
			for _, r := range ExtractTables(stmts[0]) {
				got = append(got, strings.TrimPrefix(r.Schema+"."+r.Table, "."))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractTables(%q) = %v，期望 %v", tc.sql, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ExtractTables(%q) = %v，期望 %v", tc.sql, got, tc.want)
				}
			}
		})
	}
}

// ── 段二后半：授权比对——白名单命中/未知表/未授权表（AC 3，§5.2）──────────────

func TestAuthorizeTables(t *testing.T) {
	ref := TableRef{Schema: "", Table: "orders"}

	t.Run("白名单命中", func(t *testing.T) {
		err := AuthorizeTables([]TableRef{ref}, testResolve, testAllow("svc.db.orders"))
		if err != nil {
			t.Fatalf("应放行: %s", err)
		}
	})

	t.Run("多候选其一命中", func(t *testing.T) {
		resolve := func(r TableRef) []string { return []string{"svc.db.orders", "other.db.orders"} }
		err := AuthorizeTables([]TableRef{ref}, resolve, testAllow("other.db.orders"))
		if err != nil {
			t.Fatalf("任一候选命中即放行: %s", err)
		}
	})

	t.Run("未知表拒绝", func(t *testing.T) {
		err := AuthorizeTables([]TableRef{{Table: "secret"}}, testResolve, testAllow("svc.db.orders"))
		if err == nil {
			t.Fatal("未知表应拒绝")
		}
		if err.Kind != gwerr.KindPermission {
			t.Errorf("kind = %s，期望 %s", err.Kind, gwerr.KindPermission)
		}
		if err.Details["reason"] != "unknown_table" {
			t.Errorf("reason = %v，期望 unknown_table", err.Details["reason"])
		}
		if err.Details["table"] != "secret" {
			t.Errorf("details.table = %v，期望 secret", err.Details["table"])
		}
	})

	t.Run("未授权表拒绝", func(t *testing.T) {
		err := AuthorizeTables([]TableRef{ref}, testResolve, testAllow("svc.db.users"))
		if err == nil {
			t.Fatal("未授权表应拒绝")
		}
		if err.Kind != gwerr.KindPermission {
			t.Errorf("kind = %s，期望 %s", err.Kind, gwerr.KindPermission)
		}
		if err.Details["reason"] != "not_granted" {
			t.Errorf("reason = %v，期望 not_granted", err.Details["reason"])
		}
		if got := err.Details["candidate_fqns"]; got == nil {
			t.Error("details 应带 candidate_fqns 供调用方诊断")
		}
	})

	t.Run("allow 为 nil 全拒（fail closed）", func(t *testing.T) {
		err := AuthorizeTables([]TableRef{ref}, testResolve, nil)
		if err == nil {
			t.Fatal("allow=nil 应全拒")
		}
	})

	t.Run("多表其一未授权即拒绝", func(t *testing.T) {
		err := AuthorizeTables([]TableRef{ref, {Table: "events"}}, testResolve, testAllow("svc.db.orders"))
		if err == nil {
			t.Fatal("任一表未授权即拒绝")
		}
		if err.Details["table"] != "events" {
			t.Errorf("details.table = %v，期望先失败表 events", err.Details["table"])
		}
	})
}

// ── 段一+二端到端：拒绝语义机器可区分（AC 4，§5.2 拒绝语义）──────────────────

func TestCheckRejectionSemantics(t *testing.T) {
	t.Run("语法错误 → invalid_request/syntax_error", func(t *testing.T) {
		assertReject(t, `SELEC 1`, gwerr.KindInvalidRequest, "syntax_error")
	})

	t.Run("语法错误含位置信息", func(t *testing.T) {
		_, err := Check(`SELECT FROM WHERE`, testResolve, testAllow("svc.db.orders"))
		if err == nil {
			t.Fatal("应拒绝")
		}
		if err.Details["error"] == "" {
			t.Error("details.error 应含解析器原始错误")
		}
	})

	t.Run("非 SELECT → invalid_request/non_select（非权限错误）", func(t *testing.T) {
		err := assertReject(t, `DELETE FROM orders`, gwerr.KindInvalidRequest, "non_select")
		if err.Details["stmt"] != "delete_stmt" {
			t.Errorf("details.stmt = %v，期望 delete_stmt", err.Details["stmt"])
		}
	})

	t.Run("无权限 → permission_denied/not_granted（与语法错误同请求可区分）", func(t *testing.T) {
		err := assertReject(t, `SELECT * FROM users`, gwerr.KindPermission, "not_granted")
		if err.Details["table"] != "users" {
			t.Errorf("details.table = %v，期望 users", err.Details["table"])
		}
	})

	t.Run("通过：Report 返回语法层表引用", func(t *testing.T) {
		rep, err := Check(`SELECT * FROM orders o JOIN users u ON o.uid = u.id`, testResolve, testAllow("svc.db.orders", "svc.db.users"))
		if err != nil {
			t.Fatalf("不应拒绝: %s", err)
		}
		if len(rep.Tables) != 2 {
			t.Fatalf("Tables = %v，期望 2 个", rep.Tables)
		}
	})

	t.Run("EXPLAIN 不作授权依据：整体按 utility 拒绝，不提取内部表", func(t *testing.T) {
		_, err := Check(`EXPLAIN SELECT * FROM secret_table`, testResolve, testAllow("svc.db.orders"))
		if err == nil {
			t.Fatal("EXPLAIN 应作为 utility 拒绝")
		}
		if err.Kind != gwerr.KindInvalidRequest {
			t.Errorf("kind = %s，期望 %s", err.Kind, gwerr.KindInvalidRequest)
		}
	})
}
