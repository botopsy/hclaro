package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
	"hclaro/internal/workspace"
)

// hubEngine serves the synthetic per-workspace states without a subprocess.
type hubEngine struct {
	project, selected string
	calls             *atomic.Int64
}

func (e hubEngine) Version(context.Context) (string, error) { e.calls.Add(1); return "1.9.8", nil }
func (e hubEngine) Workspace(context.Context) (string, error) {
	e.calls.Add(1)
	return e.selected, nil
}
func (e hubEngine) State(context.Context) ([]byte, error) {
	e.calls.Add(1)
	return fixtures.HubState(e.project, e.selected), nil
}
func (e hubEngine) ListWorkspaces(context.Context) (engine.WorkspaceList, error) {
	e.calls.Add(1)
	return engine.WorkspaceList{Names: []string{"default", "dev", "prod"}, Current: "dev"}, nil
}

type hubServer struct {
	t       *testing.T
	handler http.Handler
	hub     *projects.Hub
	calls   *atomic.Int64
	dir     string
}

func newHubServer(t *testing.T) *hubServer {
	t.Helper()
	t.Setenv("TF_DATA_DIR", "")
	dir := t.TempDir()
	if err := fixtures.WriteHubProjects(dir); err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.EvalSymlinks(dir)
	hub := projects.NewHub(projects.Open(t.TempDir(), time.Now()), "")
	calls := &atomic.Int64{}
	hub.Engines = func(root, preference, selected string, _ []string) (string, string, projects.Adapter, error) {
		return "terraform", "/synthetic/terraform", hubEngine{filepath.Base(root), selected, calls}, nil
	}
	t.Cleanup(hub.Close)
	store := workspace.NoProject()
	manager := operations.NewManager(store, nil, "")
	t.Cleanup(func() { manager.Close(time.Second) })
	h := Handler(store, manager, hub, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	return &hubServer{t, h, hub, calls, dir}
}

type opt func(*http.Request)

func without(header string) opt { return func(r *http.Request) { r.Header.Del(header) } }
func with(header, value string) opt {
	return func(r *http.Request) { r.Header.Set(header, value) }
}

func (s *hubServer) do(method, path, body string, opts ...opt) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+session)
	if method != "GET" {
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Hclaro-CSRF", csrf)
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if bytes.Contains(w.Body.Bytes(), []byte(fixtures.Canary)) {
		s.t.Fatalf("canary in %s %s response", method, path)
	}
	return w
}

func (s *hubServer) add(name string) projects.Summary {
	s.t.Helper()
	w := s.do("POST", "/api/v1/projects", fmt.Sprintf(`{"path":%q,"alias":"Infra"}`, filepath.Join(s.dir, name)))
	if w.Code != 201 {
		s.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return decode[projects.Summary](s.t, w)
}

func TestHubMutationsNeedSessionOriginAndCSRF(t *testing.T) {
	s := newHubServer(t)
	body := fmt.Sprintf(`{"path":%q}`, filepath.Join(s.dir, "network"))
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
		{"wrong host", []opt{func(r *http.Request) { r.Host = "evil.example:9273" }}, 403},
	} {
		if w := s.do("POST", "/api/v1/projects", body, tc.opts...); w.Code != tc.code {
			t.Fatalf("%s: %d", tc.name, w.Code)
		}
	}
	if list, _ := s.hub.List(); len(list) != 0 {
		t.Fatal("a rejected request registered a project")
	}
	p := s.add("network")
	for _, method := range []string{"PATCH", "DELETE"} {
		if w := s.do(method, "/api/v1/projects/"+p.ID, `{}`, without("X-Hclaro-CSRF")); w.Code != 403 {
			t.Fatalf("%s without csrf: %d", method, w.Code)
		}
	}
	if w := s.do("PUT", "/api/v1/projects/"+p.ID, `{}`); w.Code != 405 {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := s.do("DELETE", "/api/v1/projects/"+p.ID, ``); w.Code != 413 {
		t.Fatalf("DELETE without body: %d", w.Code)
	}
	if w := s.do("PATCH", "/api/v1/projects/"+p.ID, `{"alias":"x","rootPath":"/","extra":1}`); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if s.calls.Load() != 0 {
		t.Fatal("registry changes ran engine commands")
	}
}

func TestHubRegistryRoutes(t *testing.T) {
	s := newHubServer(t)
	a := s.add("network")
	b := s.add("platform")
	if a.Alias != b.Alias || a.ID == b.ID {
		t.Fatal("same alias should keep distinct projects")
	}
	w := s.do("POST", "/api/v1/projects", fmt.Sprintf(`{"path":%q}`, filepath.Join(s.dir, "network", ".")))
	if w.Code != 409 || !strings.Contains(w.Body.String(), a.ID) {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"relative", s.dir, "/definitely/missing"} {
		if w := s.do("POST", "/api/v1/projects", fmt.Sprintf(`{"path":%q}`, path)); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_path") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := s.do("PATCH", "/api/v1/projects/"+a.ID, `{"alias":"Network","pinned":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"pinned":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/projects/order", fmt.Sprintf(`{"ids":[%q,%q]}`, b.ID, a.ID)); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	list := decode[struct {
		Projects []projects.Summary
		Registry map[string]string
	}](t, s.do("GET", "/api/v1/projects", ""))
	if len(list.Projects) != 2 || list.Projects[0].ID != b.ID || list.Registry["status"] != "ready" {
		t.Fatalf("%+v", list)
	}
	if w := s.do("DELETE", "/api/v1/projects/"+a.ID, `{}`); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "network", "main.tf")); err != nil {
		t.Fatal("unregister deleted project files")
	}
	if w := s.do("DELETE", "/api/v1/projects/"+a.ID, `{}`); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
	if w := s.do("POST", "/api/v1/projects/browse", fmt.Sprintf(`{"path":%q}`, s.dir)); w.Code != 200 || !strings.Contains(w.Body.String(), `"network"`) || strings.Contains(w.Body.String(), ".fake") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if s.calls.Load() != 0 {
		t.Fatal("GET or registry routes ran engine commands")
	}
}

