package gitctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"hclaro/internal/rootlock"
)

// switchService runs real Git and records every command: only allowlisted reads and the
// exact switch argv may run. It never runs fetch, pull, merge, commit, push, stash, reset or
// branch creation.
func switchService(t *testing.T) (*Service, *[][]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	var mu sync.Mutex
	calls := &[][]string{}
	run := Exec{Observe: func(args []string) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, args)
	}}
	s := NewWith(run, nil)
	s.Switcher = run
	s.Environ = func() []string { return nil }
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range *calls {
			isSwitch := len(c) == 5 && slices.Equal(c, SwitchArgs(c[4])) && ValidBranchName(c[4]) != ""
			if !isSwitch && !Allowed(c) {
				t.Fatalf("unexpected git command: %v", c)
			}
		}
	})
	return s, calls
}

func freeClaim(string) (func(), bool) { return func() {}, true }

// envRepo has main, dev, demo and prod, each with its own main.tf.
func envRepo(t *testing.T) string {
	t.Helper()
	dir := repo(t)
	for _, b := range []string{"dev", "demo", "prod"} {
		fixture(t, dir, "switch", "-q", "-c", b, "main")
		write(t, filepath.Join(dir, "main.tf"), "# "+b+"\n"+mainTF)
		fixture(t, dir, "commit", "-q", "-am", b)
	}
	fixture(t, dir, "switch", "-q", "main")
	return dir
}

func head(t *testing.T, dir string) (string, string) {
	t.Helper()
	h := ReadHead(dir)
	if h == nil {
		t.Fatal("no HEAD")
	}
	return h.Branch, h.Commit
}

func switchTo(t *testing.T, s *Service, dir, target string) (SwitchResult, error) {
	t.Helper()
	b, c := head(t, dir)
	return s.Switch(context.Background(), dir, target, b, c, freeClaim)
}

