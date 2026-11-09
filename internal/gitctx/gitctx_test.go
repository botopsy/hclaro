package gitctx

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

const canary = "DO_NOT_LEAK_canary_6f1e"

// recorder wraps the real runner and records every command the code under test runs, so
// each test can prove that only read-only Git subcommands were used.
type recorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recorder) observe(args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
}

func (r *recorder) assertReadOnly(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if !slices.Contains([]string{"rev-parse", "status", "diff"}, c[0]) || !Allowed(c) {
			t.Fatalf("non read-only git command ran: %v", c)
		}
	}
}

func service(t *testing.T) (*Service, *recorder, *[]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	rec := &recorder{}
	opened := &[]string{}
	s := NewWith(Exec{Observe: rec.observe}, func(_ context.Context, link string) error {
		*opened = append(*opened, link)
		return nil
	})
	s.Environ = func() []string { return nil }
	t.Cleanup(func() { rec.assertReadOnly(t) })
	return s, rec, opened
}

// fixture runs Git to build a disposable repository. This is test setup, not code under
// test: it is the only place in the suite where Git writes, and only inside t.TempDir().
func fixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Hclaro Test", "-c", "user.email=test@example.invalid", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(Environ(os.Environ()), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const mainTF = `resource "aws_s3_bucket" "logs" {
  bucket = "example-logs"
}

