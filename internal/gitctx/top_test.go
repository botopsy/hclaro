package gitctx

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A repository top that cannot be resolved refuses the scope instead of continuing without a
// repository boundary. The path reaches .git lexically through a dangling symlink.
func TestUnresolvableRepositoryTopIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}
	path := dir + string(filepath.Separator) + "broken" + string(filepath.Separator) + ".."
	if FindGit(path) == "" {
		t.Fatal("setup: .git not found")
	}
	if top, err := repositoryTop(path); err == nil {
		t.Fatalf("unresolvable top accepted as %q", top)
	}
	if top, err := repositoryTop(t.TempDir()); err != nil || top != "" {
		t.Fatalf("outside a repository: %q %v", top, err)
	}
}
