package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// Tests for Hub-scoped plan routes (M24.05, ADR M24.03). Hub engine commands run the
// synthetic CLI (scripts/fake-engine) in temporary roots; the launch project runs
// engine.Fake. Nothing contacts a real backend, provider or account.

type scopedServer struct {
	*hubServer
	launch *operations.Manager
	spy    string
	bin    string
	ids    map[string]string
	roots  map[string]string
}

func newScopedServer(t *testing.T) *scopedServer {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the synthetic engine")
	}
	bin := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	for _, name := range []string{"terraform", "tofu"} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, name+exe), "hclaro/scripts/fake-engine")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	spy := filepath.Join(t.TempDir(), "argv")
	t.Setenv("HCLARO_FAKE_SPY", spy)
	t.Setenv("HCLARO_FAKE_SPY_ENV", "CWD,AWS_PROFILE")
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_plan", "TF_CLI_ARGS_show", "TF_CLI_ARGS_workspace", "TF_DATA_DIR", "TF_WORKSPACE", "HCLARO_FAKE_PLAN", "HCLARO_FAKE_WORKSPACE"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	if err := fixtures.WriteHubProjects(dir); err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	hub := projects.NewHub(projects.Open(t.TempDir(), time.Now()), "")
	cli := func(root, preference, selected string, environ []string) *engine.CLI {
		name := preference
		if name == "" {
			name = "terraform"
		}
		return &engine.CLI{Name: name, Executable: filepath.Join(bin, name+exe), Root: root, SelectedWorkspace: selected, IgnoreInheritedWorkspace: true, Environ: environ}
	}
	hub.Engines = func(root, preference, selected string, environ []string) (string, string, projects.Adapter, error) {
		c := cli(root, preference, selected, environ)
		return c.Name, c.Executable, c, nil
	}
	hub.Executors = func(root, preference, selected string, environ []string) (string, string, engine.Planner, error) {
		c := cli(root, preference, selected, environ)
		return c.Name, c.Executable, c, nil
	}
	hub.ScopedExecution = true
	hub.TempDir = t.TempDir()
	hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	t.Cleanup(hub.Close)

	// The launch project is another root with its own manager, sharing the root claims.
	fake := &engine.Fake{Lines: bytes.Split(bytes.TrimSpace(fixtures.PlanLog), []byte("\n")), PlanJSON: fixtures.PlanAllActions, VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	launch := operations.NewManager(store, fake, t.TempDir())
	launch.TempDir = t.TempDir()
	launch.Locks = hub.Locks
	t.Cleanup(func() { launch.Close(5 * time.Second) })

	h := Handler(store, launch, hub, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	s := &scopedServer{hubServer: &hubServer{t, h, hub, &atomic.Int64{}, dir}, launch: launch, spy: spy, bin: bin, ids: map[string]string{}, roots: map[string]string{}}
	for _, p := range fixtures.HubProjects {
		summary, err := hub.Add(filepath.Join(dir, p.Dir), p.Dir, p.Engine)
		if err != nil {
			t.Fatal(err)
		}
		s.ids[p.Dir], s.roots[p.Dir] = summary.ID, summary.RootPath
	}
	return s
}

// do also fails when a response names the synthetic engine's location.
func (s *scopedServer) do(method, path, body string, opts ...opt) *httptest.ResponseRecorder {
	s.t.Helper()
	w := s.hubServer.do(method, path, body, opts...)
	if strings.Contains(w.Body.String(), s.bin) || strings.Contains(w.Body.String(), "HCLARO_FAKE_SPY") {
		s.t.Fatalf("%s %s response names the executable or environment: %s", method, path, w.Body)
	}
	return w
}

// openLoaded opens a fresh read context over HTTP and waits until its read finished.
func (s *scopedServer) openLoaded(project, ws, profileID string) string {
	s.t.Helper()
	w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":%q,"profileId":%q,"reload":true}`, s.ids[project], ws, profileID))
	if w.Code != 200 {
		s.t.Fatalf("open %s/%s: %d %s", project, ws, w.Code, w.Body)
	}
	cid := decode[projects.Context](s.t, w).ID
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		body := decode[map[string]any](s.t, s.do("GET", "/api/v1/contexts/"+cid, ""))
		if body["status"] != "loading" {
			return cid
		}
	}
	s.t.Fatal("context did not load")
	return ""
}

