// Package execrecord 实现执行记录（06 票，ADR-0006 / spec §4.6）：
// 结构化 JSONL（宿主机文件 + 按日轮转），非 SQLite 证据存储、无 CLI 查询面、
// 不接 OTel。
//
// 字段契约 = 时间 / 用户 / key / 工具 / 参数（execute_sql 的 SQL 原文入库、
// 不脱敏——宿主机权限即访问边界）/ 分阶段耗时（认证→权限→解析→执行→返回，
// 缺失 = 该阶段未打点或工具不适用）/ 状态（成功/拒绝/超时/解析失败）/ 行数 /
// truncated / plan_id（透传）/ 被拒原因（gwerr 原文，如实）。范围 = 六工具
// 全记（kind=tool_call）+ 认证失败（kind=auth_failure）+ key 生命周期
// （kind=key_lifecycle，CLI 侧一行）。
//
// 保留期（spec §4.9「env 可覆盖」）= 原始 ~7 天轮转 + 聚合摘要 ~30 天；
// 原始文件按日分文件（raw-YYYY-MM-DD.jsonl），跨日轮转时把前一日原始文件
// 聚合成摘要（summary-YYYY-MM-DD.jsonl，幂等一日一份）；保留期清理在写入
// 路径上执行，原始文件过期先补摘要再删除（网关跨日停机也不丢聚合信号）。
// 聚合摘要喂知识采集信号（08/12）：被拒查询分布 / 原料路径（工具分布）/
// 搜索关键词。
//
// 写入失败（磁盘满等）返回错误由调用方决定处理——网关侧记录失败不阻断
// 调用（ADR-0006「故障响应不依赖任何审计设施」）；启动失败（目录不可建/
// 保留期非法）由 New 报错 = 配置错误 fail fast。
package execrecord

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// 记录 kind（记录类型分派，JSONL 行的 kind 字段）。
const (
	KindToolCall     = "tool_call"     // 六工具调用（含权限/限流/超时/解析拒绝）
	KindAuthFailure  = "auth_failure"  // HTTP 认证失败（401/403，身份未知）
	KindKeyLifecycle = "key_lifecycle" // key 创建/吊销（CLI 侧一行）
)

// 状态（spec §4.6：成功/拒绝/超时/解析失败；拒绝原因见 reject 字段）。
const (
	StatusSuccess    = "success"
	StatusRejected   = "rejected"
	StatusTimeout    = "timeout"
	StatusParseError = "parse_error"
)

// 分阶段耗时键（spec §4.6：认证→权限→解析→执行→返回；缺失 = 未打点，
// 如 stdio 形态无 per-call 认证）。网关在各阶段位置打点，本包只负责落盘。
const (
	StageAuth   = "auth"   // 认证：HTTP verifyToken 实测耗时
	StagePerm   = "perm"   // 权限：表授权比对耗时
	StageParse  = "parse"  // 解析：AST 分类 + 表提取
	StageExec   = "exec"   // 执行：查询 + 结果编码
	StageReturn = "return" // 返回：结果/记录组装耗时（落盘是记录器内部开销，不进调用链阶段）
)

// key 生命周期事件（kind=key_lifecycle 的 event 字段）。
const (
	EventKeyCreated = "key_created"
	EventKeyRevoked = "key_revoked"
)

// 默认保留期（spec §4.9 参数表；env 可覆盖）。
const (
	DefaultRawRetentionDays     = 7
	DefaultSummaryRetentionDays = 30
)

// ToolCall 是一次工具调用的执行记录（kind=tool_call）。六工具全记；权限拒绝
// /限流拒绝/超时/解析失败同样以本类型落记录，被拒原因如实（Reject = gwerr
// 原文）。
type ToolCall struct {
	TS        time.Time        `json:"ts"`                  // 时间
	User      string           `json:"user,omitempty"`      // 用户
	Key       string           `json:"key,omitempty"`       // 凭据 key 行 ID
	Tool      string           `json:"tool,omitempty"`      // 工具名
	Params    any              `json:"params,omitempty"`    // 参数（execute_sql 的 SQL 原文入库、不脱敏）
	StagesMS  map[string]int64 `json:"stages_ms,omitempty"` // 分阶段耗时（认证→权限→解析→执行→返回）
	Status    string           `json:"status,omitempty"`    // 状态（成功/拒绝/超时/解析失败）
	Rows      *int             `json:"rows,omitempty"`      // execute_sql 实际返回行数（无结果的记录为 nil）
	Truncated *bool            `json:"truncated,omitempty"` // 超过限额截断（无结果的记录为 nil）
	PlanID    string           `json:"plan_id,omitempty"`   // 溯源透传（v1 不校验）
	Reject    *gwerr.Error     `json:"reject,omitempty"`    // 被拒原因（成功为 nil）
}

