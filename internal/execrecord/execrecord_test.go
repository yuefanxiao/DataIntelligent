package execrecord

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuefanxiao/DataIntelligent/internal/gwerr"
)

// ── 测试骨架 ──────────────────────────────────────────────────────────────

// testNow 是固定时间源（测试 seam：轮转/保留期断言与真实时钟解耦）。
func testNow() time.Time { return time.Date(2026, 8, 12, 10, 0, 0, 0, time.Local) }

// newTestLogger 建一个指向临时目录的记录器（原始 7 天 / 摘要 30 天，可配
// 注入保留期与时间源）。
func newTestLogger(t *testing.T, dir string, rawRet, sumRet int, now func() time.Time) *Logger {
	t.Helper()
	l, err := New(Config{
		Dir:                  dir,
		RawRetentionDays:     rawRet,
		SummaryRetentionDays: sumRet,
		Now:                  now,
	})
	if err != nil {
		t.Fatalf("execrecord.New: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// readLines 读文件全部行（JSONL 逐行）。
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			lines = append(lines, s)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return lines
}

// parseLine 把一行 JSONL 解成通用映射（字段断言用）。
func parseLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("JSONL 行解析失败: %v\n%s", err, line)
	}
	return m
}

// ── 字段契约 ──────────────────────────────────────────────────────────────

