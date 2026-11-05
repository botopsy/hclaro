package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/projects"
)

// initServer is a Hub server whose init runs engine.Fake. The test binary stands in for the
// executable so its identity can be pinned.
func initServer(t *testing.T, fake *engine.Fake) (*hubServer, projects.Summary) {
	t.Helper()
	t.Setenv("TF_CLI_ARGS", "")
	t.Setenv("TF_CLI_ARGS_init", "")
	s := newHubServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.hub.Initers = func(root, preference string, environ []string) (string, string, engine.Initer, error) {
		return "terraform", exe, fake, nil
	}
	p := s.add("legacy")
	fake.InitDataDir = filepath.Join(p.RootPath, ".terraform")
	return s, p
}

func (s *hubServer) initStatus(id string) projects.InitStatus {
	s.t.Helper()
	w := s.do("GET", "/api/v1/projects/"+id+"/init-status", "")
	if w.Code != 200 {
		s.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return decode[projects.InitStatus](s.t, w)
}

func (s *hubServer) waitOp(id string) operations.Operation {
	s.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		op := decode[operations.Operation](s.t, s.do("GET", "/api/v1/operations/"+id, ""))
		switch op.Status {
		case operations.Queued, operations.Running, operations.Cancelling:
			continue
		}
		return op
	}
	s.t.Fatal("operation did not finish")
	return operations.Operation{}
}

func TestInitStartNeedsCSRFAndAReviewedContext(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8"}
	s, p := initServer(t, fake)
	status := s.initStatus(p.ID)
	if !status.CanInit || status.Initialized || len(fake.Calls()) != 0 {
		t.Fatalf("%+v calls %v", status, fake.Calls())
	}
	path := "/api/v1/projects/" + p.ID + "/operations/init"
	body := fmt.Sprintf(`{"mode":"standard","expectedContextId":%q}`, status.ContextID)
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
		if w := s.do("POST", path, body, tc.opts...); w.Code != tc.code {
			t.Fatalf("%s: %d", tc.name, w.Code)
		}
	}
	for _, tc := range []struct{ body, code string }{
		{`{"mode":"upgrade","expectedContextId":"` + status.ContextID + `"}`, "invalid_mode"},
		{`{"mode":"standard","expectedContextId":"` + status.ContextID + `","args":["-upgrade"]}`, "invalid_request"},
		{`{"mode":"standard","expectedContextId":"` + status.ContextID + `","backendConfig":"secret=1"}`, "invalid_request"},
		{`{"mode":"standard"}`, "invalid_context"},
		{`{"mode":"standard","expectedContextId":"../x"}`, "invalid_context"},
	} {
		if w := s.do("POST", path, tc.body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	if w := s.do("POST", path, `{"mode":"standard","expectedContextId":"`+strings.Repeat("0", 64)+`"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "context_changed") {
		t.Fatalf("stale context: %d %s", w.Code, w.Body)
	}
	if w := s.do("GET", path, ""); w.Code == 202 {
		t.Fatal("GET started init")
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("refused requests ran the engine: %v", fake.Calls())
	}
	if w := s.do("POST", "/api/v1/projects/"+strings.Repeat("a", 32)+"/operations/init", body); w.Code != 404 {
		t.Fatalf("unknown project: %d", w.Code)
	}
}

func TestInitOverHTTPStreamsSanitizedEvents(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8", InitLines: [][]byte{
		[]byte(`{"type":"init_output","@level":"info","@message":"Initializing the backend..."}`),
		[]byte(`{"type":"init_output","@level":"info","@message":"token=` + fixtures.Canary + `"}`),
	}}
	s, p := initServer(t, fake)
	status := s.initStatus(p.ID)
	w := s.do("POST", "/api/v1/projects/"+p.ID+"/operations/init", fmt.Sprintf(`{"mode":"standard","expectedContextId":%q}`, status.ContextID))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[operations.Operation](t, w)
	if op.Kind != "init" || op.ProjectID != p.ID {
		t.Fatalf("%+v", op)
	}
	if done := s.waitOp(op.ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	events := s.do("GET", "/api/v1/operations/"+op.ID+"/events", "")
	text := events.Body.String()
	if !strings.Contains(text, "Initializing the backend...") || !strings.Contains(text, "event: end") || !strings.Contains(text, `"status":"succeeded"`) {
		t.Fatalf("events:\n%s", text)
	}
	if after := s.initStatus(p.ID); !after.Initialized || after.Operation == nil || after.Operation.ID != op.ID {
		t.Fatalf("%+v", after)
	}
	if len(fake.InitCalls()) != 1 {
		t.Fatalf("init ran %d times", len(fake.InitCalls()))
	}
}

func TestInitSurvivesClosedBrowserAndCancelsOverHTTP(t *testing.T) {
	fake := &engine.Fake{VersionValue: "1.9.8", BlockInit: true}
	s, p := initServer(t, fake)
	status := s.initStatus(p.ID)
	path := "/api/v1/projects/" + p.ID + "/operations/init"
	body := fmt.Sprintf(`{"mode":"standard","expectedContextId":%q}`, status.ContextID)
	op := decode[operations.Operation](t, s.do("POST", path, body))
	if w := s.do("POST", path, body); w.Code != 409 || !strings.Contains(w.Body.String(), "project_busy") {
		t.Fatalf("concurrent init: %d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"default"}`, p.ID)); w.Code != 409 || !strings.Contains(w.Body.String(), "project_busy") {
		t.Fatalf("read during init: %d %s", w.Code, w.Body)
	}
	// A browser that disconnects from the event stream does not change the operation.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "GET", "http://"+host+"/api/v1/operations/"+op.ID+"/events", nil)
	r.Header.Set("Authorization", "Bearer "+session)
	s.handler.ServeHTTP(httptest.NewRecorder(), r)
	if now := decode[operations.Operation](t, s.do("GET", "/api/v1/operations/"+op.ID, "")); now.Status != operations.Running {
		t.Fatalf("after disconnect: %+v", now)
	}
	w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`)
	if w.Code != 202 || decode[operations.Operation](t, w).Status != operations.Cancelling {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if done := s.waitOp(op.ID); done.Status != operations.Cancelled {
		t.Fatalf("%+v", done)
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`); w.Code != 409 {
		t.Fatalf("second cancel: %d", w.Code)
	}
	if len(fake.InitCalls()) != 1 || strings.Contains(strings.Join(fake.Calls(), " "), "apply") {
		t.Fatalf("calls %v", fake.Calls())
	}
	if w := s.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"default"}`, p.ID)); w.Code == http.StatusConflict {
		t.Fatal("root still claimed after cancel")
	}
}
