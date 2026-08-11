package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
)

// 五个语义工具的 handler（08 票，替换 01 的 stub；ADR-0002/0003）：
// search_entities / get_entity / traverse_relations / get_metric_definition
// / list_enum_values。语义元数据面认证即读（spec §4.4：不做表级授权，
// 敏感信息不进语义层 YAML 面）；结果一律结构化 JSON（SDK 把 Out 编码进
// StructuredContent + TextContent）；全部调用经 logged 包装落执行记录
// （06 票六工具全记，本文件不重复埋点）。
//
// 错误映射统一走 semToolErr：实体不存在/类型不符 = invalid_request（调用
// 方可调整参数重试），存储故障 = internal（不可自愈）。

// ── search_entities ───────────────────────────────────────────────────

type searchEntitiesResult struct {
	Hits  []searchHit `json:"hits"`  // ≤ 20 条（spec §4.9）
	Total int         `json:"total"` // 候选并集总数（关键词+向量两通道）
}

type searchHit struct {
	FQN         string `json:"fqn"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleSearchEntities 双入口检索：关键词 FTS5 主通道 + 向量兜底，加权
// RRF 融合（关键词命中恒在向量命中之前，ADR-0002）。向量通道未配置
// （WithSearchEmbed 未注入）或向量库不可用 = 纯关键词检索（降级不报错）。
func (g *Gateway) handleSearchEntities(ctx context.Context, req *mcp.CallToolRequest, in searchEntitiesInput) (*mcp.CallToolResult, *searchEntitiesResult, error) {
	if strings.TrimSpace(in.Query) == "" {
		return errResult(gwerr.InvalidRequest("查询为空", map[string]any{"reason": "empty"})), nil, nil
	}
	switch in.Type {
	case "", "concept", "metric":
	default:
		return errResult(gwerr.InvalidRequest(
			fmt.Sprintf("未知实体类型 %q（concept / metric）", in.Type),
			map[string]any{"reason": "bad_type", "type": in.Type})), nil, nil
	}
	// 向量通道降级（embedding 服务/向量库不可用）如实落网关日志——「向量
	// 兜底缺失」对排障可见（review 修复）。
	logf := func(format string, args ...any) {
		g.logger.Warn("search_entities 向量通道降级（纯关键词检索）", "tool", "search_entities", "err", fmt.Sprintf(format, args...))
	}
	hits, total, err := semantic.SearchEntities(ctx, g.store, in.Query, in.Type, g.searchEmbed, semantic.SearchLimit, logf)
	if err != nil {
		return errResult(gwerr.Internal(fmt.Sprintf("语义检索失败: %v", err))), nil, nil
	}
	out := &searchEntitiesResult{Total: total}
	for _, h := range hits {
		out.Hits = append(out.Hits, searchHit{FQN: h.FQN, Kind: string(h.Kind), Name: h.Name, Description: h.Description})
	}
	return nil, out, nil
}

// ── get_entity ────────────────────────────────────────────────────────

type getEntityResult struct {
	FQN         string `json:"fqn"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// column 专属：
	DataType string `json:"data_type,omitempty"`
	IsTime   bool   `json:"is_time"` // 时间轴标注（时间列检索的判别依据）
	// table 专属：
	PGSchema string `json:"pg_schema,omitempty"`
	// metric 专属（machine-readable 口径）：
	Expression  string `json:"expression,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
	Filter      string `json:"filter,omitempty"`
	// 枚举挂列（列实体）+ 关系摘要：
	Enums          []enumValueResult       `json:"enums"`
	EnumsTruncated bool                    `json:"enums_truncated,omitempty"`
	Relations      []relationSummaryResult `json:"relations"`
	RelTruncated   bool                    `json:"rel_truncated,omitempty"`
}

type enumValueResult struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type relationSummaryResult struct {
	Type     string   `json:"type"`
	Outgoing []string `json:"outgoing,omitempty"` // 本实体出发的边目标
	Incoming []string `json:"incoming,omitempty"` // 指向本实体的边来源
}

// handleGetEntity FQN 精确查询（含枚举挂列、is_time、关系摘要）。
func (g *Gateway) handleGetEntity(ctx context.Context, req *mcp.CallToolRequest, in getEntityInput) (*mcp.CallToolResult, *getEntityResult, error) {
	if strings.TrimSpace(in.FQN) == "" {
		return errResult(gwerr.InvalidRequest("FQN 为空", map[string]any{"reason": "empty"})), nil, nil
	}
	d, err := semantic.GetEntityDetail(ctx, g.store, in.FQN)
	if err != nil {
		return errResult(semToolErr(err)), nil, nil
	}
	e := d.Entity
	out := &getEntityResult{
		FQN: e.FQN, Kind: string(e.Kind), Name: e.Name, Description: e.Description,
		DataType: e.DataType, IsTime: e.IsTime, PGSchema: e.PGSchema,
		Expression: e.Expression, Aggregation: e.Aggregation, Filter: e.Filter,
		// 空切片初始化：非列实体也输出 []（不是 null），JSON 形状稳定。
		Enums: []enumValueResult{}, Relations: []relationSummaryResult{},
		EnumsTruncated: d.EnumsTruncated, RelTruncated: d.RelTruncated,
	}
	for _, v := range d.Enums {
		out.Enums = append(out.Enums, enumValueResult{Value: v.Value, Label: v.Label})
	}
	for _, r := range d.Relations {
		out.Relations = append(out.Relations, relationSummaryResult{
			Type: string(r.Type), Outgoing: r.Outgoing, Incoming: r.Incoming,
		})
	}
	return nil, out, nil
}

// ── traverse_relations ────────────────────────────────────────────────

type traverseRelationsResult struct {
	Nodes     []traverseNode `json:"nodes"` // 访问到的节点（去重，含起点）
	Edges     []traverseEdge `json:"edges"` // 触达的边（去重）
	Truncated bool           `json:"truncated,omitempty"`
}

type traverseNode struct {
	FQN         string `json:"fqn"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type traverseEdge struct {
	Type string `json:"type"`
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Meta string `json:"meta,omitempty"` // references 的 on 条件等
}

// handleTraverseRelations 类型化边遍历（双向多跳、有界：深度硬上限 5、
// 节点数硬上限 200，触界 truncated 标记——与 SQL 行数截断同一哲学）。
func (g *Gateway) handleTraverseRelations(ctx context.Context, req *mcp.CallToolRequest, in traverseRelationsInput) (*mcp.CallToolResult, *traverseRelationsResult, error) {
	if strings.TrimSpace(in.FQN) == "" {
		return errResult(gwerr.InvalidRequest("FQN 为空", map[string]any{"reason": "empty"})), nil, nil
	}
	switch semantic.RelationType(in.Relation) {
	case semantic.RelConnectsTo, semantic.RelContains, semantic.RelReferences, semantic.RelDescribes:
	default:
		return errResult(gwerr.InvalidRequest(
			fmt.Sprintf("未知关系边类型 %q（connects_to / contains / references / describes）", in.Relation),
			map[string]any{"reason": "bad_relation", "relation": in.Relation})), nil, nil
	}
	direction := in.Direction
	if direction == "" {
		direction = "out"
	}
	switch direction {
	case "out", "in", "both":
	default:
		return errResult(gwerr.InvalidRequest(
			fmt.Sprintf("未知遍历方向 %q（out / in / both）", in.Direction),
			map[string]any{"reason": "bad_direction", "direction": in.Direction})), nil, nil
	}
	if in.MaxDepth < 0 {
		return errResult(gwerr.InvalidRequest(
			"max_depth 不能为负", map[string]any{"reason": "bad_depth", "max_depth": in.MaxDepth})), nil, nil
	}
	res, err := semantic.TraverseRelations(ctx, g.store, in.FQN,
		semantic.RelationType(in.Relation), direction, in.MaxDepth, semantic.MaxTraverseNodes)
	if err != nil {
		return errResult(semToolErr(err)), nil, nil
	}
	out := &traverseRelationsResult{Truncated: res.Truncated}
	for _, n := range res.Nodes {
		out.Nodes = append(out.Nodes, traverseNode{FQN: n.FQN, Kind: string(n.Kind), Name: n.Name, Description: n.Description})
	}
	for _, e := range res.Edges {
		out.Edges = append(out.Edges, traverseEdge{Type: string(e.Type), Src: e.SrcFQN, Dst: e.DstFQN, Meta: e.Meta})
	}
	return nil, out, nil
}

// ── get_metric_definition ─────────────────────────────────────────────

type getMetricDefinitionResult struct {
	FQN         string `json:"fqn"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// machine-readable 口径（ADR-0001，OSI 式）：
	Expression  string   `json:"expression"`
	Aggregation string   `json:"aggregation"`
	Filter      string   `json:"filter,omitempty"`
	Tables      []string `json:"tables"` // 依赖底层表（describes 边 dst）
	// 可选带时间参数 dry-run 展开（不执行）：
	DryRunSQL   string `json:"dry_run_sql,omitempty"`
	TimeApplied bool   `json:"time_applied,omitempty"`
	Note        string `json:"note,omitempty"`
}

// handleGetMetricDefinition 指标口径 + 可选带时间参数 dry-run 展开：
// 表达式/聚合/过滤原样 + 时间谓词（[start, end) 半开区间）应用到依赖表
// 的 is_time 列，产出可直接交给 execute_sql 的 SQL 文本；本工具不执行。
func (g *Gateway) handleGetMetricDefinition(ctx context.Context, req *mcp.CallToolRequest, in getMetricDefinitionInput) (*mcp.CallToolResult, *getMetricDefinitionResult, error) {
	if strings.TrimSpace(in.FQN) == "" {
		return errResult(gwerr.InvalidRequest("指标 FQN 为空", map[string]any{"reason": "empty"})), nil, nil
	}
	var start, end *time.Time
	if in.TimeRange != nil {
		var err error
		start, end, err = parseTimeRange(in.TimeRange.Start, in.TimeRange.End)
		if err != nil {
			return errResult(gwerr.InvalidRequest(err.Error(), map[string]any{"reason": "bad_time"})), nil, nil
		}
	}
	d, err := semantic.MetricDefinition(ctx, g.store, in.FQN, start, end)
	if err != nil {
		return errResult(semToolErr(err)), nil, nil
	}
	return nil, &getMetricDefinitionResult{
		FQN: d.FQN, Name: d.Name, Description: d.Description,
		Expression: d.Expression, Aggregation: d.Aggregation, Filter: d.Filter,
		Tables: d.Tables, DryRunSQL: d.DryRunSQL, TimeApplied: d.TimeApplied, Note: d.Note,
	}, nil
}

// parseTimeRange 解析并校验时间窗口（ISO 8601 / RFC3339；start < end）。
func parseTimeRange(startStr, endStr string) (*time.Time, *time.Time, error) {
	if strings.TrimSpace(startStr) == "" || strings.TrimSpace(endStr) == "" {
		return nil, nil, fmt.Errorf("时间参数需同时给出 start 与 end（ISO 8601）")
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return nil, nil, fmt.Errorf("时间起点 %q 不是 ISO 8601（RFC3339）: %v", startStr, err)
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		return nil, nil, fmt.Errorf("时间终点 %q 不是 ISO 8601（RFC3339）: %v", endStr, err)
	}
	if !start.Before(end) {
		return nil, nil, fmt.Errorf("时间窗口无效：start %s 必须早于 end %s", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return &start, &end, nil
}

// ── list_enum_values ──────────────────────────────────────────────────

type listEnumValuesResult struct {
	ColumnFQN string            `json:"column_fqn"`
	Values    []enumValueResult `json:"values"`
	Total     int               `json:"total"`
	Truncated bool              `json:"truncated,omitempty"`
}

// handleListEnumValues 列枚举取值（CHECK 约束语义）：按 value 排序、
// 有界（≤100 + total + truncated）。
func (g *Gateway) handleListEnumValues(ctx context.Context, req *mcp.CallToolRequest, in listEnumValuesInput) (*mcp.CallToolResult, *listEnumValuesResult, error) {
	if strings.TrimSpace(in.FQN) == "" {
		return errResult(gwerr.InvalidRequest("列 FQN 为空", map[string]any{"reason": "empty"})), nil, nil
	}
	values, total, truncated, err := semantic.ListEnumValues(ctx, g.store, in.FQN, semantic.EnumValuesLimit)
	if err != nil {
		return errResult(semToolErr(err)), nil, nil
	}
	out := &listEnumValuesResult{ColumnFQN: in.FQN, Total: total, Truncated: truncated}
	for _, v := range values {
		out.Values = append(out.Values, enumValueResult{Value: v.Value, Label: v.Label})
	}
	return nil, out, nil
}

// semToolErr 把语义检索层错误映射为结构化错误（spec §4.5「任何一段失败 =
// 结构化错误回传」）：
//   - 实体不存在（ErrNotFound）→ invalid_request/not_found（FQN 打错，
//     调用方可调整重试）；
//   - 类型不符（ErrNotMetric / ErrNotColumn）→ invalid_request/wrong_kind
//     （工具与实体类型不匹配）；
//   - 其余 → internal（存储故障等，调用方不可自愈）。
func semToolErr(err error) *gwerr.Error {
	switch {
	case errors.Is(err, semantic.ErrNotFound):
		return gwerr.InvalidRequest(err.Error(), map[string]any{"reason": "not_found"})
	case errors.Is(err, semantic.ErrNotMetric), errors.Is(err, semantic.ErrNotColumn):
		return gwerr.InvalidRequest(err.Error(), map[string]any{"reason": "wrong_kind"})
	default:
		return gwerr.Internal(err.Error())
	}
}
