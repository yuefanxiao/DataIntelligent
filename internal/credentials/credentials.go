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

// Verify 校验明文凭据：哈希比对命中且未吊销则返回绑定用户；否则 ErrInvalidKey。
func Verify(ctx context.Context, db *sql.DB, plaintext string) (string, error) {
	var userID string
	var revokedAt sql.NullString
	err := db.QueryRowContext(ctx,
		"SELECT user_id, revoked_at FROM dgw_credentials WHERE key_hash = ?",
		Hash(plaintext)).Scan(&userID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidKey
	}
	if err != nil {
		return "", fmt.Errorf("lookup key: %w", err)
	}
	if revokedAt.Valid {
		return "", ErrInvalidKey
	}
	return userID, nil
}
