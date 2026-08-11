package collector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// liveTable 是 PG 侧 introspection 的一张表（校准基线）。
type liveTable struct {
	Schema  string
	Name    string
	Columns []liveColumn
}

// liveColumn 是 PG 侧的一列；Type 是归一化类型（带 typmod：
// character varying(128) / numeric(30,12)），与草稿可比。
type liveColumn struct {
	Name string
	Type string
}

// Calibrate 连只读从库（共享只读角色，v1 低优先、可跳过）做生产校准：
// 草稿（migration 推导）vs PG 实际结构，只报告不改（ADR-0007
// 「漂移报告：只报告不自动改」）。查询只用 information_schema 与
// pg_catalog 只读视图，不做任何写操作。
//
// schemas 是本次校准的 schema 清单（"" = public；草稿里每个表的
// schema 去重后即为候选）。返回按（severity, message）排序的发现。
func Calibrate(ctx context.Context, dsn string, st *Structure) ([]Finding, error) {
	// 会话级只读强制：只读契约不能只依赖 DSN 角色——角色配错/未来
	// 加写逻辑都会击穿「只报告不改」，连接参数兜底。
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析校准 DSN 失败: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("连接校准库失败（请核对只读从库 DSN）: %w", err)
	}
	defer conn.Close(ctx)

	// 生产形态 = 每服务一库：连接的库即服务边界，全 schema 扫描
	// （限制在草稿 schema 会让「生产表草稿缺失」漏掉迁移新建的
	// schema 里的表）。
	live, err := introspect(ctx, conn)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	liveByTable := map[string]*liveTable{}
	for i := range live {
		liveByTable[qualKey(live[i].Schema, live[i].Name)] = &live[i]
	}
	// 草稿有而 PG 没有 = error（草稿编错了表/列——校准抓的就是这个）。
	for _, t := range st.Tables {
		lt, ok := liveByTable[qualKey(schemaOrPublic(t.Schema), t.Name)]
		if !ok {
			findings = append(findings, Finding{SourceCalibrate, SeverityError,
				fmt.Sprintf("草稿表 %s 在生产库不存在（schema %q 未建表或表名不一致）", t.Name, schemaOrPublic(t.Schema))})
			continue
		}
		liveCols := map[string]string{}
		for _, c := range lt.Columns {
			liveCols[c.Name] = c.Type
		}
		for _, c := range t.Columns {
			lt, ok := liveCols[c.Name]
			if !ok {
				findings = append(findings, Finding{SourceCalibrate, SeverityError,
					fmt.Sprintf("草稿列 %s.%s 在生产库不存在", t.Name, c.Name)})
				continue
			}
			// 类型比较用归一化等价（varchar(128) vs character
			// varying(128) 同型；带 typmod 的差异才是漂移信号）。
			if draftTypeToInfoSchema(c.Type) != lt {
				findings = append(findings, Finding{SourceCalibrate, SeverityWarn,
					fmt.Sprintf("列 %s.%s 类型漂移：草稿=%s 生产=%s", t.Name, c.Name, c.Type, lt)})
			}
		}
		for _, lc := range lt.Columns {
			if st.findTable(t.Name).findColumn(lc.Name) == nil {
				findings = append(findings, Finding{SourceCalibrate, SeverityWarn,
					fmt.Sprintf("生产列 %s.%s 草稿缺失（迁移文件未建/迁移滞后，提示性）", t.Name, lc.Name)})
			}
		}
	}
	// PG 有而草稿没有的表 = warn（手工 DDL 先例（payment_channel 事件）
	// 正是校准要暴露的漂移）。
	for _, lt := range live {
		if _, ok := liveByTable[qualKey(lt.Schema, lt.Name)]; !ok {
			continue
		}
		if st.findTable(lt.Name) == nil {
			findings = append(findings, Finding{SourceCalibrate, SeverityWarn,
				fmt.Sprintf("生产表 %s.%s 草稿缺失（迁移外手工 DDL 或迁移滞后）", lt.Schema, lt.Name)})
		}
	}
	sortFindings(findings)
	return findings, nil
}

