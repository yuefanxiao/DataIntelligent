// Command accept 是 v1 验收重放 harness（build 12；spec §5 测试决策主
// seam、§6.3 负向/边界 5 例、§6.4 判定三件套、§6.5 执行方式）。
//
// 官方 go-sdk 客户端打自己的网关——HTTP（Streamable HTTP + bearer）与
// stdio（拉起 dgw serve-stdio）双形态真实 MCP 往返；用例按序重放；判定
// 三件套逐项断言；输出 markdown 报告（留档供 30min demo 与团队评审）。
//
// 只测外部行为（工具协议层）、不测实现细节：用例定义在 cases.yaml（工具
// 调用 + 期望断言），本程序不含任何业务断言逻辑——build 14 的 13 服务
// 用例矩阵只增 cases.yaml 条目。
//
// 判定三件套（spec §6.4）：
//
//	(a) 数字一致：psql_compare 用例结果与 psql 同库同 SQL 逐项一致；
//	(b) 执行记录可复现：网关 JSONL 完整记录整条调用链（工具/参数/耗时/
//	    状态/行数），且 --replay-from 形态从记录重放复现（同状态同行数）；
//	(c) 零未授权访问：全程被拒原因如实落记录，permission_denied 只出现
//	    在预期负向例上，无 auth_failure。
//
// 形态约束（架构使然，README 有说明）：stdio = 单 key 单进程——多身份
// 用例（无 grants 用户 neg-001a / 进程级并发 conc-002）仅 http；其余
// 用例双形态覆盖。
//
// 用法（run.sh 编排，见 README）：
//
//	accept --mode http --addr http://127.0.0.1:8080 \
//	  --keys dev-alice=KEY,ghost=KEY,p1=KEY --log-dir /path/logs \
//	  --cases cases.yaml --report report.md --psql-prefix docker,compose,...
//	accept --mode stdio --dgw-bin /path/dgw --stdio-user dev-alice ...
//	accept --mode http --addr ... --replay-from /path/logs --cases cases.yaml ...
//
// 退出码：0 = 全过；1 = 任一断言失败或环境错误（run.sh 据此判定验收）。
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/yuefanxiao/DataIntelligent/internal/execrecord"
	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 六工具名（tools/list 形状断言；与 internal/gateway 注册面一致）。
var toolNames = []string{
	"search_entities", "get_entity", "traverse_relations",
	"get_metric_definition", "list_enum_values", "execute_sql",
}

// ── 用例模型（cases.yaml）───────────────────────────────────────────────

type caseFile struct {
	Version int    `yaml:"version"`
	Cases   []Case `yaml:"cases"`
}

type Case struct {
	ID          string         `yaml:"id"`
	Name        string         `yaml:"name"`
	Modes       []string       `yaml:"modes"`
	Tool        string         `yaml:"tool"`
	Args        map[string]any `yaml:"args"`
	Key         string         `yaml:"key"`
	Keys        []string       `yaml:"keys"`
	Concurrency int            `yaml:"concurrency"`
	SQLs        []string       `yaml:"sqls"`
	Steps       []Step         `yaml:"steps"`
	Expect      Expect         `yaml:"expect"`
	ReplaySkip  bool           `yaml:"replay_skip"`
}

type Step struct {
	Tool   string         `yaml:"tool"`
	Args   map[string]any `yaml:"args"`
	Key    string         `yaml:"key"`
	Expect Expect         `yaml:"expect"`
}

type Expect struct {
	Status         string         `yaml:"status"`
	Kind           string         `yaml:"kind"`
	Reason         string         `yaml:"reason"`
	Rows           *int           `yaml:"rows"`
	Truncated      *bool          `yaml:"truncated"`
	Paths          []PathAssert   `yaml:"paths"`
	PsqlCompare    bool           `yaml:"psql_compare"`
	StatusMultiset map[string]int `yaml:"status_multiset"`
	RejectedReason string         `yaml:"rejected_reason"`
	// RejectWithinMS 并发用例的「不排队」断言：被拒调用往返耗时上限（毫秒；
	// 缺省 0 = 不断言）。被拒 = 闸在查询执行前快速失败，不排队等待。
	RejectWithinMS int64 `yaml:"reject_within_ms"`
}

type PathAssert struct {
	Path string `yaml:"path"`
	Eq   any    `yaml:"eq"`
}

// callPlan 是一次具体工具调用（单调用用例 / sqls 逐句 / steps 逐步 /
// 并发用例的每个并发位）。
type callPlan struct {
	Tool   string
	Args   map[string]any
	Key    string // key 标签 = 绑定用户（--keys 映射取明文）
	Expect Expect
	Label  string // 报告用标签：pos-001 / neg-002#3 / neg-005.step1
	Skip   bool   // replay_skip（并发用例：顺序重放无法复现并发拒绝）
}

// ── 观测 ────────────────────────────────────────────────────────────────

// obs 是 harness 侧对一次调用的观测（与 JSONL 记录对照的基准）。
type obs struct {
	Label        string
	Tool         string
	Params       map[string]any
	Key          string
	Status       string // success / rejected（execrecord 状态常量）
	Rows         *int
	Truncated    *bool
	RejectKind   string
	RejectReason string
	Text         string // 结果 text content（JSON 原文，断言/psql 对照用）
	ElapsedMS    int64  // 调用往返耗时（并发「不排队」断言用）
	CallErr      string // 客户端/协议层失败（非工具错误结果）
}

// ── CLI ─────────────────────────────────────────────────────────────────

type config struct {
	mode       string
	addr       string
	keys       map[string]string // 标签(=用户) → 明文 key
	stdioUser  string
	dgwBin     string
	casesPath  string
	logDir     string
	reportPath string
	psqlPrefix []string
	replayFrom string
	timeout    time.Duration
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("accept: ")

	var (
		mode       = flag.String("mode", "", "形态：http / stdio")
		addr       = flag.String("addr", "", "http 形态网关地址（Streamable HTTP 根路径）")
		keysFlag   = flag.String("keys", "", "key 映射 user=明文key,user=明文key（http 必需；stdio 仅用于身份断言）")
		stdioUser  = flag.String("stdio-user", "dev-alice", "stdio 形态进程绑定用户（记录身份断言用）")
		dgwBin     = flag.String("dgw-bin", "", "stdio 形态 dgw 可执行文件路径（拉起 serve-stdio）")
		casesPath  = flag.String("cases", "cases.yaml", "用例文件")
		logDir     = flag.String("log-dir", "", "网关执行记录目录（JSONL 断言；重放形态忽略）")
		reportPath = flag.String("report", "accept-report.md", "报告输出路径")
		psqlPrefix = flag.String("psql-prefix", "", "psql 命令前缀（逗号分隔；空 = 跳过 psql 对照断言）")
		replayFrom = flag.String("replay-from", "", "重放形态：从该目录的 JSONL 读调用链并重放复现")
		timeout    = flag.Duration("timeout", 90*time.Second, "单次工具调用的超时")
	)
	flag.Parse()

	cfg := config{
		mode:       *mode,
		addr:       *addr,
		keys:       parseKeys(*keysFlag),
		stdioUser:  *stdioUser,
		dgwBin:     *dgwBin,
		casesPath:  *casesPath,
		logDir:     *logDir,
		reportPath: *reportPath,
		psqlPrefix: splitCSVArgs(*psqlPrefix),
		replayFrom: *replayFrom,
		timeout:    *timeout,
	}
	if err := run(cfg); err != nil {
		log.Printf("FAIL: %v", err)
		os.Exit(1)
	}
}

