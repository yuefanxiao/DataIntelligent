// Package semantic 是语义层（ADR-0001/0002/0005）的作者入口与同步管线域。
//
// 分工：
//   - 作者入口 = 按服务拆 YAML + 全局指标/概念文件（内部语义仓库），
//     本包负责解析与编译校验；
//   - 运行时 = 网关的 SQLite 单文件（dgw_sem_* 表，schema 在 store 包），
//     本包只通过 store 访问，运行时的检索由 08 票消费；
//   - 同步管线 = 编译校验 → dry-run diff → 幂等 upsert + 墓碑软删除，
//     运行时只查 SQLite 不查 YAML（ADR-0002「运行时只查运行时存储」）。
//
// FQN 命名空间（ADR-0001「稳定 FQN」）：服务 / 服务.库 / 服务.库.表 /
// 服务.库.表.列 四级拓扑 + 指标/概念两个命名空间（单段名字，不点分）。
// FQN 全库唯一（含指标/概念）：get_entity 的精确查询以 FQN 为键，同名
// 必歧义；表 FQN 与 dgw_table_grants.table_fqn 同一命名空间（权限挂载点）。
package semantic

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// DBer 是语义层访问运行时存储的最小接口（store.Store 实现；测试可注入
// 任意 DB() *sql.DB 的对象）。查询/应用/备份函数一律以 DBer 收口，
// 不依赖具体 store 类型（ADR-0005「存储接口抽象保留」）。
type DBer interface {
	DB() *sql.DB
}

// Kind 是六类实体的类型枚举（ADR-0001）。
type Kind string

const (
	KindService  Kind = "service"
	KindDatabase Kind = "database"
	KindTable    Kind = "table"
	KindColumn   Kind = "column"
	KindMetric   Kind = "metric"
	KindConcept  Kind = "concept"
)

// RelationType 是四类关系边（ADR-0001，双向可遍历）。
type RelationType string

const (
	RelConnectsTo RelationType = "connects_to" // 服务↔库
	RelContains   RelationType = "contains"    // 库→表、表→列
	RelReferences RelationType = "references"  // 表↔表 join 条件
	RelDescribes  RelationType = "describes"   // 概念↔表/列/指标；指标→依赖表
)

// 命名空间段数（grants.ValidateFQN 的 3 段表 FQN 与本定义同源）。
const (
	SegTable  = 3
	SegColumn = 4
)

// ErrRefMissing 是引用完整性错误（编译期）：YAML 引用了不存在的实体。
// 用 errors.Is 区分「引用缺失」与「FQN 重复」「SQL 不可解析」「枚举非法」。
var ErrRefMissing = errors.New("reference to missing entity")

// Entity 是编译后的一个实体（六类之一）。字段按 kind 使用，与
// dgw_sem_entities 表列一一对应。
type Entity struct {
	FQN         string
	Kind        Kind
	Name        string
	Description string
	// column 专属：
	DataType string
	IsTime   bool
	// table 专属：
	PGSchema string
	// metric 专属（machine-readable 口径）：
	Expression  string
	Aggregation string
	Filter      string
}

// EnumValue 是列的一个枚举取值（挂列）。
type EnumValue struct {
	ColumnFQN string
	Value     string
	Label     string
}

// Relation 是编译后的关系边（双向：查询时按 src/dst 都可遍历）。
type Relation struct {
	Type   RelationType
	SrcFQN string
	DstFQN string
	Meta   string // references 的 on 条件等
}

// Target 是编译产物：一次同步管线的目标全量状态（六类实体 + 边 + 枚举）。
// diff 与 apply 都以它为输入；同输入重跑产出确定的目标（§5.3 seam）。
type Target struct {
	Entities  []Entity
	Relations []Relation
	Enums     []EnumValue
}

// EntityFQN 按层级拼接 FQN（parts 非空、每段非空）。
func EntityFQN(parts ...string) (string, error) {
	for i, p := range parts {
		if p == "" {
			return "", fmt.Errorf("FQN 第 %d 段为空", i+1)
		}
		if strings.ContainsAny(p, ". \t\r\n") {
			return "", fmt.Errorf("FQN 段 %q 不能含点或空白", p)
		}
	}
	return strings.Join(parts, "."), nil
}

// JoinFQN 拼接 FQN（调用方保证段合法，编译期用）。
func JoinFQN(parts ...string) string { return strings.Join(parts, ".") }
