package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// toolServer is a Hub server whose project tools run engine.Fake. The test binary stands in
// for the executable so its identity can be pinned.
func toolServer(t *testing.T, fake *engine.Fake) (*hubServer, projects.Summary) {
	t.Helper()
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_validate", "TF_CLI_ARGS_fmt", "TF_CLI_ARGS_providers", "TF_CLI_ARGS_output", "TF_CLI_ARGS_version"} {
		t.Setenv(key, "")
	}
	s := newHubServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	calls := s.calls
	s.hub.Engines = func(root, preference, selected string, _ []string) (string, string, projects.Adapter, error) {
		return "terraform", exe, hubEngine{"network", selected, calls}, nil
	}
	selected := &atomic.Value{}
	s.hub.Tools = func(root, preference string, environ []string, ws string) (string, string, engine.Tools, error) {
		selected.Store(ws)
		return "terraform", exe, fake, nil
	}
	return s, s.add("network")
}

func (s *hubServer) noSecrets(bodies ...string) {
	s.t.Helper()
	for _, b := range bodies {
		for _, secret := range []string{fixtures.Canary, "plain-unflagged-secret-7f3a", "example.invalid"} {
			if strings.Contains(b, secret) {
				s.t.Fatalf("%q leaked:\n%s", secret, b)
			}
		}
	}
}