// parseKeys 解析 --keys 的 user=明文key 映射（逗号分隔；格式错误即配置错误）。
func parseKeys(s string) map[string]string {
	m := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		u, k, ok := strings.Cut(pair, "=")
		if !ok || u == "" || k == "" {
			log.Fatalf("--keys 格式错误：%q（user=明文key）", pair)
		}
		m[u] = k
	}
	return m
}

// splitCSVArgs 拆逗号分隔的 psql 命令前缀（路径含逗号不在支持面；验收环境
// 路径固定）。
func splitCSVArgs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// ── 主流程 ──────────────────────────────────────────────────────────────

func run(cfg config) error {
	cf, err := loadCases(cfg.casesPath)
	if err != nil {
		return fmt.Errorf("加载用例 %s: %w", cfg.casesPath, err)
	}

	report := newReport(cfg)
	defer func() {
		if werr := report.write(cfg.reportPath); werr != nil {
			log.Printf("写报告 %s 失败: %v", cfg.reportPath, werr)
		}
	}()

	if cfg.replayFrom != "" {
		return runReplay(cfg, cf, report)
	}

	// 运行窗口起点：JSONL 对照只取本窗口记录（日志目录可能复用）。
	runStart := time.Now()

	// 用例按序（文件顺序）；形态过滤。
	plans := buildPlans(cf.Cases, cfg.mode)
	if len(plans) == 0 {
		return fmt.Errorf("形态 %s 无适用用例（cases.yaml 的 modes 字段）", cfg.mode)
	}

	cli, err := newClient(cfg)
	if err != nil {
		return err
	}
	defer cli.close()

	// 工具面形状断言（六工具全在）。
	if err := cli.checkTools(context.Background()); err != nil {
		return err
	}

	// 用例按序重放。
	obsList, err := executePlans(cfg, cli, plans)
	if err != nil {
		return err
	}

	// 逐调用断言（含并发用例的总体多集断言）。
	results := assertPlans(plans, obsList)
	report.addResults(results)

	// 三件套 (a)：psql 对照（psql_compare 断言）。
	if len(cfg.psqlPrefix) > 0 {
		report.addPsql(psqlCompareAll(cfg, plans, obsList))
	} else {
		report.note("psql 对照未执行（未给 --psql-prefix）")
	}

	// 三件套 (b)/(c)：JSONL 完整性 + 忠实度 + 零未授权。范围 = 本次运行
	// 窗口（since=runStart）：目录可能残留历史记录（复用网关/日志目录重跑），
	// 只对照本窗口内的调用链。
	allRecs, err := readJSONLAll(cfg.logDir, runStart)
	if err != nil {
		return err
	}
	report.addJSONL(checkJSONL(plans, obsList, allRecs))
	// 把本窗口的调用链快照成 chain 文件（重放复现的输入；位置 = 报告旁，
	// 命名 <report>.chain.jsonl）。
	if err := writeChain(cfg.reportPath, toolCallRecords(allRecs)); err != nil {
		return err
	}
	report.note(fmt.Sprintf("调用链快照：%s.chain.jsonl（重放输入）", cfg.reportPath))

	ok := report.conclude()
	if !ok {
		return fmt.Errorf("存在失败断言（详见报告 %s）", cfg.reportPath)
	}
	return nil
}

// ── 用例 → 调用计划 ────────────────────────────────────────────────────

// buildPlans 把用例展开为有序调用计划；mode 过滤（stdio 单 key 单进程，
// 多身份用例不适用）。
func buildPlans(cases []Case, mode string) []callPlan {
	var plans []callPlan
	for i := range cases {
		c := &cases[i]
		if !slices.Contains(c.Modes, mode) {
			continue
		}
		switch {
		case c.Concurrency > 1:
			keys := c.Keys
			if len(keys) == 0 {
				keys = []string{c.keyOrDev()}
			}
			for _, k := range keys {
				for n := 0; n < c.Concurrency; n++ {
					plans = append(plans, callPlan{
						Tool: c.Tool, Args: c.Args, Key: k, Expect: c.Expect,
						Label: c.ID, Skip: c.ReplaySkip,
					})
				}
			}
		case len(c.SQLs) > 0:
			for n, sql := range c.SQLs {
				args := cloneArgs(c.Args)
				args["sql"] = sql
				plans = append(plans, callPlan{
					Tool: c.Tool, Args: args, Key: c.keyOrDev(), Expect: c.Expect,
					Label: fmt.Sprintf("%s#%d", c.ID, n+1),
				})
			}
		case len(c.Steps) > 0:
			for n := range c.Steps {
				s := &c.Steps[n]
				key := s.Key
				if key == "" {
					key = c.keyOrDev()
				}
				plans = append(plans, callPlan{
					Tool: s.Tool, Args: s.Args, Key: key, Expect: s.Expect,
					Label: fmt.Sprintf("%s.step%d", c.ID, n+1),
				})
			}
		default:
			plans = append(plans, callPlan{
				Tool: c.Tool, Args: c.Args, Key: c.keyOrDev(), Expect: c.Expect,
				Label: c.ID,
			})
		}
	}
	return plans
}

// keyOrDev 返回用例默认用户（未指定 key 时 = dev-alice，主用户）。
func (c *Case) keyOrDev() string {
	if c.Key != "" {
		return c.Key
	}
	return "dev-alice"
}

// cloneArgs 拷贝参数 map（sqls 逐句改写 sql 时不动用例原 args）。
func cloneArgs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

// collectGroup 收集从 i 开始的相邻同标签计划（并发组；buildPlans 保证
// 组内相邻）：返回 (组, 组后下一索引)。三种执行/断言路径共用同一分组
// 规则——观测与记录的分组边界永不漂移。
func collectGroup(plans []callPlan, i int) ([]callPlan, int) {
	p := &plans[i]
	group := []callPlan{}
	for i < len(plans) && plans[i].Label == p.Label {
		group = append(group, plans[i])
		i++
	}
	return group, i
}

// ── 客户端 ──────────────────────────────────────────────────────────────

// client 封装两种形态的连接：http = 每用户一个会话（bearer）；stdio = 拉起
// dgw serve-stdio 进程单会话（env 传配置与 DGW_API_KEY）。
type client struct {
	cfg      config
	httpAddr string
	sessions map[string]*mcp.ClientSession // 标签 → 会话（http 复用）
	proc     *exec.Cmd
	procErr  *strings.Builder // stdio 进程 stderr（失败时报告）
	mu       sync.Mutex
}

func newClient(cfg config) (*client, error) {
	c := &client{cfg: cfg, sessions: map[string]*mcp.ClientSession{}}
	switch cfg.mode {
	case "http":
		if cfg.addr == "" {
			return nil, fmt.Errorf("http 形态需要 --addr")
		}
		if len(cfg.keys) == 0 {
			return nil, fmt.Errorf("http 形态需要 --keys（user=明文key）")
		}
		c.httpAddr = cfg.addr
	case "stdio":
		if cfg.dgwBin == "" {
			return nil, fmt.Errorf("stdio 形态需要 --dgw-bin")
		}
	default:
		return nil, fmt.Errorf("未知 --mode %q（http / stdio）", cfg.mode)
	}
	return c, nil
}

