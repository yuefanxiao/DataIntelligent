package collector

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReconcileDrafts 全量扫描清理清单外残稿；增量扫描不动。
func TestReconcileDrafts(t *testing.T) {
	mk := func(out string, names ...string) {
		dir := filepath.Join(out, "services")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n+".yaml"), []byte("version: 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	man := &Manifest{Services: []ManifestService{
		{Name: "a", Dir: "a", DB: "d"},
		{Name: "b", Dir: "b", DB: "d"},
	}}

	out := t.TempDir()
	mk(out, "a", "b", "stale")
	if err := reconcileDrafts(out, man); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "services", "stale.yaml")); !os.IsNotExist(err) {
		t.Error("清单外残稿应被清理")
	}
	for _, n := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(out, "services", n+".yaml")); err != nil {
			t.Errorf("清单内草稿 %s 应保留", n)
		}
	}

	// 缺 services/ 目录 = no-op。
	empty := t.TempDir()
	if err := reconcileDrafts(empty, man); err != nil {
		t.Errorf("缺目录应 no-op: %v", err)
	}
}