type planOptions struct {
	CanPlan               bool                         `json:"canPlan"`
	Code                  string                       `json:"code"`
	Reason                string                       `json:"reason"`
	Scope                 *projects.ScopeView          `json:"scope"`
	BackendAccessRequired bool                         `json:"backendAccessRequired"`
	VarFiles              []string                     `json:"varFiles"`
	ExecutionContext      *operations.ExecutionContext `json:"executionContext"`
	ContextFingerprint    string                       `json:"contextFingerprint"`
}

func (s *scopedServer) options(cid string) planOptions {
	s.t.Helper()
	w := s.do("GET", "/api/v1/contexts/"+cid+"/plans/options", "")
	if w.Code != 200 {
		s.t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	return decode[planOptions](s.t, w)
}

type scopedOp struct {
	operations.Operation
	Scope projects.ScopeView `json:"scope"`
}

// plan reads the options and starts a plan with them, as the UI would.
func (s *scopedServer) plan(cid string) scopedOp {
	s.t.Helper()
	o := s.options(cid)
	w := s.do("POST", "/api/v1/contexts/"+cid+"/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"contextFingerprint":%q}`, o.ContextFingerprint))
	if w.Code != 202 {
		s.t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	return decode[scopedOp](s.t, w)
}

func (s *scopedServer) waitScoped(cid, oid string) scopedOp {
	s.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		w := s.do("GET", "/api/v1/contexts/"+cid+"/operations/"+oid, "")
		if w.Code != 200 {
			s.t.Fatalf("operation: %d %s", w.Code, w.Body)
		}
		if op := decode[scopedOp](s.t, w); op.Status != operations.Running {
			return op
		}
	}
	s.t.Fatal("operation did not finish")
	return scopedOp{}
}

func (s *scopedServer) planCalls() [][]string {
	s.t.Helper()
	b, err := os.ReadFile(s.spy)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	out := [][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Split(line, "\x00")
		for _, f := range fields {
			if strings.Contains(f, "=") {
				continue
			}
			if f == "plan" {
				out = append(out, fields)
			}
			break
		}
	}
	return out
}

func spyField(fields []string, prefix string) string {
	for _, f := range fields {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix)
		}
	}
	return ""
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) map[string]string {
	t.Helper()
	body := decode[map[string]string](t, w)
	if w.Code != status || body["error"] != code {
		t.Fatalf("want %d %s, got %d %s", status, code, w.Code, w.Body)
	}
	return body
}

type scopedPlanDetail struct {
	operations.Plan
	CurrentEngineVersion string                 `json:"currentEngineVersion"`
	ContextChanged       bool                   `json:"contextChanged"`
	Apply                operations.ApplyStatus `json:"apply"`
	Scope                projects.ScopeView     `json:"scope"`
}