// KeyLifecycle 是 key 生命周期记录（kind=key_lifecycle；CLI 侧一行）。
type KeyLifecycle struct {
	TS    time.Time `json:"ts"`             // 时间
	Event string    `json:"event"`          // key_created / key_revoked
	User  string    `json:"user,omitempty"` // 绑定用户（创建者 / 被吊销 key 的属主）
	Key   string    `json:"key,omitempty"`  // key 行 ID
}

// Config 是执行记录的配置（spec §4.9：原始 7 天轮转 + 聚合摘要 30 天，
// 可配）。保留期 <1 或目录为空 = New 报错（配置错误 fail fast）。
type Config struct {
	Dir                  string           // 宿主机日志目录（原始 JSONL + 聚合摘要）
	RawRetentionDays     int              // 原始保留天数（默认 7）
	SummaryRetentionDays int              // 聚合摘要保留天数（默认 30）
	Now                  func() time.Time // 时间源（测试 seam：轮转/保留期与真实时钟解耦；缺省 time.Now）
}

// Logger 是执行记录写入器（一个进程一个实例，跨调用共享）：原始 JSONL 按日
// 分文件，跨日轮转聚合前一日，按保留期清理过期文件。并发安全（一次 MCP
// 调用一行，互斥锁下写文件，调用频率低锁开销可忽略）。
type Logger struct {
	dir              string
	rawRetention     int
	summaryRetention int
	now              func() time.Time

	mu      sync.Mutex
	day     string // 当前打开的原始文件日（"2006-01-02"；空 = 未打开）
	rawFile *os.File
	closed  bool
}

// New 构造记录器：校验配置、建目录（MkdirAll）、预开今日原始文件——启动即
// 验证目录可写（不可写 = 启动失败，不静默降级为「不记录」）。
func New(cfg Config) (*Logger, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("执行记录目录为空（DGW_EXEC_LOG_DIR）")
	}
	rawRet, sumRet := cfg.RawRetentionDays, cfg.SummaryRetentionDays
	if rawRet < 1 {
		return nil, fmt.Errorf("原始保留天数 %d < 1（spec §4.9 默认 7）", rawRet)
	}
	if sumRet < 1 {
		return nil, fmt.Errorf("聚合摘要保留天数 %d < 1（spec §4.9 默认 30）", sumRet)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("建执行记录目录 %s: %w", cfg.Dir, err)
	}
	l := &Logger{
		dir:              cfg.Dir,
		rawRetention:     rawRet,
		summaryRetention: sumRet,
		now:              cfg.Now,
	}
	// 预开今日文件：启动即可写（目录只读等配置错误在启动期暴露）。
	if err := l.rollover(dayOf(cfg.Now())); err != nil {
		return nil, err
	}
	return l, nil
}

// LogToolCall 落一行工具调用记录（kind=tool_call）。返回写入错误（磁盘满
// 等），调用方决定处理（网关侧记录失败不阻断调用）。
func (l *Logger) LogToolCall(c ToolCall) error {
	rec := struct {
		Kind string `json:"kind"`
		ToolCall
	}{KindToolCall, c}
	return l.write(c.TS, rec)
}

// LogAuthFailure 落一行认证失败记录（kind=auth_failure）：身份未知（认证
// 失败无法归因 user/key），工具名不可知（MCP 请求体在认证层不解析），被拒
// 原因如实（gwerr 原文）。
func (l *Logger) LogAuthFailure(ts time.Time, e *gwerr.Error) error {
	rec := struct {
		Kind   string       `json:"kind"`
		Status string       `json:"status"`
		Reject *gwerr.Error `json:"reject"`
		ToolCall
	}{KindAuthFailure, StatusRejected, e, ToolCall{TS: ts}}
	return l.write(ts, rec)
}

