package safefile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAtomicReplacesPrivately(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.json")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(dir, "data.json", ".data-", []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	info, _ := os.Stat(target)
	if err != nil || string(b) != "new" || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("%q %v %v", b, err, info.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".data-*.tmp")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// A failed rename (the name is a folder) keeps the old content and leaves no temporary file.
func TestWriteAtomicFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "busy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "busy", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(dir, "busy", ".busy-", []byte("new")); err == nil {
		t.Fatal("replaced a folder")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

// os.Root keeps the write inside the folder.
func TestWriteAtomicInStaysInRoot(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := WriteAtomicIn(r, "../escape.json", ".x-", []byte("x")); err == nil {
		t.Fatal("wrote outside the root")
	}
}

// Keys name folders and records already on disk: they must not change.
func TestProjectKeyIsStable(t *testing.T) {
	if a, b := ProjectKey("hclaro-ai", "/p"), ProjectKey("hclaro-cost", "/p"); a != "3db925130210ea0c" || b != "e48b944593ce50fa" {
		t.Fatalf("%s %s", a, b)
	}
}
