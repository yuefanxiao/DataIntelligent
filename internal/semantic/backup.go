package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
)

// Backup 执行运行时存储备份（ADR-0005「备份 = WAL checkpoint + 文件拷贝」；
// 10 部署接入）：
//
//  1. WAL checkpoint(TRUNCATE)：把 WAL 内容合并回主库文件并截断 WAL——
//     只拷主库文件即可得到一致快照（WAL 下文件拷贝非 checkpoint 会丢
//     未 checkpoint 的最近提交）；
//  2. 逐字节拷贝主库文件到 dst。
//
// dst 是目标文件路径（调用方负责目录存在；覆盖已存在文件）。
func Backup(ctx context.Context, st interface{ DB() *sql.DB }, dst string) error {
	if _, err := st.DB().ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("WAL checkpoint: %w", err)
	}
	// 主库路径：DSN 是 file:<escaped path>?_pragma=...，从连接级拿不可靠；
	// 用 PRAGMA database_list 取主库真实路径（现代 sqlite 支持）。
	path, err := mainDBPath(ctx, st)
	if err != nil {
		return err
	}
	if err := copyFile(path, dst); err != nil {
		return fmt.Errorf("备份 %s → %s: %w", path, dst, err)
	}
	return nil
}

// mainDBPath 经 PRAGMA database_list 取主库（seq=0）文件路径。
func mainDBPath(ctx context.Context, st interface{ DB() *sql.DB }) (string, error) {
	rows, err := st.DB().QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", fmt.Errorf("PRAGMA database_list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", fmt.Errorf("scan database_list: %w", err)
		}
		if seq == 0 {
			if file == "" {
				return "", fmt.Errorf("主库路径为空（内存库不支持文件备份）")
			}
			return file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("database_list 无主库行")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