func TestToolRoutesNeedSessionCSRFAndAnOpenContext(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8"}
	s, p := toolServer(t, fake)
	cid := s.open(p.ID, "dev")
	base := "/api/v1/projects/" + p.ID + "/operations/"
	for _, tool := range []string{"validate", "fmt-check", "providers", "outputs-metadata"} {
		route := base + tool
		body := fmt.Sprintf(`{"contextId":%q}`, cid)
		for _, tc := range []struct {
			name string
			opts []opt
			code int
		}{
			{"no session", []opt{without("Authorization")}, 401},
			{"no csrf", []opt{without("X-Hclaro-CSRF")}, 403},
			{"wrong csrf", []opt{with("X-Hclaro-CSRF", strings.Repeat("c", 64))}, 403},
			{"cross-site origin", []opt{with("Origin", "https://evil.example")}, 403},
			{"cross-site fetch", []opt{with("Sec-Fetch-Site", "cross-site")}, 403},
			{"wrong host", []opt{func(r *http.Request) { r.Host = "evil.example" }}, 403},
			{"form post", []opt{with("Content-Type", "text/plain")}, 415},
		} {
			if w := s.do("POST", route, body, tc.opts...); w.Code != tc.code {
				t.Fatalf("%s %s: %d", tool, tc.name, w.Code)
			}
		}
		if w := s.do("GET", route, ""); w.Code == 200 || w.Code == 202 {
			t.Fatalf("GET %s: %d", tool, w.Code)
		}
		for _, tc := range []struct{ body, code string }{
			{`{}`, "invalid_context"},
			{`{"contextId":"../x"}`, "invalid_context"},
			{fmt.Sprintf(`{"contextId":%q,"args":["-write=true"]}`, cid), "invalid_request"},
			{fmt.Sprintf(`{"contextId":%q,"chdir":"/"}`, cid), "invalid_request"},
			{fmt.Sprintf(`{"contextId":%q,"executable":"/bin/sh"}`, cid), "invalid_request"},
		} {
			if w := s.do("POST", route, tc.body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("%s %s: %d %s", tool, tc.body, w.Code, w.Body)
			}
		}
		if tool != "fmt-check" {
			if w := s.do("POST", route, fmt.Sprintf(`{"contextId":%q,"recursive":true}`, cid)); w.Code != 400 {
				t.Fatalf("%s accepted recursive: %d", tool, w.Code)
			}
		}
		if w := s.do("POST", route, `{"contextId":"`+strings.Repeat("0", 32)+`"}`); w.Code != 404 || !strings.Contains(w.Body.String(), "context_closed") {
			t.Fatalf("%s unknown context: %d %s", tool, w.Code, w.Body)
		}
	}
	for _, tool := range []string{"fmt", "init", "apply", "output", "fmt-write"} {
		if w := s.do("POST", base+tool, fmt.Sprintf(`{"contextId":%q}`, cid)); w.Code == 202 || w.Code == 200 {
			t.Fatalf("%s accepted: %d", tool, w.Code)
		}
	}
	if w := s.do("POST", "/api/v1/projects/"+strings.Repeat("a", 32)+"/operations/validate", fmt.Sprintf(`{"contextId":%q}`, cid)); w.Code != 404 {
		t.Fatalf("unknown project: %d", w.Code)
	}
	other := s.add("platform")
	if w := s.do("POST", "/api/v1/projects/"+other.ID+"/operations/validate", fmt.Sprintf(`{"contextId":%q}`, cid)); w.Code != 404 {
		t.Fatalf("context of another project: %d %s", w.Code, w.Body)
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("refused requests ran the engine: %v", fake.Calls())
	}
	before := s.calls.Load()
	if w := s.do("GET", "/api/v1/projects/"+p.ID+"/operations", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"operations":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if s.calls.Load() != before || len(fake.Calls()) != 0 {
		t.Fatal("GET ran commands")
	}
}

func TestToolsOverHTTPReturnSanitizedResults(t *testing.T) {
	fake := &engine.Fake{
		VersionValue: "1.9.8",
		ValidateOut:  engine.Capture{Stdout: fixtures.ValidateInvalid, ExitCode: 1},
		FormatOut:    engine.Capture{Stdout: []byte("main.tf\n"), ExitCode: 3},
		ProvidersOut: engine.Capture{Stdout: fixtures.Providers},
		OutputsOut:   engine.Capture{Stdout: fixtures.Outputs, Stderr: []byte("password=" + fixtures.Canary)},
	}
	s, p := toolServer(t, fake)
	cid := s.open(p.ID, "dev")
	base := "/api/v1/projects/" + p.ID + "/operations/"
	run := func(tool, body string) operations.Operation {
		t.Helper()
		w := s.do("POST", base+tool, body)
		if w.Code != 202 {
			t.Fatalf("%s: %d %s", tool, w.Code, w.Body)
		}
		op := decode[operations.Operation](t, w)
		done := s.waitOp(op.ID)
		events := s.do("GET", "/api/v1/operations/"+op.ID+"/events", "").Body.String()
		if !strings.Contains(events, "event: end") {
			t.Fatalf("events:\n%s", events)
		}
		s.noSecrets(w.Body.String(), events, s.do("GET", "/api/v1/operations/"+op.ID, "").Body.String())
		return done
	}
	ctx := fmt.Sprintf(`{"contextId":%q}`, cid)
	if op := run("validate", ctx); op.Kind != "tool" || op.Diagnostic.Code != "validation_errors" || op.Tool.Errors != 3 || op.Tool.Findings[0].File != "main.tf" {
		t.Fatalf("%+v %+v", op, op.Tool)
	}
	if op := run("fmt-check", fmt.Sprintf(`{"contextId":%q,"recursive":false}`, cid)); op.Diagnostic.Code != "unformatted" || op.Tool.Format.Unformatted[0] != "main.tf" || strings.Join(op.Command, " ") != "terraform fmt -check -no-color" {
		t.Fatalf("%+v %+v", op, op.Tool.Format)
	}
	if op := run("providers", ctx); op.Status != operations.Succeeded || op.Tool.Workspace != "dev" || !op.Tool.UsesWorkspace {
		t.Fatalf("%+v", op)
	}
	op := run("outputs-metadata", ctx)
	if op.Status != operations.Succeeded || len(op.Tool.Outputs.Items) != 4 || strings.Join(op.Command, " ") != "terraform output -json" {
		t.Fatalf("%+v", op.Tool.Outputs)
	}
	list := s.do("GET", "/api/v1/projects/"+p.ID+"/operations", "")
	s.noSecrets(list.Body.String())
	if !strings.Contains(list.Body.String(), `"db_password"`) || strings.Count(list.Body.String(), `"kind":"tool"`) != 4 {
		t.Fatalf("%s", list.Body)
	}
}

func TestToolBusyDuplicateAndCancelOverHTTP(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8", BlockTool: true}
	s, p := toolServer(t, fake)
	cid := s.open(p.ID, "dev")
	route := "/api/v1/projects/" + p.ID + "/operations/providers"
	body := fmt.Sprintf(`{"contextId":%q}`, cid)
	release, ok := s.hub.Locks.Acquire(p.RootPath, "init")
	if !ok {
		t.Fatal("claim")
	}
	if w := s.do("POST", route, body); w.Code != 409 || !strings.Contains(w.Body.String(), "project_busy") {
		t.Fatalf("during init: %d %s", w.Code, w.Body)
	}
	release()
	w := s.do("POST", route, body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[operations.Operation](t, w)
	if w := s.do("POST", route, body); w.Code != 409 || !strings.Contains(w.Body.String(), "tool_running") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	// Other tools in the same context run alongside it.
	fake2 := s.do("POST", "/api/v1/projects/"+p.ID+"/operations/validate", body)
	if fake2.Code != 202 {
		t.Fatalf("parallel tool: %d %s", fake2.Code, fake2.Body)
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if done := s.waitOp(op.ID); done.Status != operations.Cancelled || done.Diagnostic.Code != "cancelled" {
		t.Fatalf("%+v", done)
	}
	second := decode[operations.Operation](t, fake2)
	s.do("POST", "/api/v1/operations/"+second.ID+"/cancel", `{}`)
	s.waitOp(second.ID)
	if _, ok := s.hub.Locks.Acquire(p.RootPath, "init"); !ok {
		t.Fatal("claims not released")
	}
}