data "aws_caller_identity" "current" {}
`

// repo creates a committed repository with main.tf and returns its canonical path.
func repo(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	write(t, filepath.Join(dir, "main.tf"), mainTF)
	write(t, filepath.Join(dir, "README.md"), "docs\n")
	fixture(t, dir, "init", "-q")
	fixture(t, dir, "add", ".")
	fixture(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func TestAllowedIsReadOnly(t *testing.T) {
	ok := [][]string{
		{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all"},
		{"rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir"},
		{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--unified=3", "HEAD", "--", ":(literal)main.tf"},
	}
	for _, args := range ok {
		if !Allowed(args) {
			t.Fatalf("refused %v", args)
		}
	}
	bad := [][]string{
		{}, {"checkout", "main"}, {"reset", "--hard"}, {"pull"}, {"push"}, {"commit", "-m", "x"}, {"fetch"}, {"stash"},
		{"config", "core.pager", "x"}, {"update-index", "--refresh"}, {"gc"}, {"worktree", "add", "x"},
		{"status", "--porcelain=v2", "--output=/tmp/x"}, {"status", "-c", "core.pager=sh"},
		{"diff", "HEAD", "--", "main.tf"}, {"diff", "HEAD", "--", ":(glob)*"}, {"diff", "--ext-diff", "HEAD", "--", ":(literal)a"},
		{"diff", "HEAD", "--", ":(literal)a", ":(literal)b"}, {"diff", "--no-color"}, {"diff", "--output=x", "--", ":(literal)a"},
	}
	for _, args := range bad {
		if Allowed(args) {
			t.Fatalf("allowed %v", args)
		}
		if _, _, err := (Exec{Observe: func([]string) { t.Fatalf("ran %v", args) }}).Run(context.Background(), t.TempDir(), 10, args...); !errors.Is(err, ErrRefused) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestEnvironDropsGitVariables(t *testing.T) {
	env := Environ([]string{"PATH=/bin", "GIT_DIR=/elsewhere", "git_work_tree=/x", "GIT_EXTERNAL_DIFF=sh", "GIT_CONFIG_PARAMETERS='core.pager=sh'", "LC_ALL=fr", "HOME=/h"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"GIT_DIR", "git_work_tree", "GIT_EXTERNAL_DIFF", "GIT_CONFIG_PARAMETERS", "LC_ALL=fr"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("%s kept: %v", bad, env)
		}
	}
	for _, want := range []string{"PATH=/bin", "HOME=/h", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"} {
		if !slices.Contains(env, want) {
			t.Fatalf("missing %s: %v", want, env)
		}
	}
}

func TestCleanBranch(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	head := s.HeadOf(dir)
	if head.State != StateRepository || head.Head.Branch != "main" || head.Head.Detached || len(head.Head.Commit) != 40 {
		t.Fatalf("head %+v", head.Head)
	}
	c := s.Status(context.Background(), dir)
	if c.State != StateRepository || !c.Clean || len(c.Files) != 0 || c.Head.Branch != "main" || c.Head.Commit != head.Head.Commit || c.Repository.ProjectPath != "." || c.Repository.Worktree {
		t.Fatalf("status %+v", c)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings %+v", c.Warnings)
	}
}

func TestDirtyUntrackedAndVariableFiles(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "main.tf"), mainTF+"\n# changed\n")
	write(t, filepath.Join(dir, "network.tf"), `resource "aws_vpc" "main" {}`+"\n")
	write(t, filepath.Join(dir, "prod.tfvars"), `db_password = "`+canary+`"`+"\n")
	write(t, filepath.Join(dir, "notes.txt"), "x\n")
	c := s.Status(context.Background(), dir)
	if c.Clean || c.Counts.Changed != 3 || c.Counts.Untracked != 2 || c.Counts.Other != 1 {
		t.Fatalf("counts %+v", c.Counts)
	}
	byPath := map[string]GitFileStatus{}
	for _, f := range c.Files {
		byPath[f.Path] = f
	}
	if f := byPath["main.tf"]; f.Status != "modified" || !f.Diffable || !f.Unstaged || f.Kind != KindHCL || len(f.ID) != 32 {
		t.Fatalf("main.tf %+v", f)
	}
	if f := byPath["network.tf"]; f.Status != "untracked" || !f.Diffable {
		t.Fatalf("network.tf %+v", f)
	}
	if f := byPath["prod.tfvars"]; f.Diffable || f.Kind != KindVars || f.Reason == "" {
		t.Fatalf("tfvars %+v", f)
	}
	if !hasWarning(c, "untracked_iac") {
		t.Fatalf("warnings %+v", c.Warnings)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), canary) || strings.Contains(string(b), dir) {
		t.Fatalf("status leaked content or absolute path: %s", b)
	}
	// A variable file is never diffed, even with its own ID.
	if _, err := s.Diff(context.Background(), dir, byPath["prod.tfvars"].ID); !errors.Is(err, ErrNotDiffable) {
		t.Fatalf("tfvars diff: %v", err)
	}
	// Unknown and forged IDs are refused.
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "", "../main.tf"} {
		if _, err := s.Diff(context.Background(), dir, id); !errors.Is(err, ErrUnknownFile) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
}

func hasWarning(c GitContext, code string) bool {
	for _, w := range c.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestDetachedHead(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	commit := fixture(t, dir, "rev-parse", "HEAD")
	fixture(t, dir, "checkout", "-q", "--detach", commit)
	c := s.Status(context.Background(), dir)
	if !c.Head.Detached || c.Head.Branch != "" || c.Head.Commit != commit || !hasWarning(c, "detached_head") {
		t.Fatalf("detached %+v %+v", c.Head, c.Warnings)
	}
	if h := s.HeadOf(dir); !h.Head.Detached || h.Head.Commit != commit {
		t.Fatalf("head %+v", h.Head)
	}
}

func TestLinkedWorktree(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	wt := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-wt")
	fixture(t, dir, "worktree", "add", "-q", "-b", "feature", wt)
	t.Cleanup(func() { os.RemoveAll(wt) })
	write(t, filepath.Join(wt, "main.tf"), mainTF+"# wt\n")
	c := s.Status(context.Background(), wt)
	if c.State != StateRepository || !c.Repository.Worktree || c.Head.Branch != "feature" || c.Counts.Changed != 1 {
		t.Fatalf("worktree %+v %+v %+v", c.Repository, c.Head, c.Counts)
	}
	if h := s.HeadOf(wt); h.Head == nil || h.Head.Branch != "feature" || !h.Head.Worktree {
		t.Fatalf("head %+v", h)
	}
	// The main checkout is unaffected.
	if c := s.Status(context.Background(), dir); !c.Clean || c.Repository.Worktree {
		t.Fatalf("main %+v", c)
	}
}

func modulesJSON(t *testing.T, root string, entries ...[3]string) {
	t.Helper()
	type entry struct{ Key, Source, Dir string }
	m := struct{ Modules []entry }{Modules: []entry{{"", "", "."}}}
	for _, e := range entries {
		m.Modules = append(m.Modules, entry{e[0], e[1], e[2]})
	}
	b, _ := json.Marshal(m)
	write(t, filepath.Join(root, ".terraform", "modules", "modules.json"), string(b))
}

func TestNestedRootAndLocalModules(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	app := filepath.Join(dir, "stacks", "app")
	write(t, filepath.Join(app, "main.tf"), `module "net" {
  source = "../../modules/net"
}
`)
	write(t, filepath.Join(dir, "modules", "net", "main.tf"), `resource "aws_subnet" "a" {}`+"\n")
	write(t, filepath.Join(dir, "other", "main.tf"), "# other stack\n")
	write(t, filepath.Join(dir, ".gitignore"), ".terraform/\n")
	fixture(t, dir, "add", ".")
	fixture(t, dir, "commit", "-q", "-m", "stacks")
	modulesJSON(t, app, [3]string{"net", "../../modules/net", "../../modules/net"}, [3]string{"remote", "registry.terraform.io/x/y/aws", ".terraform/modules/remote"})
	write(t, filepath.Join(app, "main.tf"), "# edited\n")
	write(t, filepath.Join(dir, "modules", "net", "main.tf"), "# module edited\n")
	write(t, filepath.Join(dir, "other", "main.tf"), "# other edited\n")
	write(t, filepath.Join(dir, "main.tf"), "# top edited\n")
	c := s.Status(context.Background(), app)
	if c.Repository.ProjectPath != "stacks/app" || c.Repository.LocalModules != 1 {
		t.Fatalf("repository %+v", c.Repository)
	}
	paths := []string{}
	for _, f := range c.Files {
		paths = append(paths, f.Place+":"+f.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"module:modules/net/main.tf", "root:stacks/app/main.tf"}) || c.Counts.Outside != 2 || !hasWarning(c, "outside_changes") {
		t.Fatalf("files %v counts %+v", paths, c.Counts)
	}
	// The module change can be diffed; the other stack cannot be named.
	for _, f := range c.Files {
		d, err := s.Diff(context.Background(), app, f.ID)
		if err != nil || d.Added == 0 {
			t.Fatalf("diff %s: %v %+v", f.Path, err, d)
		}
	}
}

func TestNotARepositoryAndMissingGit(t *testing.T) {
	s, _, _ := service(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(dir, "main.tf"), mainTF)
	if c := s.Status(context.Background(), dir); c.State != StateNotRepo || c.Head != nil || c.Files == nil {
		t.Fatalf("not repo %+v", c)
	}
	// Without git on PATH the context is unavailable, not an error page.
	d := repo(t)
	t.Setenv("PATH", t.TempDir())
	if c := s.Status(context.Background(), d); c.State != StateUnavailable || !strings.Contains(c.Message, "PATH") {
		t.Fatalf("missing git %+v", c)
	}
}

func TestDiffRedactsSecrets(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "main.tf"), mainTF+`
resource "aws_db_instance" "db" {
  username = "admin"
  password = "`+canary+`"
  tags = { Name = "db" }
  user_data = <<EOF
#!/bin/sh
export TOKEN=`+canary+`
EOF
  api = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
  master_password = var.db_password
}
`)
	c := s.Status(context.Background(), dir)
	d, err := s.Diff(context.Background(), dir, c.Files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	text := string(b)
	if strings.Contains(text, canary) || strings.Contains(text, "ghp_abc") || strings.Contains(text, "export TOKEN") {
		t.Fatalf("secret leaked: %s", text)
	}
	if d.Redacted < 3 || d.Base != "HEAD" || len(d.Hunks) != 1 || d.Added != 12 || d.Removed != 0 {
		t.Fatalf("diff %+v", d)
	}
	for _, want := range []string{`username = \"admin\"`, `password = \"[redacted]\"`, "master_password = var.db_password", `tags = { Name = \"db\" }`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	h := d.Hunks[0]
	if h.Lines[0].Kind != "context" || h.Lines[0].Old != 3 || h.Lines[0].New != 3 {
		t.Fatalf("line numbers %+v", h.Lines[:2])
	}
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(outside, "secret.tf"), `password = "`+canary+`"`+"\n")
	if err := os.Symlink(filepath.Join(outside, "secret.tf"), filepath.Join(dir, "evil.tf")); err != nil {
		t.Skip("symlinks unavailable")
	}
	c := s.Status(context.Background(), dir)
	var id string
	for _, f := range c.Files {
		if f.Path == "evil.tf" {
			id = f.ID
		}
	}
	if id == "" {
		t.Fatalf("symlink not listed: %+v", c.Files)
	}
	if d, err := s.Diff(context.Background(), dir, id); !errors.Is(err, ErrOutsideScope) || strings.Contains(d.Notice, canary) {
		t.Fatalf("escape diff: %v %+v", err, d)
	}
	if _, err := s.OpenEditor(context.Background(), dir, id, "vscode", 1); err == nil {
		t.Fatal("editor opened a file outside the scope")
	}
	// The source fingerprint does not read it either.
	scope, _ := NewScope(dir, dir, nil)
	files, _ := scope.Files()
	for _, f := range files {
		if f.Rel == "evil.tf" {
			t.Fatal("escaping symlink in scope files")
		}
	}
	// A symlinked directory is not followed.
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	files, _ = scope.Files()
	for _, f := range files {
		if strings.HasPrefix(f.Rel, "linked") {
			t.Fatal("followed a symlinked directory")
		}
	}
}

