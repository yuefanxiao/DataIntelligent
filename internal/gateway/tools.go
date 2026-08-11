package gateway

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 六工具（ADR-0003）：五个语义检索原语 + execute_sql。
// 输入 schema 即 v1 工具面，全部 stub：调用返回结构化「未实现」错误。
// 描述与术语以 CONTEXT.md 为准；参数细节由 04/08 票落地。

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
	MaxDepth  int    `json:"max_depth,omitempty" jsonschema:"最大跳数；缺省 1"`
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

// stub 构造一个返回结构化「未实现」错误的类型化 handler。
// Out 用 any：stub 阶段不声明输出 schema，实现落地时（04/08）替换。
func stub[In any](tool string) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		return errResult(gwerr.NotImplemented(tool)), nil, nil
	}
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

func registerTools(s *mcp.Server, g *Gateway) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_entities",
		Description: "双入口关键词检索：按业务概念或指标定位实体（关键词+向量 RRF 混合，≤20 条 + total）。",
		Annotations: readOnly(),
	}, stub[searchEntitiesInput]("search_entities"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_entity",
		Description: "FQN 精确查询单个实体：含枚举挂列、is_time、关系摘要。",
		Annotations: readOnly(),
	}, stub[getEntityInput]("get_entity"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "traverse_relations",
		Description: "沿类型化关系边遍历（connects_to / contains / references / describes，双向、多跳），理解服务→库→表→列→指标→概念链路与可 join 关系。",
		Annotations: readOnly(),
	}, stub[traverseRelationsInput]("traverse_relations"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_metric_definition",
		Description: "读取指标口径（表达式 + 聚合 + 过滤，机器可读），可选带时间参数做 dry-run 展开（不执行）。",
		Annotations: readOnly(),
	}, stub[getMetricDefinitionInput]("get_metric_definition"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_enum_values",
		Description: "查询列的枚举取值（status 类字段的业务含义）。",
		Annotations: readOnly(),
	}, stub[listEnumValuesInput]("list_enum_values"))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "execute_sql",
		Description: "执行只读 SQL（经校验层四段链：AST 分类 → 表授权 → 物理边界 → 限额包层；结果有界 + truncated 标记；dbname 指定目标库）。",
		Annotations: readOnly(),
	}, g.handleExecuteSQL)
}
