package collector

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/yuefanxiao/DataIntelligent/internal/semantic"
)

// draftFile 是采集产出的服务 YAML 草稿结构（与 07 同步管线作者入口
// services/<service>.yaml 同构——semantic.Load + Compile 直接兼容）。
// 结构字段（库/表/列/枚举值/引用边）由采集器机械产出；语义字段
// （description/is_time/enum label）采集器不产出——由人工/Agent 回写，
// 采集重跑时经 MergeSemantics 按 FQN 保留（ADR-0007「结构自动、
// 语义人工」，结构永远以新采集为准）。
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
	IsTime      bool        `yaml:"is_time,omitempty"`
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
	f := draftFile{Version: yamlVersion, Service: st.Service}
	if st.DB != "" {
		// 无持库服务（清单未配置 db）不产出 database 条目——服务实体
		// 草稿没有库层，编译为「服务 →（无子实体）」。
		f.Databases = []draftDB{db}
	}
	return encodeDraft(f)
}

// parseDraftStrict 按 KnownFields 严格解析现有作者入口（与 07 同步管线
// semantic.Load 同契约）：未知字段 = 错误。合并路径必须严格——宽松解析
// 会把未建模字段静默丢弃（见 MergeSemantics 注释）。
func parseDraftStrict(data []byte, out *draftFile) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// encodeDraft 按草稿统一格式渲染（2 空格缩进，yaml.v3 编码器；
// RenderDraft 与 MergeSemantics 共用，保证输出风格一致）。
func encodeDraft(f draftFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("渲染服务 %s 草稿: %w", f.Service, err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MergeSemantics 把现有作者入口文件里已确认的语义（服务/库/表/列的
// description、列 is_time、枚举 label）按 FQN 保留到新草稿——采集重跑
// 只更新结构，不覆盖人工语义（ADR-0007「结构自动、语义人工」：
// 语义回写后不会因增量采集丢失，纯结构变更批量确认 US-16 负担≈零）。
// 合并只读语义字段：结构（表/列/枚举值/引用）永远以新采集为准，
// 已删除的表/列/枚举值的语义自然随结构丢弃；列类型变化的 is_time
// 标注一并丢弃（陈旧时间轴不进入 dry-run 展开）。
//
// 现有文件按 KnownFields 严格解析（与 semantic.Load 同契约）：作者入口
// 出现本包未建模的字段 = 合并失败（响亮拒绝覆盖），绝不静默丢弃——
// 语义层新增字段时采集重跑不会悄悄抹掉它，而是报错等人工处理。
//
// 确定性：同输入同输出（输出经统一渲染，与 RenderDraft 同序）。
func MergeSemantics(newDraft, existing []byte) ([]byte, error) {
	var cur draftFile
	if err := yaml.Unmarshal(newDraft, &cur); err != nil {
		return nil, fmt.Errorf("解析新草稿: %w", err)
	}
	var prev draftFile
	if err := parseDraftStrict(existing, &prev); err != nil {
		return nil, fmt.Errorf("合并语义失败（现有作者入口 %s 解析失败或含未建模字段，拒绝覆盖以免丢失语义）: %w", cur.Service, err)
	}
	if prev.Service != "" && prev.Service != cur.Service {
		return nil, fmt.Errorf("合并语义失败（现有作者入口服务名 %q ≠ 草稿 %q，拒绝覆盖）", prev.Service, cur.Service)
	}
	cur.Description = prev.Description
	for i := range cur.Databases {
		pdb := findDraftDB(prev, cur.Databases[i].Name)
		if pdb == nil {
			continue
		}
		cur.Databases[i].Description = pdb.Description
		for j := range cur.Databases[i].Tables {
			pt := findDraftTable(pdb, cur.Databases[i].Tables[j].Name)
			if pt == nil {
				continue
			}
			cur.Databases[i].Tables[j].Description = pt.Description
			for k := range cur.Databases[i].Tables[j].Columns {
				pc := findDraftColumn(pt, cur.Databases[i].Tables[j].Columns[k].Name)
				if pc == nil {
					continue
				}
				col := &cur.Databases[i].Tables[j].Columns[k]
				col.Description = pc.Description
				// is_time 只在类型未变时保留：列类型变化说明时间轴语义
				// 存疑，陈旧标注会进入 dry-run 时间展开（在非时间列上
				// 生成谓词）——随结构丢弃，等人工重新确认。
				if pc.Type == col.Type {
					col.IsTime = pc.IsTime
				}
				for m := range col.EnumValues {
					if pe := findDraftEnum(pc, col.EnumValues[m].Value); pe != nil {
						col.EnumValues[m].Label = pe.Label
					}
				}
			}
		}
	}
	return encodeDraft(cur)
}

func findDraftDB(f draftFile, name string) *draftDB {
	for i := range f.Databases {
		if f.Databases[i].Name == name {
			return &f.Databases[i]
		}
	}
	return nil
}

func findDraftTable(db *draftDB, name string) *draftTable {
	for i := range db.Tables {
		if db.Tables[i].Name == name {
			return &db.Tables[i]
		}
	}
	return nil
}

func findDraftColumn(t *draftTable, name string) *draftColumn {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

func findDraftEnum(c *draftColumn, value string) *draftEnum {
	for i := range c.EnumValues {
		if c.EnumValues[i].Value == value {
			return &c.EnumValues[i]
		}
	}
	return nil
}

// WriteDraft 把草稿写到 outDir/services/<service>.yaml（覆盖写，
// 采集 = 全量重建结构，幂等）。已有文件时先 MergeSemantics 保留
// 人工语义——重跑采集不丢描述/is_time/枚举 label。
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
	if existing, err := os.ReadFile(path); err == nil {
		if data, err = MergeSemantics(data, existing); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取现有草稿 %s: %w", path, err)
	}
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
