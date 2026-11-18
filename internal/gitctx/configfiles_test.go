package gitctx

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func keys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ConfigFiles is an allowlist: only configuration files Git does not ignore, under every kind of
// ignore rule, and never files of a nested repository.
func TestConfigFiles(t *testing.T) {
	top := repo(t) // HOME is the repository folder, so its .gitconfig is the global one
	write(t, filepath.Join(top, ".gitconfig"), "[core]\n\texcludesFile = "+filepath.ToSlash(filepath.Join(top, "global-ignore"))+"\n")
	write(t, filepath.Join(top, "global-ignore"), "global.tf\n")
	write(t, filepath.Join(top, ".git", "info", "exclude"), "local-exclude.tf\n")
	write(t, filepath.Join(top, "infra", ".gitignore"), "secret.tf\ngen/\n")
	write(t, filepath.Join(top, "infra", "modules", "db", ".gitignore"), "*.auto.tf\n")
	for _, f := range []string{"main.tf", "secret.tf", "gen/a.tf", "global.tf", "local-exclude.tf", "j.tf.json", "x.tofu", "modules/db/main.tf", "modules/db/pw.auto.tf", "notes.md", ".terraform.lock.hcl"} {
		write(t, filepath.Join(top, "infra", filepath.FromSlash(f)), "# x\n")
	}
	// A nested repository: its own ignore rules are not the parent's, so none of it is listed.
	nested := filepath.Join(top, "infra", "vendor-repo")
	write(t, filepath.Join(nested, "ok.tf"), "# x\n")
	write(t, filepath.Join(nested, "secret.tf"), "# x\n")
	write(t, filepath.Join(nested, ".gitignore"), "secret.tf\n")
	fixture(t, nested, "init", "-q")
	fixture(t, top, "add", "infra/main.tf") // tracked and untracked files are both listed

	s := New()
	files, repo, ok := s.ConfigFiles(context.Background(), filepath.Join(top, "infra"))
	want := []string{".terraform.lock.hcl", "j.tf.json", "main.tf", "modules/db/main.tf", "x.tofu"}
	if !ok || !repo || !slices.Equal(keys(files), want) {
		t.Fatalf("%v repo=%v ok=%v, want %v", keys(files), repo, ok, want)
	}

	// Not a repository: known, nothing to filter.
	plain := t.TempDir()
	if files, repo, ok := s.ConfigFiles(context.Background(), plain); !ok || repo || files != nil {
		t.Fatalf("%v %v %v", files, repo, ok)
	}
	// A repository but no Git on PATH: unknown, so callers block.
	t.Setenv("PATH", "")
	if _, repo, ok := s.ConfigFiles(context.Background(), filepath.Join(top, "infra")); ok || !repo {
		t.Fatalf("git missing: repo=%v ok=%v", repo, ok)
	}
}

func TestConfigFilesArgsAreTheOnlyLsFiles(t *testing.T) {
	if !Allowed(ConfigFilesArgs) {
		t.Fatal("ConfigFilesArgs refused")
	}
	for _, bad := range [][]string{
		{"ls-files"},
		append(append([]string{}, ConfigFilesArgs...), "*.tfvars"),
		{"ls-files", "-z", "--cached", "--others", "--", "*.tf"},
	} {
		if Allowed(bad) {
			t.Fatalf("allowed %v", bad)
		}
	}
}

// As in Git, ignore rules apply only to untracked files: a tracked file that matches a
// .gitignore pattern is listed (documented in README and disclosure v6).
func TestConfigFilesListsTrackedFilesMatchingIgnoreRules(t *testing.T) {
	top := repo(t)
	write(t, filepath.Join(top, "tracked-secret.tf"), "# x\n")
	fixture(t, top, "add", "tracked-secret.tf")
	write(t, filepath.Join(top, ".gitignore"), "tracked-secret.tf\nuntracked-secret.tf\n")
	write(t, filepath.Join(top, "untracked-secret.tf"), "# x\n")
	files, _, ok := New().ConfigFiles(context.Background(), top)
	if !ok || !files["tracked-secret.tf"] || files["untracked-secret.tf"] {
		t.Fatalf("%v %v", keys(files), ok)
	}
}
