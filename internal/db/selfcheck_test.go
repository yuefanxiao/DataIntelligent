package db

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 启动自检 e2e（ADR-0009 两条硬校验的真实形态验证）：Docker 起「主 + 从」
// 流复制栈——主库正常；从库容器启动时先 pg_basebackup（-R 写
// primary_conninfo + standby.signal）再以 hot_standby 启动，模拟 CNPG
// 一主两从拓扑的单从形态。共享只读角色 dgw_ro 建在主库（经 WAL 复制到从库）。
//
// 用例覆盖三条路径：
//   - 通过：DSN 指向从库 + 角色级 statement_timeout 生效 → 自检通过；
//   - 拒启（连错主库）：DSN 指向主库 → pg_is_in_recovery() = false；
//   - 拒启（角色级超时未生效）：RESET 角色 statement_timeout → 纯净连接
//     SHOW 回落默认值，与配置不符。
//
// docker 不可用（CI 等）→ 整文件跳过，不影响其他包（与 gateway e2e 同一惯例）。

var (
	pgOK      bool
	pgNet     string // 测试用 docker 网络
	pgPrimary string // 主库容器名
	pgReplica string // 从库容器名
	primPort  int    // 主库宿主端口（拒启用例直连）
	replPort  int    // 从库宿主端口（产品 DSN 目标）
	replDSN   string // dgw_ro × 从库 bss
	primDSN   string // dgw_ro × 主库 bss（连错主库演示）
)

func TestMain(m *testing.M) {
	if err := startPGReplicaStack(); err != nil {
		fmt.Fprintf(os.Stderr, "selfcheck 主从 PG（Docker）不可用，跳过真实实例测试: %v\n", err)
	} else {
		pgOK = true
	}
	code := m.Run()
	if pgOK {
		stopPGReplicaStack()
	}
	os.Exit(code)
}

