package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
)

// fixtureManifest 加载 golden 测试清单（testdata/manifest.yaml）。
func fixtureManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := LoadManifest(filepath.Join("testdata", "manifest.yaml"))
	if err != nil {
		t.Fatalf("加载 fixture 清单: %v", err)
	}
	return m
}

// collectFixture 对 fixture 语料跑完整采集（GORM 开）。
func collectFixture(t *testing.T, m *Manifest) *CollectResult {
	t.Helper()
	out := t.TempDir()
	res, err := Collect(CollectConfig{
		Repo:     filepath.Join("testdata", "neo-cloud"),
		Manifest: m,
		GORM:     true,
		OutDir:   out,
	})
	if err != nil {
		t.Fatalf("采集 fixture: %v", err)
	}
	return res
}

// TestGoldenDrafts 真实语料 golden test：迁移语料 → 草稿字节与
// golden 文件逐字节一致（ADR-0007「真实迁移语料即测试集」）。
// 环境变量 UPDATE_GOLDEN=1 时重写 golden（语料更新后的例行操作，
// 必须人工 review diff）。
func TestGoldenDrafts(t *testing.T) {
	m := fixtureManifest(t)
	res := collectFixture(t, m)
	for _, sr := range res.Services {
		got, err := os.ReadFile(filepath.Join(res.OutDir, "services", sr.Name+".yaml"))
		if err != nil {
			t.Fatalf("读草稿 %s: %v", sr.Name, err)
		}
		golden := filepath.Join("testdata", "golden", "services", sr.Name+".yaml")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatalf("写 golden %s: %v", golden, err)
			}
			t.Logf("已更新 golden %s", golden)
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("读 golden %s（语料变更后先 UPDATE_GOLDEN=1 重新生成并 review）: %v", golden, err)
		}
		if string(got) != string(want) {
			t.Errorf("草稿 %s.yaml 与 golden 不一致（diff 见下，确认有意变更后 UPDATE_GOLDEN=1）\n--- got ---\n%s\n--- want ---\n%s",
				sr.Name, got, want)
		}
	}
}

// TestGoldenDeterminism 同输入同输出：同一语料采集两次，输出逐字节
// 一致（确定性契约，采集器正确性的第一道闸）。
func TestGoldenDeterminism(t *testing.T) {
	m := fixtureManifest(t)
	first := collectFixture(t, m)
	second := collectFixture(t, m)
	for _, sr := range first.Services {
		a, err := os.ReadFile(filepath.Join(first.OutDir, "services", sr.Name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(second.OutDir, "services", sr.Name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("服务 %s 两次采集输出不一致（确定性破坏）", sr.Name)
		}
	}
}

// TestCompileCompatibility 采集产出与 07 同步管线编译校验兼容
// （第三道闸：semantic.Load + Compile 全过 = 可进同步管线）。
func TestCompileCompatibility(t *testing.T) {
	m := fixtureManifest(t)
	res := collectFixture(t, m)
	if res.CompileErr != nil {
		t.Fatalf("采集产出编译校验失败: %v", res.CompileErr)
	}
	// 直接验证 Load+Compile 路径（与同步管线同一入口）。
	in, err := semantic.Load(res.OutDir)
	if err != nil {
		t.Fatalf("semantic.Load: %v", err)
	}
	if _, err := semantic.Compile(in); err != nil {
		t.Fatalf("semantic.Compile: %v", err)
	}
}

// TestGoldenFindings 交叉验证发现确定性：fixture 语料的 GORM 门禁
// 发现与 golden 文件逐行一致（发现是采集产物的一部分）。
func TestGoldenFindings(t *testing.T) {
	m := fixtureManifest(t)
	res := collectFixture(t, m)
	var lines []string
	for _, sr := range res.Services {
		for _, f := range sr.Findings {
			lines = append(lines, sr.Name+" | "+f.String())
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	golden := filepath.Join("testdata", "golden", "findings.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("写 golden %s: %v", golden, err)
		}
		t.Logf("已更新 golden %s", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("读 golden %s: %v", golden, err)
	}
	if got != string(want) {
		t.Errorf("交叉验证发现与 golden 不一致（确认有意变更后 UPDATE_GOLDEN=1）\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestFullCorpusScan 全量验收（acceptance「对 neo-cloud 全部持库服务
// 运行采集」）：真实 neo-cloud 存在时跑全量采集 + 编译兼容；
// 语料不在时跳过（CI 环境无业务仓库）。
func TestFullCorpusScan(t *testing.T) {
	repo := os.Getenv("NEO_CLOUD_REPO")
	if repo == "" {
		repo = os.ExpandEnv("$HOME/cloud/neo-cloud")
	}
	if _, err := os.Stat(repo); err != nil {
		t.Skipf("neo-cloud 不在 %s（设 NEO_CLOUD_REPO 指向业务仓库后运行全量验收）", repo)
	}
	m, err := LoadManifest(filepath.Join("..", "..", "samples", "collector", "manifest.yaml"))
	if err != nil {
		t.Fatalf("加载全量清单: %v", err)
	}
	res, err := Collect(CollectConfig{
		Repo:     repo,
		Manifest: m,
		GORM:     true,
		OutDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("全量采集: %v", err)
	}
	if len(res.Services) != len(m.Services) {
		t.Errorf("采集服务数 = %d, want %d", len(res.Services), len(m.Services))
	}
	if res.CompileErr != nil {
		t.Errorf("全量采集产出编译校验失败: %v", res.CompileErr)
	}
}