// AC1：execute_sql 全调用链落 JSONL —— SQL 原文、分阶段耗时、状态、行数、
// truncated、plan_id、被拒原因，一行一个记录，kind=tool_call。
func TestLogToolCallFullContract(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	ts := time.Date(2026, 8, 12, 9, 30, 0, 0, time.Local)
	rows, truncated := 42, true
	rec := ToolCall{
		TS:        ts,
		User:      "dev-alice",
		Key:       "3",
		Tool:      "execute_sql",
		Params:    map[string]any{"sql": "SELECT * FROM orders WHERE status = 'paid'", "dbname": "bss"},
		StagesMS:  map[string]int64{StageAuth: 1, StagePerm: 2, StageParse: 15, StageExec: 120, StageReturn: 1},
		Status:    StatusSuccess,
		Rows:      &rows,
		Truncated: &truncated,
		PlanID:    "plan-42",
	}
	if err := l.LogToolCall(rec); err != nil {
		t.Fatalf("LogToolCall: %v", err)
	}

	lines := readLines(t, filepath.Join(dir, "raw-2026-08-12.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("行数 = %d，期望 1", len(lines))
	}
	m := parseLine(t, lines[0])
	if m["kind"] != "tool_call" || m["user"] != "dev-alice" || m["key"] != "3" || m["tool"] != "execute_sql" {
		t.Fatalf("基础字段 = %v", m)
	}
	if m["status"] != "success" {
		t.Errorf("status = %v，期望 success", m["status"])
	}
	if m["plan_id"] != "plan-42" {
		t.Errorf("plan_id = %v，期望 plan-42（透传）", m["plan_id"])
	}
	// SQL 原文入库（不脱敏，宿主机权限即访问边界）
	params, ok := m["params"].(map[string]any)
	if !ok || params["sql"] != "SELECT * FROM orders WHERE status = 'paid'" {
		t.Errorf("params.sql = %v，期望 SQL 原文", params["sql"])
	}
	// 分阶段耗时（认证→权限→解析→执行→返回）
	stages, ok := m["stages_ms"].(map[string]any)
	if !ok || stages["auth"] == nil || stages["perm"] == nil || stages["parse"] == nil ||
		stages["exec"] == nil || stages["return"] == nil {
		t.Errorf("stages_ms = %v，期望五阶段齐全", m["stages_ms"])
	}
	// 行数/截断
	if m["rows"] != float64(42) {
		t.Errorf("rows = %v，期望 42", m["rows"])
	}
	if m["truncated"] != true {
		t.Errorf("truncated = %v，期望 true", m["truncated"])
	}
	// ts 可解析（RFC3339Nano）
	if _, err := time.Parse(time.RFC3339Nano, m["ts"].(string)); err != nil {
		t.Errorf("ts = %v 非 RFC3339Nano: %v", m["ts"], err)
	}
}

// 字段契约：被拒记录（被拒原因如实 = gwerr 原文 kind/code/message/details），
// 状态映射由网关推导，本包负责原样落盘。
func TestLogToolCallRejected(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	e := gwerr.PermissionDenied("未授权表",
		map[string]any{"reason": "not_granted", "table": "bss.bss.secret"})
	rec := ToolCall{
		TS: time.Now(), Tool: "execute_sql", Status: StatusRejected,
		Params: map[string]any{"sql": "SELECT * FROM secret", "dbname": "bss"}, Reject: e,
	}
	if err := l.LogToolCall(rec); err != nil {
		t.Fatalf("LogToolCall: %v", err)
	}
	m := parseLine(t, readLines(t, filepath.Join(dir, rawName(dayOf(rec.TS))))[0])
	if m["status"] != "rejected" {
		t.Errorf("status = %v", m["status"])
	}
	rej, ok := m["reject"].(map[string]any)
	if !ok || rej["kind"] != "permission_denied" || rej["code"] != "DGW_PERMISSION_DENIED" {
		t.Fatalf("reject = %v，期望 gwerr 原文", m["reject"])
	}
	details, ok := rej["details"].(map[string]any)
	if !ok || details["reason"] != "not_granted" {
		t.Errorf("reject.details.reason = %v，期望 not_granted（被拒原因如实）", rej["details"])
	}
	// 拒绝记录不携带行数/截断（无结果）
	if _, ok := m["rows"]; ok {
		t.Errorf("拒绝记录不应有 rows: %v", m)
	}
}

// 认证失败记录：kind=auth_failure，身份未知（user/key 缺失），被拒原因如实。
func TestLogAuthFailure(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	if err := l.LogAuthFailure(time.Now(), gwerr.Unauthorized("missing or invalid bearer key")); err != nil {
		t.Fatalf("LogAuthFailure: %v", err)
	}
	m := parseLine(t, readLines(t, filepath.Join(dir, rawName(dayOf(time.Now()))))[0])
	if m["kind"] != "auth_failure" || m["status"] != "rejected" {
		t.Fatalf("认证失败记录 = %v", m)
	}
	if _, ok := m["user"]; ok {
		t.Errorf("认证失败身份未知，不应有 user: %v", m)
	}
	rej := m["reject"].(map[string]any)
	if rej["kind"] != "unauthorized" {
		t.Errorf("reject.kind = %v，期望 unauthorized", rej["kind"])
	}
}

// AC3：key 创建/吊销（CLI 侧）各记一行，kind=key_lifecycle。
func TestLogKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	ts := time.Now()
	if err := l.LogKeyLifecycle(KeyLifecycle{TS: ts, Event: EventKeyCreated, User: "dev-alice", Key: "5"}); err != nil {
		t.Fatalf("LogKeyLifecycle: %v", err)
	}
	if err := l.LogKeyLifecycle(KeyLifecycle{TS: ts, Event: EventKeyRevoked, User: "dev-alice", Key: "5"}); err != nil {
		t.Fatalf("LogKeyLifecycle: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, rawName(dayOf(ts))))
	if len(lines) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(lines))
	}
	created := parseLine(t, lines[0])
	if created["kind"] != "key_lifecycle" || created["event"] != "key_created" ||
		created["user"] != "dev-alice" || created["key"] != "5" {
		t.Errorf("创建记录 = %v", created)
	}
	revoked := parseLine(t, lines[1])
	if revoked["event"] != "key_revoked" {
		t.Errorf("吊销记录 = %v", revoked)
	}
}

// ── 轮转与保留期 ──────────────────────────────────────────────────────────