// qualKey 拼 schema.表 复合键（同名表跨 schema 不互相干扰）。
func qualKey(schema, name string) string { return schema + "." + name }

// introspect 查 information_schema.columns（排除系统 schema/表）。
// 类型归一化：data_type + character_maximum_length/numeric_precision/
// numeric_scale 拼回带 typmod 形态，与草稿可比。
func introspect(ctx context.Context, conn *pgx.Conn) ([]liveTable, error) {
	rows, err := conn.Query(ctx, `
		SELECT table_schema, table_name, column_name, data_type,
		       character_maximum_length, numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
		  AND table_schema NOT LIKE 'pg_toast%'
		  AND table_name NOT LIKE 'pg_%'
		ORDER BY table_schema, table_name, ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("查询 information_schema.columns 失败: %w", err)
	}
	defer rows.Close()

	byName := map[string]*liveTable{}
	var order []string
	for rows.Next() {
		var schema, table, column, typ string
		var charLen, numPrec, numScale *int
		if err := rows.Scan(&schema, &table, &column, &typ, &charLen, &numPrec, &numScale); err != nil {
			return nil, err
		}
		key := qualKey(schema, table)
		t, ok := byName[key]
		if !ok {
			t = &liveTable{Schema: schema, Name: table}
			byName[key] = t
			order = append(order, key)
		}
		t.Columns = append(t.Columns, liveColumn{Name: column, Type: normalizeInfoSchemaType(typ, charLen, numPrec, numScale)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]liveTable, 0, len(order))
	for _, key := range order {
		out = append(out, *byName[key])
	}
	return out, nil
}

// normalizeInfoSchemaType 把 information_schema 的类型字段归一为
// 与草稿可比的形态：varchar(128) → character varying(128)、
// numeric(30,12) → numeric(30,12)、无 typmod → 原名。
func normalizeInfoSchemaType(typ string, charLen, numPrec, numScale *int) string {
	switch typ {
	case "character varying":
		if charLen != nil {
			return fmt.Sprintf("character varying(%d)", *charLen)
		}
		return "character varying"
	case "character":
		if charLen != nil {
			return fmt.Sprintf("character(%d)", *charLen)
		}
		return "character"
	case "numeric", "decimal":
		if numPrec != nil && numScale != nil {
			return fmt.Sprintf("numeric(%d,%d)", *numPrec, *numScale)
		}
		return "numeric"
	case "timestamp with time zone", "timestamp without time zone",
		"time with time zone", "time without time zone":
		// datetime_precision 缺省 6（微秒）是 PG 默认，作者写 timestamptz
		// 不带精度；显式精度差异不算漂移信号（v1 校准只抓类型族差异）。
	}
	return typ
}

func schemaOrPublic(s string) string {
	if s == "" {
		return "public"
	}
	return s
}

// draftTypeToInfoSchema 把草稿的作者类型写法归一为 information_schema
// 形态（varchar(128) → character varying(128)、timestamptz →
// timestamp with time zone），与 normalizeInfoSchemaType 的输出可比。
func draftTypeToInfoSchema(t string) string {
	base := strings.ToLower(t)
	mods := ""
	if i := strings.Index(base, "("); i >= 0 {
		base = base[:i]
		mods = t[i:]
	}
	norm := map[string]string{
		"varchar": "character varying", "char": "character",
		"int2": "smallint", "int4": "integer", "int8": "bigint",
		"bool": "boolean", "float4": "real", "float8": "double precision",
		"timestamptz": "timestamp with time zone", "timestamp": "timestamp without time zone",
		"timetz": "time with time zone", "time": "time without time zone",
		"decimal": "numeric",
		// serial 族：information_schema 报底层类型（bigint），
		// 不归一的话每个自增主键都报假漂移。
		"serial": "integer", "bigserial": "bigint", "smallserial": "smallint",
	}
	if v, ok := norm[base]; ok {
		return v + mods
	}
	return t
}