// LogKeyLifecycle 落一行 key 生命周期记录（kind=key_lifecycle；CLI 侧一行，
// spec §4.6「key 创建/吊销各记一行」）。
func (l *Logger) LogKeyLifecycle(k KeyLifecycle) error {
	rec := struct {
		Kind string `json:"kind"`
		KeyLifecycle
	}{KindKeyLifecycle, k}
	return l.write(k.TS, rec)
}

// Close 关闭当前原始文件；关闭后写入返回错误。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.rawFile != nil {
		err := l.rawFile.Close()
		l.rawFile = nil
		return err
	}
	return nil
}

// write 是统一落盘路径：互斥锁下 轮转检查（跨日 → 聚合前一日 + 换日文件 +
// 保留期清理）→ JSONL 追加。
func (l *Logger) write(ts time.Time, rec any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return fmt.Errorf("执行记录器已关闭")
	}
	day := dayOf(ts)
	if day != l.day {
		if err := l.rollover(day); err != nil {
			return err
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("执行记录序列化失败: %w", err)
	}
	if _, err := l.rawFile.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("执行记录写入失败: %w", err)
	}
	return nil
}

// rollover 打开指定日的原始文件：前一日文件收尾（关闭 + 幂等聚合摘要）→
// 打开新日文件 → 保留期清理。
//
// 失败路径状态干净可自愈：旧文件关闭后立即置 nil；摘要失败时 l.day 保持
// 前一日——下次写入重试该日摘要（幂等，成功才推进状态），记录器不会停留
// 在「文件已关但日期未推进」的哑死状态。
func (l *Logger) rollover(day string) error {
	if l.day != "" {
		if l.rawFile != nil {
			if err := l.rawFile.Close(); err != nil {
				// 关闭失败也置 nil（已关句柄二次 Close 恒失败——不置 nil 会
				// 让每次写入都在同一处失败，记录器哑死）；错误如实上报，
				// 下次写入跳过关闭重试摘要。
				l.rawFile = nil
				return fmt.Errorf("关闭 %s: %w", rawName(l.day), err)
			}
			l.rawFile = nil
		}
		if err := l.summarize(l.day); err != nil {
			return fmt.Errorf("聚合摘要 %s: %w", l.day, err)
		}
		l.day = ""
	}
	f, err := os.OpenFile(filepath.Join(l.dir, rawName(day)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", rawName(day), err)
	}
	l.day = day
	l.rawFile = f
	return l.prune()
}

// prune 按保留期清理过期文件：原始文件过期先补摘要再删除（网关跨日停机时
// 前一日摘要可能未生成）；摘要按保留期直接删除。日期比较用 "2006-01-02"
// 字符串序（零填充，字典序即时间序）。删除条件 date <= cutoff：保留恰好
// N 天（今日-N+1 … 今日），与 spec §4.9「原始 7 天 / 摘要 30 天」一致。
func (l *Logger) prune() error {
	rawCutoff := dayOf(l.now().AddDate(0, 0, -l.rawRetention))
	sumCutoff := dayOf(l.now().AddDate(0, 0, -l.summaryRetention))
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return fmt.Errorf("列执行记录目录: %w", err)
	}
	for _, e := range entries {
		date, kind, ok := parseName(e.Name())
		if !ok {
			continue
		}
		if date == l.day {
			continue // 当前打开文件不参与清理（先写后清，保留期从下次轮转生效）
		}
		switch kind {
		case "raw-":
			if date <= rawCutoff {
				if err := l.summarize(date); err != nil {
					return err // 先补摘要再删（聚合信号不丢）
				}
				if err := os.Remove(filepath.Join(l.dir, e.Name())); err != nil {
					return fmt.Errorf("清理原始文件 %s: %w", e.Name(), err)
				}
			}
		case "summary-":
			if date <= sumCutoff {
				if err := os.Remove(filepath.Join(l.dir, e.Name())); err != nil {
					return fmt.Errorf("清理摘要文件 %s: %w", e.Name(), err)
				}
			}
		}
	}
	return nil
}

