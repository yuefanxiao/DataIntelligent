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
	"strings"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/config"
	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/db"
	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gateway"
	"github.com/yuefanxiao/DataIntelligent/internal/grants"
	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
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
	case "semantic-sync":
		cmdSemanticSync()
	case "semantic-backup":
		cmdSemanticBackup()
	case "selfcheck":
		cmdSelfCheck()
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
  dgw grants-apply --file PATH [--db PATH]  把 grants YAML 全量编译进权限表（指标/概念/通配授权经语义层展开）
  dgw grants-snapshot [--db PATH]           查看授权快照（key + 表授权）
  dgw semantic-sync --dir DIR [--db PATH] [--dry-run]   语义同步管线：编译 → dry-run diff → 应用
  dgw semantic-backup --out PATH [--db PATH] 运行时存储备份（WAL checkpoint + 文件拷贝）
  dgw selfcheck       启动自检：逐 dbname 两条硬校验（pg_is_in_recovery + 角色级 statement_timeout），不过拒启

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
  DGW_EXEC_LOG_DIR                执行记录目录（默认 ./logs；原始 JSONL + 聚合摘要）
  DGW_EXEC_RAW_RETENTION_DAYS     执行记录原始保留天数（默认 7，spec §4.9）
  DGW_EXEC_SUMMARY_RETENTION_DAYS 聚合摘要保留天数（默认 30，spec §4.9）
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

// gatewayOpts 组装 New 的可选注入（execute_sql 路由 + 限额 + 并发闸 +
// 执行记录），并返回 router（nil = 未配置 PG 路由）与 cleanup。
// 并发闸恒注入：即使未配置 PG 路由，env 数值也经 WithLoadGate 校验
// （越界配置同样启动失败，不会静默退化为默认 2/8）。执行记录恒注入：
// 目录不可写 = 启动失败（execLog fail fast），不存在「带病不记录」形态。
// cleanup 关闭执行记录器与 PG 路由（路由未配置时只关记录器）。
func gatewayOpts(cfg config.Config) (opts []gateway.Option, router *db.Router, cleanup func(), err error) {
	lg := execLog(cfg)
	opts = []gateway.Option{
		gateway.WithLoadGate(cfg.KeyConcurrency, cfg.ProcessConcurrency),
		gateway.WithExecLog(lg),
	}
	router, err = buildRouter(cfg)
	if err != nil {
		_ = lg.Close()
		return nil, nil, nil, err
	}
	if router == nil {
		return opts, nil, func() { _ = lg.Close() }, nil
	}
	opts = append(opts, gateway.WithExecuteSQL(router, cfg.SQLLimit))
	return opts, router, func() { router.Close(); _ = lg.Close() }, nil
}

// runStartupSelfCheck 是启动自检接线（ADR-0009，不过拒启）：serve /
// serve-stdio 在网关服务前对全部 dbname 路由跑两条硬校验，失败 = 进程
// 退出（拒启）。router 为 nil（未配置 DGW_PG_DATABASES）时无可自检对象，
// 跳过——execute_sql 未配置，网关只服务语义工具（结构化「未配置」拒绝）。
func runStartupSelfCheck(cfg config.Config, router *db.Router) {
	if router == nil {
		log.Printf("未配置 DGW_PG_DATABASES，跳过启动自检（execute_sql 未配置，网关只服务语义工具）")
		return
	}
	if err := router.SelfCheck(context.Background(), time.Duration(cfg.PGTimeoutMS)*time.Millisecond); err != nil {
		log.Fatalf("启动自检失败（拒启）: %v", err)
	}
	log.Printf("启动自检通过：%d 条 dbname 路由全部连到从库（pg_is_in_recovery() = true）+ 角色级 statement_timeout 生效",
		len(router.DBNames()))
}

// execLog 打开执行记录写入器（06 票；spec §4.9 参数表：原始 7 天轮转 +
// 聚合摘要 30 天，env 可覆盖；ADR-0009 部署 volume /logs）。目录不可建/
// 保留期非法 = 启动失败（配置错误 fail fast）——serve / serve-stdio 形态
// 共用：六工具全记是网关契约（spec §4.6），记录设施不可用 = 带病服务，
// 启动期尽早暴露；一次性 CLI 命令（key-create/revoke）用 openExecLog
// 降级（记录失败不影响命令结果，ADR-0006「故障响应不依赖任何审计设施」）。
func execLog(cfg config.Config) *execrecord.Logger {
	l, err := openExecLog(cfg)
	if err != nil {
		log.Fatalf("执行记录初始化失败: %v", err)
	}
	return l
}

