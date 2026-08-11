package gateway

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/validate"
)

// pgTypeMap 是 PG 内置类型 OID → 类型名的默认注册表（pgtype 自带）。
var pgTypeMap = pgtype.NewMap()

// handleExecuteSQL 是 execute_sql 工具实现（04 票）：校验层四段链全链挂载。
//
// 链（ADR-0008）：
//  1. AST 分类 + 表提取 + 授权比对 —— validate 包（03 票）：Resolve 注入
//     dbname 路由的服务归属（FQN 服务段），Allow 注入 authz 表 FQN 白名单
//     （02 票，默认拒绝）；
//  2. PG 物理边界 —— db.Router 连接级：共享只读角色 + statement_timeout +
//     default_transaction_read_only（禁 SET ROLE = 物理 + 分类双保险）；
//  3. 执行限额 —— LIMIT 包层（多取一行检测截断 + truncated 标记）；
//  4. 结构化 JSON 结果（列名+类型+行数组+元信息），渲染交给 Agent。
//
// 任何一段失败 = 结构化错误回传（gwerr），网关不重试、无自愈循环。
// 执行记录（06 票）在此挂接点之前/之后插入，不改本函数签名。
// 负载防护（05 票并发闸）挂在链首：饱和时连路由/校验都不做，直接拒绝。
func (g *Gateway) handleExecuteSQL(ctx context.Context, req *mcp.CallToolRequest, in executeSQLInput) (*mcp.CallToolResult, *sqlResult, error) {
	if g.execSQL == nil {
		return errResult(gwerr.InvalidRequest(
			"execute_sql 未配置：网关未注入 PG 路由（DGW_PG_DATABASES）",
			map[string]any{"reason": "not_configured"},
		)), nil, nil
	}
	if strings.TrimSpace(in.SQL) == "" {
		return errResult(gwerr.InvalidRequest(
			"SQL 为空",
			map[string]any{"reason": "empty"},
		)), nil, nil
	}

	// ── 闸：并发闸（负载防护，05 票）─────────────────────────────────────
	// 每 key 并发 2 + 进程级总并发 8 双信号量（spec §4.9）：超限结构化拒绝
	// （rate_limited，不排队、快速失败，§6.3 负向例 4）。闸在路由/校验之前
	// ——饱和时连校验 CPU 都不花。key = 调用凭据（KeyFromContext：HTTP 经
	// TokenInfo.Extra 注入，stdio 进程 key 预置——一用户多 key 各占配额）；
	// 身份缺失时回退用户/unknown（防御，正常路径不可达）。stdio 形态单 key
	// 单进程，天然退化为每进程闸。占用位持有到本调用结束（defer Release）；
	// 进程级闸全 key 共享（守护进程语义）。
	keyID, _ := KeyFromContext(ctx)
	if keyID == "" {
		if uid, ok := UserFromContext(ctx); ok {
			keyID = uid
		} else {
			keyID = "unknown"
		}
	}
	if e := g.loadGate.TryAcquire(keyID); e != nil {
		return errResult(e), nil, nil
	}
	defer g.loadGate.Release(keyID)

	start := time.Now()
	// 分阶段耗时（06 票执行记录）：认证阶段由 logged 包装器从 TokenInfo
	// 注入；本函数打 权限（allow 回调）/解析（Parse+Check 总耗时减权限）/
	// 执行（查询+编码）。stageTimerFrom 在 logged 注入的 ctx 上恒有值。
	timer := stageTimerFrom(ctx)

	// ── 段 0：dbname 路由（目标库 + 服务归属）──────────────────────────────
	dbname := in.DBName
	if dbname == "" {
		if single := g.execSQL.router.Single(); single != "" {
			dbname = single
		} else {
			return errResult(gwerr.InvalidRequest(
				"需指定 dbname（配置了多个数据库，无法推断目标库）",
				map[string]any{"reason": "dbname_required"},
			)), nil, nil
		}
	}
	pool, service, ok := g.execSQL.router.Lookup(dbname)
	if !ok {
		return errResult(gwerr.InvalidRequest(
			fmt.Sprintf("未知数据库：%q（不在 DGW_PG_DATABASES 路由表）", dbname),
			map[string]any{"reason": "unknown_dbname", "dbname": dbname},
		)), nil, nil
	}

	// ── 段 1：AST 分类 + 表提取 + 授权比对（03 票，fail closed）────────────
	// 授权判定必须经 AuthorizeBusinessTable（02 票的单入口契约）：
	// 授权路径永远走它，validate 只决定错误形状（unknown_table 是映射失败，
	// 不走授权判定）。
	resolve := func(ref validate.TableRef) []string {
		// v1 表均在 public schema（生产形态，spec §8）：未限定或 public →
		// 单候选 FQN 服务.库.表；其他 schema 无法映射（未知表，拒绝）。
		if ref.Schema != "" && ref.Schema != "public" {
			return nil
		}
		return []string{service + "." + dbname + "." + ref.Table}
	}
	allow := func(fqn string) bool {
		permStart := time.Now()
		ok := g.AuthorizeBusinessTable(ctx, fqn) == nil
		timer.Add(execrecord.StagePerm, time.Since(permStart))
		return ok
	}
	// 语句计数先于 Check（Check 内部会再解析一次——WASM 解析毫秒级，可接受）：
	// 0 条 = 纯注释等无可执行内容；>1 条 = 批处理（pgx 单语句协议 + 包层括号
	// 内不允许语句分隔，v1 无批处理语义）——都显式结构化拒绝，避免误导性 42601。
	parseStart := time.Now()
	stmts, perr := validate.Parse(in.SQL)
	if perr != nil {
		timer.Add(execrecord.StageParse, time.Since(parseStart))
		return errResult(perr), nil, nil
	}
	if len(stmts) == 0 {
		timer.Add(execrecord.StageParse, time.Since(parseStart))
		return errResult(gwerr.InvalidRequest(
			"SQL 不含可执行语句（仅注释/空白）",
			map[string]any{"reason": "empty"},
		)), nil, nil
	}
	if len(stmts) > 1 {
		timer.Add(execrecord.StageParse, time.Since(parseStart))
		return errResult(gwerr.InvalidRequest(
			"多条语句不被支持（execute_sql 一次执行一条只读查询）",
			map[string]any{"reason": "multi_statement"},
		)), nil, nil
	}
	if _, verr := validate.Check(in.SQL, resolve, allow); verr != nil {
		// 解析阶段 = 链总耗时减权限比对（Check 内部：解析→分类→提取→逐表
		// 比对，唯一的外部回调是 allow（perm 已单独打点），不重叠）。
		timer.Add(execrecord.StageParse, time.Since(parseStart)-timer.Get(execrecord.StagePerm))
		return errResult(verr), nil, nil
	}
	timer.Add(execrecord.StageParse, time.Since(parseStart)-timer.Get(execrecord.StagePerm))

	// ── 段 3+4：限额包层执行 + 结果编码 ───────────────────────────────────
	execStart := time.Now()
	rows, err := pool.Query(ctx, wrapLimit(in.SQL, g.execSQL.limit))
	if err != nil {
		timer.Add(execrecord.StageExec, time.Since(execStart))
		return errResult(pgError(err)), nil, nil
	}
	defer rows.Close()

	res, err := encodeRows(rows, g.execSQL.limit, dbname, in.PlanID, time.Since(start))
	if err != nil {
		// 执行期错误可能在结果迭代阶段才浮现（如 statement_timeout 57014
		// 经 rows.Err() 返回）——同样按 pgError 分类，不吞成 internal。
		timer.Add(execrecord.StageExec, time.Since(execStart))
		return errResult(pgError(err)), nil, nil
	}
	timer.Add(execrecord.StageExec, time.Since(execStart))
	return nil, res, nil
}

