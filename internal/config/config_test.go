package config

import (
	"testing"
)

// §4.9 参数表「env 可覆盖」：未设置项取 spec 默认值。
func TestFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvDBPath, "")
	t.Setenv(EnvHTTPAddr, "")
	t.Setenv(EnvSQLLimit, "")
	t.Setenv(EnvPGTimeoutMS, "")
	t.Setenv(EnvKeyConcurrency, "")
	t.Setenv(EnvProcessConcurrency, "")
	t.Setenv(EnvExecLogDir, "")
	t.Setenv(EnvExecRawRetention, "")
	t.Setenv(EnvExecSumRetention, "")

	cfg := FromEnv()
	if cfg.SQLLimit != 500 {
		t.Errorf("SQLLimit = %d，期望默认 500", cfg.SQLLimit)
	}
	if cfg.PGTimeoutMS != 30000 {
		t.Errorf("PGTimeoutMS = %d，期望默认 30000（statement_timeout 30s）", cfg.PGTimeoutMS)
	}
	if cfg.KeyConcurrency != 2 {
		t.Errorf("KeyConcurrency = %d，期望默认 2（每 key 并发）", cfg.KeyConcurrency)
	}
	if cfg.ProcessConcurrency != 8 {
		t.Errorf("ProcessConcurrency = %d，期望默认 8（进程级并发）", cfg.ProcessConcurrency)
	}
	if cfg.DBPath != "./dgw.db" || cfg.HTTPAddr != ":8080" {
		t.Errorf("DBPath/HTTPAddr 默认 = %q/%q", cfg.DBPath, cfg.HTTPAddr)
	}
	// 执行记录（06 票，spec §4.9）：原始 7 天轮转 + 聚合摘要 30 天
	if cfg.ExecLogDir != "./logs" {
		t.Errorf("ExecLogDir 默认 = %q，期望 ./logs", cfg.ExecLogDir)
	}
	if cfg.ExecRawRetentionDays != 7 || cfg.ExecSummaryRetentionDays != 30 {
		t.Errorf("执行记录保留期默认 = %d/%d，期望 7/30",
			cfg.ExecRawRetentionDays, cfg.ExecSummaryRetentionDays)
	}
}

// §4.9 参数表各参数 env 可覆盖（数值型全表）。
func TestFromEnvOverrides(t *testing.T) {
	t.Setenv(EnvSQLLimit, "1000")
	t.Setenv(EnvPGTimeoutMS, "5000")
	t.Setenv(EnvKeyConcurrency, "3")
	t.Setenv(EnvProcessConcurrency, "12")
	t.Setenv(EnvExecLogDir, "/var/log/dgw")
	t.Setenv(EnvExecRawRetention, "14")
	t.Setenv(EnvExecSumRetention, "60")

	cfg := FromEnv()
	if cfg.SQLLimit != 1000 {
		t.Errorf("SQLLimit = %d，期望 1000", cfg.SQLLimit)
	}
	if cfg.PGTimeoutMS != 5000 {
		t.Errorf("PGTimeoutMS = %d，期望 5000", cfg.PGTimeoutMS)
	}
	if cfg.KeyConcurrency != 3 {
		t.Errorf("KeyConcurrency = %d，期望 3", cfg.KeyConcurrency)
	}
	if cfg.ProcessConcurrency != 12 {
		t.Errorf("ProcessConcurrency = %d，期望 12", cfg.ProcessConcurrency)
	}
	if cfg.ExecLogDir != "/var/log/dgw" {
		t.Errorf("ExecLogDir = %q，期望 /var/log/dgw", cfg.ExecLogDir)
	}
	if cfg.ExecRawRetentionDays != 14 || cfg.ExecSummaryRetentionDays != 60 {
		t.Errorf("执行记录保留期 = %d/%d，期望 14/60",
			cfg.ExecRawRetentionDays, cfg.ExecSummaryRetentionDays)
	}
}

// 非法数值（非整数）退回默认：解析失败不吞启动——数值本身合法但越界的
// 配置（如并发上限 0/负值）交给网关启动校验 fail fast，getenvInt 不回退。
func TestFromEnvInvalidIntFallsBack(t *testing.T) {
	t.Setenv(EnvSQLLimit, "abc")
	t.Setenv(EnvKeyConcurrency, "two")
	t.Setenv(EnvProcessConcurrency, "12.5")

	cfg := FromEnv()
	if cfg.SQLLimit != 500 {
		t.Errorf("非法 SQLLimit 应退回默认 500，得到 %d", cfg.SQLLimit)
	}
	if cfg.KeyConcurrency != 2 {
		t.Errorf("非法 KeyConcurrency 应退回默认 2，得到 %d", cfg.KeyConcurrency)
	}
	if cfg.ProcessConcurrency != 8 {
		t.Errorf("非法 ProcessConcurrency 应退回默认 8，得到 %d", cfg.ProcessConcurrency)
	}
}

// 合法但越界的数值不回退（如 DGW_KEY_CONCURRENCY=-5）：getenvInt 原样
// 透传，由网关启动校验拒绝（WithLoadGate fail fast）。
func TestFromEnvPassesOutOfRangeInt(t *testing.T) {
	t.Setenv(EnvKeyConcurrency, "-5")
	cfg := FromEnv()
	if cfg.KeyConcurrency != -5 {
		t.Errorf("KeyConcurrency = %d，期望 -5 原样透传（启动校验处理）", cfg.KeyConcurrency)
	}
}