// A/dev, then B/staging, then A/prod: each plan runs in its own root and workspace, records
// its scope, fingerprints and saved-plan digest, and is listed only in its own context.
func TestScopedPlansRunInTheirOwnRootAndWorkspace(t *testing.T) {
	s := newScopedServer(t)
	type target struct{ project, ws, engine, version string }
	targets := []target{{"network", "dev", "terraform", "1.9.8"}, {"platform", "staging", "tofu", "1.8.3"}, {"network", "prod", "terraform", "1.9.8"}}
	plans := map[string]string{}
	for i, tc := range targets {
		cid := s.openLoaded(tc.project, tc.ws, "")
		o := s.options(cid)
		if !o.CanPlan || !o.BackendAccessRequired || o.Scope == nil || o.ExecutionContext != nil || o.ContextFingerprint != "" {
			t.Fatalf("%+v", o)
		}
		if v := *o.Scope; v.ContextID != cid || v.ProjectID != s.ids[tc.project] || v.RootPath != s.roots[tc.project] || v.Workspace != tc.ws || v.Engine != tc.engine || v.Version != tc.version || v.Backend != "local" || v.ExecutionMode != "local-cli" || v.Profile != nil {
			t.Fatalf("%+v", v)
		}
		op := s.plan(cid)
		if op.Kind != "plan" || op.Scope.Workspace != tc.ws || op.Scope.RootPath != s.roots[tc.project] {
			t.Fatalf("%+v", op)
		}
		done := s.waitScoped(cid, op.ID)
		if done.Status != operations.Succeeded || done.PlanID == "" {
			t.Fatalf("%+v", done)
		}
		calls := s.planCalls()
		if len(calls) != i+1 {
			t.Fatalf("%d plan calls", len(calls))
		}
		cwd, _ := filepath.EvalSymlinks(spyField(calls[i], "CWD="))
		if cwd != s.roots[tc.project] || spyField(calls[i], "TF_WORKSPACE=") != tc.ws {
			t.Fatalf("plan %d ran in %q workspace %q", i, cwd, spyField(calls[i], "TF_WORKSPACE="))
		}
		w := s.do("GET", "/api/v1/contexts/"+cid+"/plans/"+done.PlanID, "")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		p := decode[scopedPlanDetail](t, w)
		if p.Workspace != tc.ws || p.Engine != tc.engine || p.EngineVersion != tc.version || len(p.ArtifactSHA256) != 64 || p.Fingerprint == "" || p.Scope.ContextID != cid || p.Scope.Backend != "local" || p.ContextChanged {
			t.Fatalf("%+v", p.Plan)
		}
		// The saved plan is kept and, since M24.06, can be applied through this context only.
		if !p.Apply.Available || p.Apply.Code != "" || !p.Apply.ArtifactRetained {
			t.Fatalf("%+v", p.Apply)
		}
		for id := range p.Details {
			if w := s.do("GET", "/api/v1/contexts/"+cid+"/plans/"+p.ID+"/changes/"+id, ""); w.Code != 200 {
				t.Fatalf("change detail: %d", w.Code)
			}
			break
		}
		list := decode[struct {
			Plans []planSummary       `json:"plans"`
			Scope *projects.ScopeView `json:"scope"`
		}](t, s.do("GET", "/api/v1/contexts/"+cid+"/plans", ""))
		if len(list.Plans) != 1 || list.Plans[0].ID != p.ID || list.Scope == nil || list.Scope.ContextID != cid {
			t.Fatalf("%+v", list)
		}
		ops := decode[struct {
			Operations []operations.Operation `json:"operations"`
		}](t, s.do("GET", "/api/v1/contexts/"+cid+"/operations", ""))
		if len(ops.Operations) != 1 || ops.Operations[0].ID != op.ID {
			t.Fatalf("%+v", ops)
		}
		events := parseSSE(s.do("GET", "/api/v1/contexts/"+cid+"/operations/"+op.ID+"/events", "").Body.String())
		if len(events) == 0 || events[len(events)-1].event != "end" {
			t.Fatalf("%+v", events)
		}
		plans[cid] = p.ID
	}
	// The launch project saw none of it.
	if len(s.launch.Plans()) != 0 || len(s.launch.Operations()) != 0 {
		t.Fatal("scoped plan reached the launch manager")
	}
	if len(plans) != 3 {
		t.Fatal("contexts reused")
	}
}

