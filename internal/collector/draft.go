package collector

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
)

// draftFile 是采集产出的服务 YAML 草稿结构（与 07 同步管线作者入口
// services/<service>.yaml 同构——semantic.Load + Compile 直接兼容）。
// 语义字段（description/is_time/enum label）留空：语义知识人工
// （ADR-0007「结构自动、语义人工」），草稿只承载结构。
type draftFile struct {
	Version     int       `yaml:"version"`
	Service     string    `yaml:"service"`
	Description string    `yaml:"description,omitempty"`
	Databases   []draftDB `yaml:"databases"`
}

type draftDB struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Tables      []draftTable `yaml:"tables"`
}

type draftTable struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Schema      string        `yaml:"schema,omitempty"`
	Columns     []draftColumn `yaml:"columns"`
	References  []draftRef    `yaml:"references,omitempty"`
}

type draftColumn struct {
	Name        string      `yaml:"name"`
	Type        string      `yaml:"type"`
	Description string      `yaml:"description,omitempty"`
	EnumValues  []draftEnum `yaml:"enum_values,omitempty"`
}

type draftEnum struct {
	Value string `yaml:"value"`
	Label string `yaml:"label,omitempty"`
}

type draftRef struct {
	To string `yaml:"to"`
	On string `yaml:"on"`
}

const yamlVersion = 1

// RenderDraft 把结构中间态渲染为 YAML 草稿字节。
// 确定性：表按名字典序、列按 DDL 顺序、枚举按值、引用按目标排序。
func RenderDraft(st *Structure) ([]byte, error) {
	db := draftDB{Name: st.DB}
	seenRef := map[string]bool{}
	for _, t := range st.Tables {
		dt := draftTable{Name: t.Name, Schema: t.Schema}
		for _, c := range t.Columns {
			dc := draftColumn{Name: c.Name, Type: c.Type}
			for _, v := range c.EnumValues {
				dc.EnumValues = append(dc.EnumValues, draftEnum{Value: v})
			}
			dt.Columns = append(dt.Columns, dc)
		}
		// 引用边：目标必须在本服务结构内（跨服务 FK 无目标服务名可
		// 写，FQN 编不出来——由调用方交叉检查阶段提示，这里跳过）。
		refs := make([]*Reference, 0, len(t.References))
		for _, r := range t.References {
			if st.findTable(r.TargetTable) != nil {
				refs = append(refs, r)
			}
		}
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].TargetTable != refs[j].TargetTable {
				return refs[i].TargetTable < refs[j].TargetTable
			}
			return refs[i].On(t.Name) < refs[j].On(t.Name)
		})
		for _, r := range refs {
			to := st.Service + "." + st.DB + "." + r.TargetTable
			on := r.On(t.Name)
			key := to + "|" + on
			if seenRef[key] {
				continue
			}
			seenRef[key] = true
			dt.References = append(dt.References, draftRef{To: to, On: on})
		}
		db.Tables = append(db.Tables, dt)
	}
	f := draftFile{
		Version:   yamlVersion,
		Service:   st.Service,
		Databases: []draftDB{db},
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("渲染服务 %s 草稿: %w", st.Service, err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteDraft 把草稿写到 outDir/services/<service>.yaml（覆盖写，
// 采集 = 全量重建语义，幂等）。
func WriteDraft(outDir string, st *Structure) (string, error) {
	data, err := RenderDraft(st)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(outDir, "services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建草稿目录 %s: %w", dir, err)
	}
	path := filepath.Join(dir, st.Service+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("写草稿 %s: %w", path, err)
	}
	return path, nil
}

// CheckCompile 用 07 同步管线的编译校验验证采集产出（第三道闸）：
// semantic.Load + Compile——FQN 唯一 / 引用完整性 / 枚举合法全过。
// 失败返回 error（调用方按原子拒绝处理，与同步管线一致）。
func CheckCompile(outDir string) error {
	in, err := semantic.Load(outDir)
	if err != nil {
		return fmt.Errorf("采集产出无法被同步管线读取（原子拒绝）: %w", err)
	}
	if _, err := semantic.Compile(in); err != nil {
		return fmt.Errorf("采集产出编译校验失败（原子拒绝）: %w", err)
	}
	return nil
}