// summarize 把某日原始文件聚合成摘要（幂等：摘要已存在则跳过）。摘要内容
// （喂知识采集信号，ADR-0006）：
//
//	calls    各工具调用分布（总/成功/拒绝/超时/解析失败/行数/截断）——
//	         原料路径信号（execute_sql 直接查 = 原料路径）
//	rejects  被拒原因分布（gwerr details.reason，缺省用 kind；认证失败计
//	         unauthorized）
//	keywords search_entities 关键词分布（去重计数）
//
// 幂等键 = 「摘要已存在且其 raw_size 与当前原始文件大小一致」：乱序追加
// （跨零点调用晚到）与崩溃残留（半截/0 字节摘要）都会导致不一致 → 重算，
// 摘要永不为陈旧数据所固化。写入 = 唯一临时文件 + rename（同目录原子，
// 崩溃/跨进程并发（守护进程与 CLI 共享 /logs）都不留撕裂摘要）。
// 坏行跳过（尽力而为的信号源，不因单行损坏丢弃整日聚合）。
func (l *Logger) summarize(date string) error {
	rawPath := filepath.Join(l.dir, rawName(date))
	rawSize, err := fileSize(rawPath)
	if err != nil {
		return err
	}
	sumPath := filepath.Join(l.dir, summaryName(date))
	if b, err := os.ReadFile(sumPath); err == nil {
		var existing struct {
			RawSize int64 `json:"raw_size"`
		}
		if json.Unmarshal(b, &existing) == nil && existing.RawSize == rawSize {
			return nil // 幂等：原始文件自上次聚合以来无变化
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读摘要 %s: %w", sumPath, err)
	}
	agg, err := aggregateFile(rawPath)
	if err != nil {
		return err
	}
	if agg.Date == "" {
		return nil // 空文件（无有效记录）：不生成摘要（预开文件/坏行场景）
	}
	agg.RawSize = rawSize
	line, err := json.Marshal(agg)
	if err != nil {
		return fmt.Errorf("摘要序列化失败: %w", err)
	}
	// 原子写：同目录唯一临时文件 + rename（os.WriteFile 直写目标路径会在
	// 崩溃/并发下留下撕裂摘要，且被幂等键误判为「已聚合」）。
	tmp, err := os.CreateTemp(l.dir, "summary-"+date+".*.tmp")
	if err != nil {
		return fmt.Errorf("建摘要临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败路径清理（成功 rename 后文件已不存在）
	if _, err := tmp.Write(append(line, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("写摘要临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关摘要临时文件: %w", err)
	}
	if err := os.Rename(tmpName, sumPath); err != nil {
		return fmt.Errorf("落摘要 %s: %w", sumPath, err)
	}
	return nil
}

// fileSize 返回文件字节数（保留期/幂等判断用；文件缺失 = 错误——摘要的
// 输入不存在时调用方应能感知，而非静默跳过）。
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return fi.Size(), nil
}

// ── 聚合 ─────────────────────────────────────────────────────────────────

// toolStats 是单工具调用分布（摘要的 calls 值）。
type toolStats struct {
	Calls      int `json:"calls"`
	Success    int `json:"success"`
	Rejected   int `json:"rejected"`
	Timeout    int `json:"timeout"`
	ParseError int `json:"parse_error"`
	Rows       int `json:"rows"`
	Truncated  int `json:"truncated"`
}

// keywordCount 是搜索关键词分布（摘要的 keywords 元素）。
type keywordCount struct {
	Keyword string `json:"keyword"`
	Count   int    `json:"count"`
}

// dailySummary 是某日的聚合摘要（summary-YYYY-MM-DD.jsonl 一行）。
type dailySummary struct {
	Date     string               `json:"date"`
	RawSize  int64                `json:"raw_size"` // 聚合时的原始文件字节数（幂等键：变化即重算）
	Calls    map[string]toolStats `json:"calls"`
	Rejects  map[string]int       `json:"rejects"`
	Keywords []keywordCount       `json:"keywords,omitempty"`
}

// aggregateFile 逐行聚合原始 JSONL（坏行跳过）→ 摘要。
func aggregateFile(path string) (*dailySummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读原始文件 %s: %w", path, err)
	}
	defer f.Close()

	sum := &dailySummary{
		Date:    "",
		Calls:   map[string]toolStats{},
		Rejects: map[string]int{},
	}
	kwCount := map[string]int{}

	sc := bufio.NewScanner(f)
	// 行可远超默认 64KB（execute_sql 的 SQL 原文无长度上限）——Scanner 默认
	// 上限会让超长行永久中断聚合（记录器哑死），放宽到 16MB。
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// 记录字段契约的单一事实源 = ToolCall（写入形态已平铺，解析直接
		// 复用——无第二份字段清单）；kind 单独分派。
		var kind struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(line), &kind) != nil {
			continue // 坏行跳过（尽力而为的信号源）
		}
		switch kind.Kind {
		case KindToolCall:
			var rec ToolCall
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if sum.Date == "" {
				sum.Date = dayOf(rec.TS)
			}
			st := sum.Calls[rec.Tool]
			st.Calls++
			switch rec.Status {
			case StatusSuccess:
				st.Success++
				if rec.Rows != nil {
					st.Rows += *rec.Rows
				}
				if rec.Truncated != nil && *rec.Truncated {
					st.Truncated++
				}
			case StatusTimeout:
				st.Timeout++
			case StatusParseError:
				st.ParseError++
			default:
				st.Rejected++
			}
			sum.Calls[rec.Tool] = st
			if rec.Reject != nil {
				sum.Rejects[rejectKey(rec.Reject)]++
			}
			if rec.Tool == "search_entities" {
				if p, ok := rec.Params.(map[string]any); ok {
					if q, ok := p["query"].(string); ok && q != "" {
						kwCount[q]++
					}
				}
			}
		case KindAuthFailure:
			var rec struct {
				Reject *gwerr.Error `json:"reject"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil || rec.Reject == nil {
				continue
			}
			sum.Rejects[rejectKey(rec.Reject)]++
		case KindKeyLifecycle:
			// key 生命周期不是查询信号，不进聚合
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读原始文件 %s: %w", path, err)
	}

	sum.Keywords = make([]keywordCount, 0, len(kwCount))
	for kw, n := range kwCount {
		sum.Keywords = append(sum.Keywords, keywordCount{Keyword: kw, Count: n})
	}
	// 确定性排序：次数降序，同次数按关键词升序
	sort.Slice(sum.Keywords, func(i, j int) bool {
		if sum.Keywords[i].Count != sum.Keywords[j].Count {
			return sum.Keywords[i].Count > sum.Keywords[j].Count
		}
		return sum.Keywords[i].Keyword < sum.Keywords[j].Keyword
	})
	return sum, nil
}

// rejectKey 是「被拒原因」的聚合键：details.reason 优先（机器可区分的拒绝
// 类别，如 not_granted/timeout/syntax_error），缺省退回 kind（unauthorized
// 等无 reason 的拒绝）。
func rejectKey(e *gwerr.Error) string {
	if e == nil {
		return "unknown"
	}
	if r, ok := e.Details["reason"].(string); ok && r != "" {
		return r
	}
	return string(e.Kind)
}

// ── 文件名与日期 ─────────────────────────────────────────────────────────

// dayOf 返回时间的日历日（"2006-01-02"；记录用自身时区，与文件日期一致）。
func dayOf(t time.Time) string { return t.Format("2006-01-02") }

func rawName(day string) string     { return "raw-" + day + ".jsonl" }
func summaryName(day string) string { return "summary-" + day + ".jsonl" }

// parseName 解析记录文件名 → (日期, 前缀)；非本记录文件（外部文件等）返回
// ok=false。日期零填充 + 定长，字符串序即时间序（保留期比较依赖）。
func parseName(name string) (day, kind string, ok bool) {
	if !strings.HasSuffix(name, ".jsonl") {
		return "", "", false
	}
	for _, prefix := range []string{"raw-", "summary-"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		day = strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if _, err := time.Parse("2006-01-02", day); err == nil {
			return day, prefix, true
		}
	}
	return "", "", false
}