// AC4：原始按日轮转——跨日记录进各自日文件；跨日写入触发前一日聚合摘要。
func TestRotationByDay(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 59, 0, 0, time.Local)
	d2 := time.Date(2026, 8, 12, 0, 1, 0, 0, time.Local)
	if err := l.LogToolCall(ToolCall{TS: d1, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: d2, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}

	rawD1 := readLines(t, filepath.Join(dir, "raw-2026-08-11.jsonl"))
	rawD2 := readLines(t, filepath.Join(dir, "raw-2026-08-12.jsonl"))
	if len(rawD1) != 1 || len(rawD2) != 1 {
		t.Fatalf("各日行数 = %d/%d，期望 1/1", len(rawD1), len(rawD2))
	}
	// 跨日轮转时生成 8-11 的聚合摘要（幂等，一日一份）
	sum := readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))
	if len(sum) != 1 {
		t.Fatalf("摘要行数 = %d，期望 1", len(sum))
	}
	if parseLine(t, sum[0])["date"] != "2026-08-11" {
		t.Errorf("摘要 date = %v", parseLine(t, sum[0])["date"])
	}
}

// AC4：保留期——原始超过 7 天轮转清理（清理前补摘要），摘要超过 30 天删除。
func TestRetentionPruning(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)

	// 写入 25 天前 / 3 天前 / 今天的记录（8-12 为 now；now 注入固定）
	write := func(day string) {
		ts, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.LogToolCall(ToolCall{TS: ts.Add(12 * time.Hour), Tool: "execute_sql", Status: StatusSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-07-18") // 25 天前：原始过期（>7 天）→ 先补摘要再删；摘要 25 天 < 30 天 → 保留
	write("2026-08-09") // 3 天前：原始保留

	// 32 天前的摘要（构造：直接放一个文件，模拟超 30 天保留期的摘要；须在
	// 触发维护的写入之前放好——保留期检查只在跨日轮转路径上执行）
	sumOld := filepath.Join(dir, "summary-2026-07-10.jsonl")
	if err := os.WriteFile(sumOld, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	write("2026-08-12") // 今天：跨日轮转触发维护（补摘要 + 清理）

	for _, p := range []string{
		filepath.Join(dir, "raw-2026-07-18.jsonl"),     // 25 天前原始 → 已删（先补摘要）
		filepath.Join(dir, "summary-2026-07-10.jsonl"), // 32 天前摘要 → 已删（超 30 天）
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应被保留期清理删除", p)
		}
	}
	for _, p := range []string{
		filepath.Join(dir, "raw-2026-08-09.jsonl"),     // 3 天前原始 → 保留
		filepath.Join(dir, "summary-2026-07-18.jsonl"), // 25 天前原始补出的摘要 → 保留（30 天内）
		filepath.Join(dir, "raw-2026-08-12.jsonl"),     // 今日 → 保留
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 应在保留期内: %v", p, err)
		}
	}
}

// 摘要幂等：重复触发不重写、不重复行。
func TestSummarizeIdempotent(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 8, 12, 1, 0, 0, 0, time.Local)
	d3 := time.Date(2026, 8, 13, 1, 0, 0, 0, time.Local)
	for _, ts := range []time.Time{d1, d2, d3} {
		if err := l.LogToolCall(ToolCall{TS: ts, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	// d2/d3 触发时都应对 d1/d2 做摘要；每次触发都应跳过已存在的摘要
	if got := len(readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))); got != 1 {
		t.Fatalf("d1 摘要行数 = %d，期望 1（幂等）", got)
	}
	if got := len(readLines(t, filepath.Join(dir, "summary-2026-08-12.jsonl"))); got != 1 {
		t.Fatalf("d2 摘要行数 = %d，期望 1（幂等）", got)
	}
}

// 对抗评审收敛（P1）：跨零点乱序写入（零点前发起的调用晚到）不得让当日
// 摘要提前固化——摘要按 raw_size 变化重算，晚到记录最终进聚合。
func TestSummaryRecomputesOnOutOfOrderAppend(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 59, 55, 0, time.Local) // 零点前发起
	d2 := time.Date(2026, 8, 12, 0, 0, 5, 0, time.Local)    // 零点后

	// 顺序写入 d2 → 反向回写 d1（跨零点调用晚到）→ 再写两条 d2
	recs := []ToolCall{
		{TS: d2, Tool: "execute_sql", Status: StatusSuccess},
		{TS: d1, Tool: "execute_sql", Status: StatusSuccess},
		{TS: d2.Add(time.Minute), Tool: "execute_sql", Status: StatusSuccess},
		{TS: d2.Add(2 * time.Minute), Tool: "execute_sql", Status: StatusSuccess},
	}
	for _, r := range recs {
		if err := l.LogToolCall(r); err != nil {
			t.Fatal(err)
		}
	}
	// 次日首写触发维护：d1/d2 摘要都应按最终 raw 内容聚合（含晚到记录）
	if err := l.LogToolCall(ToolCall{TS: d2.Add(24 * time.Hour), Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	sum2 := parseLine(t, readLines(t, filepath.Join(dir, "summary-2026-08-12.jsonl"))[0])
	calls := sum2["calls"].(map[string]any)["execute_sql"].(map[string]any)
	if calls["calls"] != float64(3) {
		t.Errorf("d2 聚合 calls = %v，期望 3（晚到/追加记录不丢）", calls["calls"])
	}
	sum1 := parseLine(t, readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))[0])
	calls1 := sum1["calls"].(map[string]any)["execute_sql"].(map[string]any)
	if calls1["calls"] != float64(1) {
		t.Errorf("d1 聚合 calls = %v，期望 1", calls1["calls"])
	}
}

