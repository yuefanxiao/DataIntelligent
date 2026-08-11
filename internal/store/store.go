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

	_ "modernc.org/sqlite" // 驱动名 "sqlite"，纯 Go 实现
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

-- 实体向量（search_entities 向量兜底；07 只写入，08 票接 sqlite-vec/暴力余弦）。
-- vector = float32 小端序列（1536 维 × 4 字节）。
CREATE TABLE IF NOT EXISTS dgw_sem_embeddings (
    entity_fqn TEXT PRIMARY KEY,
    model      TEXT NOT NULL,              -- 如 text-embedding-3
    vector     BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
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
	return nil
}

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