// session 返回指定标签（= 用户）的会话：http 每用户一个（复用）；
// stdio 恒为进程单会话。
func (c *client) session(ctx context.Context, label string) (*mcp.ClientSession, error) {
	if c.cfg.mode == "stdio" {
		return c.stdioSession(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[label]; ok {
		return s, nil
	}
	key, ok := c.cfg.keys[label]
	if !ok {
		return nil, fmt.Errorf("--keys 缺少用户 %q", label)
	}
	s, err := c.connectHTTP(ctx, key)
	if err != nil {
		return nil, err
	}
	c.sessions[label] = s
	return s, nil
}

// freshSessions 为并发用例创建独立会话（每调用一个；key 按调用各自绑定）。
// 每 key 并发闸是服务端语义（token 里的 key 身份），会话数不影响闸；
// 独立会话保证调用真并发，不依赖 SDK 单会话并发能力。
func (c *client) freshSessions(ctx context.Context, keys []string) ([]*mcp.ClientSession, error) {
	if c.cfg.mode == "stdio" {
		return nil, fmt.Errorf("stdio 形态不支持并发用例（单连接）")
	}
	out := make([]*mcp.ClientSession, 0, len(keys))
	for _, label := range keys {
		key, ok := c.cfg.keys[label]
		if !ok {
			c.closeSessions(out)
			return nil, fmt.Errorf("--keys 缺少用户 %q", label)
		}
		s, err := c.connectHTTP(ctx, key)
		if err != nil {
			c.closeSessions(out)
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *client) connectHTTP(ctx context.Context, key string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "dgw-accept", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.httpAddr,
		HTTPClient: &http.Client{Transport: bearerTransport{token: key}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %v", c.httpAddr, err)
	}
	return session, nil
}

func (c *client) stdioSession(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[""]; ok {
		return s, nil
	}
	cmd := exec.Command(c.cfg.dgwBin, "serve-stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.procErr = &strings.Builder{}
	cmd.Stderr = c.procErr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("拉起 %s serve-stdio 失败: %v", c.cfg.dgwBin, err)
	}
	c.proc = cmd
	// 会话建立失败也要收掉进程（defer 在 close 里统一兜底）。
	s, err := mcp.NewClient(&mcp.Implementation{Name: "dgw-accept", Version: "1"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("stdio 会话建立失败: %v（进程 stderr: %s）", err, c.procErr.String())
	}
	c.sessions[""] = s
	return s, nil
}

// close 关闭全部会话与 stdio 进程（defer 兜底；幂等）。
func (c *client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, s := range c.sessions {
		_ = s.Close()
		delete(c.sessions, k)
	}
	if c.proc != nil {
		_ = c.proc.Process.Kill()
		_ = c.proc.Wait()
	}
}

// closeSessions 关闭一批临时会话（并发用例的独立会话）。
func (c *client) closeSessions(ss []*mcp.ClientSession) {
	for _, s := range ss {
		_ = s.Close()
	}
}

// bearerTransport 给 SDK 客户端的每个请求附加 Authorization header。
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	if t.base == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}

// checkTools 断言工具面形状（六工具全在，spec §4.3 工具面冻结）。
func (c *client) checkTools(ctx context.Context) error {
	s, err := c.session(ctx, c.cfg.stdioUser)
	if err != nil {
		return err
	}
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("tools/list 失败: %v", err)
	}
	got := map[string]bool{}
	for _, t := range res.Tools {
		got[t.Name] = true
	}
	var missing []string
	for _, n := range toolNames {
		if !got[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("工具面缺失 %v（tools/list 返回 %d 个）", missing, len(res.Tools))
	}
	return nil
}

// ── 执行 ────────────────────────────────────────────────────────────────

// executePlans 按序执行调用计划；并发用例（相邻同 Label 且 Skip）用独立
// 会话同时发出，验证「不排队、快速失败」语义。
func executePlans(cfg config, cli *client, plans []callPlan) ([]obs, error) {
	ctx := context.Background()
	var out []obs
	for i := 0; i < len(plans); {
		p := &plans[i]
		if p.Skip {
			// 并发组：收集相邻同标签计划，独立会话同时发出。
			group, next := collectGroup(plans, i)
			i = next
			obsGroup, err := fireConcurrent(ctx, cfg, cli, group)
			if err != nil {
				return nil, err
			}
			out = append(out, obsGroup...)
			continue
		}
		o, err := callOnce(ctx, cfg, cli, p)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
		i++
	}
	return out, nil
}

// callOnce 单次工具调用：独立 ctx 超时；错误结果（IsError）与客户端错误
// 都归一进 obs（断言侧区分）。
func callOnce(ctx context.Context, cfg config, cli *client, p *callPlan) (obs, error) {
	callCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	s, err := cli.session(callCtx, p.Key)
	if err != nil {
		return obs{}, err
	}
	start := time.Now()
	res, err := s.CallTool(callCtx, &mcp.CallToolParams{Name: p.Tool, Arguments: p.Args})
	return normalizeObs(p, res, err, time.Since(start).Milliseconds()), nil
}

// fireConcurrent 并发组：每个调用一个独立会话（按各自 key 绑定），同时
// 发出；结果按计划顺序收集（组内乱序无妨——断言是多集）。
func fireConcurrent(ctx context.Context, cfg config, cli *client, group []callPlan) ([]obs, error) {
	keys := make([]string, len(group))
	for i := range group {
		keys[i] = group[i].Key
	}
	sessions, err := cli.freshSessions(ctx, keys)
	if err != nil {
		return nil, err
	}
	defer cli.closeSessions(sessions)

	results := make([]obs, len(group))
	var wg sync.WaitGroup
	for i := range group {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
			defer cancel()
			start := time.Now()
			res, err := sessions[i].CallTool(callCtx, &mcp.CallToolParams{Name: group[i].Tool, Arguments: group[i].Args})
			results[i] = normalizeObs(&group[i], res, err, time.Since(start).Milliseconds())
		}(i)
	}
	wg.Wait()
	return results, nil
}

// normalizeObs 把一次 CallTool 的结果归一为观测（顺序/并发两条执行路径
// 共用同一归一化，观测与记录的对照基准永不漂移）。
func normalizeObs(p *callPlan, res *mcp.CallToolResult, callErr error, elapsedMS int64) obs {
	o := obs{Label: p.Label, Tool: p.Tool, Params: p.Args, Key: p.Key, ElapsedMS: elapsedMS}
	if callErr != nil {
		o.CallErr = callErr.Error()
		return o
	}
	o.Text = textContent(res)
	if res.IsError {
		o.Status = execrecord.StatusRejected
		if e, ok := parseGwerr(o.Text); ok {
			o.RejectKind = string(e.Kind)
			if r, _ := e.Details["reason"].(string); r != "" {
				o.RejectReason = r
			}
		}
		return o
	}
	o.Status = execrecord.StatusSuccess
	if sr, ok := parseSQLResult(o.Text); ok {
		o.Rows = &sr.Meta.RowCount
		o.Truncated = &sr.Meta.Truncated
	}
	return o
}

// ── 结果解析（外部行为侧）──────────────────────────────────────────────

// textContent 取结果的第一段 text content（网关全部结果 = 单段 JSON 文本）。
func textContent(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// parseGwerr 解析结构化错误（gwerr JSON；错误结果的内容）。
func parseGwerr(s string) (*gwerr.Error, bool) {
	var e gwerr.Error
	if json.Unmarshal([]byte(s), &e) == nil && e.Kind != "" {
		return &e, true
	}
	return nil, false
}

// sqlResult 是 execute_sql 结果的结构化形状（与网关 json 契约一致；
// 数值精度用 UseNumber 保留）。
type sqlResult struct {
	Columns []sqlColumn `json:"columns"`
	Rows    [][]any     `json:"rows"`
	Meta    struct {
		RowCount  int    `json:"row_count"`
		Truncated bool   `json:"truncated"`
		DBName    string `json:"dbname"`
	} `json:"meta"`
}

type sqlColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// parseSQLResult 解析 execute_sql 结果（UseNumber 保数值精度）；非
// SQL 结果（缺 columns）返回 false——语义工具结果不会误判成 0 行成功。
func parseSQLResult(s string) (*sqlResult, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var r sqlResult
	if dec.Decode(&r) != nil {
		return nil, false
	}
	// 只有 execute_sql 结果才有列清单（语义工具结果无 columns 字段——
	// 缺字段时零值会误判成「0 行成功」，破坏记录对照）。
	if r.Columns == nil {
		return nil, false
	}
	return &r, true
}

// ── 断言 ────────────────────────────────────────────────────────────────

type caseResult struct {
	Label  string
	Name   string
	Pass   bool
	Detail string
}

// assertPlans 逐调用断言 + 并发组多集断言。
func assertPlans(plans []callPlan, obsList []obs) []caseResult {
	var results []caseResult
	for i := 0; i < len(plans); {
		p := &plans[i]
		if p.Skip {
			// 并发组：收集全部观测，多集断言。
			group, next := collectGroup(plans, i)
			i = next
			o := obsList[:len(group)]
			obsList = obsList[len(group):]
			results = append(results, assertConcurrentGroup(group[0].Label, group, o))
			continue
		}
		o := obsList[0]
		obsList = obsList[1:]
		results = append(results, assertCall(p, o))
		i++
	}
	return results
}

// assertCall 单调用断言（期望字段逐项比对；失败详情记首个不符项）。
func assertCall(p *callPlan, o obs) caseResult {
	r := caseResult{Label: p.Label, Name: p.Label, Pass: true}
	var fails []string
	// 结果解析失败优先（协议层失败也算失败——外部行为没测到）。
	if o.CallErr != "" {
		return caseResult{Label: p.Label, Pass: false, Detail: "调用失败: " + o.CallErr}
	}
	if e := p.Expect; e.Status != "" && o.Status != e.Status {
		fails = append(fails, fmt.Sprintf("status=%s（期望 %s）", o.Status, e.Status))
	}
	if e := p.Expect; e.Kind != "" && o.RejectKind != e.Kind {
		fails = append(fails, fmt.Sprintf("kind=%s（期望 %s）", o.RejectKind, e.Kind))
	}
	if e := p.Expect; e.Reason != "" && o.RejectReason != e.Reason {
		fails = append(fails, fmt.Sprintf("reason=%s（期望 %s）", o.RejectReason, e.Reason))
	}
	if e := p.Expect; e.Rows != nil && (o.Rows == nil || *o.Rows != *e.Rows) {
		got := "<nil>"
		if o.Rows != nil {
			got = strconv.Itoa(*o.Rows)
		}
		fails = append(fails, fmt.Sprintf("rows=%s（期望 %d）", got, *e.Rows))
	}
	if e := p.Expect; e.Truncated != nil && (o.Truncated == nil || *o.Truncated != *e.Truncated) {
		got := "<nil>"
		if o.Truncated != nil {
			got = strconv.FormatBool(*o.Truncated)
		}
		fails = append(fails, fmt.Sprintf("truncated=%s（期望 %v）", got, *e.Truncated))
	}
	if len(p.Expect.Paths) > 0 {
		doc := parseJSONDoc(o.Text)
		for _, pa := range p.Expect.Paths {
			got, ok := resolvePath(doc, pa.Path)
			if !ok {
				fails = append(fails, fmt.Sprintf("路径 %s 不存在", pa.Path))
				continue
			}
			if !eqValue(got, pa.Eq) {
				fails = append(fails, fmt.Sprintf("%s=%v（期望 %v）", pa.Path, got, pa.Eq))
			}
		}
	}
	if len(fails) > 0 {
		r.Pass = false
		r.Detail = strings.Join(fails, "; ")
	} else {
		r.Detail = detailOf(o)
	}
	return r
}

// assertConcurrentGroup 并发组多集断言：{success: n, rejected: m} + 被拒
// 调用统一 reason + 被拒耗时上限（不排队、快速失败；reject_within_ms
// 缺省不断言）。
func assertConcurrentGroup(label string, plans []callPlan, obs []obs) caseResult {
	r := caseResult{Label: label, Pass: true}
	counts := map[string]int{}
	var reasons []string
	e := plans[0].Expect
	for _, o := range obs {
		counts[o.Status]++
		if o.CallErr != "" {
			r.Pass = false
			r.Detail = "调用失败: " + o.CallErr
		}
		if o.Status == execrecord.StatusRejected {
			reasons = append(reasons, o.RejectReason)
			if o.RejectKind != string(gwerr.KindRateLimited) {
				r.Pass = false
				r.Detail = fmt.Sprintf("被拒 kind=%s（期望 %s）", o.RejectKind, gwerr.KindRateLimited)
			}
			// 不排队：被拒调用在闸处快速失败（远快于慢查询持闸时长），
			// 排队实现会让被拒调用等到窗口期结束。
			if e.RejectWithinMS > 0 && o.ElapsedMS > e.RejectWithinMS {
				r.Pass = false
				r.Detail = fmt.Sprintf("被拒调用耗时 %dms（上限 %dms，疑似排队）", o.ElapsedMS, e.RejectWithinMS)
			}
		}
	}
	if e.StatusMultiset != nil {
		for status, want := range e.StatusMultiset {
			if counts[status] != want {
				r.Pass = false
				r.Detail = fmt.Sprintf("%s=%d（期望 %d）", status, counts[status], want)
			}
		}
	}
	if e.RejectedReason != "" {
		for _, reason := range reasons {
			if reason != e.RejectedReason {
				r.Pass = false
				r.Detail = fmt.Sprintf("被拒 reason=%s（期望 %s）", reason, e.RejectedReason)
			}
		}
	}
	if r.Pass {
		r.Detail = fmt.Sprintf("%d 成功 + %d 拒绝（%s）", counts[execrecord.StatusSuccess], counts[execrecord.StatusRejected], e.RejectedReason)
	}
	return r
}

// detailOf 组装成功观测的展示详情。
func detailOf(o obs) string {
	if o.Rows != nil && o.Truncated != nil {
		return fmt.Sprintf("rows=%d truncated=%v", *o.Rows, *o.Truncated)
	}
	return "ok"
}

// parseJSONDoc 解析结果 JSON（UseNumber 保留精度）。
func parseJSONDoc(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// resolvePath 解析点分路径（total / meta.row_count；数组下标 [n]，如
// hits[0].fqn）。
func resolvePath(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		idx := -1
		if i := strings.Index(seg, "["); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSuffix(seg[i+1:], "]"))
			if err != nil {
				return nil, false
			}
			idx = n
			seg = seg[:i]
		}
		switch v := cur.(type) {
		case map[string]any:
			var ok bool
			cur, ok = v[seg]
			if !ok {
				return nil, false
			}
		case []any:
			if idx < 0 {
				return nil, false
			}
			if idx >= len(v) {
				return nil, false
			}
			cur = v[idx]
		default:
			return nil, false
		}
		if idx >= 0 {
			// 段本身是数组名（如 hits）→ 取下标元素。
			switch v := cur.(type) {
			case []any:
				if idx >= len(v) {
					return nil, false
				}
				cur = v[idx]
			case nil:
				return nil, false
			}
		}
	}
	return cur, true
}

// eqValue 期望值比较（YAML 数字 int/float vs JSON json.Number）。
func eqValue(got any, want any) bool {
	switch w := want.(type) {
	case int:
		if g, ok := got.(json.Number); ok {
			return g.String() == strconv.Itoa(w)
		}
	case float64:
		if g, ok := got.(json.Number); ok {
			f, err := g.Float64()
			return err == nil && f == w
		}
	case string:
		g, ok := got.(string)
		return ok && g == w
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case nil:
		return got == nil
	}
	return false
}

// ── 三件套 (a)：psql 对照 ───────────────────────────────────────────────

type psqlResult struct {
	Label  string
	Pass   bool
	Detail string
}

// psqlCompareAll 对 psql_compare 断言的观测逐项执行 psql 同库同 SQL 对照。
// 声明集从计划回查（用例文件 → 计划，harness 不内嵌业务断言；并发/被拒
// 调用没有该断言）。
func psqlCompareAll(cfg config, plans []callPlan, obsList []obs) []psqlResult {
	// 声明 psql_compare 的调用标签集。
	want := map[string]bool{}
	for _, p := range plans {
		if p.Expect.PsqlCompare {
			want[p.Label] = true
		}
	}
	var out []psqlResult
	for i := range obsList {
		o := &obsList[i]
		if o.Tool != "execute_sql" || o.Status != execrecord.StatusSuccess || !want[o.Label] {
			continue
		}
		out = append(out, psqlCompareOne(cfg, o))
	}
	return out
}

func psqlCompareOne(cfg config, o *obs) psqlResult {
	r := psqlResult{Label: o.Label}
	sql, _ := o.Params["sql"].(string)
	dbname, _ := o.Params["dbname"].(string)
	if dbname == "" {
		r.Detail = "用例无 dbname，无法选对照库"
		return r
	}
	res, ok := parseSQLResult(o.Text)
	if !ok {
		r.Detail = "网关结果 JSON 解析失败"
		return r
	}
	// 截断用例（truncated=true）：网关返回前 row_count 行——psql 侧用
	// 同样的有界查询（LIMIT row_count）对照「返回的行与 psql 一致」；
	// 未截断用例直接同 SQL 全量对照。
	psqlSQL := sql
	if res.Meta.Truncated {
		psqlSQL = fmt.Sprintf("SELECT * FROM (%s) _q LIMIT %d", strings.TrimSuffix(strings.TrimSpace(sql), ";"), res.Meta.RowCount)
	}
	psqlRows, err := runPSQL(cfg, dbname, psqlSQL)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	if len(psqlRows) != len(res.Rows) {
		r.Detail = fmt.Sprintf("行数不一致：psql=%d 网关=%d", len(psqlRows), len(res.Rows))
		return r
	}
	for i := range psqlRows {
		psqlRow, gwRow := psqlRows[i], res.Rows[i]
		if len(psqlRow) != len(gwRow) {
			r.Detail = fmt.Sprintf("第 %d 行列数不一致：psql=%d 网关=%d", i+1, len(psqlRow), len(gwRow))
			return r
		}
		for j := range psqlRow {
			detail := compareCell(res.Columns[j].Type, psqlRow[j], gwRow[j])
			if detail != "" {
				r.Detail = fmt.Sprintf("第 %d 行 %s 列不符（%s）: psql=%q 网关=%v", i+1, res.Columns[j].Name, res.Columns[j].Type, psqlRow[j], gwRow[j])
				return r
			}
		}
	}
	r.Pass = true
	r.Detail = fmt.Sprintf("%d 行逐项一致", len(psqlRows))
	return r
}

// runPSQL 在对照库执行同 SQL（psql 走同一共享只读角色，同一从库数据）。
func runPSQL(cfg config, dbname, sql string) ([][]string, error) {
	args := append(append([]string{}, cfg.psqlPrefix...), "-d", dbname, "-c", sql)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("psql 执行失败: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	cr := csv.NewReader(strings.NewReader(stdout.String()))
	cr.FieldsPerRecord = -1 // 行数列数不强制（列数由网关结果侧校验）
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("psql CSV 解析失败: %v\n%s", err, stdout.String())
	}
	// 空结果（0 行查询）：psql 输出空串 → ReadAll 返回空切片，与「0 行」
	// 一致即可（列数对照由网关侧 0 行短路）。
	return rows, nil
}

// compareCell 按列类型做单元格归一化对照；返回 "" = 一致，否则为差异描述。
// 类型覆盖 = 验收用例实际出现的类型（int/numeric/text/timestamptz 等）；
// 未覆盖类型走严格字符串对照（宁可报失败也不静默放行——矩阵作者可见）。
func compareCell(colType, psql string, gw any) string {
	psql = strings.TrimSpace(psql)
	switch {
	case gw == nil:
		if psql != "" {
			return "null 不一致"
		}
		return ""
	case psql == "":
		return "psql 为空但网关非空"
	}
	switch colType {
	case "int2", "int4", "int8", "oid", "bigint", "smallint", "integer":
		a, err1 := strconv.ParseInt(psql, 10, 64)
		b, err2 := jsonInt64(gw)
		if err1 != nil || err2 != nil || a != b {
			return "整数不一致"
		}
		return ""
	case "numeric", "money":
		a, ok1 := new(big.Rat).SetString(psql)
		b, ok2 := ratOf(gw)
		if !ok1 || !ok2 || a.Cmp(b) != 0 {
			return "numeric 不一致"
		}
		return ""
	case "float4", "float8":
		a, err1 := strconv.ParseFloat(psql, 64)
		b, ok2 := gw.(json.Number)
		if err1 != nil {
			return "float 解析失败"
		}
		f, err2 := b.Float64()
		if !ok2 || err2 != nil || a != f {
			return "float 不一致"
		}
		return ""
	case "bool":
		if !(psql == "t" && gw == true || psql == "f" && gw == false) {
			return "bool 不一致"
		}
		return ""
	case "text", "varchar", "char", "bpchar", "name", "citext":
		if gs, ok := gw.(string); !ok || gs != psql {
			return "文本不一致"
		}
		return ""
	case "timestamptz":
		gs, ok := gw.(string)
		if !ok {
			return "timestamptz 网关值非文本"
		}
		a, err1 := parsePSQLTime(psql)
		b, err2 := time.Parse(time.RFC3339Nano, gs)
		if err1 != nil || err2 != nil || !a.Equal(b) {
			return "timestamptz 不一致"
		}
		return ""
	case "timestamp", "date", "time", "timetz", "interval":
		gs, ok := gw.(string)
		if !ok || gs != psql {
			return colType + " 不一致"
		}
		return ""
	case "uuid":
		gs, ok := gw.(string)
		if !ok || !strings.EqualFold(gs, psql) {
			return "uuid 不一致"
		}
		return ""
	case "bytea":
		gs, ok := gw.(string)
		if !ok || !strings.EqualFold(strings.TrimPrefix(gs, `\x`), strings.TrimPrefix(psql, `\x`)) {
			return "bytea 不一致"
		}
		return ""
	case "json", "jsonb":
		gs, ok := gw.(string)
		if !ok || !jsonSemanticEqual(gs, psql) {
			return "json 不一致"
		}
		return ""
	default:
		// 未覆盖类型：严格文本对照（并注明类型——矩阵作者需显式归一化）。
		gs, ok := gw.(string)
		if !ok {
			return "未知类型 " + colType + " 且网关值非文本"
		}
		if gs != psql {
			return "未覆盖类型 " + colType + " 文本不一致"
		}
		return ""
	}
}

// jsonInt64 把 JSON 数字值转 int64（整数列对照用）。
func jsonInt64(v any) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("非数字")
	}
	return n.Int64()
}