// 对抗评审收敛（P1）：超长记录行（>64KB Scanner 默认上限，如超长 SQL 原文）
// 不得让聚合永久失败——16MB 行上限下照常聚合。
func TestAggregateOversizedLine(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 8, 12, 1, 0, 0, 0, time.Local)
	bigSQL := strings.Repeat("SELECT ", 20*1024) // ~140KB 行
	if err := l.LogToolCall(ToolCall{TS: d1, Tool: "execute_sql", Status: StatusSuccess,
		Params: map[string]any{"sql": bigSQL}}); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: d2, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err) // 跨日轮转要聚合超长行——不得失败
	}
	sum := parseLine(t, readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))[0])
	calls := sum["calls"].(map[string]any)["execute_sql"].(map[string]any)
	if calls["calls"] != float64(1) {
		t.Errorf("超长行聚合 calls = %v，期望 1", calls["calls"])
	}
}

// 对抗评审收敛（P2）：摘要撕裂/0 字节残留（崩溃窗口）不得被幂等键误判——
// raw_size 不匹配即重算。
func TestSummaryRecoversFromTornFile(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 8, 12, 1, 0, 0, 0, time.Local)
	if err := l.LogToolCall(ToolCall{TS: d1, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃残留：0 字节摘要文件（WriteFile 截断后进程崩溃窗口）
	sumPath := filepath.Join(dir, "summary-2026-08-11.jsonl")
	if err := os.WriteFile(sumPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: d2, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	// 0 字节摘要被重算覆盖（而非永久跳过）
	sum := parseLine(t, readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))[0])
	if sum["date"] != "2026-08-11" {
		t.Errorf("撕裂摘要未重算: %v", sum)
	}
	calls := sum["calls"].(map[string]any)["execute_sql"].(map[string]any)
	if calls["calls"] != float64(1) {
		t.Errorf("重算 calls = %v，期望 1", calls["calls"])
	}
}

