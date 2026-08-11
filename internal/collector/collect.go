package collector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ServiceResult 是一个服务的一次采集结果。
type ServiceResult struct {
	Name     string
	DB       string
	Tables   int
	Columns  int
	Enums    int
	Refs     int
	Findings []Finding
	// Schema 是表的主要 schema 前缀（"" = public；信息用）。
	Schema string
}

// Errors 返回该服务的 error 级发现（门禁判定用）。
func (r *ServiceResult) Errors() int { return countSeverity(r.Findings, "error") }

// CollectConfig 是 scan 子命令的输入。
type CollectConfig struct {
	Repo     string // 服务仓库根（monorepo root）
	Manifest *Manifest
	// Service 非空时只采集清单里这一个服务（其余跳过）。
	Service string
	// GORM 交叉验证开关（缺省 true）。
	GORM bool
	// OutDir 是草稿输出目录（services/ 子目录）。
	OutDir string
}

// CollectResult 是一次 scan 的全部产物。
type CollectResult struct {
	Services []*ServiceResult
	// CompileErr 是采集产出过 07 编译校验的失败（第三道闸）；
	// 非 nil 时草稿仍已写出（给人 review），但门禁失败。
	CompileErr error
	// OutDir 是草稿输出目录。
	OutDir string
}

// Collect 跑完整采集：manifest → 逐服务（迁移解析 → GORM 交叉验证 →
// 草稿写出）→ 全量编译兼容检查。按清单顺序处理（确定性）。
func Collect(cfg CollectConfig) (*CollectResult, error) {
	if cfg.Manifest == nil {
		return nil, fmt.Errorf("Collect 需要非空 Manifest")
	}
	res := &CollectResult{OutDir: cfg.OutDir}
	for i := range cfg.Manifest.Services {
		ms := &cfg.Manifest.Services[i]
		if cfg.Service != "" && ms.Name != cfg.Service {
			continue
		}
		sr, err := collectService(cfg, ms)
		if err != nil {
			return nil, err
		}
		res.Services = append(res.Services, sr)
	}
	if len(res.Services) == 0 {
		return nil, fmt.Errorf("没有采集任何服务（清单为空或 --service %q 不在清单里）", cfg.Service)
	}
	// 协调残稿：全量扫描（--service 为空）时，services/ 里不在清单中
	// 的旧草稿（服务从清单移除后）必须清掉——全量重建语义，避免陈旧
	// 服务混进门禁与同步管线。增量扫描（--service）不动其他服务草稿。
	if cfg.Service == "" {
		if err := reconcileDrafts(cfg.OutDir, cfg.Manifest); err != nil {
			return nil, err
		}
	}
	// 第三道闸：采集产出必须过同步管线编译校验（可进同步管线）。
	res.CompileErr = CheckCompile(cfg.OutDir)
	return res, nil
}

// reconcileDrafts 删除 services/ 下不在清单里的草稿文件（幂等）。
func reconcileDrafts(outDir string, m *Manifest) error {
	dir := filepath.Join(outDir, "services")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取草稿目录 %s: %w", dir, err)
	}
	inManifest := map[string]bool{}
	for _, s := range m.Services {
		inManifest[s.Name] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		if inManifest[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("清理陈旧草稿 %s: %w", e.Name(), err)
		}
		fmt.Printf("dgw: 清理陈旧草稿 services/%s.yaml（不在采集清单中）\n", name)
	}
	return nil
}

// collectService 采集一个服务：解析迁移 → 交叉验证 → 写草稿。
func collectService(cfg CollectConfig, ms *ManifestService) (*ServiceResult, error) {
	st, findings, err := ParseServiceMigrations(ms, cfg.Repo)
	if err != nil {
		return nil, err
	}
	findings = append(findings, unresolvedRefFindings(st)...)

	if cfg.GORM {
		models, gormFindings := ExtractGormModels(filepath.Join(ms.ServiceDir(cfg.Repo), ms.ModelsDir))
		findings = append(findings, gormFindings...)
		findings = append(findings, CrossCheck(st, models)...)
	}

	sr := &ServiceResult{Name: ms.Name, DB: ms.DB, Findings: findings}
	sr.Tables, sr.Columns, sr.Enums, sr.Refs = st.stats()
	sr.Schema = distinctSchemas(st)

	if _, err := WriteDraft(cfg.OutDir, st); err != nil {
		return nil, err
	}
	return sr, nil
}

// PrintFindings 打印服务发现（CLI 用）；返回 error 级数量。
func (r *ServiceResult) PrintFindings() int {
	for _, f := range r.Findings {
		fmt.Printf("  %s\n", f.String())
	}
	return r.Errors()
}

// ParseServiceMigrations 是「按清单条目解析一个服务的迁移」的共享入口
// （scan 与 calibrate 的基线同源：calibrate 对照的就是草稿的推导源）。
func ParseServiceMigrations(ms *ManifestService, repo string) (*Structure, []Finding, error) {
	files, err := DiscoverMigrations(ms.ServiceDir(repo))
	if err != nil {
		return nil, nil, fmt.Errorf("服务 %s: %w", ms.Name, err)
	}
	st, findings := ParseMigrations(ms.Name, ms.DB, files)
	return st, findings, nil
}

// unresolvedRefFindings 报告引用边目标不在本服务结构内的发现
// （跨服务 FK 无法编 FQN——目标服务名未知；提示人工确认，
// 采集产出的引用边只保留同服务目标，draft.go 侧同样跳过）。
func unresolvedRefFindings(st *Structure) []Finding {
	var fs []Finding
	for _, t := range st.Tables {
		for _, r := range t.References {
			if st.findTable(r.TargetTable) == nil {
				fs = append(fs, Finding{SourceMigration, SeverityWarn,
					fmt.Sprintf("表 %s 的外键引用目标 %s 不在本服务结构内（跨服务 FK 不进草稿引用边，请人工确认）",
						t.Name, r.TargetTable)})
			}
		}
	}
	sortFindings(fs)
	return fs
}

// distinctSchemas 返回服务结构的去重 schema 列表（信息性输出；
// "" 显示为 public）。
func distinctSchemas(st *Structure) string {
	seen := map[string]bool{}
	var schemas []string
	for _, t := range st.Tables {
		s := t.Schema
		if s == "" {
			s = "public"
		}
		if !seen[s] {
			seen[s] = true
			schemas = append(schemas, s)
		}
	}
	sort.Strings(schemas)
	return strings.Join(schemas, ",")
}
