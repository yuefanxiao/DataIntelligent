// Package config 提供网关的 env 配置面（spec §4.9「env 可覆盖」；
// ADR-0009「stdio 调试形态经 env 传 key」）。
package config

import (
	"os"
	"strconv"
)

// Env 变量名（`DGW_` 前缀与凭据前缀同源，标识网关域）。
const (
	EnvDBPath             = "DGW_DB_PATH"                 // SQLite 运行时存储路径
	EnvHTTPAddr           = "DGW_HTTP_ADDR"               // Streamable HTTP 监听地址
	EnvAPIKey             = "DGW_API_KEY"                 // stdio 调试形态的凭据（env 传入）
	EnvPGDatabases        = "DGW_PG_DATABASES"            // execute_sql 路由表（JSON 数组 [{dbname, service, dsn}]，DSN 即凭证）
	EnvSQLLimit           = "DGW_SQL_LIMIT"               // 行数上限（默认 500，范围 500-5000，越界启动失败）
	EnvPGTimeoutMS        = "DGW_PG_STATEMENT_TIMEOUT_MS" // statement_timeout 毫秒（默认 30000）
	EnvKeyConcurrency     = "DGW_KEY_CONCURRENCY"         // 每 key 并发查询上限（默认 2，spec §4.9）
	EnvProcessConcurrency = "DGW_PROCESS_CONCURRENCY"     // 进程级总并发上限（默认 8，spec §4.9）
)

// §4.9 参数表默认值（env 可覆盖；网关/测试共用的单一事实源——
// cmd/dgw 与 gateway 包都从这里取，避免字面量漂移）。
const (
	DefaultSQLLimit           = 500
	DefaultPGTimeoutMS        = 30000
	DefaultKeyConcurrency     = 2
	DefaultProcessConcurrency = 8
)

// Config 是一次进程启动的配置快照。
type Config struct {
	// DBPath 是 SQLite 运行时存储文件路径。
	DBPath string
	// HTTPAddr 是 Streamable HTTP 守护进程的监听地址。
	HTTPAddr string
	// APIKey 是 stdio 调试形态的凭据，env 传入，缺省拒绝启动。
	APIKey string
	// PGDatabases 是 execute_sql 路由表原文（JSON 数组，db 包解析）。
	PGDatabases string
	// SQLLimit 是行数默认上限（spec §4.9：500-5000）。
	SQLLimit int
	// PGTimeoutMS 是连接级 statement_timeout 毫秒数。
	PGTimeoutMS int
	// KeyConcurrency 是每 key 并发查询上限（spec §4.9 默认 2）。
	KeyConcurrency int
	// ProcessConcurrency 是进程级总并发上限（spec §4.9 默认 8）。
	ProcessConcurrency int
}

// FromEnv 从环境变量构造配置，未设置项取默认值。
func FromEnv() Config {
	return Config{
		DBPath:             getenv(EnvDBPath, "./dgw.db"),
		HTTPAddr:           getenv(EnvHTTPAddr, ":8080"),
		APIKey:             os.Getenv(EnvAPIKey),
		PGDatabases:        os.Getenv(EnvPGDatabases),
		SQLLimit:           getenvInt(EnvSQLLimit, DefaultSQLLimit),
		PGTimeoutMS:        getenvInt(EnvPGTimeoutMS, DefaultPGTimeoutMS),
		KeyConcurrency:     getenvInt(EnvKeyConcurrency, DefaultKeyConcurrency),
		ProcessConcurrency: getenvInt(EnvProcessConcurrency, DefaultProcessConcurrency),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getenvInt 解析整数 env；缺失或非法退回默认（fail-safe 方向：限额默认 500
// 最严；越界值留给网关启动校验报错）。
func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
