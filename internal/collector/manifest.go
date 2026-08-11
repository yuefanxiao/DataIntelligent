package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest 是采集清单：把服务仓库目录映射到生产形态
// （ADR-0007「每服务一库/schema 前缀」——生产库名不从 docker-compose
// 或服务配置推断，各服务命名不统一（notification / iam_audit /
// bss_invoice…），清单是显式、可 git review 的事实源）。
type Manifest struct {
	Version  int               `yaml:"version"`
	Services []ManifestService `yaml:"services"`
}

// ManifestService 是清单里一个服务条目。
type ManifestService struct {
	// Name 是服务名（FQN 第一段，语义作者入口文件名）。
	Name string `yaml:"name"`
	// Dir 是服务目录（相对 repo root）。
	Dir string `yaml:"dir"`
	// DB 是生产库名（每服务一库，FQN 第二段）；空 = 服务无持库
	// （纯编排/聚合/事件采集类服务：仍产出服务实体草稿，无表结构，
	// 保证语义层覆盖全部后端服务）。
	DB string `yaml:"db,omitempty"`
	// ModelsDir 是 GORM 模型目录（相对服务目录，缺省 internal/data）。
	ModelsDir string `yaml:"models_dir,omitempty"`
}

const manifestVersion = 1

// LoadManifest 读取采集清单（路径不存在 = 错误，避免把错误路径当成空清单）。
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取采集清单 %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析采集清单 %s: %w", path, err)
	}
	if m.Version != manifestVersion {
		return nil, fmt.Errorf("采集清单 %s: version = %d, want %d", path, m.Version, manifestVersion)
	}
	seen := map[string]bool{}
	for i := range m.Services {
		s := &m.Services[i]
		if s.Name == "" || s.Dir == "" {
			return nil, fmt.Errorf("采集清单 %s: 第 %d 个服务缺 name/dir 字段", path, i+1)
		}
		// 服务名/库名是 FQN 段与草稿文件名：只允许标识符形态
		// （防路径穿越——name 直接拼进 services/<name>.yaml）。
		if !identRe.MatchString(s.Name) {
			return nil, fmt.Errorf("采集清单 %s: 服务名 %q 非法（只允许小写字母/数字/连字符/下划线，且不以连字符开头）", path, s.Name)
		}
		if s.DB != "" && !identRe.MatchString(s.DB) {
			return nil, fmt.Errorf("采集清单 %s: 服务 %s 的库名 %q 非法（只允许小写字母/数字/连字符/下划线，且不以连字符开头）", path, s.Name, s.DB)
		}
		// dir/models_dir 是相对 repo root/服务目录的路径：
		// 拒绝绝对路径与 .. 逃逸（models_dir 缺省 internal/data）。
		if filepath.IsAbs(s.Dir) || strings.Contains(s.Dir, "..") {
			return nil, fmt.Errorf("采集清单 %s: 服务 %s 的 dir %q 必须相对且不含 ..", path, s.Name, s.Dir)
		}
		if filepath.IsAbs(s.ModelsDir) || strings.Contains(s.ModelsDir, "..") {
			return nil, fmt.Errorf("采集清单 %s: 服务 %s 的 models_dir %q 必须相对且不含 ..", path, s.Name, s.ModelsDir)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("采集清单 %s: 服务名重复 %q", path, s.Name)
		}
		seen[s.Name] = true
		if s.ModelsDir == "" {
			s.ModelsDir = "internal/data"
		}
	}
	return &m, nil
}

// identRe 是服务名/库名的合法形态（FQN 段 + 草稿文件名双约束；
// 下划线放行——生产库名含下划线，如 bss_invoice/iam_audit）。
var identRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Find 按服务名找清单条目。
func (m *Manifest) Find(name string) (*ManifestService, error) {
	for i := range m.Services {
		if m.Services[i].Name == name {
			return &m.Services[i], nil
		}
	}
	return nil, fmt.Errorf("采集清单里没有服务 %q", name)
}

// ServiceDir 返回服务目录绝对路径。
func (s *ManifestService) ServiceDir(repo string) string {
	return filepath.Join(repo, s.Dir)
}
