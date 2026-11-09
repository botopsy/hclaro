package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

// gitSetup runs Git to build a disposable repository (test setup only).
func gitSetup(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Hclaro Test", "-c", "user.email=test@example.invalid", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitctx.Environ(os.Environ()), "HOME="+dir, "XDG_CONFIG_HOME="+dir, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitRecorder records editor links and Git commands run by the code under test.
type gitRecorder struct {
	mu     sync.Mutex
	calls  [][]string
	opened []string
}

func (g *gitRecorder) service(t *testing.T) *gitctx.Service {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	s := gitctx.NewWith(gitctx.Exec{Observe: func(a []string) {
		g.mu.Lock()
		g.calls = append(g.calls, a)
		g.mu.Unlock()
	}}, func(_ context.Context, link string) error {
		g.mu.Lock()
		g.opened = append(g.opened, link)
		g.mu.Unlock()
		return nil
	})
	s.Environ = func() []string { return nil }
	t.Cleanup(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, c := range g.calls {
			if !gitctx.Allowed(c) || !slices.Contains([]string{"rev-parse", "status", "diff"}, c[0]) {
				t.Errorf("non read-only git command: %v", c)
			}
		}
	})
	return s
}

const launchTF = `resource "aws_s3_bucket" "artifacts" {
  bucket = "artifacts"
}

resource "aws_iam_role" "deployer" {
  name = "deployer"
}
`

// newGitLaunchServer serves a launch project that is a Git repository, planned with the
// fake engine.
func newGitLaunchServer(t *testing.T) (*planServer, string, *gitRecorder) {
	t.Helper()
	rec := &gitRecorder{}
	svc := rec.service(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", root)
	put(t, filepath.Join(root, "main.tf"), launchTF)
	put(t, filepath.Join(root, "prod.tfvars"), "region = \"eu-west-1\"\n")
	gitSetup(t, root, "init", "-q")
	gitSetup(t, root, "add", ".")
	gitSetup(t, root, "commit", "-q", "-m", "initial")
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State, PlanJSON: fixtures.PlanAllActions}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, root)
	manager.TempDir = t.TempDir()
	manager.Reload = func(context.Context) {}
	manager.Git = svc
	manager.GitProbe = func(context.Context, string) preflight.GitResult {
		return preflight.GitResult{State: preflight.GitClean, Paths: []string{}}
	}
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	h := Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	return &planServer{t, h, fake, manager}, root, rec
}

func fileID(t *testing.T, c gitctx.GitContext, path string) string {
	t.Helper()
	for _, f := range c.Files {
		if f.Path == path {
			return f.ID
		}
	}
	t.Fatalf("%s not listed: %+v", path, c.Files)
	return ""
}