func TestBinaryAndLargeFiles(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "blob.tf"), "abc\x00def")
	var big strings.Builder
	for i := 0; i < 6000; i++ {
		big.WriteString("# line\n")
	}
	write(t, filepath.Join(dir, "big.tf"), big.String())
	c := s.Status(context.Background(), dir)
	for _, f := range c.Files {
		d, err := s.Diff(context.Background(), dir, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch f.Path {
		case "blob.tf":
			if !d.Binary || len(d.Hunks) != 0 {
				t.Fatalf("binary %+v", d)
			}
		case "big.tf":
			if !d.Truncated || d.Added != maxDiffLines {
				t.Fatalf("big %+v", d.Added)
			}
		}
	}
	// Committed binary content changed: Git reports a binary diff.
	fixture(t, dir, "add", "blob.tf")
	fixture(t, dir, "commit", "-q", "-m", "blob")
	write(t, filepath.Join(dir, "blob.tf"), "xyz\x00uvw")
	c = s.Status(context.Background(), dir)
	for _, f := range c.Files {
		if f.Path == "blob.tf" {
			d, err := s.Diff(context.Background(), dir, f.ID)
			if err != nil || !d.Binary || strings.Contains(d.Notice, "xyz") {
				t.Fatalf("committed binary %v %+v", err, d)
			}
		}
	}
}