// ratOf 把 JSON 数字/文本转 big.Rat（numeric 精确对照用）。
func ratOf(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case json.Number:
		return new(big.Rat).SetString(x.String())
	case string:
		return new(big.Rat).SetString(x)
	}
	return nil, false
}

// jsonSemanticEqual 语义级 JSON 比较（键序/空白不敏感——PG 侧 jsonb 显示
// 顺序与 pgx 解码文本可能不同，值等价即一致）。
func jsonSemanticEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return jsonEqual(va, vb)
}

// jsonEqual 递归比较解码后的 JSON 值（map/数组逐项，标量序列化比较）。
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsonEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		return string(ja) == string(jb)
	}
}

// parsePSQLTime 解析 psql 的 timestamptz 文本（可变小数位数 + 数字时区
// 偏移，无冒号）。
func parsePSQLTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
	}
	var lastErr error
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// ── 三件套 (b)/(c)：JSONL ──────────────────────────────────────────────

// jsonlRecord 是执行记录 JSONL 一行的读取侧形状：kind 分派 + 复用写入侧
// ToolCall 字段契约（internal/execrecord）——读取与写入共用同一类型，
// 字段清单永不漂移（含 plan_id/rows/truncated/reject 全契约）。
type jsonlRecord struct {
	Kind string `json:"kind"`
	execrecord.ToolCall
}