func code(err error) string {
	var se *SwitchError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestSwitchBetweenEnvironmentBranches(t *testing.T) {
	s, calls := switchService(t)
	dir := envRepo(t)
	list := s.Branches(context.Background(), dir)
	if list.State != StateRepository || len(list.Branches) != 4 {
		t.Fatalf("%+v", list)
	}
	for _, b := range list.Branches {
		if b.Current != (b.Name == "main") {
			t.Fatalf("active branch wrong: %+v", list.Branches)
		}
	}
	for _, target := range []string{"dev", "demo", "prod", "main", "dev"} {
		p, err := s.PreviewSwitch(context.Background(), dir, target)
		if err != nil || !p.CanSwitch || p.Source.InProject != 1 {
			t.Fatalf("%s preview: %+v %v", target, p, err)
		}
		r, err := switchTo(t, s, dir, target)
		if err != nil || r.To != target || r.Head.Branch != target {
			t.Fatalf("%s: %+v %v", target, r, err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "main.tf"))
		if want := "# " + target + "\n"; target != "main" && !strings.HasPrefix(string(b), want) {
			t.Fatalf("%s: main.tf is %q", target, b)
		}
		list := s.Branches(context.Background(), dir)
		for _, b := range list.Branches {
			if b.Current != (b.Name == target) {
				t.Fatalf("active branch after switching to %s: %+v", target, list.Branches)
			}
		}
	}
	if _, err := switchTo(t, s, dir, "dev"); code(err) != "already_current" {
		t.Fatalf("switch to the current branch: %v", err)
	}
	// No branch was created and nothing but reads and the exact switch ran.
	if got := fixture(t, dir, "for-each-ref", "--format=%(refname)"); got != "refs/heads/demo\nrefs/heads/dev\nrefs/heads/main\nrefs/heads/prod" {
		t.Fatalf("refs changed: %q", got)
	}
	for _, c := range *calls {
		if slices.ContainsFunc(c, func(a string) bool {
			return slices.Contains([]string{"-c", "-C", "--force", "-f", "--discard-changes", "--merge", "fetch", "pull", "merge", "commit", "push", "stash", "reset", "rebase"}, a)
		}) {
			t.Fatalf("forbidden git argument: %v", c)
		}
	}
}

func TestSwitchRejectsMalformedAndUnknownBranches(t *testing.T) {
	s, _ := switchService(t)
	dir := envRepo(t)
	fixture(t, dir, "tag", "v1")
	// A remote-tracking branch is never checked out or turned into a local branch.
	fixture(t, dir, "update-ref", "refs/remotes/origin/staging", "HEAD")
	fixture(t, dir, "update-ref", "refs/remotes/origin/dev", "HEAD")
	ran := false
	s.Switcher = switcherFunc(func(context.Context, string, string) error { ran = true; return nil })
	for target, want := range map[string]string{
		"-f": "invalid_branch", "--force": "invalid_branch", "-": "invalid_branch", "dev..prod": "invalid_branch", "main:dev": "invalid_branch",
		"+main:dev": "invalid_branch", "HEAD~1": "invalid_branch", "HEAD": "invalid_branch", "dev\n": "invalid_branch", "dev\x00": "invalid_branch",
		"de\x1bv": "invalid_branch", "refs/heads/dev": "invalid_branch", "dev.lock": "invalid_branch", "/dev": "invalid_branch", "dev/": "invalid_branch",
		"dev@{1}": "invalid_branch", "": "invalid_branch", strings.Repeat("a", 300): "invalid_branch",
		"origin/dev": "unknown_branch", "staging": "unknown_branch", "nope": "unknown_branch", "v1": "unknown_branch", "Dev": "unknown_branch",
	} {
		if _, err := s.PreviewSwitch(context.Background(), dir, target); code(err) != want {
			t.Fatalf("preview %q: %v, want %s", target, err, want)
		}
		if _, err := switchTo(t, s, dir, target); code(err) != want {
			t.Fatalf("switch %q: %v, want %s", target, err, want)
		}
	}
	if ran {
		t.Fatal("git switch ran for an invalid branch")
	}
	if got := fixture(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads/"); strings.Contains(got, "staging") || strings.Count(got, "\n") != 3 {
		t.Fatalf("a local branch was created: %q", got)
	}
	if b, _ := head(t, dir); b != "main" {
		t.Fatalf("branch changed to %s", b)
	}
	// The real switcher refuses invalid names on its own too.
	if err := (Exec{}).Switch(context.Background(), dir, "-f"); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
}

type switcherFunc func(context.Context, string, string) error

func (f switcherFunc) Switch(ctx context.Context, dir, b string) error { return f(ctx, dir, b) }

func TestDirtyWorkingTreeBlocksSwitch(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"modified": func(t *testing.T, dir string) { write(t, filepath.Join(dir, "main.tf"), "# local edit\n") },
		"staged": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "main.tf"), "# local edit\n")
			fixture(t, dir, "add", "main.tf")
		},
		"untracked": func(t *testing.T, dir string) { write(t, filepath.Join(dir, "notes", "local edit"), "# local edit\n") },
		"non-IaC":   func(t *testing.T, dir string) { write(t, filepath.Join(dir, "README.md"), "# local edit\n") },
	}
	for name, dirty := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := switchService(t)
			dir := envRepo(t)
			dirty(t, dir)
			before := snapshotFiles(t, dir)
			p, err := s.PreviewSwitch(context.Background(), dir, "dev")
			if err != nil || p.CanSwitch || p.Tree.Clean || p.Blocked[0].Code != "dirty_tree" {
				t.Fatalf("%+v %v", p, err)
			}
			if _, err := switchTo(t, s, dir, "dev"); code(err) != "dirty_tree" {
				t.Fatalf("switch: %v", err)
			}
			if b, _ := head(t, dir); b != "main" || !maps(before, snapshotFiles(t, dir)) {
				t.Fatal("the dirty working tree was changed")
			}
		})
	}
}

func snapshotFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !strings.Contains(p, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestIgnoredFilesAreNeverOverwritten(t *testing.T) {
	s, _ := switchService(t)
	dir := repo(t)
	// dev tracks secret.auto.tfvars; main ignores it and keeps a local copy.
	fixture(t, dir, "switch", "-q", "-c", "dev")
	write(t, filepath.Join(dir, "secret.auto.tfvars"), "token = \"from-dev\"\n")
	fixture(t, dir, "add", ".")
	fixture(t, dir, "commit", "-q", "-m", "dev vars")
	fixture(t, dir, "switch", "-q", "main")
	write(t, filepath.Join(dir, ".gitignore"), "*.tfvars\n.terraform/\n")
	fixture(t, dir, "add", ".gitignore")
	fixture(t, dir, "commit", "-q", "-m", "ignore")
	write(t, filepath.Join(dir, "secret.auto.tfvars"), "token = \""+canary+"\"\n")
	write(t, filepath.Join(dir, ".terraform", "terraform.tfstate"), "{}")
	p, err := s.PreviewSwitch(context.Background(), dir, "dev")
	if err != nil || p.CanSwitch || !p.Tree.Clean || p.Blocked[0].Code != "ignored_overwrite" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := switchTo(t, s, dir, "dev"); code(err) != "ignored_overwrite" {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "secret.auto.tfvars")); !strings.Contains(string(b), canary) {
		t.Fatal("ignored file was overwritten")
	}
	// Without the colliding file, ignored folders the target does not track are kept.
	if err := os.Remove(filepath.Join(dir, "secret.auto.tfvars")); err != nil {
		t.Fatal(err)
	}
	if _, err := switchTo(t, s, dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".terraform", "terraform.tfstate")); err != nil {
		t.Fatal("ignored folder was removed")
	}
	if n := overwritesIgnored([]string{"a"}, []string{"a/b.tfvars"}); n != 1 {
		t.Fatalf("an ignored file inside a folder that becomes a file: %d", n)
	}
}

func TestDetachedHeadAndStaleReview(t *testing.T) {
	s, _ := switchService(t)
	dir := envRepo(t)
	_, commit := head(t, dir)
	fixture(t, dir, "checkout", "-q", "--detach", commit)
	list := s.Branches(context.Background(), dir)
	if !list.Head.Detached || slices.ContainsFunc(list.Branches, func(b Branch) bool { return b.Current }) {
		t.Fatalf("%+v", list)
	}
	p, err := s.PreviewSwitch(context.Background(), dir, "main")
	if err != nil || !p.CanSwitch || p.Warnings[0].Code != "detached_head" {
		t.Fatalf("%+v %v", p, err)
	}
	// The review must name the HEAD that is checked out now.
	for _, expected := range [][2]string{{"main", commit}, {"", strings.Repeat("0", 40)}} {
		if _, err := s.Switch(context.Background(), dir, "dev", expected[0], expected[1], freeClaim); code(err) != "head_changed" {
			t.Fatalf("%v: %v", expected, err)
		}
	}
	if r, err := s.Switch(context.Background(), dir, "main", "", commit, freeClaim); err != nil || r.From != "" || r.Head.Branch != "main" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSwitchNeedsTheTreeClaim(t *testing.T) {
	s, _ := switchService(t)
	dir := envRepo(t)
	locks := rootlock.New()
	// A plan running in the project holds its root; the switch is refused.
	release, _ := locks.Acquire(dir, "plan")
	b, c := head(t, dir)
	claim := func(top string) (func(), bool) { return locks.AcquireTree(top, "branch") }
	if _, err := s.Switch(context.Background(), dir, "dev", b, c, claim); code(err) != "operation_running" {
		t.Fatal(err)
	}
	release()
	// Concurrent switches of the same review: exactly one wins.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Switch(context.Background(), dir, []string{"dev", "demo", "prod", "dev"}[i], b, c, claim)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch code(err) {
		case "":
			won++
		case "operation_running", "head_changed":
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d switches won: %v", won, errs)
	}
}

