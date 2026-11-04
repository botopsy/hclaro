package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

const (
	host    = "127.0.0.1:9273"
	session = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	csrf    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	canary  = "DO_NOT_LEAK_HCLARO_TEST_SECRET"
)

type planServer struct {
	t       *testing.T
	handler http.Handler
	fake    *engine.Fake
	manager *operations.Manager
}

func newPlanServer(t *testing.T, fake *engine.Fake) *planServer {
	t.Helper()
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, t.TempDir())
	manager.TempDir = t.TempDir()
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	return &planServer{t, Handler(store, manager, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}
}

// do sends a request with the headers a same-origin browser fetch would send.
func (s *planServer) do(method, path, body string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+session)
	if method == "POST" {
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Hclaro-CSRF", csrf)
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if bytes.Contains(w.Body.Bytes(), []byte(canary)) {
		s.t.Fatalf("canary in %s %s response", method, path)
	}
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return v
}

func (s *planServer) wait(id string) operations.Operation {
	s.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		op := decode[operations.Operation](s.t, s.do("GET", "/api/v1/operations/"+id, ""))
		if op.Status != operations.Running {
			return op
		}
	}
	s.t.Fatal("operation did not finish")
	return operations.Operation{}
}

type sseEvent struct{ id, event, data string }

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.event != "" {
				out = append(out, current)
			}
			current = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			current.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			current.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			current.data = line[6:]
		}
	}
	return out
}

func TestPlanFlowOverHTTP(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{Lines: bytes.Split(bytes.TrimSpace(fixtures.PlanLog), []byte("\n")), PlanJSON: fixtures.PlanAllActions})
	if got := decode[map[string]string](t, s.do("GET", "/api/v1/session", "")); got["csrfToken"] != csrf {
		t.Fatal("csrf token not delivered to the authorized client")
	}
	options := decode[map[string]any](t, s.do("GET", "/api/v1/plans/options", ""))
	if options["canPlan"] != true || options["workspace"] != "demo" {
		t.Fatalf("%v", options)
	}
	for body, want := range map[string]string{
		`{}`:                                 "acknowledgement_required",
		`{"acknowledgeBackendAccess":false}`: "acknowledgement_required",
		`{"acknowledgeBackendAccess":true,"kind":"destroy"}`:    "invalid_request",
		`{"acknowledgeBackendAccess":true,"varFiles":["../x"]}`: "invalid_var_files",
		`{"acknowledgeBackendAccess":true} {}`:                  "invalid_request",
		`[]`:                                                    "invalid_request",
	} {
		w := s.do("POST", "/api/v1/plans", body)
		if w.Code != 400 || decode[map[string]string](t, w)["error"] != want {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if strings.Contains(strings.Join(s.fake.Calls(), ","), "plan") {
		t.Fatal("rejected requests reached the engine")
	}
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	op := s.wait(decode[operations.Operation](t, w).ID)
	if op.Status != operations.Succeeded {
		t.Fatalf("%+v", op)
	}
	events := parseSSE(s.do("GET", "/api/v1/operations/"+op.ID+"/events", "").Body.String())
	if len(events) < 5 || events[len(events)-1].event != "end" || events[len(events)-2].event != "status" || events[0].id != "1" {
		t.Fatalf("events: %+v", events)
	}
	resumed := parseSSE(s.do("GET", "/api/v1/operations/"+op.ID+"/events?after=3", "").Body.String())
	if resumed[0].id != "4" {
		t.Fatalf("bounded replay: %+v", resumed[0])
	}
	if s.do("GET", "/api/v1/operations/"+op.ID+"/events?after=-1", "").Code != 400 {
		t.Fatal("bad cursor accepted")
	}
	list := decode[struct {
		Plans []planSummary `json:"plans"`
	}](t, s.do("GET", "/api/v1/plans", ""))
	if len(list.Plans) != 1 || list.Plans[0].Changes != 11 || list.Plans[0].Drift != 1 {
		t.Fatalf("%+v", list)
	}
	detail := decode[struct {
		operations.Plan
		ContextChanged bool `json:"contextChanged"`
	}](t, s.do("GET", "/api/v1/plans/"+op.PlanID, ""))
	if detail.Apply.Available || detail.ArtifactRetained || detail.ContextChanged || len(detail.Changes) != 13 || detail.ArtifactSHA256 == "" {
		t.Fatalf("plan detail: %+v", detail.Plan)
	}
	// Every change and drift record is individually retrievable and redacted.
	for _, c := range append(detail.Changes, detail.Drift...) {
		if s.do("GET", "/api/v1/plans/"+op.PlanID+"/changes/"+c.ID, "").Code != 200 {
			t.Fatal(c.Address)
		}
	}
	for _, path := range []string{"/changes/" + strings.Repeat("0", 32), "/changes/x"} {
		if s.do("GET", "/api/v1/plans/"+op.PlanID+path, "").Code != 404 {
			t.Fatal(path)
		}
	}
	if s.do("POST", "/api/v1/plans/"+op.PlanID+"/apply", `{}`).Code != 405 {
		t.Fatal("apply must not exist")
	}
	if calls := strings.Count(strings.Join(s.fake.Calls(), ","), "plan"); calls != 2 {
		t.Fatalf("GET requests must never run the engine: %v", s.fake.Calls())
	}
}

func TestCancelOverHTTP(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{Block: true})
	op := decode[operations.Operation](t, s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`))
	if w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`); w.Code != 409 {
		t.Fatalf("concurrent plan: %d", w.Code)
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{"force":true}`); w.Code != 400 {
		t.Fatal("unknown cancel fields accepted")
	}
	if w := s.do("POST", "/api/v1/operations/"+strings.Repeat("0", 32)+"/cancel", `{}`); w.Code != 404 {
		t.Fatal("unknown operation")
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	done := s.wait(op.ID)
	if done.Status != operations.Cancelled || done.PlanID != "" {
		t.Fatalf("%+v", done)
	}
	if w := s.do("POST", "/api/v1/operations/"+op.ID+"/cancel", `{}`); w.Code != 409 {
		t.Fatal("finished operation cancelled")
	}
	plans := decode[map[string][]planSummary](t, s.do("GET", "/api/v1/plans", ""))
	if len(plans["plans"]) != 0 {
		t.Fatal("cancelled plan listed")
	}
}

func TestPlanningUnavailable(t *testing.T) {
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "cloud", ExecutionMode: "remote-exec"})
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State}
	store.Load(context.Background(), fake, "terraform")
	s := &planServer{t, Handler(store, operations.NewManager(store, fake, t.TempDir()), fstest.MapFS{}, host, session, csrf, "test"), fake, nil}
	options := decode[map[string]any](t, s.do("GET", "/api/v1/plans/options", ""))
	if options["canPlan"] != false || !strings.Contains(options["reason"].(string), "Remote execution") {
		t.Fatalf("%v", options)
	}
	if w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`); w.Code != 409 {
		t.Fatalf("%d", w.Code)
	}
}
