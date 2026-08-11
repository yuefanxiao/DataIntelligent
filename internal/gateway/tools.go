package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 六工具（ADR-0003）：五个语义检索原语 + execute_sql。
// 输入 schema 即 v1 工具面；handler 实现：五个语义工具在 semantic_tools.go
// （08 票，替换 01 的 stub），execute_sql 在 execute_sql.go（04 票）。
// 描述与术语以 CONTEXT.md 为准。

type searchEntitiesInput struct {
	Query string `json:"query" jsonschema:"搜索关键词（业务概念或指标的双入口）"`
	Type  string `json:"type,omitempty" jsonschema:"实体类型限定：concept 或 metric；缺省双入口混合检索"`
}

type getEntityInput struct {
	FQN string `json:"fqn" jsonschema:"实体稳定 FQN（服务.库.表.列 / 指标 / 概念）；同一命名空间即权限挂载点"`
}

type traverseRelationsInput struct {
	FQN       string `json:"fqn" jsonschema:"起始实体 FQN"`
	Relation  string `json:"relation" jsonschema:"关系边类型：connects_to / contains / references / describes"`
	Direction string `json:"direction,omitempty" jsonschema:"遍历方向：out / in / both；缺省 out"`
	MaxDepth  int    `json:"max_depth,omitempty" jsonschema:"最大跳数；缺省 1，硬上限 5（触界截断）"`
}

type timeRange struct {
	Start string `json:"start,omitempty" jsonschema:"时间窗口起点（ISO 8601）；时间过滤是查询参数，不进指标定义"`
	End   string `json:"end,omitempty" jsonschema:"时间窗口终点（ISO 8601）"`
}

type getMetricDefinitionInput struct {
	FQN       string     `json:"fqn" jsonschema:"指标 FQN"`
	TimeRange *timeRange `json:"time_range,omitempty" jsonschema:"可选时间参数：dry-run 展开时代入（不执行）"`
}

type listEnumValuesInput struct {
	FQN string `json:"fqn" jsonschema:"列 FQN（服务.库.表.列），返回该列的枚举取值"`
}

type executeSQLInput struct {
	SQL    string `json:"sql" jsonschema:"只读 SQL（仅 SELECT；行数默认上限 500 / 硬上限 5000，超限截断 + truncated 标记）"`
	DBName string `json:"dbname,omitempty" jsonschema:"目标数据库（dbname 路由；配置单库时缺省推断，多库时必填）"`
	PlanID string `json:"plan_id,omitempty" jsonschema:"溯源透传字段（v1 不校验；v2 规划引擎启用后关联查询计划）"`
}

// errResult 把 gwerr 编码进工具调用结果：isError=true + JSON text content；
// 再经 SetError 记录底层错误，服务端中间件可用 GetError 检视。
func errResult(e *gwerr.Error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(e.JSON())}},
	}
	res.SetError(e)
	return res
}

// readOnly 是六工具的公共注解：全部只读（execute_sql 的只读由校验层强制）。
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// logged 包装类型化工具 handler：调用前后打执行记录（kind=tool_call，
// spec §4.6 六工具全记）。执行记录失败不影响调用结果——记录是诊断设施，
// 不阻断请求（ADR-0006「故障响应不依赖任何审计设施」，fail-open）。
//
// 记录内容：身份（ctx）/参数（input 原文，execute_sql 的 SQL 不脱敏）/
// 状态与被拒原因（由结果或 gwerr 推导）/行数·truncated·plan_id（execute_sql
// 结果元信息）/分阶段耗时（认证→权限→解析→执行→返回，handler 内打点 +
// 本包装器的返回阶段）。
func logged[In, Out any](g *Gateway, tool string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		if g.execlog == nil {
			return h(ctx, req, input)
		}
		callStart := time.Now()
		timer := newStageTimer(ctx)
		res, out, err := h(withStageTimer(ctx, timer), req, input)
		retStart := time.Now()
		rec := buildToolRecord(ctx, timer, callStart, tool, input, res, out, err)
		timer.Add(execrecord.StageReturn, time.Since(retStart))
		rec.StagesMS = timer.ms()
		if lerr := g.execlog.LogToolCall(rec); lerr != nil {
			g.logger.Error("执行记录写入失败（不阻断调用）", "tool", tool, "err", lerr)
		}
		return res, out, err
	}
}