// startPGReplicaStack 起主从栈 + provisioning（角色/库/表/授权/角色级超时）。
func startPGReplicaStack() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("缺少 docker 可执行文件")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		return fmt.Errorf("docker daemon 不可用: %v\n%s", err, out)
	}
	primPort = freePort()
	replPort = freePort()
	pgNet = fmt.Sprintf("dgw-test-net-%d-%d", os.Getpid(), replPort)
	pgPrimary = fmt.Sprintf("dgw-test-pri-%d", os.Getpid())
	pgReplica = fmt.Sprintf("dgw-test-rep-%d", os.Getpid())

	run := func(args ...string) error {
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("docker %s 失败: %v\n%s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	_ = run("network", "rm", pgNet) // 幂等重跑
	if err := run("network", "create", pgNet); err != nil {
		return err
	}
	// 主库：wal_level=replica + walsender/槽数（流复制源），trust 认证。
	if err := run("run", "-d", "--rm", "--network", pgNet, "--name", pgPrimary,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", primPort),
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust",
		"postgres:17",
		"postgres", "-c", "listen_addresses=*",
		"-c", "wal_level=replica", "-c", "max_wal_senders=10", "-c", "max_replication_slots=10",
	); err != nil {
		stopPGReplicaStack()
		return err
	}
	if err := waitCmd(60*time.Second, func() bool {
		out, err := exec.Command("docker", "exec", "-u", "postgres", pgPrimary,
			"pg_isready", "-U", "postgres", "-h", "127.0.0.1").CombinedOutput()
		return err == nil && strings.Contains(string(out), "accepting")
	}); err != nil {
		stopPGReplicaStack()
		return fmt.Errorf("主库就绪超时: %v", err)
	}
	// 官方镜像 trust 只对普通连接全开；流复制连接需显式追加 pg_hba 行
	// （replication 源 = docker 网络段）并 reload。
	if err := exec.Command("docker", "exec", pgPrimary, "sh", "-c",
		`echo "host replication all all trust" >> /var/lib/postgresql/data/pg_hba.conf`).Run(); err != nil {
		stopPGReplicaStack()
		return fmt.Errorf("追加 replication pg_hba 失败: %v", err)
	}
	if out, err := exec.Command("docker", "exec", pgPrimary, "psql", "-U", "postgres",
		"-tAc", "SELECT pg_reload_conf()").CombinedOutput(); err != nil {
		stopPGReplicaStack()
		return fmt.Errorf("pg_reload_conf 失败: %v\n%s", err, out)
	}

	// 从库：启动时 basebackup 拉主库全量（-R 写 primary_conninfo +
	// standby.signal），再以 hot_standby 启动（绕过 entrypoint，全手动）。
	// chmod 700：镜像 VOLUME 目录初始为 root 属主，basebackup 后需修正
	// 数据目录权限 postgres 才能启动。
	if err := run("run", "-d", "--rm", "--network", pgNet, "--name", pgReplica,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", replPort),
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust",
		"postgres:17",
		"sh", "-c",
		fmt.Sprintf("until pg_isready -h %s -U postgres -q; do sleep 1; done; "+
			"gosu postgres pg_basebackup -h %s -U postgres -D /var/lib/postgresql/data -R -X stream && "+
			"chmod 700 /var/lib/postgresql/data && "+
			"exec gosu postgres postgres -c listen_addresses=* -c hot_standby=on",
			pgPrimary, pgPrimary),
	); err != nil {
		stopPGReplicaStack()
		return err
	}
	// 从库进入 recovery 形态（pg_is_in_recovery() = true）。
	if err := waitCmd(120*time.Second, func() bool {
		out, err := exec.Command("docker", "exec", pgReplica, "psql", "-U", "postgres",
			"-tAc", "SELECT pg_is_in_recovery()").CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) == "t"
	}); err != nil {
		stopPGReplicaStack()
		return fmt.Errorf("从库 recovery 就绪超时: %v", err)
	}

	// provisioning 建在主库（WAL 复制到从库；角色配置/库/表/授权全量复制）。
	psql := func(dbname string, stmts ...string) error {
		args := []string{"exec", "-u", "postgres", pgPrimary, "psql",
			"-U", "postgres", "-h", "127.0.0.1", "-d", dbname, "-v", "ON_ERROR_STOP=1", "-q"}
		for _, s := range stmts {
			args = append(args, "-c", s)
		}
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("psql(%s) 失败: %v\n%s", dbname, err, out)
		}
		return nil
	}
	if err := psql("postgres",
		"CREATE ROLE dgw_ro LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT",
		"CREATE DATABASE bss",
		"ALTER ROLE dgw_ro SET statement_timeout = '30s'",
	); err != nil {
		stopPGReplicaStack()
		return err
	}
	if err := psql("bss",
		"CREATE TABLE orders (id bigint PRIMARY KEY, status text NOT NULL)",
		"INSERT INTO orders VALUES (1, 'paid'), (2, 'refunded')",
		"GRANT SELECT ON orders TO dgw_ro",
	); err != nil {
		stopPGReplicaStack()
		return err
	}

	// 等 WAL 追平：从库上 dgw_ro 能查 bss.orders（角色/库/表/超时全就位）。
	if err := waitCmd(60*time.Second, func() bool {
		out, err := exec.Command("docker", "exec", pgReplica, "psql", "-U", "dgw_ro",
			"-d", "bss", "-tAc", "SELECT count(*) FROM orders").CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) == "2"
	}); err != nil {
		stopPGReplicaStack()
		return fmt.Errorf("从库 WAL 追平超时: %v", err)
	}

	replDSN = fmt.Sprintf("postgres://dgw_ro@127.0.0.1:%d/bss?sslmode=disable", replPort)
	primDSN = fmt.Sprintf("postgres://dgw_ro@127.0.0.1:%d/bss?sslmode=disable", primPort)
	return nil
}

func stopPGReplicaStack() {
	if pgReplica != "" {
		exec.Command("docker", "rm", "-f", pgReplica).Run()
		pgReplica = ""
	}
	if pgPrimary != "" {
		exec.Command("docker", "rm", "-f", pgPrimary).Run()
		pgPrimary = ""
	}
	if pgNet != "" {
		exec.Command("docker", "network", "rm", pgNet).Run()
		pgNet = ""
	}
}

// waitCmd 轮询 cond 直到为真或超时（测试基建通用）。
func waitCmd(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("等待超时（%v）", timeout)
}

// freePort 拿一个随机空闲端口（listen :0 后关闭——竞态可接受，测试专用）。
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 55432
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func requirePG(t *testing.T) {
	t.Helper()
	if !pgOK {
		t.Skip("本机无 docker/PG 容器，跳过真实实例 e2e")
	}
}