// An ID of one context is indistinguishable from a missing one in another context, in the
// launch routes, and the other way around. Cancellation is bound to the owning context.
func TestScopedIDsDoNotCrossContexts(t *testing.T) {
	s := newScopedServer(t)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "staging", "")
	idle := s.openLoaded("network", "prod", "") // open, but no scope yet
	opA := s.plan(a)
	doneA := s.waitScoped(a, opA.ID)
	s.options(b) // B has a scope of its own
	// A launch plan.
	launchOp, err := s.launch.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if op, _, _ := s.launch.Operation(launchOp.ID); op.Status != operations.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launch plan did not finish")
		}
	}
	launchPlan := s.launch.Plans()[0].ID
	missing := strings.Repeat("0", 32)

	same := func(path string, status int, code string) {
		t.Helper()
		got := s.do("GET", path, "")
		if got.Code != status || got.Body.String() != fmt.Sprintf("{\"error\":%q}\n", code) {
			t.Fatalf("GET %s: %d %s", path, got.Code, got.Body)
		}
	}
	for _, cid := range []string{b, idle} {
		for _, id := range []string{doneA.PlanID, launchPlan, missing} {
			same("/api/v1/contexts/"+cid+"/plans/"+id, 404, "plan_not_found")
			same("/api/v1/contexts/"+cid+"/plans/"+id+"/changes/"+missing, 404, "plan_not_found")
			// Query parameters cannot redirect the lookup.
			same("/api/v1/contexts/"+cid+"/plans/"+id+"?contextId="+a+"&cid="+a, 404, "plan_not_found")
		}
		for _, id := range []string{opA.ID, launchOp.ID, missing} {
			same("/api/v1/contexts/"+cid+"/operations/"+id, 404, "operation_not_found")
			same("/api/v1/contexts/"+cid+"/operations/"+id+"/events", 404, "operation_not_found")
			wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/operations/"+id+"/cancel", `{}`), 404, "operation_not_found")
		}
		list := decode[map[string]any](t, s.do("GET", "/api/v1/contexts/"+cid+"/plans", ""))
		if len(list["plans"].([]any)) != 0 {
			t.Fatalf("%v", list)
		}
	}
	// Scoped IDs are unknown to the launch routes.
	same("/api/v1/plans/"+doneA.PlanID, 404, "plan_not_found")
	same("/api/v1/operations/"+opA.ID, 404, "operation_not_found")
	wantError(t, s.do("POST", "/api/v1/operations/"+opA.ID+"/cancel", `{}`), 404, "operation_not_found")
	if w := s.do("POST", "/api/v1/plans/"+doneA.PlanID+"/approval", `{"preflightBinding":"x","acknowledged":[]}`); w.Code != 404 {
		t.Fatalf("launch approval of a scoped plan: %d %s", w.Code, w.Body)
	}
	// Malformed and unknown context IDs.
	for _, cid := range []string{missing, "ABC", strings.Repeat("a", 64), "..%2f..%2fetc"} {
		if w := s.do("GET", "/api/v1/contexts/"+cid+"/plans/"+doneA.PlanID, ""); w.Code != 404 {
			t.Fatalf("%s: %d %s", cid, w.Code, w.Body)
		}
	}
	// A running plan can be cancelled only from its own context.
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	slow := s.plan(a)
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/operations/"+slow.ID+"/cancel", `{}`), 404, "operation_not_found")
	if op := decode[scopedOp](t, s.do("GET", "/api/v1/contexts/"+a+"/operations/"+slow.ID, "")); op.Status != operations.Running || op.CancelRequested {
		t.Fatalf("%+v", op)
	}
	if w := s.do("POST", "/api/v1/contexts/"+a+"/operations/"+slow.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if done := s.waitScoped(a, slow.ID); done.Status != operations.Cancelled {
		t.Fatalf("%+v", done)
	}
	// A closed context's plans are gone with it.
	if w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"dev","reload":true}`, s.ids["network"])); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	wantError(t, s.do("GET", "/api/v1/contexts/"+a+"/plans/"+doneA.PlanID, ""), 404, "context_closed")
	wantError(t, s.do("GET", "/api/v1/contexts/"+a+"/operations/"+opA.ID, ""), 404, "context_closed")
}

func TestScopedPlanRequestValidation(t *testing.T) {
	s := newScopedServer(t)
	if err := os.WriteFile(filepath.Join(s.roots["network"], "dev.tfvars"), []byte("# synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cid := s.openLoaded("network", "dev", "")
	o := s.options(cid)
	if len(o.VarFiles) != 1 || o.VarFiles[0] != "dev.tfvars" {
		t.Fatalf("%+v", o.VarFiles)
	}
	path := "/api/v1/contexts/" + cid + "/plans"
	for _, tc := range []struct {
		body, code string
		status     int
	}{
		{`{}`, "acknowledgement_required", 400},
		{`{"acknowledgeBackendAccess":false}`, "acknowledgement_required", 400},
		// No field can carry a target, path, binary, command, plan file or workspace.
		{`{"acknowledgeBackendAccess":true,"target":["aws_s3_bucket.dev_logs"]}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"planFile":"/tmp/x.tfplan"}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"executable":"/bin/sh"}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"command":["rm","-rf","/"]}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"workspace":"prod"}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"profileId":"` + strings.Repeat("a", 32) + `"}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"rootPath":"/"}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"env":{"AWS_PROFILE":"prod"}}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true} {}`, "invalid_request", 400},
		{`{"acknowledgeBackendAccess":true,"varFiles":["../dev.tfvars"]}`, "invalid_var_files", 400},
		{`{"acknowledgeBackendAccess":true,"varFiles":["/etc/passwd"]}`, "invalid_var_files", 400},
		{`{"acknowledgeBackendAccess":true,"varFiles":["dev.tfvars","dev.tfvars"]}`, "invalid_var_files", 400},
		{`{"acknowledgeBackendAccess":true,"replace":["aws_s3_bucket.dev_logs"]}`, "invalid_replace", 400},
		{`{"acknowledgeBackendAccess":true,"contextFingerprint":"reviewed-something-else"}`, "context_changed", 409},
	} {
		wantError(t, s.do("POST", path, tc.body), tc.status, tc.code)
	}
	if calls := s.planCalls(); len(calls) != 0 {
		t.Fatalf("a refused request ran the engine: %q", calls)
	}
	// A valid selection reaches the engine as the root-relative file only.
	w := s.do("POST", path, `{"acknowledgeBackendAccess":true,"varFiles":["dev.tfvars"]}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[scopedOp](t, w)
	if done := s.waitScoped(cid, op.ID); done.Status != operations.Succeeded || len(done.VarFiles) != 1 || done.VarFiles[0] != "dev.tfvars" {
		t.Fatalf("%+v", done)
	}
	if calls := s.planCalls(); len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-var-file=dev.tfvars") {
		t.Fatalf("%q", calls)
	}
	// One operation at a time per project root, across contexts.
	// (Opening a context of the same root while a plan runs is refused too: reads hold a
	// shared claim, so the other context is opened first.)
	other := s.openLoaded("network", "prod", "")
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	running := s.plan(cid)
	body := wantError(t, s.do("POST", "/api/v1/contexts/"+other+"/plans", `{"acknowledgeBackendAccess":true}`), 409, "project_busy")
	if !strings.Contains(body["reason"], "(plan)") || strings.Contains(body["reason"], cid) {
		t.Fatalf("%v", body)
	}
	wantError(t, s.do("POST", path, `{"acknowledgeBackendAccess":true}`), 409, "project_busy")
	s.do("POST", "/api/v1/contexts/"+cid+"/operations/"+running.ID+"/cancel", `{}`)
	s.waitScoped(cid, running.ID)
}

