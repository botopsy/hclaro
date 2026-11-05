package api

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// workspaceServer is a Hub server whose workspace commands run engine.Fake. The test binary
// stands in for the executable so its identity can be pinned.
func workspaceServer(t *testing.T, fake *engine.Fake) (*hubServer, projects.Summary) {
	t.Helper()
	t.Setenv("TF_CLI_ARGS", "")
	t.Setenv("TF_CLI_ARGS_workspace", "")
	t.Setenv("TF_CLI_ARGS_show", "")
	s := newHubServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.hub.WorkspaceManagers = func(root, preference string, environ []string) (string, string, engine.WorkspaceManager, error) {
		return "terraform", exe, fake, nil
	}
	return s, s.add("network")
}

func (s *hubServer) review(id, action, name string) (int, projects.WorkspaceReview) {
	s.t.Helper()
	w := s.do("POST", "/api/v1/projects/"+id+"/workspaces/review", fmt.Sprintf(`{"action":%q,"name":%q}`, action, name))
	if w.Code != 200 {
		return w.Code, projects.WorkspaceReview{}
	}
	return w.Code, decode[projects.WorkspaceReview](s.t, w)
}

func newFake() *engine.Fake {
	return &engine.Fake{VersionValue: "1.9.8", Workspaces: []string{"default", "dev", "prod"}, CLIDefault: "dev", WorkspaceStates: map[string][]byte{
		"prod": fixtures.HubState("network", "prod"), "empty": []byte(`{"format_version":"1.0"}`),
	}}
}