// selfRouter 建单条 dbname 路由（自检探测对象）。
func selfRouter(t *testing.T, dsn string) *Router {
	t.Helper()
	r, err := NewRouter(context.Background(), []Entry{{DBName: "bss", Service: "bss", DSN: dsn}}, 30*time.Second)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

// ── 单元：SHOW 值解析 ────────────────────────────────────────────────────

func TestParseTimeoutMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"30000", 30000, true},   // 纯数字 = 毫秒
		{"30s", 30000, true},     // 秒
		{"30000ms", 30000, true}, // 毫秒后缀
		{"1min", 60000, true},    // 分钟
		{"2h", 7200000, true},    // 小时
		{"1d", 86400000, true},   // 天
		{"1.5s", 1500, true},     // 小数秒
		{"0", 0, true},           // 未设置 = 0（PG 默认关闭超时）
		{"", 0, false},           // 空
		{"abc", 0, false},        // 垃圾
		{"30 s", 0, false},       // 带空格（PG SHOW 不产生，拒绝未知形态）
		{"-5s", -5000, true},     // 负值可解析（上层按不相等拒启）
	}
	for _, c := range cases {
		got, err := parseTimeoutMs(c.in)
		if c.ok && err != nil {
			t.Errorf("parseTimeoutMs(%q) 不应报错: %v", c.in, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("parseTimeoutMs(%q) 应报错，得到 %d", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("parseTimeoutMs(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestDBNames(t *testing.T) {
	r, err := NewRouter(context.Background(), []Entry{
		{DBName: "iam", Service: "iam", DSN: "postgres://u@h/iam"},
		{DBName: "bss", Service: "bss", DSN: "postgres://u@h/bss"},
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer r.Close()
	got := strings.Join(r.DBNames(), ",")
	if got != "bss,iam" {
		t.Fatalf("DBNames = %q，期望排序 bss,iam", got)
	}
}

// ── e2e：两条硬校验 ──────────────────────────────────────────────────────

func TestSelfCheck(t *testing.T) {
	requirePG(t)

	t.Run("通过：从库 + 角色级 timeout 生效", func(t *testing.T) {
		r := selfRouter(t, replDSN)
		if err := r.SelfCheck(context.Background(), 30*time.Second); err != nil {
			t.Fatalf("自检应通过: %v", err)
		}
	})

	t.Run("拒启：连到主库（pg_is_in_recovery = false）", func(t *testing.T) {
		r := selfRouter(t, primDSN)
		err := r.SelfCheck(context.Background(), 30*time.Second)
		if err == nil {
			t.Fatal("连主库应拒启（无错误返回）")
		}
		if !strings.Contains(err.Error(), "pg_is_in_recovery") || !strings.Contains(err.Error(), "主库") {
			t.Fatalf("错误应点名主库问题，得到: %v", err)
		}
	})

	t.Run("拒启：角色级 statement_timeout 未生效", func(t *testing.T) {
		// RESET 角色配置（主库改、WAL 复制到从库；poll 等生效）。
		exec.Command("docker", "exec", "-u", "postgres", pgPrimary, "psql",
			"-U", "postgres", "-h", "127.0.0.1", "-d", "postgres",
			"-c", "ALTER ROLE dgw_ro RESET statement_timeout").Run()
		if err := waitCmd(60*time.Second, func() bool {
			out, err := exec.Command("docker", "exec", pgReplica, "psql", "-U", "dgw_ro",
				"-d", "bss", "-tAc", "SHOW statement_timeout").CombinedOutput()
			return err == nil && strings.TrimSpace(string(out)) != "30s"
		}); err != nil {
			t.Fatalf("角色超时 RESET 未复制到从库: %v", err)
		}
		r := selfRouter(t, replDSN)
		err := r.SelfCheck(context.Background(), 30*time.Second)
		if err == nil {
			t.Fatal("角色级超时未生效应拒启（无错误返回）")
		}
		if !strings.Contains(err.Error(), "statement_timeout") {
			t.Fatalf("错误应点名 statement_timeout，得到: %v", err)
		}
		// 恢复（后续用例依赖角色边界）。
		exec.Command("docker", "exec", "-u", "postgres", pgPrimary, "psql",
			"-U", "postgres", "-h", "127.0.0.1", "-d", "postgres",
			"-c", "ALTER ROLE dgw_ro SET statement_timeout = '30s'").Run()
		if err := waitCmd(60*time.Second, func() bool {
			out, err := exec.Command("docker", "exec", pgReplica, "psql", "-U", "dgw_ro",
				"-d", "bss", "-tAc", "SHOW statement_timeout").CombinedOutput()
			return err == nil && strings.TrimSpace(string(out)) == "30s"
		}); err != nil {
			t.Fatalf("角色超时恢复未复制到从库: %v", err)
		}
	})

	t.Run("拒启：目标不可达（连不上 = 拒启）", func(t *testing.T) {
		dead := fmt.Sprintf("postgres://dgw_ro@127.0.0.1:%d/bss?sslmode=disable", freePort())
		r := selfRouter(t, dead)
		err := r.SelfCheck(context.Background(), 30*time.Second)
		if err == nil {
			t.Fatal("连不上应拒启（无错误返回）")
		}
		if !strings.Contains(err.Error(), "连接失败") {
			t.Fatalf("错误应点名连接失败，得到: %v", err)
		}
	})

	t.Run("配置不一致拒启：角色 30s vs 配置 15s", func(t *testing.T) {
		r := selfRouter(t, replDSN)
		if err := r.SelfCheck(context.Background(), 15*time.Second); err == nil {
			t.Fatal("角色 30s vs 配置 15s 应拒启")
		}
	})
}
