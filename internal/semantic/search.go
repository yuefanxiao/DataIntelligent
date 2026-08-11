package semantic

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 检索域（08 票，ADR-0002/0005）：五个语义工具的数据层原语——双入口
// 关键词检索（FTS5 主通道 + vec0 向量兜底 RRF 混合）、FQN 精确查询
// （含枚举挂列与关系摘要）、类型化边遍历（双向多跳、有界）、指标口径
// + 带时间参数 dry-run 展开、列枚举取值。运行时只查 SQLite（ADR-0002）。

// ErrNotFound 是检索目标实体不存在的哨兵错误（get_entity /
// traverse_relations / get_metric_definition / list_enum_values 的
// 404 语义；网关映射为结构化 invalid_request/not_found）。
var ErrNotFound = errors.New("entity not found")

// ErrNotMetric / ErrNotColumn 是实体类型与工具不匹配的哨兵错误
// （get_metric_definition 收到非指标、list_enum_values 收到非列；
// 网关映射为 invalid_request/wrong_kind——工具用错，可调整重试）。
var (
	ErrNotMetric = errors.New("entity is not a metric")
	ErrNotColumn = errors.New("entity is not a column")
)

// 检索边界（spec §4.9「语义列表返回上限 20 + total」，有界返回）：
const (
	// SearchLimit 是 search_entities 的返回条数上限（spec §4.9 固定值）。
	SearchLimit = 20
	// SearchCandidateCap 是 RRF 单通道的候选上限：关键词/向量各取前 N 进
	// 融合。关键词权重 2 : 向量权重 1 + 同候选上限 ⇒ 关键词命中恒排在
	// 向量命中之前（ADR-0002「关键词命中排向量命中之前」，见 rrfScore）。
	SearchCandidateCap = 50
	// rrfK / rrfKeywordWeight / rrfVectorWeight 是 RRF 融合参数（k=60 是
	// RRF 惯例常数；关键词通道权重 2 保证「关键词主通道优先」）。
	rrfK             = 60.0
	rrfKeywordWeight = 2.0
	rrfVectorWeight  = 1.0
	// EnumValuesLimit 是 list_enum_values 的返回上限（枚举取值有界；
	// CHECK 约束语义下列取值通常是个位数到几十个）。
	EnumValuesLimit = 100
	// MaxTraverseDepth 是 traverse_relations 的跳数硬上限（输入超限截断
	// 到此值，不报错——与 SQL 行数截断同一哲学）。
	MaxTraverseDepth = 5
	// MaxTraverseNodes 是 traverse_relations 的节点数硬上限（触界截断 +
	// truncated 标记）。
	MaxTraverseNodes = 200
	// maxRelationSummaryEdges 是 get_entity 关系摘要的单实体边数上限
	// （摘要 = 有界切片，触界截断 + 标记）。
	maxRelationSummaryEdges = 100
)

// SearchHit 是一次检索命中的实体摘要（结构化 JSON 的条目形状）。
type SearchHit struct {
	FQN         string
	Kind        Kind
	Name        string
	Description string
}

