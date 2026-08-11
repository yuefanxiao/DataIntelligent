// Package db 是 execute_sql 的 PG 接线面（04 票）：按 dbname 路由的连接池
// 集合 + PG 物理边界（ADR-0008 校验层段三）。
//
// 物理边界在此落地为连接级参数：
//   - 共享只读角色：DSN 即该角色的连接（网关永不超管，ADR-0004）；
//   - statement_timeout：每个连接强制（spec §4.9 默认 30s，env 可配）；
//   - default_transaction_read_only=on：即使角色配置失误，连接层也不可写；
//   - 禁 SET ROLE：物理上角色无成员资格无法切换，分类层（validate 包）同时
//     拒绝 SET 类 utility 语句——双保险。
//
// 路由表（dbname → {service, DSN}）来自 env（DGW_PG_DATABASES，JSON 数组，
// spec §4.8「数据库凭证只存该机 env 文件」）；service 段用于组装授权 FQN
// （服务.库.表，ADR-0004）——语义层拓扑（07）落地前，服务归属由配置显式给出。
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry 是一条 dbname 路由：PG 数据库 + 其服务归属（FQN 服务段）+ DSN。
// JSON 形态即 DGW_PG_DATABASES 的数组元素。
type Entry struct {
	DBName  string `json:"dbname"`
	Service string `json:"service"`
	DSN     string `json:"dsn"`
}

// ParseEntries 解析 DGW_PG_DATABASES 的 JSON 数组并校验：dbname/service/DSN
// 非空、dbname 唯一、三段都不含 "."（FQN 分隔符——含点会破坏 服务.库.表
// 命名空间）、DSN 可解析（pgx.ParseConfig 在 NewRouter 时再验）。
func ParseEntries(raw string) ([]Entry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("解析 DGW_PG_DATABASES 失败（期望 JSON 数组，元素 {dbname, service, dsn}）: %w", err)
	}
	seen := map[string]struct{}{}
	for i, e := range entries {
		if err := validateEntry(e); err != nil {
			return nil, fmt.Errorf("DGW_PG_DATABASES 第 %d 项: %w", i+1, err)
		}
		if _, dup := seen[e.DBName]; dup {
			return nil, fmt.Errorf("DGW_PG_DATABASES dbname 重复: %q", e.DBName)
		}
		seen[e.DBName] = struct{}{}
	}
	return entries, nil
}

// validateEntry 校验单条路由：FQN 段（服务/库）非空且不含 "."、DSN 非空。
func validateEntry(e Entry) error {
	if e.DBName == "" {
		return fmt.Errorf("dbname 为空")
	}
	if e.Service == "" {
		return fmt.Errorf("service 为空")
	}
	if e.DSN == "" {
		return fmt.Errorf("dsn 为空")
	}
	for _, part := range []struct{ name, v string }{{"dbname", e.DBName}, {"service", e.Service}} {
		if strings.Contains(part.v, ".") {
			return fmt.Errorf("%s %q 不能含 \".\"（FQN 服务.库.表 分隔符）", part.name, part.v)
		}
	}
	return nil
}

// Router 是按 dbname 路由的池集合：启动时按 Entry 建池（连接惰性建立），
// 每个连接强制物理边界参数；Lookup 按 dbname 取池 + 服务归属。
type Router struct {
	pools map[string]*pgxpool.Pool
	svc   map[string]string
}

// NewRouter 按路由表建池；statementTimeout 为连接级 statement_timeout。
// 任一 DSN 不可解析 = 启动失败（fail fast：配置错误绝不能带病服务）。
func NewRouter(ctx context.Context, entries []Entry, statementTimeout time.Duration) (*Router, error) {
	r := &Router{
		pools: make(map[string]*pgxpool.Pool, len(entries)),
		svc:   make(map[string]string, len(entries)),
	}
	for _, e := range entries {
		cfg, err := pgxpool.ParseConfig(e.DSN)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("dbname %q 的 DSN 不可解析: %w", e.DBName, err)
		}
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(statementTimeout.Milliseconds(), 10)
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("dbname %q 建池失败: %w", e.DBName, err)
		}
		r.pools[e.DBName] = pool
		r.svc[e.DBName] = e.Service
	}
	return r, nil
}

// Lookup 按 dbname 取池与服务归属；未知 dbname 返回 ok=false（调用方按
// 无效请求拒绝，不 panic）。
func (r *Router) Lookup(dbname string) (pool *pgxpool.Pool, service string, ok bool) {
	pool, ok = r.pools[dbname]
	if !ok {
		return nil, "", false
	}
	return pool, r.svc[dbname], true
}

// Single 返回唯一 dbname（路由表恰好一条时），供 execute_sql 省略 dbname
// 参数的缺省推断；多库配置返回 ""（调用方要求显式指定）。
func (r *Router) Single() string {
	if len(r.pools) == 1 {
		for dbname := range r.pools {
			return dbname
		}
	}
	return ""
}

// Close 关闭全部池（网关退出时调用）。
func (r *Router) Close() {
	for _, p := range r.pools {
		p.Close()
	}
}