func TestSwitchOutsideRepositoryAndMissingProjectFolder(t *testing.T) {
	s, _ := switchService(t)
	plain := t.TempDir()
	if _, err := s.PreviewSwitch(context.Background(), plain, "dev"); code(err) != "not_repository" {
		t.Fatal(err)
	}
	if list := s.Branches(context.Background(), plain); list.State != StateNotRepo || len(list.Branches) != 0 {
		t.Fatalf("%+v", list)
	}
	// A monorepo: the project is envs/prod, which exists only on main.
	dir := repo(t)
	fixture(t, dir, "branch", "bare")
	write(t, filepath.Join(dir, "envs", "prod", "main.tf"), mainTF)
	write(t, filepath.Join(dir, "envs", "dev", "main.tf"), mainTF)
	fixture(t, dir, "add", ".")
	fixture(t, dir, "commit", "-q", "-m", "envs")
	fixture(t, dir, "branch", "dev")
	project := filepath.Join(dir, "envs", "prod")
	p, err := s.PreviewSwitch(context.Background(), project, "bare")
	if err != nil || p.CanSwitch || p.Blocked[0].Code != "project_missing" {
		t.Fatalf("%+v %v", p, err)
	}
	b, c := head(t, project)
	if _, err := s.Switch(context.Background(), project, "dev", b, c, freeClaim); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedWorktreeAndHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook fixture is a POSIX shell script")
	}
	s, _ := switchService(t)
	dir := envRepo(t)
	// A post-checkout hook in the repository must not run.
	marker := filepath.Join(dir, "..", filepath.Base(dir)+"-hook-ran")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	write(t, hook, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	fixture(t, dir, "worktree", "add", "-q", wt, "dev")
	wt, _ = filepath.EvalSymlinks(wt)
	// The setup's own worktree add runs the hook; only Hclaro's switches are checked.
	if err := os.Remove(marker); err != nil {
		t.Fatalf("the hook fixture did not run during setup: %v", err)
	}
	if list := s.Branches(context.Background(), wt); !list.Head.Worktree || list.Head.Branch != "dev" {
		t.Fatalf("%+v", list.Head)
	}
	// main is checked out in the main worktree: Git refuses and Hclaro reports it.
	if _, err := switchTo(t, s, wt, "main"); code(err) != "switch_failed" {
		t.Fatal(err)
	}
	if _, err := switchTo(t, s, wt, "prod"); err != nil {
		t.Fatal(err)
	}
	if b, _ := head(t, dir); b != "main" {
		t.Fatalf("the main worktree changed to %s", b)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the post-checkout hook ran")
	}
}

func TestSwitchMakesSourceFingerprintStale(t *testing.T) {
	s, _ := switchService(t)
	dir := envRepo(t)
	recorded := s.Fingerprint(dir)
	if _, err := switchTo(t, s, dir, "prod"); err != nil {
		t.Fatal(err)
	}
	st := s.Compare(recorded, dir)
	if !st.HeadChanged || !st.FilesChanged {
		t.Fatalf("%+v", st)
	}
}

// The switch outlives the request: cancelling it (a closed tab, a deadline) must not kill
// Git halfway through the checkout.
func TestStartedSwitchIsNotCancelledWithTheRequest(t *testing.T) {
	s, _ := switchService(t)
	dir := envRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Switcher = switcherFunc(func(sctx context.Context, top, branch string) error {
		cancel()
		if sctx.Err() != nil {
			t.Error("the switch was cancelled with the request")
		}
		return (Exec{}).Switch(sctx, top, branch)
	})
	b, c := head(t, dir)
	if _, err := s.Switch(ctx, dir, "dev", b, c, freeClaim); err != nil {
		t.Fatal(err)
	}
	// A review of the old HEAD now names the switched-to branch: it is stale, not current.
	if _, err := s.Switch(context.Background(), dir, "dev", b, c, freeClaim); code(err) != "head_changed" {
		t.Fatal(err)
	}
}
