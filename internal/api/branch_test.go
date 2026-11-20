package api

import (
	"context"
	"fmt"
	"net/http"
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
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/gitctx"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

type switchRecords struct {
	mu      sync.Mutex
	records []history.Event
	git     [][]string
}

func (r *switchRecords) Record(rec history.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
}

func (r *switchRecords) finals() []history.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []history.Event{}
	for _, rec := range r.records {
		if rec.Final && rec.Kind == "branch" {
			out = append(out, rec)
		}
	}
	return out
}

// newBranchServer serves a launch repository with branches main, dev and prod. Only
// allowlisted reads and the exact switch argv may run.
func newBranchServer(t *testing.T) (*planServer, string, *switchRecords) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	rec := &switchRecords{}
	run := gitctx.Exec{Observe: func(a []string) {
		rec.mu.Lock()
		rec.git = append(rec.git, a)
		rec.mu.Unlock()
	}}
	svc := gitctx.New()
	svc.Runner, svc.OpenURL = run, nil
	svc.Switcher, svc.Environ = run, func() []string { return nil }
	t.Cleanup(func() {
		for _, c := range rec.git {
			if !gitctx.Allowed(c) && !(len(c) == 5 && slices.Equal(c, gitctx.SwitchArgs(c[4]))) {
				t.Errorf("unexpected git command: %v", c)
			}
		}
	})
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", root)
	put(t, filepath.Join(root, "main.tf"), launchTF)
	gitSetup(t, root, "init", "-q")
	gitSetup(t, root, "add", ".")
	gitSetup(t, root, "commit", "-q", "-m", "initial")
	for _, b := range []string{"dev", "prod"} {
		gitSetup(t, root, "switch", "-q", "-c", b, "main")
		put(t, filepath.Join(root, "main.tf"), "# "+b+"\n"+launchTF)
		gitSetup(t, root, "commit", "-q", "-am", b)
	}
	gitSetup(t, root, "switch", "-q", "main")
	fake := &enginetest.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State, PlanJSON: fixtures.PlanAllActions}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform", nil)
	manager := operations.New(store, fake, root, operations.Deps{})
	manager.TempDir = t.TempDir()
	manager.Reload = func(context.Context) {}
	manager.Git, manager.Recorder = svc, rec
	manager.GitProbe = func(context.Context, string) preflight.GitResult {
		return preflight.GitResult{State: preflight.GitClean, Paths: []string{}}
	}
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	services := &Services{}
	h := Handler(store, manager, nil, services, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	return &planServer{t, h, fake, manager, services}, root, rec
}

func switchBody(target string, h *gitctx.Head) string {
	return fmt.Sprintf(`{"branch":%q,"expectedBranch":%q,"expectedCommit":%q}`, target, h.Branch, h.Commit)
}

func TestBranchSwitchOverHTTP(t *testing.T) {
	s, root, rec := newBranchServer(t)
	list := decode[gitctx.BranchList](t, s.do("POST", "/api/v1/git/branches", `{}`))
	if len(list.Branches) != 3 || !list.Branches[slices.IndexFunc(list.Branches, func(b gitctx.Branch) bool { return b.Name == "main" })].Current {
		t.Fatalf("%+v", list)
	}
	// A plan made on main.
	op, err := s.manager.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID := s.wait(op.ID).PlanID
	w := s.do("POST", "/api/v1/git/switch/preview", `{"branch":"dev"}`)
	preview := decode[gitctx.SwitchPreview](t, w)
	if w.Code != 200 || !preview.CanSwitch || preview.From.Branch != "main" || preview.To.Name != "dev" || preview.Source.InProject != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// A stale review is refused: the HEAD it names is not checked out.
	stale := *list.Head
	stale.Commit = strings.Repeat("0", 40)
	if w := s.do("POST", "/api/v1/git/switch", switchBody("dev", &stale)); w.Code != 409 || !strings.Contains(w.Body.String(), "head_changed") {
		t.Fatalf("stale %d %s", w.Code, w.Body)
	}
	w = s.do("POST", "/api/v1/git/switch", switchBody("dev", preview.From))
	if w.Code != 200 || strings.Contains(w.Body.String(), root) {
		t.Fatalf("switch %d %s", w.Code, w.Body)
	}
	if h := gitctx.ReadHead(root); h.Branch != "dev" {
		t.Fatalf("on %s", h.Branch)
	}
	// The Terraform workspace is not changed and no engine command ran for the switch.
	if snap := s.manager.Running(); snap {
		t.Fatal("an operation started")
	}
	if got := decode[struct {
		Info workspace.Info `json:"info"`
	}](t, s.do("GET", "/api/v1/workspace", "")); got.Info.Workspace != "demo" {
		t.Fatalf("workspace %q", got.Info.Workspace)
	}
	// The plan made on main is now stale and cannot be applied.
	type detail struct {
		SourceStatus gitctx.SourceStatus    `json:"sourceStatus"`
		Apply        operations.ApplyStatus `json:"apply"`
	}
	d := decode[detail](t, s.do("GET", "/api/v1/plans/"+planID, ""))
	if !d.SourceStatus.HeadChanged || !d.SourceStatus.FilesChanged || d.Apply.Available {
		t.Fatalf("%+v", d)
	}
	finals := rec.finals()
	if len(finals) != 2 || finals[0].Status != operations.Failed || finals[0].Diagnostic.Code != "head_changed" || finals[1].Status != operations.Succeeded || finals[1].From != "main" || finals[1].To != "dev" {
		t.Fatalf("%+v", finals)
	}
}