// wrapLimit 是限额包层（ADR-0008 段四）：SELECT * FROM (<用户 SQL>) _q LIMIT n+1
// ——多取一行用于 truncated 判定；Limit 短路不扫描。用户 SQL 的尾部分号剥掉
// （包层括号内不允许语句分隔）。
func wrapLimit(sql string, limit int) string {
	s := strings.TrimSpace(sql)
	s = strings.TrimSuffix(s, ";")
	return fmt.Sprintf("SELECT * FROM (%s) _q LIMIT %d", s, limit+1)
}

// sqlResult 是 execute_sql 的结构化 JSON 结果（列名+类型+行数组+元信息，
// 渲染交给 Agent）。
type sqlResult struct {
	Columns []sqlColumn `json:"columns"`
	Rows    [][]any     `json:"rows"`
	Meta    sqlMeta     `json:"meta"`
}

type sqlColumn struct {
	Name string `json:"name"`
	Type string `json:"type"` // PG 类型名（pg_type.typname）
}

type sqlMeta struct {
	RowCount  int    `json:"row_count"`         // 实际返回行数（≤ limit）
	Truncated bool   `json:"truncated"`         // 超过限额被截断
	DBName    string `json:"dbname"`            // 路由目标库（审计）
	PlanID    string `json:"plan_id,omitempty"` // 溯源透传（v1 不校验）
	ElapsedMS int64  `json:"elapsed_ms"`        // 校验+执行总耗时
}

