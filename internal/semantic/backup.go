package semantic

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Backup 执行运行时存储备份（ADR-0005「备份 = WAL checkpoint + 文件拷贝」；
// 10 部署接入）：
//
//  1. WAL checkpoint(TRUNCATE)：把 WAL 内容合并回主库文件并截断 WAL——
//     只拷主库文件即可得到一致快照（WAL 下文件拷贝非 checkpoint 会丢
//     未 checkpoint 的最近提交）。checkpoint 结果必须校验：busy>0 表示
//     有读者持有读事务、checkpoint 未完成——此时继续拷贝会产出缺最近
//     提交的静默过期备份（review 修复）。
//  2. 逐字节拷贝主库文件到 dst。
//
// dst 是目标文件路径（调用方负责目录存在；覆盖已存在文件）。
// dst 不能指向源库文件本身（先截断再拷贝会把运行时库清零——review 修复）。
func Backup(ctx context.Context, st DBer, dst string) error {
	if err := checkpointWAL(ctx, st); err != nil {
		return err
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

// checkpointWAL 执行 WAL checkpoint(TRUNCATE) 并校验结果：PRAGMA 返回
// (busy, log, checkpointed) 三值，busy != 0 = 有并发读者、本次 checkpoint
// 未完成——报错而非静默继续（备份一致性是「可直接用于回滚恢复」的前提）。
func checkpointWAL(ctx context.Context, st DBer) error {
	var busy, logPages, checkpointed int
	if err := st.DB().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		return fmt.Errorf("WAL checkpoint: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint 未完成（busy=%d, log=%d, checkpointed=%d）：存在并发读者；备份中止，稍后重试", busy, logPages, checkpointed)
	}
	return nil
}

// mainDBPath 经 PRAGMA database_list 取主库（seq=0）文件路径。
func mainDBPath(ctx context.Context, st DBer) (string, error) {
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

// copyFile 拷贝文件；src 与 dst 指向同一文件（误用 --out 指到库文件）时
// 拒绝——先 os.Create 截断源再拷贝会把运行时库清零（review 修复）。
func copyFile(src, dst string) error {
	same, err := samePath(src, dst)
	if err != nil {
		return err
	}
	if same {
		return fmt.Errorf("备份目标与源库是同一文件 %q（备份会先截断源库；请用不同路径）", src)
	}
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

// samePath 判断两个路径是否指向同一文件（os.SameFile 覆盖软链接/相对路径
// 别名；目标不存在时创建前无法 Stat——此时按「不同」处理，由 Create 报错）。
func samePath(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // dst 尚不存在，必然不是同一文件
		}
		return false, err
	}
	return os.SameFile(ai, bi), nil
}
