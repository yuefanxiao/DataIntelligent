package semantic

import (
	"fmt"
	"strings"
)

// Compile 把作者入口原始定义编译为 Target：校验 FQN 唯一 / 引用完整性 /
// 指标 SQL 可解析 / 枚举合法，任一失败 = 编译错误（调用方原子拒绝，不落库）。
//
// 编译是纯函数（输入 rawInput，输出 Target）：同输入同输出（§5.3 seam 的
// 确定性前提）；不触库、不触网。
//
// 两遍编译（对抗评审修复）：一遍登记全部实体（服务→库→表→列→指标→概念），
// 引用只记录不校验；二遍统一校验引用完整性并落关系边——解除「引用指向编译
// 顺序在后的实体被误判缺失」的顺序依赖（跨服务 references 是核心建模能力，
// 不应受服务文件名排序影响）。
func Compile(in *rawInput) (*Target, error) {
	c := &compiler{entities: map[string]Entity{}}
	for _, sf := range in.services {
		if err := c.compileService(sf); err != nil {
			return nil, err
		}
	}
	for _, m := range in.metrics {
		if err := c.compileMetric(m); err != nil {
			return nil, err
		}
	}
	for _, cpt := range in.concepts {
		if err := c.compileConcept(cpt); err != nil {
			return nil, err
		}
	}
	if err := c.resolveRefs(); err != nil {
		return nil, err
	}
	t := &Target{
		Entities:  c.order,
		Relations: c.relations,
		Enums:     c.enums,
	}
	return t, nil
}

type compiler struct {
	entities  map[string]Entity // 按 FQN（kind 从实体取，不另存）
	order     []Entity          // 编译顺序（服务→库→表→列→指标→概念），diff 展示稳定
	relations []Relation
	enums     []EnumValue
	// 二遍校验的待检引用（references + 指标 tables 要 KindTable；
	// 概念 describes 允许 table/column/metric，分两列）。
	pendingTableRefs []pendingRef
	pendingDescribes []pendingRef
}

// pendingRef 是一条待二遍校验的引用：目标存在 + 类型符合才落关系边。
type pendingRef struct {
	what   string // 引用方描述（错误信息用）
	src    string // 边起点
	target string // 被引用实体 FQN
	typ    RelationType
	meta   string
}

func (c *compiler) addEntity(e Entity) error {
	if prev, dup := c.entities[e.FQN]; dup {
		return fmt.Errorf("FQN 重复: %q 同时定义为 %s 和 %s（FQN 全库唯一，含指标/概念命名空间）",
			e.FQN, prev.Kind, e.Kind)
	}
	c.entities[e.FQN] = e
	c.order = append(c.order, e)
	return nil
}

func (c *compiler) addRelation(t RelationType, src, dst, meta string) {
	c.relations = append(c.relations, Relation{Type: t, SrcFQN: src, DstFQN: dst, Meta: meta})
}

// resolveRefs 是二遍校验：全部实体登记完成后，统一校验引用完整性并落关系
// 边。任一引用缺失/类型不符 = 编译错误（原子拒绝，零写库）。
func (c *compiler) resolveRefs() error {
	for _, p := range c.pendingTableRefs {
		e, ok := c.entities[p.target]
		if !ok {
			return fmt.Errorf("%w: %s 引用 %q 不存在（引用完整性校验）", ErrRefMissing, p.what, p.target)
		}
		if e.Kind != KindTable {
			return fmt.Errorf("%s 引用 %q：类型是 %s，需要 %s", p.what, p.target, e.Kind, KindTable)
		}
		c.addRelation(p.typ, p.src, p.target, p.meta)
	}
	for _, p := range c.pendingDescribes {
		e, ok := c.entities[p.target]
		if !ok {
			return fmt.Errorf("%w: %s 引用 %q 不存在（引用完整性校验）", ErrRefMissing, p.what, p.target)
		}
		switch e.Kind {
		case KindTable, KindColumn, KindMetric:
		default:
			return fmt.Errorf("%s 引用 %q：类型是 %s，需要 table/column/metric", p.what, p.target, e.Kind)
		}
		c.addRelation(p.typ, p.src, p.target, p.meta)
	}
	return nil
}