func (s *hubServer) open(project, ws string) string {
	s.t.Helper()
	w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":%q}`, project, ws))
	if w.Code != 200 {
		s.t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[struct{ ID string }](s.t, w).ID
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		snap := decode[struct{ Status string }](s.t, s.do("GET", "/api/v1/contexts/"+id, ""))
		if snap.Status != "loading" {
			return id
		}
	}
	s.t.Fatal("context did not load")
	return ""
}

func TestContextRoutesScopeResourcesToProjectAndWorkspace(t *testing.T) {
	s := newHubServer(t)
	network := s.add("network")
	platform := s.add("platform")
	if s.calls.Load() != 0 {
		t.Fatal("adding projects ran engine commands")
	}
	inspected := decode[projects.Summary](t, s.do("POST", "/api/v1/projects/"+network.ID+"/inspect", `{}`))
	if inspected.Inspection == nil || len(inspected.Inspection.Workspaces) != 3 {
		t.Fatalf("%+v", inspected.Inspection)
	}
	dev, prod, other := s.open(network.ID, "dev"), s.open(network.ID, "prod"), s.open(platform.ID, "dev")
	type page struct {
		Resources []struct{ ID, Address string }
		Total     int
	}
	devPage := decode[page](t, s.do("GET", "/api/v1/contexts/"+dev+"/resources", ""))
	prodPage := decode[page](t, s.do("GET", "/api/v1/contexts/"+prod+"/resources", ""))
	if devPage.Total != 2 || prodPage.Total != 3 {
		t.Fatalf("%+v %+v", devPage, prodPage)
	}
	for _, r := range devPage.Resources {
		if !strings.Contains(r.Address, "dev_") {
			t.Fatalf("dev context returned %s", r.Address)
		}
		// A resource from one context is never served through another.
		if w := s.do("GET", "/api/v1/contexts/"+prod+"/resources/"+r.ID, ""); w.Code != 404 {
			t.Fatalf("cross-context resource: %d", w.Code)
		}
		detail := s.do("GET", "/api/v1/contexts/"+dev+"/resources/"+r.ID, "")
		if detail.Code != 200 || !strings.Contains(detail.Body.String(), r.Address) {
			t.Fatalf("%d %s", detail.Code, detail.Body)
		}
	}
	snap := s.do("GET", "/api/v1/contexts/"+other, "")
	if !strings.Contains(snap.Body.String(), `"projectId":"`+platform.ID+`"`) || strings.Contains(snap.Body.String(), "network") {
		t.Fatalf("%s", snap.Body)
	}
	if strings.Contains(snap.Body.String(), "/synthetic/terraform") {
		t.Fatal("engine executable path returned")
	}
	if !strings.Contains(snap.Body.String(), `"canPlan":false`) {
		t.Fatal("hub context offered planning")
	}
	// The launch routes are untouched by Hub contexts.
	if w := s.do("GET", "/api/v1/workspace", ""); !strings.Contains(w.Body.String(), "no_launch_project") {
		t.Fatalf("%s", w.Body)
	}
	if w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`); w.Code != 409 {
		t.Fatalf("plan without launch project: %d", w.Code)
	}
	if w := s.do("DELETE", "/api/v1/projects/"+network.ID, `{}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, id := range []string{dev, prod} {
		w := s.do("GET", "/api/v1/contexts/"+id+"/resources", "")
		if w.Code != 404 || !strings.Contains(w.Body.String(), "removed from the Hub") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{
		fmt.Sprintf(`{"projectId":%q,"workspace":"dev"}`, network.ID),
		fmt.Sprintf(`{"projectId":%q,"workspace":"../dev"}`, platform.ID),
		`{"projectId":"../../etc","workspace":"dev"}`,
		fmt.Sprintf(`{"projectId":%q,"workspace":"dev","path":"/"}`, platform.ID),
	} {
		if w := s.do("POST", "/api/v1/contexts", body); w.Code < 400 {
			t.Fatalf("%s accepted", body)
		}
	}
}

func TestHubGetRoutesNeverRunCommands(t *testing.T) {
	s := newHubServer(t)
	p := s.add("network")
	for _, path := range []string{"/api/v1/projects", "/api/v1/contexts/" + strings.Repeat("0", 32), "/api/v1/contexts/x/resources"} {
		s.do("GET", path, "")
	}
	if s.calls.Load() != 0 {
		t.Fatal("GET ran an engine command")
	}
	_ = p
}

func TestHubClientRoutesServeTheAppShellOnly(t *testing.T) {
	s := newHubServer(t)
	id := strings.Repeat("a", 32)
	for path, code := range map[string]int{
		"/projects":       200,
		"/projects/":      200,
		"/projects/" + id: 200,
		"/projects/" + id + "/workspaces/prod/resources":  200,
		"/projects/" + id + "/workspaces/prod/tools":      200,
		"/projects/" + id + "/workspaces/prod/tools/x":    404,
		"/projects/" + id + "/workspaces/prod":            200,
		"/projects/" + id + "/workspaces/a%2Fb/resources": 404,
		"/projects/" + id + "/workspaces/prod/state":      404,
		"/projects/notanid":                               404,
		"/projects/../index.html":                         200,
		"/api/v1/projects/unknown/route":                  404,
	} {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if path == "/projects/../index.html" {
			// The mux cleans the path and redirects; it never reaches a file outside assets.
			if w.Code/100 != 3 {
				t.Fatalf("%s: %d", path, w.Code)
			}
			continue
		}
		if strings.HasPrefix(path, "/api/") {
			r.Header.Set("Authorization", "Bearer "+session)
			w = httptest.NewRecorder()
			s.handler.ServeHTTP(w, r)
		}
		if w.Code != code {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	plain := Handler(workspace.NoProject(), operations.NewManager(workspace.NoProject(), nil, ""), nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test")
	w := httptest.NewRecorder()
	plain.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+"/projects", nil))
	if w.Code != 404 {
		t.Fatalf("hub routes without a hub: %d", w.Code)
	}
}

// M24.04: scope refusals keep stable codes, and the session reports whether scoped
// execution is enabled. No scoped execution route exists yet (M24.05).
func TestScopeRefusalsAndSessionCapability(t *testing.T) {
	s := newHubServer(t)
	for _, tc := range []struct {
		err    *projects.ScopeError
		status int
	}{
		{&projects.ScopeError{Code: projects.CodeScopedDisabled, Message: "off"}, 404},
		{&projects.ScopeError{Code: projects.CodeCapacityExceeded, Message: "full"}, 409},
		{&projects.ScopeError{Code: projects.CodeExecutionUnsupported, Message: "remote"}, 409},
		{&projects.ScopeError{Code: projects.CodeScopeChanged, Detail: projects.DetailEngineChanged, Message: "changed"}, 409},
	} {
		w := httptest.NewRecorder()
		projectError(w, s.hub, tc.err)
		body := decode[map[string]string](t, w)
		if w.Code != tc.status || body["error"] != tc.err.Code || body["reason"] != tc.err.Message || body["detail"] != tc.err.Detail {
			t.Fatalf("%s: %d %v", tc.err.Code, w.Code, body)
		}
	}
	if caps := decode[map[string]any](t, s.do("GET", "/api/v1/session", ""))["capabilities"]; caps.(map[string]any)["scopedExecution"] != false {
		t.Fatalf("%v", caps)
	}
	s.hub.ScopedExecution = true
	if caps := decode[map[string]any](t, s.do("GET", "/api/v1/session", ""))["capabilities"]; caps.(map[string]any)["scopedExecution"] != true {
		t.Fatalf("%v", caps)
	}
}
