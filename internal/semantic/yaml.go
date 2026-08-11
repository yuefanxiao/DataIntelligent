package semantic

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 作者入口目录约定（ADR-0002「按服务拆 YAML + 全局指标/概念文件」）：
//
//	<repo>/
//	  services/<service>.yaml   每服务一个文件（结构：库/表/列/枚举/references）
//	  metrics.yaml              全局指标（OSI 式口径）
//	  concepts.yaml             全局业务概念（describes）
//
// 目录内其余文件（README 等）忽略；文件名即服务名（不含扩展名）。

const (
	metricsFileName  = "metrics.yaml"
	conceptsFileName = "concepts.yaml"
	yamlVersion      = 1
)

// serviceFile 是单个服务文件的结构（services/<service>.yaml）。
type serviceFile struct {
	Version int `yaml:"version"`
	// Service 是服务名（FQN 第一段）；缺省取文件名。
	Service     string        `yaml:"service"`
	Description string        `yaml:"description"`
	Databases   []databaseDef `yaml:"databases"`
}

type databaseDef struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Tables      []tableDef `yaml:"tables"`
}

type tableDef struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Schema      string         `yaml:"schema"` // PG schema（缺省按 dbname 路由推断）
	Columns     []columnDef    `yaml:"columns"`
	References  []referenceDef `yaml:"references"`
}

type columnDef struct {
	Name        string         `yaml:"name"`
	Type        string         `yaml:"type"`
	Description string         `yaml:"description"`
	IsTime      bool           `yaml:"is_time"`
	EnumValues  []enumValueDef `yaml:"enum_values"`
}

type enumValueDef struct {
	Value string `yaml:"value"`
	Label string `yaml:"label"`
}

// referenceDef 是表↔表 join 条件（references 边）。
type referenceDef struct {
	// To 是目标表 FQN（服务.库.表）。
	To string `yaml:"to"`
	// On 是 join 条件（如 "payments.order_id = orders.id"）。
	On string `yaml:"on"`
}

// globalFile 是全局指标/概念文件的结构（metrics.yaml / concepts.yaml）。
type globalFile struct {
	Version  int          `yaml:"version"`
	Metrics  []metricDef  `yaml:"metrics"`
	Concepts []conceptDef `yaml:"concepts"`
}

type metricDef struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Expression  string `yaml:"expression"` // SQL 表达式（机器可读口径）
	Aggregation string `yaml:"aggregation"`
	Filter      string `yaml:"filter"`
	// Tables 是指标依赖的底层表（服务.库.表）——describes 边 + 授权展开依据。
	Tables []string `yaml:"tables"`
}

type conceptDef struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Describes 是概念描述的实体 FQN（表/列/指标，服务.库.表[.列] 或指标名）。
	Describes []string `yaml:"describes"`
}

// Load 读取作者入口目录并返回解析后的原始定义（未编译）。
// 目录缺 services/、metrics.yaml、concepts.yaml 不报错（空语义层合法）；
// 文件存在但解析失败 = 编译错误（原子拒绝）。
func Load(dir string) (*rawInput, error) {
	in := &rawInput{}
	if err := loadServices(filepath.Join(dir, "services"), in); err != nil {
		return nil, err
	}
	if err := loadGlobal(filepath.Join(dir, metricsFileName), in, false); err != nil {
		return nil, err
	}
	if err := loadGlobal(filepath.Join(dir, conceptsFileName), in, true); err != nil {
		return nil, err
	}
	return in, nil
}

// rawInput 是编译前的原始定义集合。
type rawInput struct {
	services []serviceFile
	metrics  []metricDef
	concepts []conceptDef
}

func loadServices(dir string, in *rawInput) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // 无 services 目录 = 空
	}
	if err != nil {
		return fmt.Errorf("读取语义目录 %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		names = append(names, name)
		files[name] = filepath.Join(dir, e.Name())
	}
	sort.Strings(names) // 确定性：文件名顺序编译，错误信息稳定
	for _, name := range names {
		var f serviceFile
		if err := parseYAMLFile(files[name], &f); err != nil {
			return err
		}
		if f.Version != yamlVersion {
			return fmt.Errorf("语义 YAML %s: version = %d, want %d", files[name], f.Version, yamlVersion)
		}
		if f.Service == "" {
			f.Service = name
		}
		in.services = append(in.services, f)
	}
	return nil
}

func loadGlobal(path string, in *rawInput, concepts bool) error {
	var f globalFile
	if err := parseYAMLFile(path, &f); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if f.Version != yamlVersion {
		return fmt.Errorf("语义 YAML %s: version = %d, want %d", path, f.Version, yamlVersion)
	}
	if concepts {
		in.concepts = f.Concepts
	} else {
		in.metrics = f.Metrics
	}
	return nil
}

// parseYAMLFile 解析一个 YAML 文件；文件不存在返回 os.ErrNotExist。
// KnownFields 强制：作者入口拼错字段名必须失败（口径权威，不静默忽略）。
func parseYAMLFile(path string, out any) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	if err := parseYAML(fh, out); err != nil {
		return fmt.Errorf("解析语义 YAML %s: %w", path, err)
	}
	return nil
}

func parseYAML(r io.Reader, out any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	return dec.Decode(out)
}