// SearchEntities 是双入口关键词+向量 RRF 混合检索（ADR-0002/0005）：
//
//   - 关键词主通道：FTS5 trigram（≥3 字符短语匹配，CJK/英文子串均可）；
//     短查询（<3 字符）退 LIKE 兜底（trigram 无法索引 3 字符以下窗口）。
//     按实体类型过滤（kind 列，查询侧）。
//   - 向量兜底通道：查询文本经 emb 嵌入 → vec0 KNN 取候选。emb 为 nil
//     或向量库不可用（未同步/维度不符/嵌入失败）= 降级为纯关键词检索
//     （向量是兜底通道，缺失不阻断主通道）；降级原因经 logf 上报
//     （调用方负责落日志，同 EmbedEntityTexts 的 logf 惯例；nil = 静默）。
//   - 融合：加权 RRF（关键词 2 : 向量 1，k=60）→ 关键词命中恒在向量
//     命中之前（ADR-0002）；返回 ≤ limit 条 + total（候选并集大小）。
//
// typ 限定实体类型："" = 概念+指标双入口混合；"concept" / "metric" 单入口。
func SearchEntities(ctx context.Context, st DBer, query, typ string, emb Embedder, limit int, logf func(format string, args ...any)) ([]SearchHit, int, error) {
	if strings.TrimSpace(query) == "" {
		return nil, 0, fmt.Errorf("SearchEntities: 查询为空")
	}
	switch typ {
	case "", "concept", "metric":
	default:
		return nil, 0, fmt.Errorf("SearchEntities: 未知实体类型 %q（concept / metric）", typ)
	}

	// ── 关键词通道（主）──────────────────────────────────────────────
	kwHits, err := keywordHits(ctx, st, query, typ, SearchCandidateCap)
	if err != nil {
		return nil, 0, err
	}

	// ── 向量通道（兜底）──────────────────────────────────────────────
	vecHits := []string{}
	if emb != nil {
		vecHits, err = vectorHits(ctx, st, emb, query, typ, SearchCandidateCap)
		if err != nil {
			// 降级：向量通道故障不阻断主通道；原因如实上报（网关落日志，
			// 「向量兜底缺失」对排障可见——review 修复）。
			if logf != nil {
				logf("search_entities 向量通道降级为纯关键词: %v", err)
			}
			vecHits = nil
		}
	}

	// ── RRF 融合 ─────────────────────────────────────────────────────
	// 排位 = 通道内名次（1 起）；score = Σ w/(k+rank)。关键词权重 2 与
	// 同候选上限 50 的组合保证：关键词最差（rank 50，2/110）仍高于向量
	// 最好（rank 1，1/61）——「关键词命中排向量命中之前」由参数保证而非
	// 依赖 bm25/distance 的数值巧合。
	union := map[string]*rrfEntry{}
	for i, f := range kwHits {
		e, ok := union[f]
		if !ok {
			e = &rrfEntry{fqn: f}
			union[f] = e
		}
		e.kwRank = i + 1
	}
	for i, f := range vecHits {
		e, ok := union[f]
		if !ok {
			e = &rrfEntry{fqn: f}
			union[f] = e
		}
		e.vecRank = i + 1
	}
	ranked := make([]*rrfEntry, 0, len(union))
	for _, e := range union {
		if e.kwRank > 0 {
			e.score += rrfKeywordWeight / (rrfK + float64(e.kwRank))
		}
		if e.vecRank > 0 {
			e.score += rrfVectorWeight / (rrfK + float64(e.vecRank))
		}
		ranked = append(ranked, e)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.score != b.score {
			return a.score > b.score
		}
		// 同分（罕见）确定性次序：关键词名次 → 向量名次 → FQN。
		if a.kwRank != b.kwRank {
			return a.kwRank < b.kwRank
		}
		if a.vecRank != b.vecRank {
			return a.vecRank < b.vecRank
		}
		return a.fqn < b.fqn
	})

	// ── 实体明细组装（候选内批量取，避免 N+1）────────────────────────
	fqns := make([]string, 0, min(limit, len(ranked)))
	for _, e := range ranked[:min(limit, len(ranked))] {
		fqns = append(fqns, e.fqn)
	}
	byFQN, err := entitiesByFQNs(ctx, st, fqns)
	if err != nil {
		return nil, 0, err
	}
	hits := make([]SearchHit, 0, len(fqns))
	for _, f := range fqns {
		e, ok := byFQN[f]
		if !ok {
			continue // 候选是索引残留（墓碑后未清理）：跳过
		}
		hits = append(hits, SearchHit{FQN: e.FQN, Kind: e.Kind, Name: e.Name, Description: e.Description})
	}
	return hits, len(ranked), nil
}

// rrfEntry 是 RRF 融合的一个候选（rank 0 = 不在该通道）。
type rrfEntry struct {
	fqn     string
	kwRank  int
	vecRank int
	score   float64
}

