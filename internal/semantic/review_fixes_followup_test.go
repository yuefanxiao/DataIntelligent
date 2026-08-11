package semantic

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// 对抗评审 P1：references 前向引用误拒——引用「编译顺序在后」的表必须通过
// （跨服务 references 是核心建模能力，不应受服务文件名排序影响）。
// 场景：a-service.yaml 排在前面，引用 z-service 的表（按文件名排序 z 在后）。
func TestCompileReferenceForwardOrdering(t *testing.T) {
	dir := writeSemantic(t, map[string]string{
		"services/a-service.yaml": `version: 1
service: a-service
databases:
  - name: a_db
    tables:
      - name: a_table
        columns:
          - name: id
            type: uuid
        references:
          - to: z-service.z_db.z_table
            on: "a_table.id = z_table.id"
`,
		"services/z-service.yaml": `version: 1
service: z-service
databases:
  - name: z_db
    tables:
      - name: z_table
        columns:
          - name: id
            type: uuid
`,
	})
	in, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target, err := Compile(in)
	if err != nil {
		t.Fatalf("前向引用被误拒（编译顺序依赖缺陷）: %v", err)
	}
	// references 边应已落位
	found := false
	for _, r := range target.Relations {
		if r.Type == RelReferences && r.SrcFQN == "a-service.a_db.a_table" &&
			r.DstFQN == "z-service.z_db.z_table" {
			found = true
		}
	}
	if !found {
		t.Error("references 边缺失（二遍校验未落边）")
	}

	// 反向仍然正确拒绝：目标确实不存在 → 引用完整性错误
	badDir := writeSemantic(t, map[string]string{
		"services/a-service.yaml": `version: 1
service: a-service
databases:
  - name: a_db
    tables:
      - name: a_table
        columns:
          - name: id
            type: uuid
        references:
          - to: z-service.z_db.no_such_table
            on: "a_table.id = no_such_table.id"
`,
	})
	in2, err := Load(badDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := Compile(in2); !errors.Is(err, ErrRefMissing) {
		t.Errorf("引用缺失应报 ErrRefMissing, got %v", err)
	}
}

// 对抗评审 P1：作者入口目录不存在 = 原子拒绝（零写库）——防 --dir 路径笔误
// 把整个运行时语义层全量墓碑化（空目录曾被当作合法空语义层）。
func TestLoadMissingDirRejected(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-semantic-repo")
	if _, err := Load(missing); err == nil {
		t.Fatal("目录不存在应报错（原子拒绝）")
	}

	// Sync 路径同样拒绝且零写库：先正常同步，再对不存在的目录重跑。
	st := newStore(t)
	ctx := context.Background()
	dir := sampleDir(t)
	if _, err := Sync(ctx, st, dir); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := Sync(ctx, st, missing); err == nil {
		t.Fatal("Sync 对不存在的目录应原子拒绝")
	}
	snap, err := Snapshot(ctx, st)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Entities) == 0 {
		t.Error("原子拒绝应零写库：实体不应被墓碑化")
	}
}

// 目录存在但无 services/ 仍合法（显式空语义层 = 清空意图，不误拒）。
func TestLoadEmptyExistingDirAllowed(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err != nil {
		t.Fatalf("存在的空目录应合法（显式清空意图）: %v", err)
	}
}
