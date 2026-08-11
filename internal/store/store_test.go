package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dgw.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// WAL 是单写者/多读者并发模型的前提（ADR-0005）。
func TestWALMode(t *testing.T) {
	s := openTemp(t)
	mode, err := s.JournalMode(context.Background())
	if err != nil {
		t.Fatalf("JournalMode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

// 路径含 `?`/空格等 DSN 特殊字符时仍开在正确位置（回归：未转义时
// `?` 会把路径截断、库静默建到错误位置）。
func TestOpenPathWithSpecialChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weird dir?name#x.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer s.Close()
	if _, err := s.DB().ExecContext(context.Background(),
		"INSERT INTO dgw_credentials (key_hash, user_id) VALUES ('h', 'u')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("库文件未出现在原路径: %v", err)
	}
}

// 重开同一文件：schema 幂等，不报错、数据仍在（CLI 反复启动的场景）。
func TestReopenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dgw.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.DB().ExecContext(context.Background(),
		"INSERT INTO dgw_credentials (key_hash, user_id) VALUES ('h1', 'u1')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var n int
	if err := s2.DB().QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM dgw_credentials").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("重开后行数 = %d, want 1", n)
	}
}

// 凭据表就绪：字段形状符合 ADR-0004（哈希、用户、role 预留、吊销列）。
func TestCredentialsSchema(t *testing.T) {
	s := openTemp(t)

	cols := map[string]bool{}
	rows, err := s.DB().QueryContext(context.Background(), "PRAGMA table_info(dgw_credentials)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	for _, want := range []string{"key_hash", "user_id", "role", "created_at", "revoked_at"} {
		if !cols[want] {
			t.Errorf("凭据表缺列 %q，实际 %v", want, cols)
		}
	}
}

// 单写者/多读者（ADR-0005）：一个写事务进行中，多个读者并发查询不被阻塞。
func TestSingleWriterMultiReader(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	// 预置数据，让读者有内容可查。
	for i := 0; i < 20; i++ {
		if _, err := s.DB().ExecContext(ctx,
			"INSERT INTO dgw_credentials (key_hash, user_id) VALUES (?, ?)",
			fmt.Sprintf("hash-%d", i), "dev-batch"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	const readers = 4
	const iterations = 50
	errCh := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				var n int
				if err := s.DB().QueryRowContext(ctx,
					"SELECT COUNT(*) FROM dgw_credentials").Scan(&n); err != nil {
					errCh <- fmt.Errorf("reader: %w", err)
					return
				}
				if n < 20 {
					errCh <- fmt.Errorf("reader 看到不完整数据: n=%d", n)
					return
				}
			}
			errCh <- nil
		}()
	}

	// 写者持续插入（WAL 下读者不阻塞写者、写者不阻塞读者）。
	for j := 0; j < iterations; j++ {
		if _, err := s.DB().ExecContext(ctx,
			"INSERT INTO dgw_credentials (key_hash, user_id) VALUES (?, ?)",
			fmt.Sprintf("write-%d", j), "dev-writer"); err != nil {
			t.Fatalf("writer: %v", err)
		}
	}

	for i := 0; i < readers; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}
