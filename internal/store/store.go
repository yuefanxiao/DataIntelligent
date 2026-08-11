// Package store 是网关的 SQLite 运行时存储（ADR-0005：modernc.org/sqlite
// 纯 Go 无 CGO、单文件、WAL 模式、单写者/多读者）。
//
// WAL 天然给出「单写者 + 多读者」并发模型：写事务在库层串行化，读者不阻塞
// 写者；备份 = checkpoint + 文件拷贝（07 票）。本包持有全部网关表（`dgw_`
// 前缀）的 schema——v1 已建凭据表（#18）与权限表（#19：表授权 + 热重载
// revision），后续 07 语义运行时表在此追加。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	_ "modernc.org/sqlite"     // 驱动名 "sqlite"，纯 Go 实现
	_ "modernc.org/sqlite/vec" // sqlite-vec（08 票，ADR-0005）：init 里 auto-extension 注册 vec0 虚拟表，进程内对所有新连接生效
)

const driverName = "sqlite"

// dsn 拼出带连接级 pragma 的 DSN：modernc 的 _pragma 对每条新连接生效，
// 是 database/sql 连接池下唯一可靠的 pragma 设置点。
//
//   - journal_mode(WAL)：读写并发形态的前提；
//   - synchronous(NORMAL)：WAL 下的常规选择（崩溃最多丢最近提交，不损坏）；
//   - busy_timeout(5000)：单写者争用等锁而不是立刻报 database is locked；
//   - foreign_keys(ON)：为后续表（权限表挂用户）预留引用完整性。
//
// 路径必须 url.PathEscape：路径里的 `?`/`#` 会截断 DSN 导致库建到错误位置。
func dsn(path string) string {
	return fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", url.PathEscape(path))
}

// Store 包装 *sql.DB，是网关访问运行时存储的唯一入口。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）运行时库，应用 schema 后返回。
func Open(path string) (*Store, error) {
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.ensureSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭底层连接池。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层句柄：凭据/权限域代码用原生 SQL 访问 `dgw_` 表。
func (s *Store) DB() *sql.DB { return s.db }

// JournalMode 返回当前 journal 模式（测试用：断言 WAL 生效）。
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", fmt.Errorf("read journal_mode: %w", err)
	}
	return mode, nil
}

// ensureSchema 幂等建表（CREATE TABLE IF NOT EXISTS，重开不重复迁移）。
func (s *Store) ensureSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS dgw_credentials (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key_hash   TEXT NOT NULL UNIQUE, -- sha256 hex；明文不落库（ADR-0004）
    user_id    TEXT NOT NULL,        -- 绑定用户身份（审计聚合维度）
    role       TEXT,                 -- 预留：v1 无角色抽象（ADR-0004）
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    revoked_at TEXT                  -- NULL = 有效；吊销即时生效
);

-- 表级授权（ADR-0004 权限模型；存储载体经 ADR-0005 修正为 SQLite）：
-- 用户 × 表 FQN 扁平白名单，业务数据面唯一授权数据。
-- FQN = 服务.库.表，与本体 Table 实体同一命名空间；指标/概念授权经编译期
-- （07 同步管线）展开为具体表行，本表不存通配模式。
CREATE TABLE IF NOT EXISTS dgw_table_grants (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    TEXT NOT NULL,        -- 绑定用户身份（同 dgw_credentials.user_id）
    table_fqn  TEXT NOT NULL,        -- 授权挂载点：服务.库.表
    granted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (user_id, table_fqn)
);
CREATE INDEX IF NOT EXISTS idx_table_grants_fqn ON dgw_table_grants (table_fqn);

-- 权限版本号（热重载信号，02 票）：任何授权变更在同一事务里 +1；
-- 网关启动读一次、之后轮询本表，revision 变化即重新加载内存快照。
-- 单行表（id 恒为 1）：CLI 与网关是不同进程，SQLite 是唯一的进程间信号通道。
CREATE TABLE IF NOT EXISTS dgw_permission_meta (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL DEFAULT 0
);
INSERT INTO dgw_permission_meta (id, revision) VALUES (1, 0)
    ON CONFLICT(id) DO NOTHING;