func TestWorkspaceRoutesNeedCSRFAndAReview(t *testing.T) {
	fake := newFake()
	s, p := workspaceServer(t, fake)
	path := "/api/v1/projects/" + p.ID + "/operations/workspace"
	reviewPath := "/api/v1/projects/" + p.ID + "/workspaces/review"
	for _, route := range []string{reviewPath, path} {
		for _, tc := range []struct {
			name string
			opts []opt
			code int
		}{
			{"no session", []opt{without("Authorization")}, 401},
			{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
			{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
			{"cross-site fetch", []opt{with("Sec-Fetch-Site", "cross-site")}, 403},
			{"form post", []opt{with("Content-Type", "text/plain")}, 415},
		} {
			if w := s.do("POST", route, `{"action":"new","name":"qa"}`, tc.opts...); w.Code != tc.code {
				t.Fatalf("%s %s: %d", route, tc.name, w.Code)
			}
		}
		if w := s.do("GET", route, ""); w.Code == 200 || w.Code == 202 {
			t.Fatalf("GET %s: %d", route, w.Code)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("refused requests ran the engine: %v", fake.Calls())
	}
	for _, tc := range []struct{ body, code string }{
		{`{"action":"new","name":"-force"}`, "invalid_workspace"},
		{`{"action":"new","name":"qa -force"}`, "invalid_workspace"},
		{`{"action":"new","name":"../x"}`, "invalid_workspace"},
		{`{"action":"rename","name":"qa"}`, "invalid_action"},
		{`{"action":"delete","name":"qa","force":true}`, "invalid_request"},
		{`{"action":"new","name":"qa","args":["-lock=false"]}`, "invalid_request"},
	} {
		if w := s.do("POST", reviewPath, tc.body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("review %s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	for _, tc := range []struct{ body, code string }{
		{`{"action":"delete","name":"-force","expectedContextId":"` + strings.Repeat("0", 64) + `"}`, "invalid_workspace"},
		{`{"action":"new","name":"qa"}`, "invalid_context"},
		{`{"action":"new","name":"qa","expectedContextId":"../x"}`, "invalid_context"},
		{`{"action":"new","name":"qa","expectedContextId":"` + strings.Repeat("0", 64) + `","force":true}`, "invalid_request"},
	} {
		if w := s.do("POST", path, tc.body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("start %s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("invalid requests ran the engine: %v", fake.Calls())
	}
	if w := s.do("POST", path, `{"action":"new","name":"qa","expectedContextId":"`+strings.Repeat("0", 64)+`"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "context_changed") {
		t.Fatalf("unreviewed: %d %s", w.Code, w.Body)
	}
	if code, _ := s.review(strings.Repeat("a", 32), "new", "qa"); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
	// Refusals come back as a review that cannot run, and nothing changes.
	for _, c := range []struct{ action, name, code string }{
		{"new", "dev", "workspace_exists"}, {"select", "dev", "already_default"}, {"delete", "dev", "workspace_active"},
		{"delete", "default", "workspace_default"}, {"delete", "prod", "workspace_not_empty"}, {"select", "qa", "workspace_missing"},
	} {
		if code, r := s.review(p.ID, c.action, c.name); code != 200 || r.CanRun || r.Code != c.code || r.ContextID != "" {
			t.Fatalf("%s %s: %d %+v", c.action, c.name, code, r)
		}
	}
	for _, call := range fake.Calls() {
		if strings.HasPrefix(call, "workspace new") || strings.HasPrefix(call, "workspace select") || strings.HasPrefix(call, "workspace delete") {
			t.Fatalf("ran %s", call)
		}
	}
}

func TestWorkspaceCreateOverHTTPStreamsSanitizedEvents(t *testing.T) {
	fake := newFake()
	fake.WorkspaceLines = [][]byte{[]byte(`Created and switched to workspace "qa"!`), []byte("password=" + fixtures.Canary)}
	s, p := workspaceServer(t, fake)
	_, r := s.review(p.ID, "new", "qa")
	if !r.CanRun || r.CLIDefault != "dev" {
		t.Fatalf("%+v", r)
	}
	body := fmt.Sprintf(`{"action":"new","name":"qa","expectedContextId":%q}`, r.ContextID)
	w := s.do("POST", "/api/v1/projects/"+p.ID+"/operations/workspace", body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[operations.Operation](t, w)
	if op.Kind != "workspace" || op.ProjectID != p.ID || strings.Join(op.Command, " ") != "terraform workspace new qa" || op.Workspace == nil || op.Workspace.CLIDefaultBefore != "dev" {
		t.Fatalf("%+v", op)
	}
	done := s.waitOp(op.ID)
	if done.Status != operations.Succeeded || done.Workspace.CLIDefaultAfter != "qa" || !done.Workspace.Verified {
		t.Fatalf("%+v %+v", done, done.Workspace)
	}
	events := s.do("GET", "/api/v1/operations/"+op.ID+"/events", "").Body.String()
	if !strings.Contains(events, "Created and switched") || !strings.Contains(events, "event: end") || !strings.Contains(events, "CLI default") {
		t.Fatalf("events:\n%s", events)
	}
	// The review was consumed.
	if w := s.do("POST", "/api/v1/projects/"+p.ID+"/operations/workspace", body); w.Code != 409 {
		t.Fatalf("reused review: %d", w.Code)
	}
	n := 0
	for _, call := range fake.Calls() {
		if strings.HasPrefix(call, "workspace new") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("calls %v", fake.Calls())
	}
	// The Hub shows the new list and CLI default.
	list := decode[struct {
		Projects []projects.Summary `json:"projects"`
	}](t, s.do("GET", "/api/v1/projects", ""))
	if in := list.Projects[0].Inspection; in == nil || in.CLIDefault != "qa" || !list.Projects[0].WorkspaceActions.Change || !list.Projects[0].WorkspaceActions.Delete {
		t.Fatalf("%+v", list.Projects[0])
	}
}

func TestConcurrentWorkspaceCommandsAndCancel(t *testing.T) {
	fake := newFake()
	fake.BlockWorkspace = true
	s, p := workspaceServer(t, fake)
	_, first := s.review(p.ID, "new", "qa")
	_, second := s.review(p.ID, "select", "prod")
	path := "/api/v1/projects/" + p.ID + "/operations/workspace"
	op := decode[operations.Operation](t, s.do("POST", path, fmt.Sprintf(`{"action":"new","name":"qa","expectedContextId":%q}`, first.ContextID)))
	if w := s.do("POST", path, fmt.Sprintf(`{"action":"select","name":"prod","expectedContextId":%q}`, second.ContextID)); w.Code != 409 || !strings.Contains(w.Body.String(), "project_busy") {
		t.Fatalf("concurrent select: %d %s", w.Code, w.Body)
	}
	if _, r := s.review(p.ID, "select", "prod"); r.Code != "busy" {
		t.Fatalf("%+v", r)
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	done := s.waitOp(op.ID)
	if done.Status != operations.Cancelled || done.Diagnostic == nil {
		t.Fatalf("%+v", done)
	}
	// The second review was not consumed by the refused start and still runs once the root is free.
	fake.BlockWorkspace = false
	if w := s.do("POST", path, fmt.Sprintf(`{"action":"select","name":"prod","expectedContextId":%q}`, second.ContextID)); w.Code != 202 {
		t.Fatalf("after cancel: %d %s", w.Code, w.Body)
	}
}