// keywordHits 关键词通道：FTS5 trigram 主查 + 短查询 LIKE 兜底，返回
// 按相关度排序的实体 FQN（≤ cap 条，已按 kind 过滤）。
func keywordHits(ctx context.Context, st DBer, query, typ string, cap int) ([]string, error) {
	kinds := searchKinds(typ)
	if utf8.RuneCountInString(query) < 3 {
		return likeHits(ctx, st, query, kinds, cap)
	}
	// 短语包裹：FTS5 查询语法字符（- " ( ) * 等）一律字面化（引号加倍），
	// 杜绝「payment-service」被解析成「payment AND NOT service」类歧义；
	// trigram 分词下短语 = 子串匹配（含 CJK）。
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	// 参数顺序与 SQL 占位符一一对应：MATCH → kind IN → LIMIT。
	args := append([]any{phrase}, append(anySlice(kinds), cap)...)
	rows, err := st.DB().QueryContext(ctx, `
		SELECT fqn FROM dgw_sem_fts
		WHERE dgw_sem_fts MATCH ? AND kind IN (`+kindPlaceholders(len(kinds))+`)
		ORDER BY rank, fqn LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search %q: %w", query, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// likeHits 是短查询（<3 字符，trigram 无法索引）的 LIKE 兜底（ADR-0005
// 「FTS5 前缀 + LIKE 兜底」）：对 fqn/name/description 做子串匹配，
// 通配符（% _ \）转义为字面。
func likeHits(ctx context.Context, st DBer, query string, kinds []Kind, cap int) ([]string, error) {
	pattern := "%" + escapeLike(query) + "%"
	rows, err := st.DB().QueryContext(ctx, `
		SELECT fqn FROM dgw_sem_entities
		WHERE tombstone = 0 AND kind IN (`+kindPlaceholders(len(kinds))+`)
		  AND (fqn LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\')
		ORDER BY fqn LIMIT ?`,
		append(anySlice(kinds), pattern, pattern, pattern, cap)...)
	if err != nil {
		return nil, fmt.Errorf("like search %q: %w", query, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// vectorHits 向量通道：查询文本嵌入 → vec0 KNN（余弦距离升序）→ 返回
// ≤ cap 条实体 FQN（按距离排序，已按 kind 过滤）。向量库不可用（表缺失
// /维度不符/嵌入失败）返回错误——调用方降级为纯关键词检索。
func vectorHits(ctx context.Context, st DBer, emb Embedder, query, typ string, cap int) ([]string, error) {
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embed query: 返回 %d 个向量（期望 1）", len(vecs))
	}
	kinds := searchKinds(typ)
	// KNN 候选先按类型过滤（vec0 无 kind 列，join 实体表取 kind）。
	// 参数顺序与 SQL 占位符一一对应：MATCH → kind IN → k。
	// k 是 vec0 的 KNN 约束（外层 LIMIT 对虚拟表扫描不可见，会报
	// 「A LIMIT or 'k = ?' constraint is required」，实测）。
	args := append([]any{encodeFloats(vecs[0])}, append(anySlice(kinds), cap)...)
	rows, err := st.DB().QueryContext(ctx, `
		SELECT v.entity_fqn FROM dgw_sem_vec v
		JOIN dgw_sem_entities e ON e.fqn = v.entity_fqn AND e.tombstone = 0
		WHERE v.vector MATCH ? AND e.kind IN (`+kindPlaceholders(len(kinds))+`)
		  AND k = ?
		ORDER BY v.distance`, args...)
	if err != nil {
		return nil, fmt.Errorf("vec0 knn: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// searchKinds 返回检索覆盖的实体类型（双入口 = 概念 + 指标）。
func searchKinds(typ string) []Kind {
	if typ == "concept" {
		return []Kind{KindConcept}
	}
	if typ == "metric" {
		return []Kind{KindMetric}
	}
	return []Kind{KindConcept, KindMetric}
}

// entitiesByFQNs 批量取实体（跳过墓碑），分块 IN 查询避免 SQLite 参数
// 上限（默认 999）。返回 map 保序性由调用方维护。
func entitiesByFQNs(ctx context.Context, st DBer, fqns []string) (map[string]Entity, error) {
	out := map[string]Entity{}
	const chunk = 500
	for start := 0; start < len(fqns); start += chunk {
		end := min(start+chunk, len(fqns))
		part := fqns[start:end]
		query := `SELECT fqn, kind, name, description, tombstone FROM dgw_sem_entities
			WHERE fqn IN (?` + strings.Repeat(",?", len(part)-1) + `)`
		args := make([]any, len(part))
		for i, f := range part {
			args[i] = f
		}
		rows, err := st.DB().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("batch get entities: %w", err)
		}
		for rows.Next() {
			var e Entity
			var kind string
			var tomb int
			if err := rows.Scan(&e.FQN, &kind, &e.Name, &e.Description, &tomb); err != nil {
				rows.Close()
				return nil, err
			}
			if tomb != 0 {
				continue
			}
			e.Kind = Kind(kind)
			out[e.FQN] = e
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ── get_entity 细节：实体 + 枚举挂列 + 关系摘要 ────────────────────────

// EntityDetail 是 get_entity 的完整返回：实体本体 + 枚举挂列（列实体）+
// 关系摘要（四类边 × 出入方向，有界）。
type EntityDetail struct {
	Entity
	Enums          []EnumValue       // 列实体的枚举取值（非列 = 空；有界）
	EnumsTruncated bool              // 枚举触界截断（>EnumValuesLimit）
	Relations      []RelationSummary // 关系摘要（按类型分组，仅非空类型）
	RelTruncated   bool              // 关系摘要触界截断
}

// RelationSummary 是一类关系边的出入摘要（FQN 字符串，按 FQN 排序）。
type RelationSummary struct {
	Type     RelationType
	Outgoing []string // 本实体出发的边目标（src = 本实体）
	Incoming []string // 指向本实体的边来源（dst = 本实体）
}

// GetEntityDetail 按 FQN 精确查询实体细节；不存在返回 (nil, ErrNotFound)。
func GetEntityDetail(ctx context.Context, st DBer, fqn string) (*EntityDetail, error) {
	e, err := GetEntity(ctx, st, fqn)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, fqn)
	}
	d := &EntityDetail{Entity: *e}
	if e.Kind == KindColumn {
		d.Enums, err = enumValuesFor(ctx, st, fqn, EnumValuesLimit+1)
		if err != nil {
			return nil, err
		}
		// 枚举挂列同样有界（spec §4.9「语义列表返回上限」）：多取一行
		// 判定截断，超限截断到上限（review 修复）。
		if len(d.Enums) > EnumValuesLimit {
			d.Enums = d.Enums[:EnumValuesLimit]
			d.EnumsTruncated = true
		}
	}
	// 关系摘要：一次查询取全部入/出边（有界 + 截断标记），按类型分组。
	rows, err := st.DB().QueryContext(ctx, `
		SELECT type, src_fqn, dst_fqn FROM dgw_sem_relations
		WHERE tombstone = 0 AND (src_fqn = ? OR dst_fqn = ?)
		ORDER BY type, src_fqn, dst_fqn LIMIT ?`, fqn, fqn, maxRelationSummaryEdges+1)
	if err != nil {
		return nil, fmt.Errorf("relations of %s: %w", fqn, err)
	}
	defer rows.Close()
	byType := map[RelationType]*RelationSummary{}
	order := []RelationType{}
	n := 0
	for rows.Next() {
		var typ string
		var r Relation
		if err := rows.Scan(&typ, &r.SrcFQN, &r.DstFQN); err != nil {
			return nil, err
		}
		if n >= maxRelationSummaryEdges {
			d.RelTruncated = true
			continue // 触界：边本身丢弃（摘要 = 有界切片）
		}
		n++
		t := RelationType(typ)
		s, ok := byType[t]
		if !ok {
			s = &RelationSummary{Type: t}
			byType[t] = s
			order = append(order, t)
		}
		if r.SrcFQN == fqn {
			s.Outgoing = append(s.Outgoing, r.DstFQN)
		} else {
			s.Incoming = append(s.Incoming, r.SrcFQN)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range order {
		d.Relations = append(d.Relations, *byType[t])
	}
	return d, nil
}

// ── traverse_relations：类型化边遍历 ──────────────────────────────────

// TraversalResult 是一次遍历的完整结果：访问到的节点（去重，含起点）+
// 触达的边（去重）+ 触界截断标记。
type TraversalResult struct {
	Nodes     []Entity
	Edges     []Relation
	Truncated bool
}

// TraverseRelations 沿类型化关系边做有界遍历（ADR-0001「双向可遍历」）：
// BFS，起点缺失返回 ErrNotFound。direction = out（沿 src→dst）/ in（沿
// dst→src 反向）/ both；maxDepth ≤ MaxTraverseDepth（输入超限截断到硬
// 上限并标记 truncated）、节点数 ≤ maxNodes（触界 truncated=true，超出
// 的边一并丢弃——结果始终是节点集内一致的子图，无悬空边）。边按库内
// 真实方向返回（in 方向遍历到的边仍是 src→dst 原方向，含 meta，如
// references 的 on 条件——review 修复：反向遍历不反转边）。
func TraverseRelations(ctx context.Context, st DBer, start string, typ RelationType, direction string, maxDepth, maxNodes int) (*TraversalResult, error) {
	if e, err := GetEntity(ctx, st, start); err != nil {
		return nil, err
	} else if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, start)
	}
	switch direction {
	case "out", "in", "both":
	default:
		return nil, fmt.Errorf("TraverseRelations: 未知遍历方向 %q（out / in / both）", direction)
	}
	depthCapped := maxDepth > MaxTraverseDepth // 输入超硬上限：结果比请求小，如实标记
	maxDepth = min(max(maxDepth, 1), MaxTraverseDepth)

	type queueItem struct {
		fqn   string
		depth int
	}
	visited := map[string]bool{start: true}
	edgeSet := map[edgeKey]Relation{}
	queue := []queueItem{{fqn: start, depth: 0}}
	truncated := depthCapped

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue // 深度已到界：不再展开（节点本身已收录）
		}
		edges, err := relationEdges(ctx, st, typ, cur.fqn, direction)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			nb := e.DstFQN
			if nb == cur.fqn {
				nb = e.SrcFQN // in 方向的边（dst = 本实体）
			}
			if !visited[nb] {
				if len(visited) >= maxNodes {
					truncated = true
					continue // 触界：节点与其边一并丢弃（子图一致）
				}
				visited[nb] = true
				queue = append(queue, queueItem{fqn: nb, depth: cur.depth + 1})
			}
			edgeSet[edgeKey{e.Type, e.SrcFQN, e.DstFQN}] = e
		}
	}

	// 节点明细（批量取）+ 边去重排序（确定性输出）。
	res := &TraversalResult{Truncated: truncated}
	fqns := make([]string, 0, len(visited))
	for f := range visited {
		fqns = append(fqns, f)
	}
	sort.Strings(fqns)
	byFQN, err := entitiesByFQNs(ctx, st, fqns)
	if err != nil {
		return nil, err
	}
	for _, f := range fqns {
		if e, ok := byFQN[f]; ok {
			res.Nodes = append(res.Nodes, e)
		}
	}
	for _, r := range edgeSet {
		res.Edges = append(res.Edges, r)
	}
	sort.Slice(res.Edges, func(i, j int) bool {
		a, b := res.Edges[i], res.Edges[j]
		if a.SrcFQN != b.SrcFQN {
			return a.SrcFQN < b.SrcFQN
		}
		return a.DstFQN < b.DstFQN
	})
	return res, nil
}

// relationEdges 返回某实体沿指定类型边的全部关系行（方向已按 direction
// 解析，边保持库内原始方向 + meta）：
//   - out：src = 本实体的边（沿 src→dst 前进）；
//   - in：dst = 本实体的边（沿 dst→src 反向前进，边的 src/dst 不反转）；
//   - both：两者并集。
//
// 排序稳定（src, dst），遍历结果确定性。
func relationEdges(ctx context.Context, st DBer, typ RelationType, fqn, direction string) ([]Relation, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT src_fqn, dst_fqn, meta FROM dgw_sem_relations
		WHERE type = ? AND tombstone = 0 AND (src_fqn = ? OR dst_fqn = ?)
		ORDER BY src_fqn, dst_fqn`, string(typ), fqn, fqn)
	if err != nil {
		return nil, fmt.Errorf("relations %s of %s: %w", typ, fqn, err)
	}
	defer rows.Close()
	out := []Relation{}
	for rows.Next() {
		var r Relation
		r.Type = typ
		if err := rows.Scan(&r.SrcFQN, &r.DstFQN, &r.Meta); err != nil {
			return nil, err
		}
		switch direction {
		case "out":
			if r.SrcFQN != fqn {
				continue
			}
		case "in":
			if r.DstFQN != fqn {
				continue
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// edgeKey 是遍历边去重键（同一条边经多条路径触达只记一次）。
type edgeKey struct {
	typ RelationType
	src string
	dst string
}

// ── get_metric_definition：口径 + dry-run 展开 ─────────────────────────

// MetricDef 是指标口径（machine-readable，ADR-0001）+ 可选带时间参数的
// dry-run 展开 SQL（不执行，spec §4.3）：表达式/聚合/过滤原样 + 依赖表
// + 时间谓词（[start, end) 半开区间，应用到各依赖表的 is_time 列）。
type MetricDef struct {
	FQN         string
	Name        string
	Description string
	Expression  string
	Aggregation string
	Filter      string
	Tables      []string // 依赖底层表（describes 边 dst，授权展开依据）
	DryRunSQL   string   // 展开 SQL（无依赖表或不可展开时为空）
	TimeApplied bool     // 时间参数是否已展开进 SQL（至少一个表应用）
	Note        string   // 展开限制说明（缺 is_time 列/多时间列等）
}

// MetricDefinition 读取指标口径并（可选）dry-run 展开；指标不存在返回
// ErrNotFound，非指标实体返回错误。
func MetricDefinition(ctx context.Context, st DBer, fqn string, start, end *time.Time) (*MetricDef, error) {
	e, err := GetEntity(ctx, st, fqn)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, fqn)
	}
	if e.Kind != KindMetric {
		return nil, fmt.Errorf("%w: %s（kind=%s）", ErrNotMetric, fqn, e.Kind)
	}
	if start != nil || end != nil {
		if start == nil || end == nil || !start.Before(*end) {
			return nil, fmt.Errorf("MetricDefinition: 时间参数需同时给出且 start < end")
		}
	}
	d := &MetricDef{
		FQN: fqn, Name: e.Name, Description: e.Description,
		Expression: e.Expression, Aggregation: e.Aggregation, Filter: e.Filter,
	}
	tables, err := MetricTables(ctx, st, fqn)
	if err != nil {
		return nil, err
	}
	d.Tables = tables
	if len(tables) == 0 {
		d.Note = "指标未声明依赖表（tables 为空），无法 dry-run 展开"
		return d, nil
	}

	// 展开 SQL：SELECT <expr> AS <name> FROM <t1>, <t2> WHERE ...
	// FROM 用 PG 表名（FQN 末段；execute_sql 路由按服务.库.表解析同名单表），
	// 时间谓词按表限定（多表时列名不歧义）。
	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s AS %s FROM %s", d.Expression, d.Name, tableNames(tables))
	conds := []string{}
	if d.Filter != "" {
		conds = append(conds, d.Filter)
	}
	if start != nil && end != nil {
		notes := []string{}
		applied := 0
		for _, tbl := range tables {
			col, n, err := timeColumnOf(ctx, st, tbl)
			if err != nil {
				return nil, err
			}
			if col == "" {
				notes = append(notes, fmt.Sprintf("表 %s 无 is_time 列，时间参数未应用", tbl))
				continue
			}
			if n > 1 {
				notes = append(notes, fmt.Sprintf("表 %s 有 %d 个 is_time 列，取第一个 %s", tbl, n, col))
			}
			applied++
			conds = append(conds, fmt.Sprintf("%s.%s >= '%s'", tableName(tbl), col,
				start.Format(time.RFC3339)))
			conds = append(conds, fmt.Sprintf("%s.%s < '%s'", tableName(tbl), col,
				end.Format(time.RFC3339)))
		}
		d.TimeApplied = applied > 0
		if len(notes) > 0 {
			d.Note = strings.Join(notes, "；")
		}
	}
	if len(conds) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(conds, " AND "))
	}
	d.DryRunSQL = sb.String()
	return d, nil
}

// timeColumnOf 返回表的时间列名（is_time=1 的列；0 或 1 个以内是常态，
// 多个取第一个并回报数量——调用方决定提示）。列不存在返回空串。
func timeColumnOf(ctx context.Context, st DBer, tableFQN string) (col string, count int, err error) {
	prefix := tableFQN + "."
	rows, err := st.DB().QueryContext(ctx, `
		SELECT name FROM dgw_sem_entities
		WHERE kind = 'column' AND tombstone = 0 AND is_time = 1
		  AND substr(fqn, 1, length(?)) = ? COLLATE BINARY
		ORDER BY fqn`, prefix, prefix)
	if err != nil {
		return "", 0, fmt.Errorf("time column of %s: %w", tableFQN, err)
	}
	defer rows.Close()
	var first string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", 0, err
		}
		count++
		if first == "" {
			first = name
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return first, count, nil
}

// tableName 返回表 FQN 的 PG 表名（FQN 末段）。
func tableName(fqn string) string {
	i := strings.LastIndex(fqn, ".")
	if i < 0 {
		return fqn
	}
	return fqn[i+1:]
}

func tableNames(fqns []string) string {
	names := make([]string, len(fqns))
	for i, f := range fqns {
		names[i] = tableName(f)
	}
	return strings.Join(names, ", ")
}

// ── list_enum_values：列枚举取值 ──────────────────────────────────────

// ListEnumValues 返回列的枚举取值（CHECK 约束语义，spec §4.3）：按
// value 排序、有界（limit + total + truncated）。列不存在返回 ErrNotFound，
// 非列实体返回错误。
func ListEnumValues(ctx context.Context, st DBer, columnFQN string, limit int) ([]EnumValue, int, bool, error) {
	e, err := GetEntity(ctx, st, columnFQN)
	if err != nil {
		return nil, 0, false, err
	}
	if e == nil {
		return nil, 0, false, fmt.Errorf("%w: %s", ErrNotFound, columnFQN)
	}
	if e.Kind != KindColumn {
		return nil, 0, false, fmt.Errorf("%w: %s（kind=%s）", ErrNotColumn, columnFQN, e.Kind)
	}
	values, err := enumValuesFor(ctx, st, columnFQN, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}
	total, err := enumCount(ctx, st, columnFQN)
	if err != nil {
		return nil, 0, false, err
	}
	return values, total, truncated, nil
}

// enumValuesFor 查一列的枚举取值（墓碑过滤，value 排序，最多 limit 条）。
func enumValuesFor(ctx context.Context, st DBer, columnFQN string, limit int) ([]EnumValue, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT column_fqn, value, label FROM dgw_sem_enum_values
		WHERE column_fqn = ? AND tombstone = 0
		ORDER BY value LIMIT ?`, columnFQN, limit)
	if err != nil {
		return nil, fmt.Errorf("enum values of %s: %w", columnFQN, err)
	}
	defer rows.Close()
	out := []EnumValue{}
	for rows.Next() {
		var v EnumValue
		if err := rows.Scan(&v.ColumnFQN, &v.Value, &v.Label); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// enumCount 返回一列的枚举取值总数（total 字段）。
func enumCount(ctx context.Context, st DBer, columnFQN string) (int, error) {
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM dgw_sem_enum_values WHERE column_fqn = ? AND tombstone = 0`,
		columnFQN).Scan(&n); err != nil {
		return 0, fmt.Errorf("count enum values of %s: %w", columnFQN, err)
	}
	return n, nil
}

// ── 小工具 ────────────────────────────────────────────────────────────

// kindPlaceholders 生成 kind IN 的占位符串（至少 1 个）。
func kindPlaceholders(n int) string {
	if n < 1 {
		n = 1
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// anySlice 把 []Kind 转成 []any（SQL 参数）。
func anySlice(kinds []Kind) []any {
	out := make([]any, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

// escapeLike 转义 LIKE 通配符（配合 ESCAPE '\'）。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