func (c *compiler) compileService(sf serviceFile) error {
	svcFQN, err := EntityFQN(sf.Service)
	if err != nil {
		return fmt.Errorf("服务 %q: %w", sf.Service, err)
	}
	if err := c.addEntity(Entity{FQN: svcFQN, Kind: KindService, Name: sf.Service, Description: sf.Description}); err != nil {
		return err
	}
	for _, db := range sf.Databases {
		dbFQN, err := EntityFQN(sf.Service, db.Name)
		if err != nil {
			return fmt.Errorf("服务 %s 的库 %q: %w", sf.Service, db.Name, err)
		}
		if err := c.addEntity(Entity{FQN: dbFQN, Kind: KindDatabase, Name: db.Name, Description: db.Description}); err != nil {
			return err
		}
		c.addRelation(RelConnectsTo, svcFQN, dbFQN, "")
		for _, tbl := range db.Tables {
			if err := c.compileTable(sf.Service, db.Name, tbl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *compiler) compileTable(service, dbName string, tbl tableDef) error {
	tblFQN, err := EntityFQN(service, dbName, tbl.Name)
	if err != nil {
		return fmt.Errorf("服务 %s 的表 %q: %w", service, tbl.Name, err)
	}
	if err := c.addEntity(Entity{
		FQN: tblFQN, Kind: KindTable, Name: tbl.Name,
		Description: tbl.Description, PGSchema: tbl.Schema,
	}); err != nil {
		return err
	}
	c.addRelation(RelContains, JoinFQN(service, dbName), tblFQN, "")
	for _, col := range tbl.Columns {
		if err := c.compileColumn(service, dbName, tbl, col); err != nil {
			return err
		}
	}
	for _, ref := range tbl.References {
		// join 条件（on 子句）也是 SQL 片段：坏条件在运行时才炸 = 口径
		// 校验缺口，编译期同样探针（review 修复）。
		if strings.TrimSpace(ref.On) != "" {
			probe := "SELECT 1 FROM (SELECT 1) _a, (SELECT 1) _b WHERE " + ref.On
			if err := parseProbe(probe); err != nil {
				return fmt.Errorf("表 %s 的 references on 条件不可解析: %w", tblFQN, err)
			}
		}
		// 引用目标存在性 = 二遍校验（references 可指向编译顺序在后的表）。
		c.pendingTableRefs = append(c.pendingTableRefs, pendingRef{
			what:   fmt.Sprintf("表 %s 的 references", tblFQN),
			src:    tblFQN,
			target: ref.To,
			typ:    RelReferences,
			meta:   ref.On,
		})
	}
	return nil
}

func (c *compiler) compileColumn(service, dbName string, tbl tableDef, col columnDef) error {
	colFQN, err := EntityFQN(service, dbName, tbl.Name, col.Name)
	if err != nil {
		return fmt.Errorf("列 %s.%s: %w", tbl.Name, col.Name, err)
	}
	if err := c.addEntity(Entity{
		FQN: colFQN, Kind: KindColumn, Name: col.Name,
		Description: col.Description, DataType: col.Type, IsTime: col.IsTime,
	}); err != nil {
		return err
	}
	c.addRelation(RelContains, JoinFQN(service, dbName, tbl.Name), colFQN, "")
	return c.compileEnums(colFQN, col.EnumValues)
}

// compileEnums 校验并登记枚举取值：value 非空、同列内不重复（枚举合法）。
func (c *compiler) compileEnums(colFQN string, values []enumValueDef) error {
	seen := map[string]bool{}
	for _, v := range values {
		if v.Value == "" {
			return fmt.Errorf("列 %s 的枚举取值 value 为空（枚举取值必须非空）", colFQN)
		}
		if seen[v.Value] {
			return fmt.Errorf("列 %s 的枚举取值重复: %q", colFQN, v.Value)
		}
		seen[v.Value] = true
		c.enums = append(c.enums, EnumValue{ColumnFQN: colFQN, Value: v.Value, Label: v.Label})
	}
	return nil
}

// validateSimpleName 校验指标/概念的单段命名空间名字：非空、不含点、
// 不含空白与控制字符（\x00 会被 grants 的复合键分隔符使用，必须拒绝——
// review 修复：与 grants.validateName 同规则，编译面先行）。
func validateSimpleName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s缺 name 字段", kind)
	}
	if strings.ContainsAny(name, ".\x00 \t\r\n") {
		return fmt.Errorf("%s FQN %q 含非法字符（单段命名空间：不含点/空白/控制字符）", kind, name)
	}
	return nil
}

func (c *compiler) compileMetric(m metricDef) error {
	if err := validateSimpleName("指标", m.Name); err != nil {
		return err
	}
	if err := c.checkMetricSQL(m); err != nil {
		return err
	}
	if err := c.addEntity(Entity{
		FQN: m.Name, Kind: KindMetric, Name: m.Name,
		Description: m.Description, Expression: m.Expression,
		Aggregation: m.Aggregation, Filter: m.Filter,
	}); err != nil {
		return err
	}
	for _, tbl := range m.Tables {
		// 指标→依赖表 = describes 边（授权展开依据：指标授权 → 底层表授权）；
		// 目标存在性 = 二遍校验。
		c.pendingTableRefs = append(c.pendingTableRefs, pendingRef{
			what:   fmt.Sprintf("指标 %s 的 tables", m.Name),
			src:    m.Name,
			target: tbl,
			typ:    RelDescribes,
		})
	}
	return nil
}

// checkMetricSQL 校验指标口径可解析（ADR-0001「指标表达式机器可读、
// 供 dry-run 校验直接使用」；spec §4.1 口径 = 表达式 + 聚合 + 过滤）：
// expression 与 filter 都是拼进查询的 SQL 片段，分别包成 SELECT 目标列与
// WHERE 条件用 PG 解析器验证。语法失败 = 编译错误（原子拒绝）。
func (c *compiler) checkMetricSQL(m metricDef) error {
	if strings.TrimSpace(m.Expression) == "" {
		return fmt.Errorf("指标 %q 的 expression 为空（口径必须 machine-readable）", m.Name)
	}
	// 指标表达式是 SELECT 的目标列表元素（可含聚合/过滤），包一层 SELECT
	// 验证可解析；不验证列名（列归属由 describes 的表承载，运行时校验）。
	probe := "SELECT " + m.Expression
	if err := parseProbe(probe); err != nil {
		return fmt.Errorf("指标 %q 的 expression 不可解析: %w", m.Name, err)
	}
	if strings.TrimSpace(m.Filter) != "" {
		// filter 是 WHERE 条件片段，同样必须可解析（写坏的过滤会在
		// get_metric_definition 的 dry-run 展开时炸，编译期拒绝更早暴露）。
		probeFilter := "SELECT 1 FROM (SELECT 1) _t WHERE " + m.Filter
		if err := parseProbe(probeFilter); err != nil {
			return fmt.Errorf("指标 %q 的 filter 不可解析: %w", m.Name, err)
		}
	}
	return nil
}

func (c *compiler) compileConcept(cpt conceptDef) error {
	if err := validateSimpleName("概念", cpt.Name); err != nil {
		return err
	}
	if err := c.addEntity(Entity{
		FQN: cpt.Name, Kind: KindConcept, Name: cpt.Name, Description: cpt.Description,
	}); err != nil {
		return err
	}
	for _, target := range cpt.Describes {
		// describes 边目标存在性/类型（table/column/metric）= 二遍校验。
		c.pendingDescribes = append(c.pendingDescribes, pendingRef{
			what:   fmt.Sprintf("概念 %q 描述", cpt.Name),
			src:    cpt.Name,
			target: target,
			typ:    RelDescribes,
		})
	}
	return nil
}