func TestScopedRoutesNeedGateSessionOriginAndCSRF(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	op := s.plan(cid)
	done := s.waitScoped(cid, op.ID)
	gets := []string{"/plans/options", "/plans", "/plans/" + done.PlanID, "/plans/" + done.PlanID + "/changes/" + strings.Repeat("0", 32), "/operations", "/operations/" + op.ID, "/operations/" + op.ID + "/events"}
	posts := []string{"/plans", "/operations/" + op.ID + "/cancel"}
	for _, p := range posts {
		path := "/api/v1/contexts/" + cid + p
		for _, tc := range []struct {
			name string
			opts []opt
			code int
		}{
			{"no session", []opt{without("Authorization")}, 401},
			{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
			{"wrong csrf", []opt{with("X-Hclaro-CSRF", strings.Repeat("c", 64))}, 403},
			{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
			{"no origin", []opt{without("Origin")}, 403},
			{"cross-site fetch", []opt{with("Sec-Fetch-Site", "cross-site")}, 403},
			{"form post", []opt{with("Content-Type", "text/plain")}, 415},
		} {
			if w := s.do("POST", path, `{"acknowledgeBackendAccess":true}`, tc.opts...); w.Code != tc.code {
				t.Fatalf("%s %s: %d", tc.name, p, w.Code)
			}
		}
	}
	for _, p := range gets {
		if w := s.do("GET", "/api/v1/contexts/"+cid+p, "", without("Authorization")); w.Code != 401 {
			t.Fatalf("GET %s without session: %d", p, w.Code)
		}
	}
	if len(s.planCalls()) != 1 {
		t.Fatal("a refused request ran the engine")
	}
	// With the gate off, every scoped route is unavailable, before any lookup.
	s.hub.ScopedExecution = false
	for _, p := range gets {
		wantError(t, s.do("GET", "/api/v1/contexts/"+cid+p, ""), 404, projects.CodeScopedDisabled)
		wantError(t, s.do("GET", "/api/v1/contexts/"+strings.Repeat("f", 32)+p, ""), 404, projects.CodeScopedDisabled)
	}
	for _, p := range posts {
		wantError(t, s.do("POST", "/api/v1/contexts/"+cid+p, `{"acknowledgeBackendAccess":true}`), 404, projects.CodeScopedDisabled)
	}
	if len(s.planCalls()) != 1 {
		t.Fatal("a disabled route ran the engine")
	}
}

func TestScopedPlanRefusalsAreTyped(t *testing.T) {
	s := newScopedServer(t)
	plan := func(cid string) *httptest.ResponseRecorder {
		return s.do("POST", "/api/v1/contexts/"+cid+"/plans", `{"acknowledgeBackendAccess":true}`)
	}
	backend := filepath.Join(s.roots["platform"], ".terraform", "terraform.tfstate")

	// Remote execution and unknown backends: options explain, plans are refused.
	for _, kind := range []string{"cloud", "remote", "weird-backend"} {
		if err := os.WriteFile(backend, []byte(`{"backend":{"type":"`+kind+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cid := s.openLoaded("platform", "staging", "")
		o := s.options(cid)
		if o.CanPlan || o.Code != projects.CodeExecutionUnsupported || o.Reason == "" || o.Scope != nil {
			t.Fatalf("%s: %+v", kind, o)
		}
		wantError(t, plan(cid), 409, projects.CodeExecutionUnsupported)
	}
	if err := os.Remove(backend); err != nil {
		t.Fatal(err)
	}

	// Missing credentials: the engine's failure is reported with a typed diagnostic.
	t.Setenv("HCLARO_FAKE_PLAN", "credentials")
	cid := s.openLoaded("network", "dev", "")
	done := s.waitScoped(cid, s.plan(cid).ID)
	if done.Status != operations.Failed || done.Diagnostic == nil || done.Diagnostic.Code != "credentials" || done.PlanID != "" {
		t.Fatalf("%+v", done)
	}
	t.Setenv("HCLARO_FAKE_PLAN", "")

	// A profile that needs a variable that is not set (for example a credential name) is
	// refused when the workspace is opened.
	p, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "needs-token", EnvNames: []string{"HCLARO_TEST_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HCLARO_TEST_TOKEN", "")
	os.Unsetenv("HCLARO_TEST_TOKEN")
	wantError(t, s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"dev","profileId":%q}`, s.ids["network"], p.ID)), 409, "env_missing")

	// A profile that becomes malformed after the workspace was opened (its variable file is
	// gone) stops the plan before anything runs, and closes the context.
	vars := filepath.Join(s.roots["network"], "prod.tfvars")
	if err := os.WriteFile(vars, []byte("# synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prod, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "prod", AWSProfile: "prod-sso", VarFiles: []string{"prod.tfvars"}})
	if err != nil {
		t.Fatal(err)
	}
	cid = s.openLoaded("network", "prod", prod.ID)
	o := s.options(cid)
	if o.ExecutionContext == nil || o.ExecutionContext.ProfileName != "prod" || o.ContextFingerprint == "" || o.Scope.Profile == nil || o.Scope.Profile.Name != "prod" {
		t.Fatalf("%+v", o)
	}
	// The plan must name the reviewed profile fingerprint.
	wantError(t, plan(cid), 409, "context_changed")
	if err := os.Remove(vars); err != nil {
		t.Fatal(err)
	}
	body := wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"contextFingerprint":%q}`, o.ContextFingerprint)), 409, projects.CodeScopeChanged)
	if body["detail"] != projects.DetailProfileChanged {
		t.Fatalf("%v", body)
	}
	wantError(t, s.do("GET", "/api/v1/contexts/"+cid+"/plans", ""), 404, "context_closed")

	// An absolute, shared TF_DATA_DIR: refused at open, at scope creation and, for an
	// existing scope, as a scope change.
	before := s.openLoaded("platform", "staging", "")
	withScope := s.openLoaded("platform", "default", "")
	if _, err := s.hub.Execution(withScope); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_DATA_DIR", filepath.Join(t.TempDir(), "shared"))
	wantError(t, s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"staging"}`, s.ids["platform"])), 409, "shared_data_dir")
	wantError(t, plan(before), 409, projects.CodeExecutionUnsupported)
	body = wantError(t, plan(withScope), 409, projects.CodeScopeChanged)
	if body["detail"] != projects.DetailDataDirChanged {
		t.Fatalf("%v", body)
	}
	t.Setenv("TF_DATA_DIR", "")

	// No refused request ran a plan: only the credentials failure reached the engine.
	if calls := s.planCalls(); len(calls) != 1 {
		t.Fatalf("%d plan calls", len(calls))
	}
	// Messages never name the executable or the shared data directory.
	if strings.Contains(body["reason"], s.bin) {
		t.Fatal("reason names the executable")
	}
}

// The scoped responses are JSON objects with a scope; this guards the shape the UI uses.
func TestScopedOperationShape(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	op := s.plan(cid)
	s.waitScoped(cid, op.ID)
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(s.do("GET", "/api/v1/contexts/"+cid+"/operations/"+op.ID, "").Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "kind", "status", "command", "scope"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing %s in %v", key, raw)
		}
	}
	var command []string
	if err := json.Unmarshal(raw["command"], &command); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(command, " "); strings.Contains(joined, "tfplan") || strings.Contains(joined, s.hub.TempDir) || !strings.Contains(joined, "-out=<private temporary file>") {
		t.Fatalf("command shows the saved plan path: %s", raw["command"])
	}
}

// M26.08: options and other reads of a Hub workspace never create or evict an execution
// scope, so reading another workspace cannot delete a reviewed plan.
func TestScopedReadsNeverEvictAReviewedPlan(t *testing.T) {
	s, _ := newScopedDiscovery(t)
	s.hub.MaxScopes = 1
	a := s.openLoaded("network", "dev", "")
	planned := s.waitScoped(a, s.plan(a).ID)
	if planned.PlanID == "" {
		t.Fatalf("%+v", planned)
	}
	b := s.openLoaded("platform", "default", "")
	for _, path := range []string{"plans/options", "imports/options", "drift-scans/options", "graphs/options", "discovery/options"} {
		if w := s.do("GET", "/api/v1/contexts/"+b+"/"+path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"contextId":"`+b+`"`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	wantError(t, s.do("GET", "/api/v1/contexts/"+b+"/discovery/scans/"+strings.Repeat("0", 32), ""), 404, "scan_not_found")
	if scope, _ := s.hub.ExistingScope(b); scope != nil {
		t.Fatal("a read created a scope")
	}
	detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+a+"/plans/"+planned.PlanID, ""))
	if !detail.Apply.Available || !detail.Apply.ArtifactRetained {
		t.Fatalf("reviewed plan lost: %+v", detail.Apply)
	}
	// Starting work in the other workspace is refused rather than deleting the plan.
	o := s.options(b)
	w := s.do("POST", "/api/v1/contexts/"+b+"/plans", fmt.Sprintf(`{"acknowledgeBackendAccess":true,"contextFingerprint":%q}`, o.ContextFingerprint))
	wantError(t, w, 409, projects.CodeCapacityExceeded)
	if detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+a+"/plans/"+planned.PlanID, "")); !detail.Apply.Available {
		t.Fatalf("reviewed plan lost: %+v", detail.Apply)
	}
}

