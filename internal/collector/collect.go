package collector

import (
	"fmt"
	"path/filepath"
	"sort"
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
	// 第三道闸：采集产出必须过同步管线编译校验（可进同步管线）。
	res.CompileErr = CheckCompile(cfg.OutDir)
	return res, nil
}

// collectService 采集一个服务：解析迁移 → 交叉验证 → 写草稿。
func collectService(cfg CollectConfig, ms *ManifestService) (*ServiceResult, error) {
	dir := ms.ServiceDir(cfg.Repo)
	files, err := DiscoverMigrations(dir)
	if err != nil {
		return nil, fmt.Errorf("服务 %s: %w", ms.Name, err)
	}
	st, findings := ParseMigrations(ms.Name, ms.DB, files)

	if cfg.GORM {
		models, gormFindings := ExtractGormModels(filepath.Join(dir, ms.ModelsDir))
		findings = append(findings, gormFindings...)
		findings = append(findings, CrossCheck(st, models)...)
	}

	sr := &ServiceResult{Name: ms.Name, DB: ms.DB, Findings: findings}
	sr.Tables, sr.Columns, sr.Enums, sr.Refs = st.stats()
	if len(st.Tables) > 0 {
		sr.Schema = st.Tables[0].Schema
	}

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

// SortedServiceNames 返回已采集服务的名字（确定性输出）。
func (r *CollectResult) SortedServiceNames() []string {
	names := make([]string, 0, len(r.Services))
	for _, s := range r.Services {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}