// openExecLog 是 execLog 的非致命形态（返回错误由调用方决定处理）。
func openExecLog(cfg config.Config) (*execrecord.Logger, error) {
	return execrecord.New(execrecord.Config{
		Dir:                  cfg.ExecLogDir,
		RawRetentionDays:     cfg.ExecRawRetentionDays,
		SummaryRetentionDays: cfg.ExecSummaryRetentionDays,
	})
}

// logKeyLifecycle 落一行 key 生命周期记录（CLI 侧，spec §4.6「各记一行」）。
// 记录失败只记日志——命令结果不依赖审计设施（ADR-0006）。
func logKeyLifecycle(lg *execrecord.Logger, kc execrecord.KeyLifecycle) {
	if err := lg.LogKeyLifecycle(kc); err != nil {
		log.Printf("执行记录写入失败: %v", err)
	}
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
	opts, router, closeRouter, err := gatewayOpts(cfg)
	if err != nil {
		log.Fatalf("execute_sql 路由配置错误: %v", err)
	}
	defer closeRouter()
	runStartupSelfCheck(cfg, router)
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
	opts, router, closeRouter, err := gatewayOpts(cfg)
	if err != nil {
		log.Fatalf("execute_sql 路由配置错误: %v", err)
	}
	defer closeRouter()
	runStartupSelfCheck(cfg, router)
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
	cfg := config.FromEnv()
	// 执行记录器先开（降级：目录不可写只记日志，不阻止 key 创建——否则
	// key 已落库而进程死于明文打印前，明文永久丢失）。
	lg, err := openExecLog(cfg)
	if err != nil {
		log.Printf("执行记录不可用（继续，不阻塞命令）: %v", err)
		lg = nil
	}
	st := openStore(*dbPath)
	defer st.Close()

	plain, id, err := credentials.Create(context.Background(), st.DB(), *user)
	if err != nil {
		log.Fatalf("创建凭据失败: %v", err)
	}
	// key 生命周期执行记录（06 票：CLI 侧一行）。
	if lg != nil {
		defer lg.Close()
		logKeyLifecycle(lg, execrecord.KeyLifecycle{
			TS: time.Now(), Event: execrecord.EventKeyCreated, User: *user, Key: strconv.FormatInt(id, 10),
		})
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
	cfg := config.FromEnv()
	// 执行记录器先开（降级：目录不可写不阻止吊销——命令结果不依赖记录）。
	lg, err := openExecLog(cfg)
	if err != nil {
		log.Printf("执行记录不可用（继续，不阻塞命令）: %v", err)
		lg = nil
	}
	st := openStore(*dbPath)
	defer st.Close()

	revoked, err := credentials.Revoke(context.Background(), st.DB(), idNum)
	if err != nil {
		log.Fatalf("吊销凭据失败: %v", err)
	}
	if revoked {
		// key 生命周期执行记录（06 票：CLI 侧一行；吊销成功才记——幂等
		// 无操作不伪造事件；被吊销 key 的属主从快照取）。
		if lg != nil {
			defer lg.Close()
			logKeyLifecycle(lg, execrecord.KeyLifecycle{
				TS: time.Now(), Event: execrecord.EventKeyRevoked,
				User: keyOwner(context.Background(), st, idNum), Key: *id,
			})
		}
		fmt.Printf("dgw: 已吊销 key #%d（即时生效）\n", idNum)
	} else {
		fmt.Printf("dgw: key #%d 不存在或已吊销（幂等，无操作）\n", idNum)
	}
}

// keyOwner 查一把 key 的绑定用户（吊销记录的属主字段；key 不存在返回空串）。
func keyOwner(ctx context.Context, st *store.Store, id int64) string {
	k, ok, err := credentials.Get(ctx, st.DB(), id)
	if err != nil || !ok {
		return ""
	}
	return k.UserID
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
	// 语义层授权展开器（07 票接线）：指标/概念/服务/库级授权经语义库展开
	// 为具体表清单；语义库未同步时展开失败 = 编译拒绝（杜绝悬空授权）。
	expand := semantic.NewGrantExpander(st)
	res, err := grants.Sync(context.Background(), st, f, expand)
	if err != nil {
		log.Fatalf("编译 grants YAML 进权限表失败: %v", err)
	}
	fmt.Printf("dgw: grants YAML 已编译进权限表（新增 %d 条 / 移除 %d 条 / 通配声明 %d 个，revision %d，热重载生效中）\n",
		res.Added, res.Removed, res.Patterns, res.Revision)
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

// cmdSemanticSync 运行语义同步管线（ADR-0002）：编译校验 → dry-run diff →
// 应用（幂等 upsert + 墓碑软删除）。--dry-run 只出 diff 不写库（§5.3 seam）。
// embedding：DGW_OPENAI_API_KEY 存在时同步期生成向量，失败降级不阻塞。
func cmdSemanticSync() {
	fs := flag.NewFlagSet("semantic-sync", flag.ExitOnError)
	dir := fs.String("dir", "", "语义作者入口目录（services/ + metrics.yaml + concepts.yaml）")
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	dryRun := fs.Bool("dry-run", false, "只计算 dry-run diff，不写库")
	fs.Parse(os.Args[2:])

	if *dir == "" {
		log.Fatal("semantic-sync 需要 --dir（语义作者入口目录）")
	}
	st := openStore(*dbPath)
	defer st.Close()
	ctx := context.Background()

	var res *semantic.Result
	var err error
	if *dryRun {
		res, err = semantic.DryRun(ctx, st, *dir)
	} else {
		res, err = semantic.Sync(ctx, st, *dir)
	}
	if err != nil {
		log.Fatalf("语义同步管线失败（未写库，原子拒绝）: %v", err)
	}

	printDiff(res.Diff)
	if *dryRun {
		fmt.Printf("dgw: dry-run 完成（未写库）；变更 %d 项\n", res.Diff.Count())
		return
	}

	// 应用后的通配覆盖告警：新表不在通配快照 = 默认拒绝，提示重展开。
	warnPatternCoverage(ctx, st)

	// embedding（ADR-0002「增量只嵌入变更实体」）：默认只对 diff 的
	// Added + Updated 实体生成向量；墓碑实体清理向量。例外 = 向量库
	// 缺失/模型不一致（API key 后配、模型切换）→ 全量回填，避免混合
	// 维度余弦（08 检索的垃圾结果）。失败降级不阻塞。
	if key := os.Getenv(config.EnvOpenAIKey); key != "" {
		model := os.Getenv(config.EnvEmbeddingModel)
		if model == "" {
			model = semantic.DefaultEmbeddingModel
		}
		emb := semantic.NewOpenAIEmbedder(key, model)
		changed := embeddingTarget(ctx, st, res.Diff, model)
		n, embErr := semantic.EmbedEntityTexts(ctx, st, changed, emb, model,
			func(format string, args ...any) { log.Printf(format, args...) })
		if embErr != nil {
			log.Printf("embedding 降级提示: %v", embErr)
		}
		if n > 0 {
			log.Printf("已生成 %d 个实体向量（%s）", n, model)
		}
		deleted := make([]string, 0, len(res.Diff.EntitiesDeleted))
		for _, e := range res.Diff.EntitiesDeleted {
			deleted = append(deleted, e.FQN)
		}
		if err := semantic.RemoveEmbeddings(ctx, st, deleted); err != nil {
			log.Printf("清理墓碑实体向量失败（降级不阻塞）: %v", err)
		}
	}
	fmt.Printf("dgw: 语义同步完成（实体 upsert %d / 墓碑 %d，边 %d/%d，枚举 %d/%d）\n",
		res.Stats.EntitiesUpserted, res.Stats.EntitiesTombstoned,
		res.Stats.RelationsUpserted, res.Stats.RelationsTombstoned,
		res.Stats.EnumsUpserted, res.Stats.EnumsTombstoned)
}

// printDiff 打印 dry-run diff（CLI 展示：增删改清单）。
func printDiff(d *semantic.Diff) {
	for _, e := range d.EntitiesAdded {
		fmt.Printf("  + 实体 %s (%s) %s\n", e.FQN, e.Kind, e.Description)
	}
	for _, e := range d.EntitiesUpdated {
		fmt.Printf("  ~ 实体 %s (%s)\n", e.FQN, e.Kind)
	}
	for _, e := range d.EntitiesDeleted {
		fmt.Printf("  - 实体 %s (%s)（墓碑）\n", e.FQN, e.Kind)
	}
	for _, r := range d.RelationsAdded {
		fmt.Printf("  + 边 %s %s → %s\n", r.Type, r.SrcFQN, r.DstFQN)
	}
	for _, r := range d.RelationsUpdated {
		fmt.Printf("  ~ 边 %s %s → %s（join 条件变化）\n", r.Type, r.SrcFQN, r.DstFQN)
	}
	for _, r := range d.RelationsDeleted {
		fmt.Printf("  - 边 %s %s → %s（墓碑）\n", r.Type, r.SrcFQN, r.DstFQN)
	}
	for _, v := range d.EnumsAdded {
		fmt.Printf("  + 枚举 %s = %s\n", v.ColumnFQN, v.Value)
	}
	for _, v := range d.EnumsDeleted {
		fmt.Printf("  - 枚举 %s = %s（墓碑）\n", v.ColumnFQN, v.Value)
	}
}

// warnPatternCoverage 检查通配授权快照是否覆盖当前全部表：对每条通配声明
// （user × pattern）逐项检查「该模式下的表」是否都有该用户的授权——未覆盖
// 的表 = 新表默认拒绝，提示重跑 grants-apply 重展开（ADR-0004「新表默认
// 拒绝 + 管线告警 + 重展开确认」）。
//
// 按 user×pattern 逐项而非跨用户汇总：用户 A 的 service:x 快照过期时，即使
// 用户 B 恰好持有该表，A 的告警也必须报出（过期的是 A 的展开面）。
func warnPatternCoverage(ctx context.Context, st *store.Store) {
	// 用户 × 表授权集合（覆盖判定面，残留授权检查共用）。
	grantsList, err := grants.Snapshot(ctx, st)
	if err != nil {
		log.Printf("读取授权快照失败（跳过覆盖检查）: %v", err)
		return
	}
	userGrants := map[string]map[string]bool{}
	for _, g := range grantsList {
		if userGrants[g.User] == nil {
			userGrants[g.User] = map[string]bool{}
		}
		userGrants[g.User][g.TableFQN] = true
	}

	// 删除方向：授权指向的表在语义库中已墓碑/不存在 = 残留授权
	// （review 修复：语义删除不自动撤权——授权事实源在 grants YAML，
	// 但必须有管线提示，否则「指标有权底层没权」的反向悬空无人知晓）。
	warnStaleGrants(ctx, st, grantsList)

	patterns, err := grants.SyncPatterns(ctx, st)
	if err != nil {
		log.Printf("读取通配声明失败（跳过通配覆盖检查）: %v", err)
		return
	}
	if len(patterns) == 0 {
		return
	}

	uncovered := 0
	for _, up := range patterns {
		user, pattern := grants.SplitExpandKey(up)
		var tables []string
		switch {
		case strings.HasPrefix(pattern, grants.PrefixService):
			tables, err = semantic.TablesForService(ctx, st, strings.TrimPrefix(pattern, grants.PrefixService))
		case strings.HasPrefix(pattern, grants.PrefixDatabase):
			tables, err = semantic.TablesForDatabase(ctx, st, strings.TrimPrefix(pattern, grants.PrefixDatabase))
		default:
			log.Printf("未知通配声明 %q（跳过）", pattern)
			continue
		}
		if err != nil {
			log.Printf("通配覆盖检查失败（%s）: %v", pattern, err)
			continue
		}
		have := userGrants[user]
		for _, tbl := range tables {
			if !have[tbl] {
				uncovered++
				log.Printf("管线告警：用户 %s 的通配 %s 未覆盖新表 %s（默认拒绝；重跑 grants-apply 触发重展开确认）",
					user, pattern, tbl)
			}
		}
	}
	if uncovered > 0 {
		log.Printf("共 %d 张新表不在通配授权快照中（默认拒绝）", uncovered)
	}
}

// warnStaleGrants 检查授权快照里指向墓碑/不存在表的残留授权：语义实体被
// 删除后，展开快照（dgw_table_grants）原样保留——业务数据面照常放行。
// 只告警不撤权（撤权 = 改 grants YAML + 重跑 grants-apply，git review 闸门），
// 与 ADR-0004「重展开确认」同一哲学。
func warnStaleGrants(ctx context.Context, st *store.Store, grantsList []grants.Grant) {
	stale := 0
	for _, g := range grantsList {
		e, err := semantic.GetEntity(ctx, st, g.TableFQN)
		if err != nil {
			log.Printf("残留授权检查失败（%s × %s）: %v", g.User, g.TableFQN, err)
			continue
		}
		if e == nil {
			stale++
			log.Printf("管线告警：用户 %s 对表 %s 的授权指向已删除/墓碑实体（残留授权；若不再需要请从 grants YAML 移除并重跑 grants-apply）",
				g.User, g.TableFQN)
		}
	}
	if stale > 0 {
		log.Printf("共 %d 条残留授权指向已删除实体（业务数据面仍放行，建议清理）", stale)
	}
}

// embeddingTarget 决定本次同步要嵌入的实体集：
//   - 全量（Snapshot 全部活跃实体）：向量库缺失或模型不一致——API key 后配
//     （首启留空）与 DGW_EMBEDDING_MODEL 切换（混合维度余弦 = 垃圾检索结果）
//     都需要全量回填；
//   - 增量（diff 的 Added + Updated）：常规幂等同步（ADR-0002「增量只嵌入
//     变更实体」）。
//
// 覆盖检查失败按全量回填处理（fail-safe 方向：宁可多嵌不可漏嵌）。
func embeddingTarget(ctx context.Context, st *store.Store, d *semantic.Diff, model string) *semantic.Target {
	missing, mismatch, err := semantic.EmbeddingCoverage(ctx, st, model)
	if err != nil || missing > 0 || mismatch > 0 {
		if err != nil {
			log.Printf("embedding 覆盖检查失败（按全量回填处理）: %v", err)
		} else if missing > 0 {
			log.Printf("embedding 全量回填：%d 个实体缺向量（API key 后配或历史失败）", missing)
		} else {
			log.Printf("embedding 全量回填：模型切换残留旧向量（混合维度不可用于检索）")
		}
		t, snapErr := semantic.Snapshot(ctx, st)
		if snapErr != nil {
			log.Printf("全量回填取快照失败（降级：跳过本轮 embedding）: %v", snapErr)
			return &semantic.Target{}
		}
		return t
	}
	return &semantic.Target{Entities: append(
		append([]semantic.Entity{}, d.EntitiesAdded...), d.EntitiesUpdated...)}
}

// cmdSemanticBackup 备份运行时存储：WAL checkpoint + 文件拷贝（ADR-0005）。
func cmdSemanticBackup() {
	fs := flag.NewFlagSet("semantic-backup", flag.ExitOnError)
	out := fs.String("out", "", "备份目标文件路径")
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	if *out == "" {
		log.Fatal("semantic-backup 需要 --out（备份目标文件路径）")
	}
	st := openStore(*dbPath)
	defer st.Close()

	if err := semantic.Backup(context.Background(), st, *out); err != nil {
		log.Fatalf("备份失败: %v", err)
	}
	fmt.Printf("dgw: 已备份到 %s（WAL checkpoint + 文件拷贝，可直接用于回滚恢复）\n", *out)
}

// cmdSelfCheck 单独跑启动自检（ADR-0009 两条硬校验，不过拒启）：与
// serve 启动时同一校验路径，独立子命令供运维排障与失败场景演示
// （「连错主库 / 角色级超时未生效 → 拒启」不必起网关即可复现）。
func cmdSelfCheck() {
	cfg := config.FromEnv()
	router, err := buildRouter(cfg)
	if err != nil {
		log.Fatalf("execute_sql 路由配置错误: %v", err)
	}
	if router == nil {
		fmt.Println("dgw: 未配置 DGW_PG_DATABASES，无可自检路由（跳过）")
		return
	}
	defer router.Close()

	names := router.DBNames()
	for _, n := range names {
		fmt.Printf("  校验 %s ...\n", n)
	}
	if err := router.SelfCheck(context.Background(), time.Duration(cfg.PGTimeoutMS)*time.Millisecond); err != nil {
		log.Printf("启动自检失败（拒启）: %v", err)
		os.Exit(1)
	}
	fmt.Printf("dgw: 启动自检通过：%d 条 dbname 路由全部连到从库（pg_is_in_recovery() = true）+ 角色级 statement_timeout 生效\n", len(names))
}