// 保留期精确性（对抗评审收敛 P3）：7 天配置恰好保留 7 个原始文件（不含
// 今日-N），摘要 30 天同理——date <= cutoff 删除。
func TestRetentionExactDays(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	// 写 7 天前的记录（应被删）+ 6 天前（应保留）
	old := time.Date(2026, 8, 5, 12, 0, 0, 0, time.Local)  // today-7
	keep := time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local) // today-6
	if err := l.LogToolCall(ToolCall{TS: old, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: keep, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: testNow(), Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw-2026-08-05.jsonl")); !os.IsNotExist(err) {
		t.Errorf("today-7 原始文件应被清理（恰好 7 天保留）")
	}
	if _, err := os.Stat(filepath.Join(dir, "raw-2026-08-06.jsonl")); err != nil {
		t.Errorf("today-6 原始文件应保留: %v", err)
	}
}

// 自愈：聚合摘要失败（如原始文件临时不可读）后记录器不哑死——l.day 保持
// 前一日，下次写入重试摘要（幂等），成功才推进到新日文件。
func TestRolloverSelfHeal(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	d1 := time.Date(2026, 8, 11, 23, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 8, 12, 1, 0, 0, 0, time.Local)
	if err := l.LogToolCall(ToolCall{TS: d1, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	// 让 d1 原始文件不可读 → 跨日轮转的摘要步骤失败（写入报错、状态保留）
	raw := filepath.Join(dir, "raw-2026-08-11.jsonl")
	if err := os.Chmod(raw, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: d2, Tool: "execute_sql", Status: StatusSuccess}); err == nil {
		t.Fatal("摘要失败应返回错误（写入不静默吞掉）")
	}
	// 恢复可读 → 下次写入重试摘要成功，记录器自愈
	if err := os.Chmod(raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.LogToolCall(ToolCall{TS: d2, Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatalf("恢复后写入应成功: %v", err)
	}
	// d1 摘要已补生成（幂等恢复）；恢复后的 d2 记录正常落新日文件
	// （失败那次写入未落盘——记录丢弃但错误如实上报，fail-open 语义）。
	if got := len(readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))); got != 1 {
		t.Fatalf("d1 摘要行数 = %d，期望 1（自愈补摘要）", got)
	}
	if got := len(readLines(t, filepath.Join(dir, "raw-2026-08-12.jsonl"))); got != 1 {
		t.Fatalf("d2 记录数 = %d，期望 1（失败写入不落盘，恢复后正常）", got)
	}
}

// ── 聚合摘要（喂知识采集信号：被拒查询分布 / 原料路径 / 搜索关键词）───────

// 摘要内容：各工具调用分布（总/成功/拒绝/超时/解析失败/行数/截断）、
// 被拒原因分布（gwerr kind/reason）、search_entities 关键词分布。
func TestSummaryAggregation(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	day := testNow().Add(-24 * time.Hour) // 昨日（8-11）——跨日轮转时聚合

	rows := 100
	calls := []ToolCall{
		{TS: day.Add(1 * time.Hour), User: "u1", Key: "1", Tool: "execute_sql",
			Params: map[string]any{"sql": "SELECT * FROM orders"}, Status: StatusSuccess, Rows: &rows, Truncated: ptr(true)},
		{TS: day.Add(2 * time.Hour), Tool: "execute_sql",
			Params: map[string]any{"sql": "SELECT * FROM secret"}, Status: StatusRejected,
			Reject: gwerr.PermissionDenied("x", map[string]any{"reason": "not_granted"})},
		{TS: day.Add(3 * time.Hour), Tool: "execute_sql",
			Params: map[string]any{"sql": "SELECT pg_sleep(1)"}, Status: StatusTimeout,
			Reject: gwerr.InvalidRequest("timeout", map[string]any{"reason": "timeout"})},
		{TS: day.Add(4 * time.Hour), Tool: "execute_sql",
			Params: map[string]any{"sql": "SELEC 1"}, Status: StatusParseError,
			Reject: gwerr.InvalidRequest("syntax", map[string]any{"reason": "syntax_error"})},
		{TS: day.Add(5 * time.Hour), Tool: "search_entities",
			Params: map[string]any{"query": "支付失败"}},
		{TS: day.Add(6 * time.Hour), Tool: "search_entities",
			Params: map[string]any{"query": "支付失败"}},
		{TS: day.Add(7 * time.Hour), Tool: "get_entity", Params: map[string]any{"fqn": "x"}},
	}
	for _, c := range calls {
		if err := l.LogToolCall(c); err != nil {
			t.Fatal(err)
		}
	}
	// 认证失败也计入被拒原因分布（unauthorized）
	if err := l.LogAuthFailure(day.Add(8*time.Hour), gwerr.Unauthorized("bad key")); err != nil {
		t.Fatal(err)
	}
	// 跨日触发聚合
	if err := l.LogToolCall(ToolCall{TS: testNow(), Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}

	m := parseLine(t, readLines(t, filepath.Join(dir, "summary-2026-08-11.jsonl"))[0])
	if m["date"] != "2026-08-11" {
		t.Fatalf("摘要 date = %v", m["date"])
	}
	callsAgg, ok := m["calls"].(map[string]any)
	if !ok {
		t.Fatalf("calls = %v", m["calls"])
	}
	exec := callsAgg["execute_sql"].(map[string]any)
	if exec["calls"] != float64(4) || exec["success"] != float64(1) ||
		exec["rejected"] != float64(1) || exec["timeout"] != float64(1) ||
		exec["parse_error"] != float64(1) {
		t.Errorf("execute_sql 分布 = %v，期望 calls=4 success=1 rejected=1 timeout=1 parse_error=1", exec)
	}
	if exec["rows"] != float64(100) || exec["truncated"] != float64(1) {
		t.Errorf("execute_sql 行数/截断 = %v，期望 rows=100 truncated=1", exec)
	}
	search := callsAgg["search_entities"].(map[string]any)
	if search["calls"] != float64(2) || search["success"] != float64(0) {
		t.Errorf("search_entities 分布 = %v", search)
	}
	rejects := m["rejects"].(map[string]any)
	if rejects["not_granted"] != float64(1) || rejects["timeout"] != float64(1) ||
		rejects["syntax_error"] != float64(1) || rejects["unauthorized"] != float64(1) {
		t.Errorf("被拒原因分布 = %v", rejects)
	}
	kws, ok := m["keywords"].([]any)
	if !ok || len(kws) != 1 {
		t.Fatalf("keywords = %v，期望 1 个去重关键词", m["keywords"])
	}
	kw := kws[0].(map[string]any)
	if kw["keyword"] != "支付失败" || kw["count"] != float64(2) {
		t.Errorf("keyword = %v，期望 支付失败×2", kw)
	}
}

// ── 重放（§6.4(b) 判定三件套前置）────────────────────────────────────────

// AC5：从 JSONL 可完整重放一次调用链——逐行解析出 工具/参数(SQL 原文)/状态/
// 行数/truncated/plan_id/分阶段耗时/被拒原因，重建调用顺序。
func TestReplayChain(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	ts := time.Now()
	rows := 2
	chain := []ToolCall{
		{TS: ts, User: "dev-alice", Key: "3", Tool: "search_entities", Status: StatusSuccess,
			Params: map[string]any{"query": "支付失败", "type": "metric"}},
		{TS: ts.Add(1 * time.Second), User: "dev-alice", Key: "3", Tool: "get_entity", Status: StatusSuccess,
			Params: map[string]any{"fqn": "bss.bss.orders"}},
		{TS: ts.Add(2 * time.Second), User: "dev-alice", Key: "3", Tool: "execute_sql",
			Params:   map[string]any{"sql": "SELECT count(*) FROM orders WHERE status='failed'", "dbname": "bss", "plan_id": "p9"},
			StagesMS: map[string]int64{StagePerm: 1, StageParse: 10, StageExec: 80},
			Status:   StatusSuccess, Rows: &rows, Truncated: ptr(false), PlanID: "p9"},
		{TS: ts.Add(3 * time.Second), User: "dev-alice", Key: "3", Tool: "execute_sql",
			Params: map[string]any{"sql": "SELECT * FROM secret"}, Status: StatusRejected,
			Reject: gwerr.PermissionDenied("未授权表", map[string]any{"reason": "not_granted"})},
	}
	for _, c := range chain {
		if err := l.LogToolCall(c); err != nil {
			t.Fatal(err)
		}
	}

	// 重放：读回并按顺序解析，逐项断言（工具/参数原文/状态/行数/plan_id/耗时/被拒原因）
	type replayed struct {
		tool, sql, status, planID string
		rows                      int
		truncated                 bool
		stages                    int
		rejectReason              string
	}
	var got []replayed
	for _, line := range readLines(t, filepath.Join(dir, rawName(dayOf(ts)))) {
		var rec ToolCall
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("重放解析失败: %v\n%s", err, line)
		}
		r := replayed{tool: rec.Tool, status: rec.Status, planID: rec.PlanID, stages: len(rec.StagesMS)}
		if p, ok := rec.Params.(map[string]any); ok {
			if s, ok := p["sql"].(string); ok {
				r.sql = s
			}
		}
		if rec.Rows != nil {
			r.rows = *rec.Rows
		}
		if rec.Truncated != nil {
			r.truncated = *rec.Truncated
		}
		if rec.Reject != nil {
			if reason, ok := rec.Reject.Details["reason"].(string); ok {
				r.rejectReason = reason
			}
		}
		got = append(got, r)
	}

	want := []replayed{
		{tool: "search_entities", status: StatusSuccess},
		{tool: "get_entity", status: StatusSuccess},
		{tool: "execute_sql", sql: "SELECT count(*) FROM orders WHERE status='failed'",
			status: StatusSuccess, rows: 2, planID: "p9", stages: 3},
		{tool: "execute_sql", sql: "SELECT * FROM secret", status: StatusRejected, rejectReason: "not_granted"},
	}
	if len(got) != len(want) {
		t.Fatalf("重放条数 = %d，期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("重放[%d] = %+v，期望 %+v", i, got[i], want[i])
		}
	}
}

// ── 启动校验 ──────────────────────────────────────────────────────────────

// 配置非法（保留期 <1 / 目录为空）→ 启动失败（配置错误 fail fast）。
func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Dir: "", RawRetentionDays: 7, SummaryRetentionDays: 30}); err == nil {
		t.Error("空目录应构造失败")
	}
	if _, err := New(Config{Dir: t.TempDir(), RawRetentionDays: 0, SummaryRetentionDays: 30}); err == nil {
		t.Error("原始保留期 0 应构造失败")
	}
	if _, err := New(Config{Dir: t.TempDir(), RawRetentionDays: 7, SummaryRetentionDays: 0}); err == nil {
		t.Error("摘要保留期 0 应构造失败")
	}
}

// 关闭后写入 → 错误（不静默吞掉）。
func TestWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	l := newTestLogger(t, dir, 7, 30, testNow)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.LogToolCall(ToolCall{TS: time.Now(), Tool: "x"}); err == nil {
		t.Error("关闭后写入应报错")
	}
}

// 文件权限（对抗评审收敛 P2）：SQL 原文落盘的世界可读性由代码闭合——
// 目录 0700 / 文件 0600（宿主机权限即访问边界，ADR-0006 不依赖部署 umask）。
func TestFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l := newTestLogger(t, dir, 7, 30, testNow)
	if err := l.LogToolCall(ToolCall{TS: testNow(), Tool: "execute_sql", Status: StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("日志目录权限 = %o，期望 0700", perm)
	}
	fi, err := os.Stat(filepath.Join(dir, rawName(dayOf(testNow()))))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("原始文件权限 = %o，期望 0600", perm)
	}
}

// ptr 是 *bool 构造助手。
func ptr(b bool) *bool { return &b }