// M26.08: while a Hub workspace runs an operation, edits that would close its view (project
// path or engine, removal, its profile) and closing the view are refused, so the operation is
// never cancelled by them. Afterwards DELETE closes the view and deletes its saved plan.
func TestScopedViewsCloseOnlyWhenIdle(t *testing.T) {
	s := newScopedServer(t)
	prod, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "prod", AWSProfile: "prod-sso"})
	if err != nil {
		t.Fatal(err)
	}
	cid := s.openLoaded("network", "prod", prod.ID)
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	op := s.plan(cid)
	project := "/api/v1/projects/" + s.ids["network"]
	for _, step := range [][3]string{
		{"PATCH", project, `{"engine":"tofu"}`},
		{"PATCH", project, fmt.Sprintf(`{"rootPath":%q}`, s.roots["network"])},
		{"DELETE", project, `{}`},
		{"PATCH", project + "/execution-profiles/" + prod.ID, `{"name":"prod","awsProfile":"other","expectedRevision":1}`},
		{"DELETE", project + "/execution-profiles/" + prod.ID, `{"expectedRevision":1}`},
		{"DELETE", "/api/v1/contexts/" + cid, `{}`},
	} {
		wantError(t, s.do(step[0], step[1], step[2]), 409, "project_busy")
	}
	if p, err := s.hub.Profiles.Get(s.ids["network"], prod.ID); err != nil || p.Revision != 1 {
		t.Fatalf("a refused edit changed the profile: %+v %v", p, err)
	}
	if w := s.do("PATCH", project, `{"alias":"renamed"}`); w.Code != 200 {
		t.Fatalf("alias: %d %s", w.Code, w.Body)
	}
	if running := decode[scopedOp](t, s.do("GET", "/api/v1/contexts/"+cid+"/operations/"+op.ID, "")); running.Status != operations.Running || running.CancelRequested {
		t.Fatalf("%+v", running)
	}
	if w := s.do("POST", "/api/v1/contexts/"+cid+"/operations/"+op.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.waitScoped(cid, op.ID)
	t.Setenv("HCLARO_FAKE_PLAN", "")
	planned := s.waitScoped(cid, s.plan(cid).ID)
	if planned.PlanID == "" || len(planFiles(t, s.hub.TempDir)) != 1 {
		t.Fatalf("%+v", planned)
	}
	if w := s.do("DELETE", "/api/v1/contexts/"+cid, `{}`); w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	body := wantError(t, s.do("GET", "/api/v1/contexts/"+cid+"/plans", ""), 404, "context_closed")
	if !strings.Contains(body["reason"], "was closed") {
		t.Fatalf("%v", body)
	}
	wantError(t, s.do("DELETE", "/api/v1/contexts/"+cid, `{}`), 404, "context_closed")
	for deadline := time.Now().Add(10 * time.Second); len(planFiles(t, s.hub.TempDir)) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("saved plan kept after close")
		}
	}
}

func planFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "hclaro-plan-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
