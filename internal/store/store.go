// Package store 是网关的 SQLite 运行时存储（ADR-0005：modernc.org/sqlite
// 纯 Go 无 CGO、单文件、WAL 模式、单写者/多读者）。
//
// WAL 天然给出「单写者 + 多读者」并发模型：写事务在库层串行化，读者不阻塞
// 写者；备份 = checkpoint + 文件拷贝（07 票）。本包持有全部网关表（`dgw_`
// 前缀）的 schema——v1 只建凭据表，后续票（02 权限表、07 语义运行时表）在此
// 追加。
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
    revoked_at TEXT                  -- NULL = 有效；吊销即时生效（02 票）
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}
