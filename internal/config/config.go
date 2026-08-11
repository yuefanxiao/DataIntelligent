// Package config 提供网关的 env 配置面（spec §4.9「env 可覆盖」；
// ADR-0009「stdio 调试形态经 env 传 key」）。
package config

import "os"

// Env 变量名（`DGW_` 前缀与凭据前缀同源，标识网关域）。
const (
	EnvDBPath   = "DGW_DB_PATH"   // SQLite 运行时存储路径
	EnvHTTPAddr = "DGW_HTTP_ADDR" // Streamable HTTP 监听地址
	EnvAPIKey   = "DGW_API_KEY"   // stdio 调试形态的凭据（env 传入）
)

// Config 是一次进程启动的配置快照。
type Config struct {
	// DBPath 是 SQLite 运行时存储文件路径。
	DBPath string
	// HTTPAddr 是 Streamable HTTP 守护进程的监听地址。
	HTTPAddr string
	// APIKey 是 stdio 调试形态的凭据，env 传入，缺省拒绝启动。
	APIKey string
}

// FromEnv 从环境变量构造配置，未设置项取默认值。
func FromEnv() Config {
	return Config{
		DBPath:   getenv(EnvDBPath, "./dgw.db"),
		HTTPAddr: getenv(EnvHTTPAddr, ":8080"),
		APIKey:   os.Getenv(EnvAPIKey),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