// fake is a runner returning canned output.
type fake struct {
	out map[string][]byte
	err error
}

func (f fake) Run(_ context.Context, _ string, _ int, args ...string) ([]byte, bool, error) {
	if !Allowed(args) {
		return nil, false, ErrRefused
	}
	return f.out[args[0]], false, f.err
}

func TestMalformedGitOutputIsRefused(t *testing.T) {
	dir := repo(t)
	revParse := []byte(dir + "\n" + filepath.Join(dir, ".git") + "\n" + filepath.Join(dir, ".git") + "\n")
	for name, status := range map[string]string{
		"garbage":       "hello world",
		"no oid":        "1 .M N... 100644 100644 100644 abc abc main.tf\x00",
		"bad xy":        "# branch.oid " + strings.Repeat("a", 40) + "\x001 ZZ N... 100644 100644 100644 abc abc main.tf\x00",
		"absolute path": "# branch.oid " + strings.Repeat("a", 40) + "\x00? /etc/passwd\x00",
		"traversal":     "# branch.oid " + strings.Repeat("a", 40) + "\x00? ../x.tf\x00",
		"short record":  "# branch.oid " + strings.Repeat("a", 40) + "\x001 .M\x00",
	} {
		s := NewWith(fake{out: map[string][]byte{"rev-parse": revParse, "status": []byte(status)}}, nil)
		if c := s.Status(context.Background(), dir); c.State != StateError || len(c.Files) != 0 {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	// Malformed rev-parse output.
	s := NewWith(fake{out: map[string][]byte{"rev-parse": []byte("relative\n")}}, nil)
	if c := s.Status(context.Background(), dir); c.State != StateError {
		t.Fatalf("rev-parse %+v", c)
	}
	// A failing git (for example safe.directory) is unavailable, with a generic message.
	s = NewWith(fake{err: &exec.ExitError{}}, nil)
	if c := s.Status(context.Background(), dir); c.State != StateUnavailable || strings.Contains(c.Message, dir) {
		t.Fatalf("exit %+v", c)
	}
}

func TestParseStatusRecords(t *testing.T) {
	oid := strings.Repeat("b", 40)
	raw := "# branch.oid " + oid + "\x00# branch.head feature/x\x00# branch.upstream origin/x\x00# branch.ab +1 -0\x00" +
		"1 M. N... 100644 100644 100644 aaa bbb staged.tf\x00" +
		"1 .D N... 100644 100644 000000 aaa aaa gone.tf\x00" +
		"1 A. N... 000000 100644 100644 000 bbb new name.tf\x00" +
		"2 R. N... 100644 100644 100644 aaa bbb R100 to.tf\x00from.tf\x00" +
		"u UU N... 100644 100644 100644 100644 a b c conflict.tf\x00" +
		"? untracked.tf\x00"
	p, err := ParseStatus([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Head.Commit != oid || p.Head.Branch != "feature/x" || len(p.Entries) != 6 {
		t.Fatalf("%+v", p)
	}
	want := map[string]string{"staged.tf": "modified", "gone.tf": "deleted", "new name.tf": "added", "to.tf": "modified", "conflict.tf": "conflicted", "untracked.tf": "untracked"}
	for _, e := range p.Entries {
		if want[e.Path] != e.Status {
			t.Fatalf("%s: %s", e.Path, e.Status)
		}
	}
	// Overflow drops the cut record and marks the result incomplete.
	p, err = ParseStatus([]byte("# branch.oid "+oid+"\x00? a.tf\x00? b.t"), true)
	if err != nil || len(p.Entries) != 1 || !p.Incomplete {
		t.Fatalf("overflow %+v %v", p, err)
	}
	// A credential-shaped branch name is not displayed.
	p, _ = ParseStatus([]byte("# branch.oid "+oid+"\x00# branch.head token=abc\x00"), false)
	if p.Head.Branch != "" {
		t.Fatalf("branch %q", p.Head.Branch)
	}
}

func TestParseDiff(t *testing.T) {
	good := "diff --git a/main.tf b/main.tf\nindex 1..2 100644\n--- a/main.tf\n+++ b/main.tf\n@@ -1,2 +1,2 @@ resource \"x\" \"y\" {\n a\n-b\n+c\n\\ No newline at end of file\n"
	var d GitDiff
	if err := ParseDiff([]byte(good), false, &d); err != nil || len(d.Hunks) != 1 || d.Added != 1 || d.Removed != 1 || len(d.Hunks[0].Lines) != 4 {
		t.Fatalf("%v %+v", err, d)
	}
	if l := d.Hunks[0].Lines; l[1].Kind != "delete" || l[1].Old != 2 || l[2].Kind != "add" || l[2].New != 2 || l[3].Kind != "note" {
		t.Fatalf("lines %+v", l)
	}
	for name, raw := range map[string]string{
		"count mismatch": "@@ -1,2 +1,2 @@\n a\n",
		"unknown line":   "@@ -1 +1 @@\n*a\n",
		"unknown header": "weird header\n@@ -1 +1 @@\n a\n",
		"two files":      "diff --git a/x b/x\n@@ -1 +1 @@\n a\ndiff --git a/y b/y\n",
		"bad range":      "@@ -x +1 @@\n a\n",
		"too many lines": "@@ -1 +1 @@\n a\n b\n",
	} {
		var d GitDiff
		if err := ParseDiff([]byte(raw), false, &d); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var bin GitDiff
	if err := ParseDiff([]byte("diff --git a/x b/x\nBinary files a/x and b/x differ\n"), false, &bin); err != nil || !bin.Binary {
		t.Fatalf("binary %v %+v", err, bin)
	}
	// Cut output is truncated, not malformed.
	var cut GitDiff
	if err := ParseDiff([]byte("@@ -1,3 +1,3 @@\n a\n b\n c"), true, &cut); err != nil || !cut.Truncated {
		t.Fatalf("cut %v %+v", err, cut)
	}
}

func TestLineRedactor(t *testing.T) {
	cases := map[string]string{
		`  password = "hunter2"`:                       `  password = "[redacted]"`,
		`  name     = "web"`:                           `  name     = "web"`,
		`  "client_secret": "abc",`:                    `  "client_secret": "[redacted]",`,
		`  key = "AKIAABCDEFGHIJKLMNOP"`:               `  key = "[redacted]"`,
		`  url = "https://user:pa55@example.com/db"`:   `  url = "[redacted]"`,
		`  db_password = var.db_password`:              `  db_password = var.db_password`,
		`  api_token = 12345`:                          `  api_token = [redacted]`,
		"  description = \"x\x1b[31m\u202ey\"":         `  description = "x[31my"`,
		`# export TOKEN=abc`:                           `[redacted]`,
		`  token_ref = aws_secretsmanager_secret.x.id`: `  token_ref = aws_secretsmanager_secret.x.id`,
	}
	for in, want := range cases {
		got, _ := (&LineRedactor{}).Line(in)
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	// Heredocs under a secret-like name are withheld until the marker.
	r := &LineRedactor{}
	lines := []string{"  private_key = <<-EOT", "abc", "def", "  EOT", `  name = "x"`}
	got := []string{}
	for _, l := range lines {
		out, _ := r.Line(l)
		got = append(got, out)
	}
	if got[1] != Redacted || got[2] != Redacted || got[3] != "  EOT" || got[4] != `  name = "x"` {
		t.Fatalf("heredoc %q", got)
	}
	long, _ := (&LineRedactor{}).Line(strings.Repeat("a", 1000))
	if len([]rune(long)) != maxLineRunes+1 {
		t.Fatalf("long %d", len(long))
	}
}

func TestSourceFingerprint(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	before := s.Record(context.Background(), dir)
	if !before.Repository || before.Branch != "main" || before.Content == "" || before.Files != 1 || !before.DirtyKnown || before.Dirty {
		t.Fatalf("record %+v", before)
	}
	if st := s.Compare(before, dir); st.State != "unchanged" {
		t.Fatalf("unchanged %+v", st)
	}
	// A commit that does not touch IaC files moves HEAD only.
	write(t, filepath.Join(dir, "README.md"), "more docs\n")
	fixture(t, dir, "commit", "-q", "-am", "docs")
	if st := s.Compare(before, dir); st.State != "changed" || !st.HeadChanged || st.FilesChanged {
		t.Fatalf("head %+v", st)
	}
	// Editing a tracked HCL file changes the source.
	write(t, filepath.Join(dir, "main.tf"), mainTF+"# edit\n")
	st := s.Compare(before, dir)
	if !st.FilesChanged || !strings.Contains(st.Message, "different source revision") {
		t.Fatalf("files %+v", st)
	}
	if after := s.Record(context.Background(), dir); !after.Dirty || after.DirtyFiles != 1 {
		t.Fatalf("dirty %+v", after)
	}
	// A local module outside the root is covered.
	app := filepath.Join(dir, "app")
	write(t, filepath.Join(app, "main.tf"), "module \"m\" { source = \"../mod\" }\n")
	write(t, filepath.Join(dir, "mod", "main.tf"), "# module\n")
	modulesJSON(t, app, [3]string{"m", "../mod", "../mod"})
	fp := s.Fingerprint(app)
	if fp.LocalModules != 1 || fp.Files != 2 {
		t.Fatalf("module fp %+v", fp)
	}
	write(t, filepath.Join(dir, "mod", "main.tf"), "# module changed\n")
	if st := s.Compare(fp, app); !st.FilesChanged {
		t.Fatalf("module change %+v", st)
	}
	// Outside a repository the content still counts.
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(plain, "main.tf"), mainTF)
	fp = s.Record(context.Background(), plain)
	if fp.Repository || fp.Content == "" || fp.DirtyKnown {
		t.Fatalf("plain %+v", fp)
	}
	if st := CompareFingerprints(fp, &PlanSourceFingerprint{}); st.State != "unknown" {
		t.Fatalf("unknown %+v", st)
	}
}

func TestLocate(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "extra.tf.json"), `{
  "resource": {
    "aws_iam_role": {
      "deploy": {}
    }
  }
}`)
	write(t, filepath.Join(dir, "mod", "main.tf"), "\n\nresource aws_subnet \"a\" {\n}\n")
	modulesJSON(t, dir, [3]string{"net", "./mod", "mod"}, [3]string{"remote", "app.terraform.io/x/y/aws", ".terraform/modules/remote"})
	ref := s.Locate(dir, "managed", "", "aws_s3_bucket", "logs")
	if ref.State != "found" || ref.Path != "main.tf" || ref.Line != 1 || ref.Confidence != "block" || len(ref.FileID) != 32 {
		t.Fatalf("root %+v", ref)
	}
	if ref := s.Locate(dir, "data", "", "aws_caller_identity", "current"); ref.Line != 5 || ref.BlockType != "data" {
		t.Fatalf("data %+v", ref)
	}
	if ref := s.Locate(dir, "managed", `module.net["a"]`, "aws_subnet", "a"); ref.State != "found" || ref.Path != "mod/main.tf" || ref.Line != 3 || ref.Module != "net" {
		t.Fatalf("module %+v", ref)
	}
	if ref := s.Locate(dir, "managed", "", "aws_iam_role", "deploy"); ref.State != "found" || ref.Confidence != "file" || ref.Line != 4 {
		t.Fatalf("json %+v", ref)
	}
	if ref := s.Locate(dir, "managed", "module.remote", "aws_vpc", "x"); ref.State != "unsupported" {
		t.Fatalf("remote %+v", ref)
	}
	if ref := s.Locate(dir, "managed", "", "aws_s3_bucket", "missing"); ref.State != "not_found" || ref.Reason == "" {
		t.Fatalf("missing %+v", ref)
	}
}

func TestOpenEditor(t *testing.T) {
	s, _, opened := service(t)
	dir := repo(t)
	spaced := filepath.Join(dir, "a dir & $(touch pwned)", "x;rm -rf.tf")
	write(t, spaced, "resource \"a\" \"b\" {}\n")
	scope, _ := NewScope(dir, dir, nil)
	files, err := scope.Files()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, f := range files {
		ids[f.Rel] = s.ids.of(scope.Base, f.Rel)
	}
	res, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "vscode", 5)
	if err != nil || res.Path != "main.tf" || res.Line != 5 {
		t.Fatalf("%v %+v", err, res)
	}
	want, _ := EditorURL("vscode", filepath.Join(dir, "main.tf"), 5)
	if len(*opened) != 1 || (*opened)[0] != want || !strings.HasPrefix(want, "vscode://file/") || !strings.HasSuffix(want, "/main.tf:5") {
		t.Fatalf("opened %v want %s", *opened, want)
	}
	rel := filepath.ToSlash(strings.TrimPrefix(spaced, dir+string(filepath.Separator)))
	if _, err := s.OpenEditor(context.Background(), dir, ids[rel], "cursor", 1); err != nil {
		t.Fatal(err)
	}
	link := (*opened)[1]
	if !strings.HasPrefix(link, "cursor://file/") || strings.ContainsAny(link, " &$();") {
		t.Fatalf("unescaped link %q", link)
	}
	for name, call := range map[string]func() error{
		"editor": func() error {
			_, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "sh -c id", 1)
			return err
		},
		"editor2": func() error {
			_, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "/usr/bin/open", 1)
			return err
		},
		"line 0": func() error {
			_, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "vscode", 0)
			return err
		},
		"line big": func() error {
			_, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "vscode", 999)
			return err
		},
		"id": func() error {
			_, err := s.OpenEditor(context.Background(), dir, "0123456789abcdef0123456789abcdef", "vscode", 1)
			return err
		},
		"readme": func() error {
			_, err := s.OpenEditor(context.Background(), dir, s.ids.of(dir, "README.md"), "vscode", 1)
			return err
		},
	} {
		if call() == nil {
			t.Fatalf("%s: opened", name)
		}
	}
	if len(*opened) != 2 {
		t.Fatalf("extra opens %v", *opened)
	}
	// A failing opener is reported.
	s.OpenURL = func(context.Context, string) error { return errors.New("no handler") }
	if _, err := s.OpenEditor(context.Background(), dir, ids["main.tf"], "vscode", 1); !errors.Is(err, ErrOpenFailed) {
		t.Fatal(err)
	}
	if _, err := EditorURL("javascript", "/x", 1); !errors.Is(err, ErrEditor) {
		t.Fatal(err)
	}
	// A native absolute path: drive-letter paths on Windows become /C:/…, spaces are encoded.
	native, want2 := "/Users/a b/main.tf", "vscode://file/Users/a%20b/main.tf:2"
	if runtime.GOOS == "windows" {
		native, want2 = `C:\Users\a b\main.tf`, "vscode://file/C:/Users/a%20b/main.tf:2"
	}
	if u, err := EditorURL("vscode", native, 2); err != nil || u != want2 {
		t.Fatalf("native path %q: %s %v", native, u, err)
	}
	if _, err := EditorURL("vscode", "relative/main.tf", 2); err == nil {
		t.Fatal("relative path accepted")
	}
}