func TestBranchSwitchRefusals(t *testing.T) {
	s, root, rec := newBranchServer(t)
	h := gitctx.ReadHead(root)
	for target, want := range map[string]string{"--force": "invalid_branch", "HEAD~1": "invalid_branch", "origin/dev": "unknown_branch", "dev..prod": "invalid_branch", "nope": "unknown_branch"} {
		for path, body := range map[string]string{"/api/v1/git/switch/preview": fmt.Sprintf(`{"branch":%q}`, target), "/api/v1/git/switch": switchBody(target, h)} {
			w := s.do("POST", path, body)
			if w.Code < 400 || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("%s %q: %d %s", path, target, w.Code, w.Body)
			}
		}
	}
	for _, op := range rec.finals() {
		if op.To != "" || op.Status != operations.Failed || op.Ran {
			t.Fatalf("a refused name was recorded: %+v", op)
		}
	}
	if w := s.do("POST", "/api/v1/git/switch", `{"branch":"dev","create":true}`); w.Code != 400 {
		t.Fatalf("unknown field %d", w.Code)
	}
	// A running operation in the project holds its root: no switch.
	release, _ := s.manager.Locks.Acquire(root, "plan")
	if w := s.do("POST", "/api/v1/git/switch", switchBody("dev", h)); w.Code != 409 || !strings.Contains(w.Body.String(), "operation_running") {
		t.Fatalf("busy %d %s", w.Code, w.Body)
	}
	release()
	// So does one in another project of the same repository (a monorepo Hub project).
	other := filepath.Join(root, "envs", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	release, _ = s.manager.Locks.Acquire(other, "init")
	if w := s.do("POST", "/api/v1/git/switch", switchBody("dev", h)); w.Code != 409 || !strings.Contains(w.Body.String(), "operation_running") {
		t.Fatalf("monorepo busy %d %s", w.Code, w.Body)
	}
	release()
	if err := os.RemoveAll(filepath.Join(root, "envs")); err != nil {
		t.Fatal(err)
	}
	// Uncommitted changes block the switch and are kept.
	put(t, filepath.Join(root, "main.tf"), "# local work\n")
	w := s.do("POST", "/api/v1/git/switch/preview", `{"branch":"dev"}`)
	if p := decode[gitctx.SwitchPreview](t, w); p.CanSwitch || p.Blocked[0].Code != "dirty_tree" {
		t.Fatalf("%s", w.Body)
	}
	if w := s.do("POST", "/api/v1/git/switch", switchBody("dev", h)); w.Code != 409 || !strings.Contains(w.Body.String(), "dirty_tree") {
		t.Fatalf("dirty %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.tf")); string(b) != "# local work\n" || gitctx.ReadHead(root).Branch != "main" {
		t.Fatal("local work was changed")
	}
}

func TestBranchRoutesNeedSessionOriginHostAndCSRF(t *testing.T) {
	s, root, rec := newBranchServer(t)
	h := gitctx.ReadHead(root)
	for _, path := range []string{"/api/v1/git/branches", "/api/v1/git/switch/preview", "/api/v1/git/switch"} {
		body := switchBody("dev", h)
		if path == "/api/v1/git/branches" {
			body = `{}`
		}
		for _, change := range []func(*http.Request){
			func(r *http.Request) { r.Header.Del("Authorization") },
			func(r *http.Request) { r.Header.Del("X-Hclaro-CSRF") },
			func(r *http.Request) { r.Header.Set("X-Hclaro-CSRF", "forged") },
			func(r *http.Request) { r.Header.Del("Origin") },
			func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
			func(r *http.Request) { r.Host = "evil.example" },
			func(r *http.Request) { r.Header.Del("Content-Type") },
		} {
			r := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+session)
			r.Header.Set("Origin", "http://"+host)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Hclaro-CSRF", csrf)
			change(r)
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatalf("%s accepted: %d", path, w.Code)
			}
		}
		if w := s.do("GET", path, ""); w.Code < 400 {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
	}
	if len(rec.git) != 0 || gitctx.ReadHead(root).Branch != "main" {
		t.Fatalf("refused requests ran git: %v", rec.git)
	}
}

// A Hub project in a monorepo: an operation in a sibling project holds the working tree.
func TestHubBranchSwitchInAMonorepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	s := newHubServer(t)
	s.hub.Git = gitctx.New()
	s.hub.Git.Runner, s.hub.Git.OpenURL = gitctx.Exec{}, nil
	s.hub.Git.Environ = func() []string { return nil }
	t.Setenv("HOME", s.dir)
	put(t, filepath.Join(s.dir, "network", "main.tf"), "# network\n")
	put(t, filepath.Join(s.dir, "platform", "main.tf"), "# platform\n")
	gitSetup(t, s.dir, "init", "-q")
	gitSetup(t, s.dir, "add", ".")
	gitSetup(t, s.dir, "commit", "-q", "-m", "initial")
	gitSetup(t, s.dir, "branch", "dev")
	network, platform := s.add("network"), s.add("platform")
	h := gitctx.ReadHead(filepath.Join(s.dir, "network"))
	path := "/api/v1/projects/" + network.ID + "/git/switch"
	release, ok := s.hub.Locks.Acquire(platform.RootPath, "init")
	if !ok {
		t.Fatal("claim refused")
	}
	if w := s.do("POST", path, switchBody("dev", h)); w.Code != 409 || !strings.Contains(w.Body.String(), "operation_running") {
		t.Fatalf("sibling busy %d %s", w.Code, w.Body)
	}
	release()
	if w := s.do("POST", path, switchBody("dev", h)); w.Code != 200 {
		t.Fatalf("switch %d %s", w.Code, w.Body)
	}
	if got := gitctx.ReadHead(filepath.Join(s.dir, "platform")); got.Branch != "dev" {
		t.Fatalf("on %s", got.Branch)
	}
}