// readJSONLAll 读目录全部 raw-*.jsonl 的全部记录（tool_call / auth_failure /
// key_lifecycle），按时间排序——(c) 零未授权扫描需要 auth_failure 也可见。
// since 非零时只取该时刻之后的记录（运行窗口：日志目录可能残留历史记录）。
func readJSONLAll(dir string, since time.Time) ([]jsonlRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读执行记录目录 %s: %w", dir, err)
	}
	var all []jsonlRecord
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "raw-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var rec jsonlRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue // 坏行跳过（与聚合器同一策略：尽力而为）
			}
			if !since.IsZero() && rec.TS.Before(since) {
				continue
			}
			all = append(all, rec)
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TS.Before(all[j].TS) })
	return all, nil
}

// writeChain 把调用链快照写成 <report>.chain.jsonl（重放复现的输入：只含
// 本次运行窗口的 tool_call 记录，与原始 JSONL 同形状）。
func writeChain(reportPath string, chain []jsonlRecord) error {
	path := reportPath + ".chain.jsonl"
	buf := &strings.Builder{}
	for i := range chain {
		b, err := json.Marshal(chain[i])
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// readReplayChain 读重放输入：chain 文件（<report>.chain.jsonl，一行一条
// 记录）或执行记录目录（raw-*.jsonl 全量）。
func readReplayChain(path string) ([]jsonlRecord, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("重放输入 %s: %w", path, err)
	}
	if fi.IsDir() {
		all, err := readJSONLAll(path, time.Time{})
		if err != nil {
			return nil, err
		}
		return toolCallRecords(all), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var chain []jsonlRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec jsonlRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		chain = append(chain, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return chain, nil
}

// toolCallRecords 过滤调用链（重放与忠实度对照只关心工具调用）。
func toolCallRecords(all []jsonlRecord) []jsonlRecord {
	var chain []jsonlRecord
	for _, r := range all {
		if r.Kind == execrecord.KindToolCall {
			chain = append(chain, r)
		}
	}
	return chain
}

// jsonlCheck 汇总三件套 (b)/(c) 的判定：
//
//	(b) 完整性：记录数 == 调用数；每条记录字段齐全（工具/参数/耗时/状态；
//	    execute_sql 成功带行数/截断；被拒带原因）；
//	(b) 忠实度：记录链与 harness 观测逐调用对照（工具/参数/身份/状态/行数）；
//	(c) 零未授权：无 auth_failure；被拒记录原因如实；permission_denied 只
//	    出现在预期负向例上（数量精确），无未预期拒绝。
type jsonlCheck struct {
	Pass         bool
	Detail       string
	Total        int
	ByStatus     map[string]int
	PermDenied   int
	PermExpected int
	ReplayReady  bool
}

func checkJSONL(plans []callPlan, obsList []obs, all []jsonlRecord) jsonlCheck {
	c := jsonlCheck{Pass: true, ByStatus: map[string]int{}}
	var fails []string

	// (c) 预扫：auth_failure 不允许出现（认证全程成功）。
	for _, rec := range all {
		if rec.Kind == execrecord.KindAuthFailure {
			fails = append(fails, "出现 auth_failure 记录（预期零认证失败）")
		}
	}
	chain := toolCallRecords(all)

	// 分组匹配：顺序执行组逐一对应；并发组（Skip）多集对照。
	recs := chain
	expPerm := 0
	for i := 0; i < len(plans); {
		p := &plans[i]
		exp := expectedPermDenied(p)
		if p.Skip {
			group, next := collectGroup(plans, i)
			i = next
			o := obsList[:len(group)]
			obsList = obsList[len(group):]
			need := len(group)
			if len(recs) < need {
				fails = append(fails, fmt.Sprintf("%s：记录不足（要 %d 条，剩 %d）", p.Label, need, len(recs)))
				recs = nil
				continue
			}
			grec := recs[:need]
			recs = recs[need:]
			if !matchGroup(grec, o) {
				fails = append(fails, fmt.Sprintf("%s：记录与观测不一致", p.Label))
			}
			expPerm += exp * need
			continue
		}
		o := obsList[0]
		obsList = obsList[1:]
		if len(recs) == 0 {
			fails = append(fails, fmt.Sprintf("%s：JSONL 缺记录（观测有，记录无）", p.Label))
			i++
			continue
		}
		rec := recs[0]
		recs = recs[1:]
		if !matchRecord(rec, o) {
			fails = append(fails, fmt.Sprintf("%s：记录与观测不一致（record status=%s vs obs status=%s）", p.Label, rec.Status, o.Status))
		}
		expPerm += exp
		i++
	}
	if len(recs) > 0 {
		fails = append(fails, fmt.Sprintf("JSONL 多出 %d 条未对照记录", len(recs)))
	}
	if len(obsList) > 0 {
		fails = append(fails, fmt.Sprintf("观测侧多出 %d 条未对照调用", len(obsList)))
	}

	// (c) 被拒原因如实 + permission_denied 精确计数。
	permDenied := 0
	for _, rec := range chain {
		c.ByStatus[rec.Status]++
		switch rec.Status {
		case execrecord.StatusSuccess:
			if rec.Reject != nil {
				fails = append(fails, "success 记录带 reject（记录矛盾）")
			}
			if rec.Tool == "execute_sql" && (rec.Rows == nil || rec.Truncated == nil) {
				fails = append(fails, "execute_sql success 记录缺 rows/truncated")
			}
		case execrecord.StatusRejected:
			if rec.Reject == nil || rec.Reject.Kind == "" {
				fails = append(fails, "被拒记录缺原因（reject 为空）")
			}
			if rec.Reject != nil && rec.Reject.Kind == gwerr.KindPermission {
				permDenied++
			}
		default:
			fails = append(fails, fmt.Sprintf("未知状态 %q", rec.Status))
		}
		if rec.Tool == "" || rec.Params == nil || rec.StagesMS == nil || rec.Status == "" {
			fails = append(fails, "记录字段不完整（tool/params/stages_ms/status 缺失）")
		}
	}
	c.Total = len(chain)
	c.PermDenied = permDenied
	c.PermExpected = expPerm
	if permDenied != expPerm {
		fails = append(fails, fmt.Sprintf("permission_denied=%d（预期 %d，零未授权访问）", permDenied, expPerm))
	}
	c.ReplayReady = len(fails) == 0 && len(chain) > 0
	if len(fails) > 0 {
		c.Pass = false
		c.Detail = strings.Join(fails, "; ")
	} else {
		c.Detail = fmt.Sprintf("完整 %d 条，状态分布 %v，permission_denied=%d（预期）", len(chain), c.ByStatus, permDenied)
	}
	return c
}

// expectedPermDenied 该调用的预期 permission_denied 次数（0/1；(c) 的
// 精确计数依据——零未授权访问 = 只出现在预期负向例上）。
func expectedPermDenied(p *callPlan) int {
	if p.Expect.Kind == string(gwerr.KindPermission) {
		return 1
	}
	return 0
}

// matchRecord 单条记录 ↔ 观测对照（工具/身份/参数/状态/行数/原因逐项）。
func matchRecord(rec jsonlRecord, o obs) bool {
	params, _ := rec.Params.(map[string]any)
	if rec.Tool != o.Tool || rec.User != o.Key || rec.Status != o.Status {
		return false
	}
	if !canonEqual(params, o.Params) {
		return false
	}
	switch o.Status {
	case execrecord.StatusSuccess:
		if o.Rows != nil && (rec.Rows == nil || *rec.Rows != *o.Rows) {
			return false
		}
		if o.Truncated != nil && (rec.Truncated == nil || *rec.Truncated != *o.Truncated) {
			return false
		}
	case execrecord.StatusRejected:
		if rec.Reject == nil || rec.Reject.Kind != gwerr.Kind(o.RejectKind) {
			return false
		}
		if o.RejectReason != "" {
			if r, _ := rec.Reject.Details["reason"].(string); r != o.RejectReason {
				return false
			}
		}
	}
	return true
}

// matchGroup 并发组多集对照（组内顺序不保证；按 (user,status) 计数）。
func matchGroup(recs []jsonlRecord, obs []obs) bool {
	type key struct{ user, status string }
	recCount := map[key]int{}
	for _, r := range recs {
		recCount[key{r.User, r.Status}]++
	}
	obsCount := map[key]int{}
	for _, o := range obs {
		obsCount[key{o.Key, o.Status}]++
	}
	for k, n := range recCount {
		if obsCount[k] != n {
			return false
		}
	}
	return true
}

// canonEqual 参数规范化比较（map JSON 序列化，Go 键序稳定）。
func canonEqual(a, b map[string]any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// ── 重放（三件套 (b) 的可复现证明）──────────────────────────────────────

// runReplay 从调用链（chain 文件或执行记录目录）重放：与主运行相同的
// 客户端逐条重放（并发用例跳过状态比对——顺序重放无法复现并发拒绝），
// 断言同状态同行数。
func runReplay(cfg config, cf *caseFile, report *report) error {
	chain, err := readReplayChain(cfg.replayFrom)
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		return fmt.Errorf("%s 无 tool_call 记录可重放", cfg.replayFrom)
	}
	report.note(fmt.Sprintf("重放源：%s（%d 条调用记录）", cfg.replayFrom, len(chain)))

	skipSet := replaySkipSet(cf.Cases, cfg.mode)
	cli, err := newClient(cfg)
	if err != nil {
		return err
	}
	defer cli.close()

	ctx := context.Background()
	results := []caseResult{}
	replayed, skipped := 0, 0
	for i := range chain {
		rec := &chain[i]
		params, _ := rec.Params.(map[string]any)
		if _, skip := skipSet[canonOf(rec.Tool, rec.User, params)]; skip {
			skipped++
			results = append(results, caseResult{Label: fmt.Sprintf("replay#%d", i+1), Name: rec.Tool, Pass: true, Detail: "跳过（并发用例，顺序重放不可复现）"})
			continue
		}
		p := &callPlan{Tool: rec.Tool, Args: params, Key: rec.User}
		o, err := callOnce(ctx, cfg, cli, p)
		if err != nil {
			return err
		}
		replayed++
		r := caseResult{Label: fmt.Sprintf("replay#%d", i+1), Name: rec.Tool, Pass: true}
		if o.CallErr != "" {
			r.Pass = false
			r.Detail = "调用失败: " + o.CallErr
		} else {
			var fails []string
			if o.Status != rec.Status {
				fails = append(fails, fmt.Sprintf("status=%s（记录 %s）", o.Status, rec.Status))
			}
			if o.Status == execrecord.StatusSuccess && rec.Status == execrecord.StatusSuccess {
				if o.Tool == "execute_sql" {
					if rec.Rows != nil && (o.Rows == nil || *o.Rows != *rec.Rows) {
						fails = append(fails, fmt.Sprintf("rows=%v（记录 %d）", o.Rows, *rec.Rows))
					}
					if rec.Truncated != nil && (o.Truncated == nil || *o.Truncated != *rec.Truncated) {
						fails = append(fails, fmt.Sprintf("truncated=%v（记录 %v）", o.Truncated, *rec.Truncated))
					}
				}
			}
			if o.Status == execrecord.StatusRejected && rec.Status == execrecord.StatusRejected {
				if rec.Reject != nil {
					if o.RejectKind != string(rec.Reject.Kind) {
						fails = append(fails, fmt.Sprintf("kind=%s（记录 %s）", o.RejectKind, rec.Reject.Kind))
					}
					// 被拒原因如实：details.reason 也要复现（主运行对照同一
					// 精度；如 unknown_table vs not_granted 不可互替）。
					if r, _ := rec.Reject.Details["reason"].(string); r != "" && o.RejectReason != r {
						fails = append(fails, fmt.Sprintf("reason=%s（记录 %s）", o.RejectReason, r))
					}
				}
			}
			if len(fails) > 0 {
				r.Pass = false
				r.Detail = strings.Join(fails, "; ")
			} else {
				r.Detail = detailOf(o)
			}
		}
		results = append(results, r)
	}
	report.addReplay(results, replayed, skipped)
	if !report.concludeReplay() {
		return fmt.Errorf("重放存在不一致（详见报告 %s）", cfg.reportPath)
	}
	return nil
}

// replaySkipSet 收集 replay_skip 用例的 (tool, user, params) 规范化键：
// 用户入键避免「不同用例同参数」误跳过（并发探测用例的用户/参数组合
// 是唯一的）。
func replaySkipSet(cases []Case, mode string) map[string]bool {
	set := map[string]bool{}
	for _, p := range buildPlans(cases, mode) {
		if p.Skip {
			set[canonOf(p.Tool, p.Key, p.Args)] = true
		}
	}
	return set
}

// canonOf 生成 (tool, user, params) 的规范化键（map JSON 序列化，键序稳定）。
func canonOf(tool, user string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return tool + "|" + user + "|" + string(b)
}

// ── 报告 ────────────────────────────────────────────────────────────────

type report struct {
	cfg                           config
	gitSHA                        string
	lines                         []string
	results                       []caseResult
	psql                          []psqlResult
	jsonl                         *jsonlCheck
	replay                        []caseResult
	replayReplayed, replaySkipped int
	notes                         []string
}

// newReport 构造报告（记录 git 短 sha 供留档溯源）。
func newReport(cfg config) *report {
	return &report{cfg: cfg, gitSHA: gitSHA()}
}

// gitSHA 返回当前提交短 sha（报告溯源；非 git 环境 = unknown）。
func gitSHA() string {
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "unknown"
}

func (r *report) addResults(results []caseResult) {
	r.results = results
}

func (r *report) addPsql(ps []psqlResult) {
	r.psql = ps
}

func (r *report) addJSONL(c jsonlCheck) {
	r.jsonl = &c
}

func (r *report) addReplay(results []caseResult, replayed, skipped int) {
	r.replay = results
	r.replayReplayed = replayed
	r.replaySkipped = skipped
}

func (r *report) note(s string) { r.notes = append(r.notes, s) }

// conclude 汇总判定：用例断言 + (a) + (b)/(c)。
func (r *report) conclude() bool {
	ok := true
	for _, res := range r.results {
		if !res.Pass {
			ok = false
		}
	}
	for _, p := range r.psql {
		if !p.Pass {
			ok = false
		}
	}
	if r.jsonl != nil && !r.jsonl.Pass {
		ok = false
	}
	return ok
}

func (r *report) concludeReplay() bool {
	for _, res := range r.replay {
		if !res.Pass {
			return false
		}
	}
	return true
}

func (r *report) write(path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# dgw 验收重放报告\n\n")
	fmt.Fprintf(&b, "- 时间：%s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "- 形态：%s\n", r.cfg.mode)
	if r.cfg.mode == "http" {
		fmt.Fprintf(&b, "- 网关：%s（Streamable HTTP，bearer）\n", r.cfg.addr)
	} else {
		fmt.Fprintf(&b, "- 网关：%s serve-stdio（用户 %s）\n", r.cfg.dgwBin, r.cfg.stdioUser)
	}
	fmt.Fprintf(&b, "- 用例：%s\n", r.cfg.casesPath)
	fmt.Fprintf(&b, "- git：%s\n", r.gitSHA)
	if len(r.notes) > 0 {
		for _, n := range r.notes {
			fmt.Fprintf(&b, "- 注：%s\n", n)
		}
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## 用例结果\n\n")
	fmt.Fprintf(&b, "| id | 结果 | 详情 |\n|---|---|---|\n")
	for _, res := range r.results {
		mark := "✅ PASS"
		if !res.Pass {
			mark = "❌ FAIL"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", res.Label, mark, res.Detail)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## 判定三件套\n\n")
	aPass, aDetail := "未执行", "-"
	if len(r.psql) > 0 {
		aPass, aDetail = verdict(r.psqlAllPass(), r.psqlDetail())
	}
	fmt.Fprintf(&b, "| 判定 | 结果 | 详情 |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| (a) 数字一致（psql 对照） | %s | %s |\n", aPass, aDetail)
	if r.jsonl != nil {
		bPass := "✅ PASS"
		if !r.jsonl.Pass {
			bPass = "❌ FAIL"
		}
		fmt.Fprintf(&b, "| (b) 执行记录可复现（JSONL 完整 + 重放） | %s | %s |\n", bPass, r.jsonl.Detail)
		cPass := "✅ PASS"
		if !r.jsonl.Pass {
			cPass = "❌ FAIL"
		}
		fmt.Fprintf(&b, "| (c) 零未授权访问 | %s | permission_denied=%d（预期 %d），无 auth_failure |\n",
			cPass, r.jsonl.PermDenied, r.jsonl.PermExpected)
	}
	fmt.Fprintf(&b, "\n")

	if len(r.psql) > 0 {
		fmt.Fprintf(&b, "### (a) psql 对照明细\n\n| 用例 | 结果 | 详情 |\n|---|---|---|\n")
		for _, p := range r.psql {
			mark := "✅"
			if !p.Pass {
				mark = "❌"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", p.Label, mark, p.Detail)
		}
		fmt.Fprintf(&b, "\n")
	}

	if r.jsonl != nil {
		fmt.Fprintf(&b, "### (b) 执行记录 JSONL\n\n")
		fmt.Fprintf(&b, "- 记录总数：%d\n", r.jsonl.Total)
		fmt.Fprintf(&b, "- 状态分布：%v\n", r.jsonl.ByStatus)
		fmt.Fprintf(&b, "\n")
	}

	if len(r.replay) > 0 {
		fmt.Fprintf(&b, "### (b) 重放复现\n\n")
		fmt.Fprintf(&b, "- 重放 %d 条，跳过 %d 条（并发用例）\n", r.replayReplayed, r.replaySkipped)
		fmt.Fprintf(&b, "\n| 记录 | 工具 | 结果 | 详情 |\n|---|---|---|---|\n")
		for _, res := range r.replay {
			mark := "✅"
			if !res.Pass {
				mark = "❌"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", res.Label, res.Name, mark, res.Detail)
		}
		fmt.Fprintf(&b, "\n")
	}

	// 总判定（重放形态看重放结果，主运行看用例 + 三件套）
	ok := r.conclude()
	if len(r.replay) > 0 {
		ok = r.concludeReplay()
	}
	verdict := "✅ 全部通过"
	if !ok {
		verdict = "❌ 存在失败"
	}
	fmt.Fprintf(&b, "## 结论\n\n%s\n", verdict)

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// psqlAllPass 汇总 (a) 判定：psql 对照全部一致。
func (r *report) psqlAllPass() bool {
	for _, p := range r.psql {
		if !p.Pass {
			return false
		}
	}
	return true
}

// psqlDetail 组装 (a) 的通过数摘要。
func (r *report) psqlDetail() string {
	pass := 0
	for _, p := range r.psql {
		if p.Pass {
			pass++
		}
	}
	return fmt.Sprintf("%d/%d 用例逐项一致", pass, len(r.psql))
}

func verdict(pass bool, detail string) (string, string) {
	if pass {
		return "✅ PASS", detail
	}
	return "❌ FAIL", detail
}

// ── 用例加载 ────────────────────────────────────────────────────────────

func loadCases(path string) (*caseFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf caseFile
	if err := yaml.Unmarshal(b, &cf); err != nil {
		return nil, err
	}
	if cf.Version != 1 {
		return nil, fmt.Errorf("不支持用例版本 %d（当前 1）", cf.Version)
	}
	for i := range cf.Cases {
		c := &cf.Cases[i]
		if c.ID == "" {
			return nil, fmt.Errorf("第 %d 个用例缺 id", i+1)
		}
		if len(c.Modes) == 0 {
			return nil, fmt.Errorf("用例 %s 缺 modes", c.ID)
		}
		if c.Concurrency > 0 && (c.Tool == "" || c.Args == nil) {
			return nil, fmt.Errorf("用例 %s：concurrency 需要 tool+args", c.ID)
		}
		if c.Concurrency == 0 && c.Tool == "" && len(c.Steps) == 0 {
			return nil, fmt.Errorf("用例 %s 无调用内容", c.ID)
		}
	}
	return &cf, nil
}