-- ===== 语义层运行时表（07 票，ADR-0001/0005）=====
-- 六类实体（service/database/table/column/metric/concept）统一存一张表，
-- kind 区分类型；类型专属字段按 kind 使用（其余为 NULL）。
-- FQN 即稳定标识（ADR-0001）：服务 / 服务.库 / 服务.库.表 / 服务.库.表.列，
-- 指标与概念用独立命名空间（单段名字，全局唯一）。表 FQN（服务.库.表）与
-- dgw_table_grants.table_fqn 同一命名空间 = 权限挂载点（ADR-0004）。
-- tombstone=1 = 墓碑软删除（ADR-0002 墓碑语义，检索默认过滤）。
CREATE TABLE IF NOT EXISTS dgw_sem_entities (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    fqn         TEXT NOT NULL UNIQUE,      -- 稳定 FQN（全 kind 唯一）
    kind        TEXT NOT NULL CHECK (kind IN ('service','database','table','column','metric','concept')),
    name        TEXT NOT NULL,             -- 实体短名（FQN 末段，冗余便于检索）
    description TEXT NOT NULL DEFAULT '',
    -- column 专属：
    data_type   TEXT,                      -- PG 数据类型（列）
    is_time     INTEGER NOT NULL DEFAULT 0,-- 时间轴标注（列，1 = 是时间列）
    -- table 专属：
    pg_schema   TEXT,                      -- PG schema（表；缺省 = 按 dbname 路由推断）
    -- metric 专属（machine-readable 口径，ADR-0001 OSI 式）：
    expression  TEXT,                      -- SQL 表达式
    aggregation TEXT,                      -- 聚合方式
    filter      TEXT,                      -- 过滤条件
    tombstone   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_sem_entities_kind ON dgw_sem_entities (kind);
CREATE INDEX IF NOT EXISTS idx_sem_entities_tombstone ON dgw_sem_entities (tombstone);

-- 四种关系边（双向可遍历：按 src 或 dst 都能查）：connects_to（服务↔库）、
-- contains（库→表、表→列）、references（表↔表 join 条件）、describes
-- （概念↔表/列/指标，指标→其依赖表也用本类型）。
-- meta 存边专属信息（references 的 on 条件），JSON 文本。
CREATE TABLE IF NOT EXISTS dgw_sem_relations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT NOT NULL CHECK (type IN ('connects_to','contains','references','describes')),
    src_fqn    TEXT NOT NULL,
    dst_fqn    TEXT NOT NULL,
    meta       TEXT NOT NULL DEFAULT '',
    tombstone  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (type, src_fqn, dst_fqn)
);
CREATE INDEX IF NOT EXISTS idx_sem_relations_src ON dgw_sem_relations (src_fqn);
CREATE INDEX IF NOT EXISTS idx_sem_relations_dst ON dgw_sem_relations (dst_fqn);

-- 列枚举取值（list_enum_values 的数据来源；挂列 = column_fqn）。
CREATE TABLE IF NOT EXISTS dgw_sem_enum_values (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    column_fqn TEXT NOT NULL,              -- 服务.库.表.列
    value      TEXT NOT NULL,              -- 枚举取值（如 'failed'）
    label      TEXT NOT NULL DEFAULT '',   -- 业务含义（如 支付失败）
    tombstone  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    UNIQUE (column_fqn, value)
);

