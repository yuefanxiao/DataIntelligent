// Package credentials 提供网关凭据（key）的生成、哈希存储与校验
// （ADR-0004：opaque 随机串、`dgw_` 前缀、sha256 哈希存储、明文仅创建时
// 打印一次、key→用户扁平映射）。
//
// 明文的生命周期只存在于本包的返回值：Create 返回后唯一持有者是调用方
// （CLI），落库的只有哈希。02 票的权限 CLI 复用同一入口。
package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	// Prefix 是凭据固定前缀（spec §4.9 参数表）。
	Prefix = "dgw_"
	// rawLen 是随机段字节数：base64url 无填充编码后 43 字符，整串 47 字符。
	rawLen = 32
)

// ErrInvalidKey 表示凭据未命中或已吊销（认证失败 → 401）。
var ErrInvalidKey = errors.New("invalid key")

// Generate 生成一把新凭据：crypto/rand 32 字节 + base64url（无填充）。
func Generate() (string, error) {
	buf := make([]byte, rawLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash 计算凭据的 sha256 hex 摘要——存储形态，明文不落库（ADR-0004）。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Create 生成一把新 key，只把哈希写入 dgw_credentials 表，返回明文。
// 调用方（CLI）负责「明文仅打印一次」：除本返回值外明文不出现在任何存储。
func Create(ctx context.Context, db *sql.DB, userID string) (string, error) {
	plain, err := Generate()
	if err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO dgw_credentials (key_hash, user_id) VALUES (?, ?)",
		Hash(plain), userID); err != nil {
		return "", fmt.Errorf("store key hash: %w", err)
	}
	return plain, nil
}

// VerifyKey 校验明文凭据：哈希比对命中且未吊销则返回该 key 的记录
// （ID = 每 key 并发闸的粒度标识，ADR-0004 key→用户扁平映射——
// 一用户多 key，各 key 独立计数）；否则 ErrInvalidKey。
func VerifyKey(ctx context.Context, db *sql.DB, plaintext string) (KeyInfo, error) {
	var k KeyInfo
	var revokedAt sql.NullString
	err := db.QueryRowContext(ctx,
		"SELECT id, user_id, revoked_at FROM dgw_credentials WHERE key_hash = ?",
		Hash(plaintext)).Scan(&k.ID, &k.UserID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return KeyInfo{}, ErrInvalidKey
	}
	if err != nil {
		return KeyInfo{}, fmt.Errorf("lookup key: %w", err)
	}
	if revokedAt.Valid {
		return KeyInfo{}, ErrInvalidKey
	}
	return k, nil
}

// Verify 校验明文凭据：命中且未吊销则返回绑定用户；否则 ErrInvalidKey。
// 只取用户身份的调用方用它；需要 key 粒度（并发闸）用 VerifyKey。
func Verify(ctx context.Context, db *sql.DB, plaintext string) (string, error) {
	k, err := VerifyKey(ctx, db, plaintext)
	if err != nil {
		return "", err
	}
	return k.UserID, nil
}

// KeyInfo 是快照视图里的一把 key（明文不存在，哈希即身份标识）。
type KeyInfo struct {
	ID        int64 // 行 ID：吊销命令的寻址句柄
	UserID    string
	CreatedAt string
	RevokedAt string // 空串 = 有效
}

// List 返回全部 key 的快照（按创建时间升序），供授权快照/吊销寻址。
func List(ctx context.Context, db *sql.DB) ([]KeyInfo, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, created_at, COALESCE(revoked_at, '')
		 FROM dgw_credentials ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.UserID, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return keys, nil
}

// Revoke 吊销一把 key（置 revoked_at）——幂等：已吊销/不存在都视为成功。
// 生效即时：Verify 每次请求查库，无需网关重启或缓存失效。
// 返回吊销前状态：false = 原本已吊销或不存在。
func Revoke(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE dgw_credentials SET revoked_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		 WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return false, fmt.Errorf("revoke key %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("revoke key %d: %w", id, err)
	}
	return n > 0, nil
}