func TestLaunchGitStatusDiffAndEditor(t *testing.T) {
	s, root, rec := newGitLaunchServer(t)
	head := decode[gitctx.GitContext](t, s.do("GET", "/api/v1/git/head", ""))
	if head.State != gitctx.StateRepository || head.Head.Branch != "main" || head.Repository.ProjectPath != "." {
		t.Fatalf("head %+v", head)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("reading HEAD ran git: %v", rec.calls)
	}
	put(t, filepath.Join(root, "main.tf"), launchTF+"\nresource \"aws_db_instance\" \"db\" {\n  password = \""+fixtures.Canary+"\"\n}\n")
	put(t, filepath.Join(root, "prod.tfvars"), "db_password = \""+fixtures.Canary+"\"\n")
	w := s.do("POST", "/api/v1/git/status", `{}`)
	status := decode[gitctx.GitContext](t, w)
	if w.Code != 200 || status.Clean || len(status.Files) != 2 || strings.Contains(w.Body.String(), root) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	w = s.do("POST", "/api/v1/git/diff", fmt.Sprintf(`{"fileId":%q}`, fileID(t, status, "main.tf")))
	diff := decode[gitctx.GitDiff](t, w)
	if w.Code != 200 || diff.Added != 4 || diff.Redacted != 1 || !strings.Contains(w.Body.String(), `password = \"[redacted]\"`) {
		t.Fatalf("diff %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/git/diff", fmt.Sprintf(`{"fileId":%q}`, fileID(t, status, "prod.tfvars"))); w.Code != 409 || !strings.Contains(w.Body.String(), "not_diffable") {
		t.Fatalf("tfvars %d %s", w.Code, w.Body)
	}
	for body, code := range map[string]string{
		`{"fileId":"../../etc/passwd"}`:                          "invalid_file",
		`{"fileId":"0123456789abcdef0123456789abcdef"}`:          "file_not_found",
		`{"path":"/etc/passwd"}`:                                 "invalid_request",
		`{"fileId":"0123456789abcdef0123456789abcdef","x":true}`: "invalid_request",
	} {
		if w := s.do("POST", "/api/v1/git/diff", body); !strings.Contains(w.Body.String(), code) {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	// Source reference of a recorded resource, then open it in an editor.
	page := decode[struct {
		Resources []struct{ ID, Address string }
	}](t, s.do("GET", "/api/v1/resources?query=deployer", ""))
	resourceID := page.Resources[0].ID
	ref := decode[gitctx.SourceReference](t, s.do("GET", "/api/v1/resources/"+resourceID+"/source", ""))
	if ref.State != "found" || ref.Path != "main.tf" || ref.Line != 5 || ref.Confidence != "block" {
		t.Fatalf("ref %+v", ref)
	}
	w = s.do("POST", "/api/v1/editor/open", fmt.Sprintf(`{"fileId":%q,"line":%d,"editor":"vscode"}`, ref.FileID, ref.Line))
	if w.Code != 200 || len(rec.opened) != 1 || !strings.HasPrefix(rec.opened[0], "vscode://file/") || !strings.HasSuffix(rec.opened[0], "/main.tf:5") || strings.Contains(w.Body.String(), root) {
		t.Fatalf("editor %d %s %v", w.Code, w.Body, rec.opened)
	}
	// No editor executable, scheme, URL or path is accepted from the browser.
	for _, body := range []string{
		fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"sh"}`, ref.FileID),
		fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"/Applications/Calculator.app"}`, ref.FileID),
		fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"vscode","url":"javascript:alert(1)"}`, ref.FileID),
		fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"vscode","path":"/etc/passwd"}`, ref.FileID),
		fmt.Sprintf(`{"fileId":%q,"line":0,"editor":"vscode"}`, ref.FileID),
		fmt.Sprintf(`{"fileId":%q,"line":99999,"editor":"cursor"}`, ref.FileID),
		`{"fileId":"0123456789abcdef0123456789abcdef","line":1,"editor":"vscode"}`,
	} {
		if w := s.do("POST", "/api/v1/editor/open", body); w.Code < 400 {
			t.Fatalf("%s accepted", body)
		}
	}
	if len(rec.opened) != 1 {
		t.Fatalf("extra opens %v", rec.opened)
	}
	if w := s.do("GET", "/api/v1/git/head?path=/etc", ""); w.Code != 400 {
		t.Fatalf("head query %d", w.Code)
	}
	if w := s.do("GET", "/api/v1/resources/0123456789abcdef0123456789abcdef/source", ""); w.Code != 404 {
		t.Fatalf("missing resource %d", w.Code)
	}
}

func TestGitRoutesNeedSessionOriginAndCSRF(t *testing.T) {
	s, _, rec := newGitLaunchServer(t)
	for _, path := range []string{"/api/v1/git/status", "/api/v1/git/diff", "/api/v1/editor/open"} {
		for _, missing := range []string{"Authorization", "X-Hclaro-CSRF", "Origin", "Content-Type"} {
			r := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+session)
			r.Header.Set("Origin", "http://"+host)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Hclaro-CSRF", csrf)
			r.Header.Del(missing)
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatalf("%s without %s: %d", path, missing, w.Code)
			}
		}
		r := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+session)
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Hclaro-CSRF", csrf)
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s cross-origin: %d", path, w.Code)
		}
		// GET cannot run Git.
		if w := s.do("GET", path, ""); w.Code < 400 {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
	}
	if len(rec.calls) != 0 || len(rec.opened) != 0 {
		t.Fatalf("refused requests ran git or opened an editor: %v %v", rec.calls, rec.opened)
	}
}

func TestPlanSourceStatusOverHTTP(t *testing.T) {
	s, root, _ := newGitLaunchServer(t)
	op, err := s.manager.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := s.wait(op.ID).PlanID
	type detail struct {
		Source       *gitctx.PlanSourceFingerprint `json:"source"`
		SourceStatus gitctx.SourceStatus           `json:"sourceStatus"`
		Apply        operations.ApplyStatus        `json:"apply"`
	}
	d := decode[detail](t, s.do("GET", "/api/v1/plans/"+id, ""))
	if d.Source == nil || d.Source.Branch != "main" || d.SourceStatus.State != "unchanged" || !d.Apply.Available {
		t.Fatalf("%+v", d)
	}
	put(t, filepath.Join(root, "main.tf"), launchTF+"# edited after the plan\n")
	d = decode[detail](t, s.do("GET", "/api/v1/plans/"+id, ""))
	if d.SourceStatus.State != "changed" || !d.SourceStatus.FilesChanged || d.Apply.Available {
		t.Fatalf("%+v", d)
	}
	w := s.do("POST", "/api/v1/plans/"+id+"/preflight", `{}`)
	report := decode[operations.PreflightReport](t, w)
	blocked := false
	for _, c := range report.Checks {
		blocked = blocked || c.ID == preflight.CheckSource && c.Status == preflight.Fail && c.Severity == preflight.Block
	}
	if !blocked || report.Binding != "" {
		t.Fatalf("%+v", report.Checks)
	}
}