// buildToolRecord 组装工具调用的执行记录：状态由结果/错误推导（成功/拒绝/
// 超时/解析失败），被拒原因 = gwerr 原文（如实）；execute_sql 成功结果附带
// 行数/truncated/plan_id（结果元信息）。ts 用调用开始时刻（跨零点调用落
// 发起日文件）。
func buildToolRecord[In, Out any](ctx context.Context, timer *stageTimer, callStart time.Time, tool string, input In, res *mcp.CallToolResult, out Out, err error) execrecord.ToolCall {
	user, _ := UserFromContext(ctx)
	key, _ := KeyFromContext(ctx)
	rec := execrecord.ToolCall{
		TS:     callStart,
		User:   user,
		Key:    key,
		Tool:   tool,
		Params: input,
	}
	if err != nil {
		// SDK 层错误返回（正常路径不可达——网关错误一律编码进结果；兜底
		// 拒绝并如实落原因）。
		rec.Status = execrecord.StatusRejected
		rec.Reject = gwerr.Internal(err.Error())
		return rec
	}
	if res != nil && res.IsError {
		if ge, ok := rejectOf(res); ok && ge != nil {
			rec.Reject = ge
			rec.Status = statusOf(ge)
		} else {
			rec.Status = execrecord.StatusRejected
		}
		return rec
	}
	rec.Status = execrecord.StatusSuccess
	if sr, ok := any(out).(*sqlResult); ok && sr != nil {
		r, tr := sr.Meta.RowCount, sr.Meta.Truncated
		rec.Rows, rec.Truncated = &r, &tr
		rec.PlanID = sr.Meta.PlanID
	}
	return rec
}

// rejectOf 从错误结果里取 gwerr：优先 SetError 的底层错误（errResult 路径），
// 兜底解析 content 的 gwerr JSON。返回 (原因, 是否错误结果)。
func rejectOf(res *mcp.CallToolResult) (*gwerr.Error, bool) {
	if res == nil || !res.IsError {
		return nil, false
	}
	if e, ok := res.GetError().(*gwerr.Error); ok {
		return e, true
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			var e gwerr.Error
			if json.Unmarshal([]byte(tc.Text), &e) == nil && e.Kind != "" {
				return &e, true
			}
		}
	}
	return nil, true
}

// statusOf 按 gwerr 推导执行记录状态（spec §4.6 状态契约）：
//   - reason=timeout → 超时（statement_timeout，pg 57014 映射）
//   - reason=syntax_error → 解析失败
//   - 其余错误 → 拒绝（被拒原因如实落 reject 字段）
func statusOf(e *gwerr.Error) string {
	if e == nil {
		return execrecord.StatusSuccess
	}
	if r, _ := e.Details["reason"].(string); r == "timeout" {
		return execrecord.StatusTimeout
	}
	if r, _ := e.Details["reason"].(string); r == "syntax_error" {
		return execrecord.StatusParseError
	}
	return execrecord.StatusRejected
}

func registerTools(s *mcp.Server, g *Gateway) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_entities",
		Description: "双入口关键词检索：按业务概念或指标定位实体（关键词+向量 RRF 混合，≤20 条 + total）。",
		Annotations: readOnly(),
	}, logged(g, "search_entities", g.handleSearchEntities))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_entity",
		Description: "FQN 精确查询单个实体：含枚举挂列、is_time、关系摘要。",
		Annotations: readOnly(),
	}, logged(g, "get_entity", g.handleGetEntity))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "traverse_relations",
		Description: "沿类型化关系边遍历（connects_to / contains / references / describes，双向、多跳、有界），理解服务→库→表→列→指标→概念链路与可 join 关系。",
		Annotations: readOnly(),
	}, logged(g, "traverse_relations", g.handleTraverseRelations))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_metric_definition",
		Description: "读取指标口径（表达式 + 聚合 + 过滤，机器可读），可选带时间参数做 dry-run 展开（不执行）。",
		Annotations: readOnly(),
	}, logged(g, "get_metric_definition", g.handleGetMetricDefinition))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_enum_values",
		Description: "查询列的枚举取值（status 类字段的业务含义），有界返回。",
		Annotations: readOnly(),
	}, logged(g, "list_enum_values", g.handleListEnumValues))

	// 并发闸数值动态注入（env 可配，Agent 侧描述与实际配置一致；
	// New 保证 loadGate 恒非 nil）。
	perKey, processTotal := g.loadGate.Limits()
	mcp.AddTool(s, &mcp.Tool{
		Name: "execute_sql",
		Description: fmt.Sprintf(
			"执行只读 SQL（经校验层四段链：AST 分类 → 表授权 → 物理边界 → 限额包层；结果有界 + truncated 标记；dbname 指定目标库）。并发闸：每 key %d / 进程级 %d 同时查询，超限 rate_limited 快速拒绝（不排队）。",
			perKey, processTotal),
		Annotations: readOnly(),
	}, logged(g, "execute_sql", g.handleExecuteSQL))
}