// encodeRows 读取 ≤ limit+1 行并编码：多取的第 limit+1 行只用于 truncated
// 判定，结果最多返回 limit 行。解码失败 = 内部错误（结果不可信，宁缺毋滥）。
func encodeRows(rows pgx.Rows, limit int, dbname, planID string, elapsed time.Duration) (*sqlResult, error) {
	res := &sqlResult{
		Meta: sqlMeta{DBName: dbname, PlanID: planID, ElapsedMS: elapsed.Milliseconds()},
	}
	for _, f := range rows.FieldDescriptions() {
		res.Columns = append(res.Columns, sqlColumn{Name: f.Name, Type: pgTypeName(f.DataTypeOID)})
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Meta.Truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, normalizeRow(vals))
	}
	res.Meta.RowCount = len(res.Rows)
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// pgTypeName 把列的类型 OID 转成 PG 类型名（pg_type.typname）；内置类型
// 由 pgtype 默认注册表覆盖，未知 OID 退回 oid 数字（不 panic）。
func pgTypeName(oid uint32) string {
	if t, ok := pgTypeMap.TypeForOID(oid); ok {
		return t.Name
	}
	return fmt.Sprintf("unknown_oid_%d", oid)
}

// normalizeRow 把 pgx 解码的一行转成稳定 JSON 形态（逐值归一化）。
func normalizeRow(vals []any) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = normalizeValue(v)
	}
	return out
}

// normalizeValue 把 pgx 解码值转成稳定 JSON 形态：
//   - 原生 Go 类型（int/float/bool/string/nil）原样（json 原生支持）；
//   - time.Time → RFC3339Nano（时间轴稳定）；
//   - []byte / [16]byte → 文本形态（bytea \x 十六进制；uuid 规范格式——
//     pgx 把 uuid 解码为原始 16 字节，JSON 数组对 Agent 无意义）；
//   - pgtype 包装结构体（numeric/interval/jsonb…）→ driver.Valuer 的文本/
//     时间形态（与 psql 展示一致），递归归一化；
//   - 其余类型原样透传（json.Marshal 兜底）。
func normalizeValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []byte:
		return fmt.Sprintf("\\x%x", x)
	case [16]byte:
		return uuidString(x)
	case netip.Addr:
		return x.String()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeValue(e)
		}
		return out
	case driver.Valuer:
		val, err := x.Value()
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return normalizeValue(val)
	default:
		return v
	}
}

// uuidString 把原始 16 字节格式化为规范 uuid 文本（8-4-4-4-12）。
func uuidString(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// pgError 把 PG 执行错误映射为结构化错误（区分可机器处理的类别）：
//   - 57014 语句超时 → invalid_request/timeout（Agent 可决定降级或放弃）；
//   - 42501 权限拒绝（物理边界兜底）→ permission_denied；
//   - 08* 连接类 / 57P* 实例停机类 → internal（调用方不可自愈）；
//   - 其余（含 42601 语法错误）→ invalid_request/pg_error + SQLSTATE。
//
// 网关不重试；「被拒原因」由 06 票的执行记录落盘。
func pgError(err error) *gwerr.Error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		details := map[string]any{"reason": "pg_error", "pg_code": pgErr.Code, "pg_message": pgErr.Message}
		switch pgErr.Code {
		case "57014": // query_canceled（statement_timeout）
			details["reason"] = "timeout"
			return gwerr.InvalidRequest(fmt.Sprintf("查询超时（statement_timeout）: %s", pgErr.Message), details)
		case "42501": // insufficient_privilege——角色层兜底拒绝
			details["reason"] = "not_granted"
			return gwerr.PermissionDenied(fmt.Sprintf("数据库层权限拒绝（物理边界兜底）: %s", pgErr.Message), details)
		}
		if strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P") {
			// 连接失败（08001/08006…）/ 实例关闭（57P01/57P02/57P03…）
			return gwerr.Internal(fmt.Sprintf("数据库不可用 [%s]: %s", pgErr.Code, pgErr.Message))
		}
		return gwerr.InvalidRequest(fmt.Sprintf("数据库执行失败 [%s]: %s", pgErr.Code, pgErr.Message), details)
	}
	return gwerr.Internal(fmt.Sprintf("数据库执行失败: %v", err))
}
