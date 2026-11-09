package operations

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

// gitFixture runs Git to build a disposable repository in a temporary directory. It is
// test setup only; the code under test runs Git through gitctx's read-only runner, which
// the recorder below checks.
func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Hclaro Test", "-c", "user.email=test@example.invalid", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitctx.Environ(os.Environ()), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeSource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sourceSetup is setup() with the project root at repo/app, a local module at repo/mod
// (outside the root) and a gitctx service whose Git commands are recorded.
func sourceSetup(t *testing.T, fake *engine.Fake) (*Manager, string, string, *[][]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", repo)
	root := filepath.Join(repo, "app")
	writeSource(t, filepath.Join(root, "main.tf"), "module \"m\" {\n  source = \"../mod\"\n}\n")
	writeSource(t, filepath.Join(repo, "mod", "main.tf"), "resource \"null_resource\" \"a\" {}\n")
	writeSource(t, filepath.Join(repo, ".gitignore"), ".terraform/\n")
	manifest, _ := json.Marshal(map[string]any{"Modules": []map[string]string{{"Key": "", "Source": "", "Dir": "."}, {"Key": "m", "Source": "../mod", "Dir": "../mod"}}})
	writeSource(t, filepath.Join(root, ".terraform", "modules", "modules.json"), string(manifest))
	gitFixture(t, repo, "init", "-q")
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-q", "-m", "initial")
	store := workspace.New(workspace.Info{Project: "app", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	m := NewManager(store, fake, root)
	m.TempDir = t.TempDir()
	calls := &[][]string{}
	m.Git = gitctx.NewWith(gitctx.Exec{Observe: func(a []string) { *calls = append(*calls, a) }}, nil)
	m.Git.Environ = func() []string { return nil }
	m.GitProbe = (&gitState{r: clean}).probe
	t.Cleanup(func() {
		m.Close(5 * time.Second)
		for _, c := range *calls {
			if !gitctx.Allowed(c) || !slices.Contains([]string{"rev-parse", "status", "diff"}, c[0]) {
				t.Errorf("non read-only git command: %v", c)
			}
		}
	})
	return m, repo, root, calls
}

func TestPlanRecordsSourceFingerprint(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, repo, _, calls := sourceSetup(t, fake)
	head := gitFixture(t, repo, "rev-parse", "HEAD")
	id := planned(t, m)
	p, _ := m.Plan(id)
	if p.Source == nil || p.Source.Commit != head || p.Source.Branch != "main" || p.Source.Content == "" || p.Source.Files != 2 || p.Source.LocalModules != 1 || !p.Source.DirtyKnown || p.Source.Dirty {
		t.Fatalf("source %+v", p.Source)
	}
	if len(*calls) == 0 {
		t.Fatal("git status was not run")
	}
	if st := m.SourceStatus(p); st.State != "unchanged" {
		t.Fatalf("%+v", st)
	}
	r := runPreflight(t, m, id, PreflightRequest{})
	c, ok := find(r, preflight.CheckSource)
	if !ok || c.Status != preflight.Pass || r.Blocked {
		t.Fatalf("%+v %+v", c, r.Checks)
	}
	if !strings.Contains(c.Evidence, head[:12]) {
		t.Fatalf("evidence %q", c.Evidence)
	}
}

func TestModifiedTrackedFileAfterPlanBlocksApply(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _, root, _ := sourceSetup(t, fake)
	id := planned(t, m)
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	// Edit a tracked HCL file in the root after the review.
	writeSource(t, filepath.Join(root, "main.tf"), "module \"m\" {\n  source = \"../mod\"\n}\n# edited\n")
	p, _ := m.Plan(id)
	if st := m.SourceStatus(p); st.State != "changed" || !st.FilesChanged || !strings.Contains(st.Message, "different source") {
		t.Fatalf("%+v", st)
	}
	if changed, status := m.ContextStatus(p); !changed || status.Available {
		t.Fatalf("status %v %+v", changed, status)
	}
	r := runPreflight(t, m, id, PreflightRequest{})
	if c, _ := find(r, preflight.CheckSource); c.Status != preflight.Fail || c.Severity != preflight.Block || !r.Blocked || r.Binding != "" {
		t.Fatalf("%+v", c)
	}
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); err == nil {
		t.Fatal("apply started")
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestLocalModuleChangeOutsideRootBlocksApply(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, repo, _, _ := sourceSetup(t, fake)
	id := planned(t, m)
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	// The module is outside the project root: the context fingerprint does not see it.
	writeSource(t, filepath.Join(repo, "mod", "main.tf"), "resource \"null_resource\" \"b\" {}\n")
	p, _ := m.Plan(id)
	if current, _ := m.currentFingerprint(p); current != p.Fingerprint {
		t.Fatal("expected the context fingerprint to be unchanged; this test covers the source fingerprint")
	}
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "preflight_blocked" && rejected(err) != "not_applicable" && rejected(err) != "context_changed" {
		t.Fatalf("apply: %v", err)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestHeadMoveWithSameFilesNeedsAcknowledgement(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, repo, _, _ := sourceSetup(t, fake)
	id := planned(t, m)
	writeSource(t, filepath.Join(repo, "README.md"), "docs\n")
	gitFixture(t, repo, "add", "README.md")
	gitFixture(t, repo, "commit", "-q", "-m", "docs")
	r := runPreflight(t, m, id, PreflightRequest{})
	c, _ := find(r, preflight.CheckSource)
	if c.Severity != preflight.Warn || c.Status != preflight.Fail || r.Blocked || !slices.Contains(r.Acknowledge, preflight.CheckSource) {
		t.Fatalf("%+v %+v", c, r.Acknowledge)
	}
	// Without the acknowledgement the approval is refused; with it, the apply runs.
	if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding}); rejected(err) != "acknowledgement_required" {
		t.Fatalf("%v", err)
	}
	a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartApply(id, a.Token, a.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, op.ID); done.Status != Succeeded || !slices.Contains(done.Preflight.Acknowledged, preflight.CheckSource) {
		t.Fatalf("%+v", done)
	}
}

func TestNoSourceTrackingKeepsM14Checks(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	m.GitProbe = (&gitState{r: clean}).probe
	id := planned(t, m)
	p, _ := m.Plan(id)
	if p.Source != nil {
		t.Fatal("source recorded without a service")
	}
	if _, ok := find(runPreflight(t, m, id, PreflightRequest{}), preflight.CheckSource); ok {
		t.Fatal("source check without a recorded fingerprint")
	}
}
