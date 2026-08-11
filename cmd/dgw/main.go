// Command dgw 是数据智能层网关的可执行入口（ADR-0009 交付物形态）。
//
// 子命令：
//
//	dgw serve                 Streamable HTTP 守护进程形态（主，bearer 认证）
//	dgw serve-stdio           调试形态（凭据经 DGW_API_KEY env 传入）
//	dgw key-create --user X   创建凭据：明文仅打印一次，哈希落库
//	dgw key-revoke --id N     吊销凭据：即时生效（宿主机运维面）
//	dgw grant-add --user X --table FQN      给用户加一张表的授权
//	dgw grant-remove --user X --table FQN   撤掉用户对一张表的授权
//	dgw grants-apply --file PATH   把 grants YAML 全量编译进权限表
//	dgw grants-snapshot            查看授权快照（key + 表授权）
//
// 权限 CLI 仅应在网关宿主机上运行（能上宿主机 = 运维者，ADR-0004 v1
// 无管理员角色）；授权变更走 git review = grants YAML 的 apply 路径。
//
// 配置面 = env（spec §4.9 参数表「env 可覆盖」），flag 可覆盖同名 env。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/config"
	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/gateway"
	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("dgw: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe()
	case "serve-stdio":
		cmdServeStdio()
	case "key-create":
		cmdKeyCreate()
	case "key-revoke":
		cmdKeyRevoke()
	case "grant-add":
		cmdGrantAdd()
	case "grant-remove":
		cmdGrantRemove()
	case "grants-apply":
		cmdGrantsApply()
	case "grants-snapshot":
		cmdGrantsSnapshot()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "dgw: 未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dgw — 数据智能层网关

用法：
  dgw serve [--db PATH] [--addr ADDR]       Streamable HTTP 守护进程形态（主）
  dgw serve-stdio [--db PATH]               调试形态（env 传 DGW_API_KEY）
  dgw key-create --user USER [--db PATH]    创建凭据：明文仅打印一次
  dgw key-revoke --id ID [--db PATH]        吊销凭据：即时生效（ID 见 grants-snapshot）
  dgw grant-add --user USER --table FQN [--db PATH]     加一张表的授权（服务.库.表）
  dgw grant-remove --user USER --table FQN [--db PATH]  撤一张表的授权
  dgw grants-apply --file PATH [--db PATH]  把 grants YAML 全量编译进权限表
  dgw grants-snapshot [--db PATH]           查看授权快照（key + 表授权）

权限 CLI 仅限网关宿主机运行（能上宿主机 = 运维者，v1 无管理员角色）；
grants YAML 是表授权的事实源（git review 即权限变更评审闸门），
grant-add/remove 是临时调整，下次 grants-apply 会被 YAML 状态覆盖。

env（flag 优先）：
  DGW_DB_PATH      SQLite 运行时存储路径（默认 ./dgw.db）
  DGW_HTTP_ADDR    HTTP 监听地址（默认 :8080）
  DGW_API_KEY      serve-stdio 的凭据
  DGW_PG_DATABASES  execute_sql 路由表（JSON 数组 [{"dbname","service","dsn"}]；DSN 即数据库凭证）
  DGW_SQL_LIMIT    execute_sql 行数上限（默认 500，范围 500-5000）
  DGW_PG_STATEMENT_TIMEOUT_MS  statement_timeout 毫秒（默认 30000）
  DGW_KEY_CONCURRENCY     每 key 并发查询上限（默认 2，超限结构化拒绝不排队）
  DGW_PROCESS_CONCURRENCY 进程级总并发上限（默认 8，守护进程语义；stdio 退化为每进程闸）
`)
}

// openStore 打开运行时存储，flag 优先于 env。
func openStore(dbFlag string) *store.Store {
	path := dbFlag
	if path == "" {
		path = config.FromEnv().DBPath
	}
	st, err := store.Open(path)
	if err != nil {
		log.Fatalf("打开运行时存储失败: %v", err)
	}
	return st
}

// buildRouter 从 env 构建 execute_sql 的 PG 路由；未配置（DGW_PG_DATABASES
// 为空）返回 nil——execute_sql 返回结构化「未配置」错误，网关其余功能照常。
func buildRouter(cfg config.Config) (*db.Router, error) {
	entries, err := db.ParseEntries(cfg.PGDatabases)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return db.NewRouter(context.Background(), entries, time.Duration(cfg.PGTimeoutMS)*time.Millisecond)
}

// gatewayOpts 组装 New 的可选注入（execute_sql 路由 + 限额 + 并发闸）。
// 并发闸恒注入：即使未配置 PG 路由，env 数值也经 WithLoadGate 校验
// （越界配置同样启动失败，不会静默退化为默认 2/8）。
func gatewayOpts(cfg config.Config) ([]gateway.Option, func(), error) {
	opts := []gateway.Option{
		gateway.WithLoadGate(cfg.KeyConcurrency, cfg.ProcessConcurrency),
	}
	router, err := buildRouter(cfg)
	if err != nil {
		return nil, nil, err
	}
	if router == nil {
		return opts, func() {}, nil
	}
	opts = append(opts, gateway.WithExecuteSQL(router, cfg.SQLLimit))
	return opts, router.Close, nil
}

func cmdServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	addr := fs.String("addr", "", "HTTP 监听地址（缺省取 DGW_HTTP_ADDR）")
	fs.Parse(os.Args[2:])

	cfg := config.FromEnv()
	if *addr != "" {
		cfg.HTTPAddr = *addr
	}

	st := openStore(*dbPath)
	defer st.Close()
	opts, closeRouter, err := gatewayOpts(cfg)
	if err != nil {
		log.Fatalf("execute_sql 路由配置错误: %v", err)
	}
	defer closeRouter()
	g, err := gateway.New(st, slog.New(slog.NewTextHandler(os.Stderr, nil)), opts...)
	if err != nil {
		log.Fatalf("网关启动失败（权限快照加载失败）: %v", err)
	}
	// 热重载：CLI grant/revoke 后无需重启即生效（进程生命周期内持续轮询）。
	g.StartAuthzReload(context.Background())

	log.Printf("MCP Streamable HTTP 监听 %s（bearer 认证）", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, g.HTTPHandler()); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}

func cmdServeStdio() {
	fs := flag.NewFlagSet("serve-stdio", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	cfg := config.FromEnv()
	apiKey := cfg.APIKey
	st := openStore(*dbPath)
	defer st.Close()
	opts, closeRouter, err := gatewayOpts(cfg)
	if err != nil {
		log.Fatalf("execute_sql 路由配置错误: %v", err)
	}
	defer closeRouter()
	g, err := gateway.New(st, slog.New(slog.NewTextHandler(os.Stderr, nil)), opts...)
	if err != nil {
		log.Fatalf("网关启动失败（权限快照加载失败）: %v", err)
	}
	g.StartAuthzReload(context.Background())

	if err := g.ServeStdio(context.Background(), apiKey); err != nil {
		log.Fatalf("%v", err)
	}
}

func cmdKeyCreate() {
	fs := flag.NewFlagSet("key-create", flag.ExitOnError)
	user := fs.String("user", "", "绑定用户身份（审计聚合维度）")
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	if *user == "" {
		log.Fatal("key-create 需要 --user（绑定用户身份）")
	}
	st := openStore(*dbPath)
	defer st.Close()

	plain, err := credentials.Create(context.Background(), st.DB(), *user)
	if err != nil {
		log.Fatalf("创建凭据失败: %v", err)
	}
	// 明文仅此一次打印；哈希已落库，此后任何地方不再持有明文。
	fmt.Printf("dgw: 新凭据（用户 %s）——明文仅打印一次，请立即保存：\n%s\n", *user, plain)
}

func cmdKeyRevoke() {
	fs := flag.NewFlagSet("key-revoke", flag.ExitOnError)
	id := fs.String("id", "", "要吊销的 key ID（grants-snapshot 可查）")
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	if *id == "" {
		log.Fatal("key-revoke 需要 --id（grants-snapshot 查看 key ID）")
	}
	idNum, err := strconv.ParseInt(*id, 10, 64)
	if err != nil {
		log.Fatalf("key ID 应为数字: %q", *id)
	}
	st := openStore(*dbPath)
	defer st.Close()

	revoked, err := credentials.Revoke(context.Background(), st.DB(), idNum)
	if err != nil {
		log.Fatalf("吊销凭据失败: %v", err)
	}
	if revoked {
		fmt.Printf("dgw: 已吊销 key #%d（即时生效）\n", idNum)
	} else {
		fmt.Printf("dgw: key #%d 不存在或已吊销（幂等，无操作）\n", idNum)
	}
}

// grantCmd 解析 grant-add / grant-remove 的公共 flag。
func grantCmd() (user, fqn, dbPath *string) {
	fs := flag.NewFlagSet("grant", flag.ExitOnError)
	user = fs.String("user", "", "绑定用户身份")
	fqn = fs.String("table", "", "表 FQN（服务.库.表）")
	dbPath = fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])
	return user, fqn, dbPath
}

func cmdGrantAdd() {
	user, fqn, dbPath := grantCmd()
	if *user == "" {
		log.Fatal("grant-add 需要 --user")
	}
	if *fqn == "" {
		log.Fatal("grant-add 需要 --table（表 FQN：服务.库.表）")
	}
	st := openStore(*dbPath)
	defer st.Close()

	if err := grants.AddGrant(context.Background(), st, *user, *fqn); err != nil {
		log.Fatalf("授权失败: %v", err)
	}
	fmt.Printf("dgw: 已授权 %s → %s（热重载：网关无需重启）\n", *user, *fqn)
}

func cmdGrantRemove() {
	user, fqn, dbPath := grantCmd()
	if *user == "" {
		log.Fatal("grant-remove 需要 --user")
	}
	if *fqn == "" {
		log.Fatal("grant-remove 需要 --table（表 FQN：服务.库.表）")
	}
	st := openStore(*dbPath)
	defer st.Close()

	if err := grants.RemoveGrant(context.Background(), st, *user, *fqn); err != nil {
		log.Fatalf("撤权失败: %v", err)
	}
	fmt.Printf("dgw: 已撤销 %s → %s（热重载：网关无需重启）\n", *user, *fqn)
}

func cmdGrantsApply() {
	fs := flag.NewFlagSet("grants-apply", flag.ExitOnError)
	file := fs.String("file", "", "grants YAML 路径")
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	if *file == "" {
		log.Fatal("grants-apply 需要 --file（grants YAML 路径）")
	}
	st := openStore(*dbPath)
	defer st.Close()

	fh, err := os.Open(*file)
	if err != nil {
		log.Fatalf("打开 grants YAML: %v", err)
	}
	defer fh.Close()
	f, err := grants.Parse(fh)
	if err != nil {
		log.Fatalf("解析 grants YAML 失败: %v", err)
	}
	res, err := grants.Sync(context.Background(), st, f)
	if err != nil {
		log.Fatalf("编译 grants YAML 进权限表失败: %v", err)
	}
	fmt.Printf("dgw: grants YAML 已编译进权限表（新增 %d 条 / 移除 %d 条，revision %d，热重载生效中）\n",
		res.Added, res.Removed, res.Revision)
}

func cmdGrantsSnapshot() {
	fs := flag.NewFlagSet("grants-snapshot", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	st := openStore(*dbPath)
	defer st.Close()
	ctx := context.Background()

	keys, err := credentials.List(ctx, st.DB())
	if err != nil {
		log.Fatalf("读取 key 快照失败: %v", err)
	}
	fmt.Println("=== 凭据（key）===")
	if len(keys) == 0 {
		fmt.Println("（无）")
	}
	for _, k := range keys {
		status := "有效"
		if k.RevokedAt != "" {
			status = "已吊销 " + k.RevokedAt
		}
		fmt.Printf("  #%-4d %-12s %s  %s\n", k.ID, k.UserID, k.CreatedAt, status)
	}

	grantsList, err := grants.Snapshot(ctx, st)
	if err != nil {
		log.Fatalf("读取授权快照失败: %v", err)
	}
	fmt.Println("=== 表授权（user × 服务.库.表）===")
	if len(grantsList) == 0 {
		fmt.Println("（无——业务数据面默认拒绝）")
	}
	for _, g := range grantsList {
		fmt.Printf("  %-12s %s\n", g.User, g.TableFQN)
	}

	rev, err := st.PermissionRevision(ctx)
	if err != nil {
		log.Fatalf("读取 revision 失败: %v", err)
	}
	fmt.Printf("权限 revision: %d\n", rev)
}
