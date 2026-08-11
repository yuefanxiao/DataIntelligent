// Command dgw 是数据智能层网关的可执行入口（ADR-0009 交付物形态）。
//
// 子命令：
//
//	dgw serve                 Streamable HTTP 守护进程形态（主，bearer 认证）
//	dgw serve-stdio           调试形态（凭据经 DGW_API_KEY env 传入）
//	dgw key-create --user X   创建凭据：明文仅打印一次，哈希落库
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

	"github.com/yuefanxiao/DataIntelligent/internal/config"
	"github.com/yuefanxiao/DataIntelligent/internal/credentials"
	"github.com/yuefanxiao/DataIntelligent/internal/gateway"
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
  dgw serve [--db PATH] [--addr ADDR]      Streamable HTTP 守护进程形态（主）
  dgw serve-stdio [--db PATH]              调试形态（env 传 DGW_API_KEY）
  dgw key-create --user USER [--db PATH]   创建凭据：明文仅打印一次

env（flag 优先）：
  DGW_DB_PATH      SQLite 运行时存储路径（默认 ./dgw.db）
  DGW_HTTP_ADDR    HTTP 监听地址（默认 :8080）
  DGW_API_KEY      serve-stdio 的凭据
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
	g := gateway.New(st, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	log.Printf("MCP Streamable HTTP 监听 %s（bearer 认证）", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, g.HTTPHandler()); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}

func cmdServeStdio() {
	fs := flag.NewFlagSet("serve-stdio", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 运行时存储路径（缺省取 DGW_DB_PATH）")
	fs.Parse(os.Args[2:])

	apiKey := config.FromEnv().APIKey
	st := openStore(*dbPath)
	defer st.Close()
	g := gateway.New(st, slog.New(slog.NewTextHandler(os.Stderr, nil)))

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
