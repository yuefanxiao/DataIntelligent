// mcp-ping：对已部署网关做真实 MCP 往返探测（HTTP 形态，官方 go-sdk）。
//
// 部署验收工具（issue #27 验收第 1 条「docker compose up 后网关可用，经 MCP
// 查询成功」与排障）：initialize → tools/list → （可选）工具调用——
// execute_sql（--dbname/--sql）或语义工具（--tool/--query，如
// search_entities/get_entity）。只测外部行为（工具协议层），不作业务断言。
//
// 用法：
//
//	go run mcp-ping.go --addr http://127.0.0.1:8080 --key <key> \
//	  --dbname bss --sql "SELECT count(*) FROM orders"
//	go run mcp-ping.go --addr ... --key ... --tool search_entities --query 支付失败
//
// 退出码：0 = 往返成功；1 = 连接/协议/工具错误（结构化错误也算成功往返，
// 按内容打印）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerTransport 给 SDK 客户端的每个请求附加 Authorization header。
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "网关 Streamable HTTP 地址（根路径）")
	key := flag.String("key", "", "网关凭据（key-create 打印的明文）")
	dbname := flag.String("dbname", "", "execute_sql 的 dbname（--sql 时用）")
	sql := flag.String("sql", "", "execute_sql 的 SQL（给则走 execute_sql）")
	tool := flag.String("tool", "", "语义工具名（search_entities/get_entity/...；不给则只做 initialize+tools/list）")
	query := flag.String("query", "", "语义工具参数（search_entities 的 query 等）")
	timeout := flag.Duration("timeout", 30*time.Second, "整体超时")
	flag.Parse()

	if *key == "" {
		fmt.Fprintln(os.Stderr, "mcp-ping: --key 必填（网关凭据）")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-ping", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   *addr,
		HTTPClient: &http.Client{Transport: bearerTransport{token: *key, base: http.DefaultTransport}},
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-ping: 连接失败: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-ping: tools/list 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mcp-ping: 已连接 %s，工具 %d 个：", *addr, len(tools.Tools))
	for i, t := range tools.Tools {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(t.Name)
	}
	fmt.Println()

	switch {
	case *sql != "":
		callTool(ctx, session, "execute_sql", map[string]any{"sql": *sql, "dbname": *dbname})
	case *tool != "":
		args := map[string]any{}
		if *query != "" {
			args["query"] = *query
		}
		callTool(ctx, session, *tool, args)
	}
}

// callTool 调用一个工具并打印结果；结构化错误按内容打印（成功往返）。
func callTool(ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-ping: %s 调用失败: %v\n", name, err)
		os.Exit(1)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if res.IsError {
				fmt.Printf("mcp-ping: %s 返回结构化错误（拒启链路正常）:\n%s\n", name, tc.Text)
			} else {
				fmt.Printf("mcp-ping: %s 成功:\n", name)
				// 结果是大 JSON：缩进打印便于人读。
				var v any
				if err := json.Unmarshal([]byte(tc.Text), &v); err == nil {
					b, _ := json.MarshalIndent(v, "", "  ")
					fmt.Println(string(b))
				} else {
					fmt.Println(tc.Text)
				}
			}
		}
	}
}
