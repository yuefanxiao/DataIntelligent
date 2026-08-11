// 启动自检（ADR-0009 / spec §4.8，不过拒启）：逐 dbname 三条硬校验——
//
//  1. pg_catalog.pg_is_in_recovery() = true：防连错主库（业务从库是唯一
//     合法目标；连到主库 = 校验层物理边界整体失效）；
//  2. 角色级 statement_timeout 生效：用「纯净连接」（不带网关连接级
//     参数）SHOW statement_timeout，值等于网关配置才放行——证明共享只读
//     角色（ADR-0004）的 provisioning 边界真实落地，而非仅依赖网关侧
//     连接参数兜底（spec §4.9「网关连接级 + 角色级双设置」）；
//  3. current_database() 与路由 dbname 一致：DSN 目标库必须等于路由声明
//     （db.go 注释的 10 票职责；DSN 指错库 = 授权 FQN 全部失效）。
//
// 校验对象 = 每条 dbname 路由（同一共享只读角色连 10 库，ADR-0004；任一
// 库连错/超时未生效 = 拒启，不留「部分边界」）。自检连接是逐条实时连接，
// 不做池缓存——自检的意义就是启动瞬间的真实边界探测；探测失败（连不上）
// 同样拒启（fail closed）。边界结论只保证启动瞬间（CNPG failover 提升
// 从库后失效直至重启；周期再校验 = ADR-0009 后置的阶段 2 项）。
package db

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// selfcheckProbeTimeout 是单条自检探测连接的超时（启动期不能无限等——
// 连不上 = 拒启，与「不过拒启」同一语义；数值为进程内常量，不入参数表）。
const selfcheckProbeTimeout = 10 * time.Second

// SelfCheck 逐 dbname 跑三条硬校验（不过拒启）。任一失败 = 聚合错误
// （全部 dbname 的失败一并报出，运维一次看到全貌；成功项不在错误里）。
// want 是配置的 statement_timeout（DGW_PG_STATEMENT_TIMEOUT_MS），角色级
// 生效值须与之相等（两边不一致 = 边界事实不清，拒启）。
func (r *Router) SelfCheck(ctx context.Context, want time.Duration) error {
	wantMs := want.Milliseconds()
	var errs []string
	for _, name := range r.DBNames() {
		if err := r.selfCheckOne(ctx, name, wantMs); err != nil {
			errs = append(errs, fmt.Sprintf("dbname %q: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("启动自检失败（拒启）：%s", strings.Join(errs, "; "))
	}
	return nil
}

// selfCheckOne 对单条 dbname 路由跑三条硬校验。
func (r *Router) selfCheckOne(ctx context.Context, dbname string, wantMs int64) error {
	rt := r.routes[dbname]
	pctx, cancel := context.WithTimeout(ctx, selfcheckProbeTimeout)
	defer cancel()

	// 纯净连接：去掉网关强制参数（statement_timeout / 只读事务），SHOW
	// 才能反映角色级真实生效值——否则校验的永远是网关自己的连接参数。
	// 大小写不敏感删除 + 剔除 options（DSN 的 options=-c statement_timeout
	// 也会随启动包下发并被后端按命令行开关应用，同样会掩盖角色级缺配）。
	cfg, err := pgx.ParseConfig(rt.dsn)
	if err != nil {
		return fmt.Errorf("DSN 解析失败: %w", err)
	}
	for key := range cfg.RuntimeParams {
		switch strings.ToLower(key) {
		case "statement_timeout", "default_transaction_read_only", "options":
			delete(cfg.RuntimeParams, key)
		}
	}
	conn, err := pgx.ConnectConfig(pctx, cfg)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	defer conn.Close(context.Background())

	// 硬校验 1：pg_catalog.pg_is_in_recovery() = true（从库才合法；限定
	// pg_catalog 前缀防 search_path 同名函数遮蔽）。
	var inRecovery bool
	if err := conn.QueryRow(pctx, `SELECT pg_catalog.pg_is_in_recovery()`).Scan(&inRecovery); err != nil {
		return fmt.Errorf("查询 pg_is_in_recovery() 失败: %w", err)
	}
	if !inRecovery {
		return fmt.Errorf("pg_is_in_recovery() = false——连到的是主库（拒启；从库连接是校验层物理边界，ADR-0008/0009）")
	}

	// 硬校验 2：角色级 statement_timeout 生效（纯净连接 SHOW = 配置值）。
	var shown string
	if err := conn.QueryRow(pctx, `SHOW statement_timeout`).Scan(&shown); err != nil {
		return fmt.Errorf("查询 statement_timeout 失败: %w", err)
	}
	gotMs, err := parseTimeoutMs(shown)
	if err != nil {
		return fmt.Errorf("角色级 statement_timeout 值 %q 无法解析: %v", shown, err)
	}
	if gotMs != wantMs {
		return fmt.Errorf("角色级 statement_timeout 未生效：当前 %s（%dms），配置要求 %dms（检查 provisioning：ALTER ROLE ... SET statement_timeout）",
			shown, gotMs, wantMs)
	}

	// 硬校验 3：pg_catalog.current_database() 与路由 dbname 一致（DSN 指错
	// 库 = 授权 FQN 与执行目标错位，整个路由失效——db.go 注释的 10 票职责）。
	var curDB string
	if err := conn.QueryRow(pctx, `SELECT pg_catalog.current_database()`).Scan(&curDB); err != nil {
		return fmt.Errorf("查询 current_database() 失败: %w", err)
	}
	if curDB != dbname {
		return fmt.Errorf("DSN 目标库 current_database() = %q，路由 dbname = %q（不一致，拒启；DSN 指错库 = 授权 FQN 全部失效）",
			curDB, dbname)
	}
	return nil
}

// DBNames 返回全部 dbname（排序，确定性输出）。
func (r *Router) DBNames() []string {
	names := make([]string, 0, len(r.routes))
	for n := range r.routes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// parseTimeoutMs 解析 PG 的 statement_timeout 显示值（SHOW 的规范化输出：
// 纯整数毫秒、或带单位 s/ms/min/h/d，单位值支持小数）→ 毫秒。解析失败
// 返回错误（自检按配置错误拒启——不能静默放行未知形态）。
func parseTimeoutMs(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("空值")
	}
	// 纯数字 = 毫秒（SET 数字时无单位）。
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, nil
	}
	units := []struct {
		suffix string
		mult   int64
	}{{"ms", 1}, {"s", 1000}, {"min", 60000}, {"h", 3600000}, {"d", 86400000}}
	for _, u := range units {
		if !strings.HasSuffix(v, u.suffix) {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSuffix(v, u.suffix), 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析 %q（期望整数毫秒或 s/ms/min/h/d 单位）", v)
		}
		return int64(f * float64(u.mult)), nil
	}
	return 0, fmt.Errorf("无法解析 %q（期望整数毫秒或 s/ms/min/h/d 单位）", v)
}