func TestHubGitIsolationAndSource(t *testing.T) {
	s := newHubServer(t)
	rec := &gitRecorder{}
	s.hub.Git = rec.service(t)
	t.Setenv("HOME", s.dir)
	put(t, filepath.Join(s.dir, "network", "buckets.tf"), "resource \"aws_s3_bucket\" \"dev_logs\" {}\n")
	gitSetup(t, s.dir, "init", "-q")
	gitSetup(t, s.dir, "add", ".")
	gitSetup(t, s.dir, "commit", "-q", "-m", "initial")
	network, platform := s.add("network"), s.add("platform")
	put(t, filepath.Join(s.dir, "network", "main.tf"), "# changed\n")
	put(t, filepath.Join(s.dir, "platform", "main.tofu"), "# changed\n")
	status := decode[gitctx.GitContext](t, s.do("POST", "/api/v1/projects/"+network.ID+"/git/status", `{}`))
	if status.Repository.ProjectPath != "network" || len(status.Files) != 1 || status.Counts.Outside != 1 {
		t.Fatalf("network %+v", status)
	}
	id := fileID(t, status, "network/main.tf")
	// The ID is not valid through another project.
	if w := s.do("POST", "/api/v1/projects/"+platform.ID+"/git/diff", fmt.Sprintf(`{"fileId":%q}`, id)); w.Code != 404 {
		t.Fatalf("cross-project diff %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/projects/"+network.ID+"/git/diff", fmt.Sprintf(`{"fileId":%q}`, id)); w.Code != 200 {
		t.Fatalf("diff %d %s", w.Code, w.Body)
	}
	cid := s.open(network.ID, "dev")
	var page struct {
		Resources []struct{ ID, Address string }
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(page.Resources) == 0; time.Sleep(10 * time.Millisecond) {
		w := s.do("GET", "/api/v1/contexts/"+cid+"/resources?query=dev_logs", "")
		if w.Code == 200 {
			_ = json.Unmarshal(w.Body.Bytes(), &page)
		}
	}
	if len(page.Resources) == 0 {
		t.Fatal("context did not load")
	}
	ref := decode[gitctx.SourceReference](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources/"+page.Resources[0].ID+"/source", ""))
	if ref.State != "found" || ref.Path != "network/buckets.tf" || ref.Line != 1 {
		t.Fatalf("ref %+v", ref)
	}
	if w := s.do("POST", "/api/v1/projects/"+platform.ID+"/editor/open", fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"vscode"}`, ref.FileID)); w.Code != 404 {
		t.Fatalf("cross-project editor %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/projects/"+network.ID+"/editor/open", fmt.Sprintf(`{"fileId":%q,"line":1,"editor":"cursor"}`, ref.FileID)); w.Code != 200 || len(rec.opened) != 1 || !strings.HasPrefix(rec.opened[0], "cursor://file/") {
		t.Fatalf("editor %d %s %v", w.Code, w.Body, rec.opened)
	}
	if w := s.do("POST", "/api/v1/projects/0123456789abcdef0123456789abcdef/git/status", `{}`); w.Code != 404 {
		t.Fatalf("unknown project %d", w.Code)
	}
	if w := s.do("GET", "/api/v1/contexts/"+strings.Repeat("0", 64)+"/resources/x/source", ""); w.Code != 404 {
		t.Fatalf("closed context %d", w.Code)
	}
}

func TestGitUnavailableStates(t *testing.T) {
	s := newHubServer(t)
	p := s.add("network")
	if w := s.do("POST", "/api/v1/projects/"+p.ID+"/git/status", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "git_unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("GET", "/api/v1/git/head", ""); w.Code != 409 {
		t.Fatalf("launch %d %s", w.Code, w.Body)
	}
	s.hub.Git = gitctx.New()
	if w := s.do("GET", "/api/v1/projects/"+p.ID+"/git/head", ""); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