-- 实体向量（search_entities 向量兜底；07 只写入，08 票接检索）。
-- vector = float32 小端序列（text-embedding-3-small 为 1536 维 × 4 字节）。
--
-- 决策记录（07 票的落地偏差，08 票执行迁移）：ADR-0005 选定 sqlite-vec
-- vec0 虚拟表做 KNN，但 modernc.org/sqlite v1.42.2 不含内置 vec；07 只承担
-- 「写入向量」，先用普通表 BLOB 落库（与 sqlite-vec 内部格式字节兼容）。
-- 08 票升级驱动至 v1.50.0（内置 sqlite-vec v0.1.9 的 CGO-free 移植）并建
-- vec0 索引表 dgw_sem_vec：本表仍是向量的事实存储面（model 元数据 +
-- EmbeddingCoverage 依据），vec0 是检索索引，两者由 SaveEmbeddings /
-- RemoveEmbeddings 双写维护；存量行经 ensureSchema 的幂等回填迁移
-- （INSERT INTO vec0 SELECT，无需重嵌入）。
CREATE TABLE IF NOT EXISTS dgw_sem_embeddings (
    entity_fqn TEXT PRIMARY KEY,
    model      TEXT NOT NULL,              -- 如 text-embedding-3-small
    vector     BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- 关键词检索索引（08 票，ADR-0005「关键词 = FTS5」）：search_entities 的
-- 关键词主通道。trigram 分词（CJK 子串 + 英文子串匹配，2 字符内查询退
-- LIKE 兜底，见 semantic/search.go）。kind 列不参与分词（UNINDEXED），
-- 供查询侧按实体类型过滤。与实体同事务维护（semantic.Apply 内增删改），
-- 墓碑实体即从索引消失——检索面与实体面原子一致。
CREATE VIRTUAL TABLE IF NOT EXISTS dgw_sem_fts USING fts5(
    fqn UNINDEXED,
    kind UNINDEXED,
    name,
    description,
    tokenize='trigram'
);

-- 向量检索索引（08 票，ADR-0005 sqlite-vec）：vec0 KNN 虚拟表。
-- entity_fqn 主键 + vector float[N]（N = 模型维度）；维度随模型固定，
-- 模型切换（维度变化）由 semantic.EnsureVecIndex 检测并重建。
-- 初次创建用 v1 默认模型 text-embedding-3-small 的维度 1536。
CREATE VIRTUAL TABLE IF NOT EXISTS dgw_sem_vec USING vec0(
    entity_fqn TEXT PRIMARY KEY,
    vector float[1536]
);

-- 服务/库级通配授权声明（ADR-0004 语法糖）：grants-apply 展开为具体表清单
-- 快照写入 dgw_table_grants，同时保留声明——同步管线（07）据此告警「新表
-- 未覆盖」（新表默认拒绝，重展开 = 重跑 grants-apply）。全库通配 * 不开放。
CREATE TABLE IF NOT EXISTS dgw_grant_patterns (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id  TEXT NOT NULL,
    pattern  TEXT NOT NULL,                -- service:XXX 或 database:XXX.YYY
    UNIQUE (user_id, pattern)
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	// 存量向量迁移（08 票，ADR-0005 落地）：07 时代的库只有 BLOB 表没有
	// vec0 索引——幂等回填（维度匹配的行补齐进索引，跳过已存在的），后续
	// 由 SaveEmbeddings 双写维护。维度过滤防模型切换残留的异维向量进 KNN
	// （混合维度余弦 = 垃圾检索结果，与 EmbeddingCoverage 同一动机）。
	// 维度取 vec0 表当前声明值（非默认常量——非 1536 维模型下回填同样正确）。
	dim, err := s.vec0DimOf(ctx)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO dgw_sem_vec (entity_fqn, vector)
SELECT entity_fqn, vector FROM dgw_sem_embeddings
WHERE length(vector) = ? AND NOT EXISTS (
    SELECT 1 FROM dgw_sem_vec v WHERE v.entity_fqn = dgw_sem_embeddings.entity_fqn)`,
		dim*4); err != nil {
		return fmt.Errorf("migrate embeddings to vec0: %w", err)
	}
	return nil
}

// vec0DimOf 读 vec0 索引的当前维度（从 sqlite_master 的建表语句解析；
// vec0 是虚拟表，维度只在 DDL 里声明，无 SQL 元数据面可查）。
func (s *Store) vec0DimOf(ctx context.Context) (int, error) {
	var sqlText string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'dgw_sem_vec'`).Scan(&sqlText)
	if err != nil {
		return 0, fmt.Errorf("read vec0 definition: %w", err)
	}
	m := vec0DimRe.FindStringSubmatch(sqlText)
	if m == nil {
		return 0, fmt.Errorf("parse vec0 definition: %q", sqlText)
	}
	dim, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("parse vec0 dim %q: %w", m[1], err)
	}
	return dim, nil
}

// vec0DimRe 匹配 vec0 建表语句里的维度声明（float[N]）。
var vec0DimRe = regexp.MustCompile(`float\[(\d+)\]`)

// PermissionRevision 返回当前权限版本号（热重载轮询的读取面）。
func (s *Store) PermissionRevision(ctx context.Context) (int64, error) {
	var rev int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT revision FROM dgw_permission_meta WHERE id = 1").Scan(&rev); err != nil {
		return 0, fmt.Errorf("read permission revision: %w", err)
	}
	return rev, nil
}

// BumpPermissionRevision 把权限版本号 +1（不经事务，供测试等无事务场景）。
func (s *Store) BumpPermissionRevision(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE dgw_permission_meta SET revision = revision + 1 WHERE id = 1"); err != nil {
		return fmt.Errorf("bump permission revision: %w", err)
	}
	return nil
}

// BumpPermissionRevisionTx 在既有事务内把权限版本号 +1——授权变更（grants 包
// 的增删/编译）必须与数据写入同一事务调用，网关侧据此触发热重载。
func (s *Store) BumpPermissionRevisionTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		"UPDATE dgw_permission_meta SET revision = revision + 1 WHERE id = 1"); err != nil {
		return fmt.Errorf("bump permission revision: %w", err)
	}
	return nil
}
