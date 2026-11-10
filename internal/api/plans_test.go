package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
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
	return &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}
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

// approve runs the preflight over HTTP and requests an approval, acknowledging each
// warning by ID as the UI does.
func (s *planServer) approve(planID string) operations.Approval {
	s.t.Helper()
	w := s.do("POST", "/api/v1/plans/"+planID+"/preflight", `{}`)
	report := decode[operations.PreflightReport](s.t, w)
	if w.Code != 200 || report.Binding == "" {
		s.t.Fatalf("preflight %d %+v", w.Code, report.Checks)
	}
	body, _ := json.Marshal(map[string]any{"preflightBinding": report.Binding, "acknowledged": report.Acknowledge})
	w = s.do("POST", "/api/v1/plans/"+planID+"/approval", string(body))
	if w.Code != 200 {
		s.t.Fatalf("approval %d %s", w.Code, w.Body.String())
	}
	return decode[operations.Approval](s.t, w)
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
	session := decode[map[string]any](t, s.do("GET", "/api/v1/session", ""))
	if session["csrfToken"] != csrf {
		t.Fatal("csrf token not delivered to the authorized client")
	}
	// Scoped execution for Hub workspaces is off unless enabled when Hclaro starts (M24.04).
	if caps, ok := session["capabilities"].(map[string]any); !ok || caps["scopedExecution"] != false {
		t.Fatalf("session capabilities: %v", session["capabilities"])
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
	if len(list.Plans) != 1 || list.Plans[0].Changes != 10 || list.Plans[0].Drift != 1 {
		t.Fatalf("%+v", list)
	}
	type planDetail struct {
		operations.Plan
		ContextChanged bool                   `json:"contextChanged"`
		Apply          operations.ApplyStatus `json:"apply"`
	}
	detail := decode[planDetail](t, s.do("GET", "/api/v1/plans/"+op.PlanID, ""))
	if !detail.Apply.Available || !detail.Apply.ArtifactRetained || detail.ContextChanged || len(detail.Changes) != 12 || detail.ArtifactSHA256 == "" {
		t.Fatalf("plan detail: %+v %+v", detail.Apply, detail.Plan)
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
	if calls := strings.Count(strings.Join(s.fake.Calls(), ","), "plan"); calls != 2 {
		t.Fatalf("GET requests must never run the engine: %v", s.fake.Calls())
	}
	applyPath := "/api/v1/plans/" + op.PlanID + "/apply"
	refused := func(w *httptest.ResponseRecorder, code int, name string) {
		t.Helper()
		if w.Code != code || !strings.Contains(w.Body.String(), name) {
			t.Fatalf("want %d %s, got %d %s", code, name, w.Code, w.Body.String())
		}
	}
	// GET never applies; apply without an approval, with a forged token, or with the wrong
	// typed confirmation is refused, and a used token cannot be replayed.
	refused(s.do("GET", applyPath, ""), 404, "route_not_found")
	refused(s.do("POST", applyPath, `{"confirmationToken":"x","confirmation":"apply demo"}`), 409, "approval_required")
	refused(s.do("POST", applyPath, `{"confirmationToken":"x","autoApprove":true}`), 400, "invalid_request")
	approval := s.approve(op.PlanID)
	if approval.Phrase != "apply demo" || !approval.Destructive || approval.ArtifactSHA256 != detail.ArtifactSHA256 || len(approval.Token) != 64 || strings.Join(approval.Command[:4], " ") != "terraform apply -input=false -json" {
		t.Fatalf("approval %+v", approval)
	}
	refused(s.do("POST", applyPath, `{"confirmationToken":"`+approval.Token+`","confirmation":"apply"}`), 409, "confirmation_mismatch")
	refused(s.do("POST", applyPath, `{"confirmationToken":"`+approval.Token+`","confirmation":"apply demo"}`), 409, "approval_required")
	approval = s.approve(op.PlanID)
	refused(s.do("POST", applyPath, `{"confirmationToken":"`+strings.Repeat("0", 64)+`","confirmation":"apply demo"}`), 409, "approval_invalid")
	if strings.Contains(strings.Join(s.fake.Calls(), ","), "apply") {
		t.Fatal("refused requests reached the engine")
	}
	approval = s.approve(op.PlanID)
	w = s.do("POST", applyPath, `{"confirmationToken":"`+approval.Token+`","confirmation":"apply demo"}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	applied := s.wait(decode[operations.Operation](t, w).ID)
	if applied.Status != operations.Succeeded || applied.Outcome != "applied" || applied.Kind != "apply" || applied.PlanID != op.PlanID {
		t.Fatalf("%+v", applied)
	}
	if got := s.fake.Applied(); len(got) != 1 || !bytes.Equal(got[0], fixtures.PlanAllActions) {
		t.Fatalf("applied %d artifacts", len(got))
	}
	// A saved plan is applied at most once.
	detail = decode[planDetail](t, s.do("GET", "/api/v1/plans/"+op.PlanID, ""))
	if detail.Apply.Available || detail.Apply.ArtifactRetained {
		t.Fatalf("%+v", detail.Apply)
	}
	refused(s.do("POST", "/api/v1/plans/"+op.PlanID+"/approval", `{}`), 409, "not_applicable")
	refused(s.do("POST", "/api/v1/plans/"+op.PlanID+"/preflight", `{}`), 200, `"blocked":true`)
	parseSSE(s.do("GET", "/api/v1/operations/"+applied.ID+"/events", "").Body.String())
}

func TestReplacePlanOverHTTP(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanCollateral})
	resources := decode[struct {
		Resources []struct{ ID, Address, Mode string } `json:"resources"`
	}](t, s.do("GET", "/api/v1/resources?query=compute.aws_instance.main%5B1&limit=5", ""))
	data := decode[struct {
		Resources []struct{ ID string } `json:"resources"`
	}](t, s.do("GET", "/api/v1/resources?mode=data", ""))
	if len(resources.Resources) == 0 || len(data.Resources) == 0 {
		t.Fatal("fixture resources missing")
	}
	first, second := resources.Resources[0], resources.Resources[1]
	for _, body := range []string{
		`{"acknowledgeBackendAccess":true,"replace":["` + data.Resources[0].ID + `"]}`,
		`{"acknowledgeBackendAccess":true,"replace":["` + first.ID + `","` + first.ID + `"]}`,
		`{"acknowledgeBackendAccess":true,"replace":["module.compute.aws_instance.main[1]"]}`,
		`{"acknowledgeBackendAccess":true,"replace":["-target=x"]}`,
	} {
		if w := s.do("POST", "/api/v1/plans", body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_replace") {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true,"replace":["`+first.ID+`","`+second.ID+`"]}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	op := s.wait(decode[operations.Operation](t, w).ID)
	requests := s.fake.Requests()
	if len(requests) != 1 || strings.Join(requests[0].Replace, "|") != first.Address+"|"+second.Address {
		t.Fatalf("requests %+v", requests)
	}
	if strings.Join(op.Command[5:], " ") != "-replace="+first.Address+" -replace="+second.Address {
		t.Fatalf("command %q", op.Command)
	}
	p := decode[operations.Plan](t, s.do("GET", "/api/v1/plans/"+op.PlanID, ""))
	// The collateral fixture replaces other instances, so both requests are flagged as
	// not replaced while every collateral change remains listed.
	if p.Kind != "replace" || len(p.Replace) != 2 || len(p.Changes) != 4 || !p.Destructive || len(p.Warnings) != 2 {
		t.Fatalf("%+v", p)
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
	s := &planServer{t, Handler(store, operations.NewManager(store, fake, t.TempDir()), nil, fstest.MapFS{}, host, session, csrf, "test"), fake, nil}
	options := decode[map[string]any](t, s.do("GET", "/api/v1/plans/options", ""))
	if options["canPlan"] != false || !strings.Contains(options["reason"].(string), "Remote execution") {
		t.Fatalf("%v", options)
	}
	if w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`); w.Code != 409 {
		t.Fatalf("%d", w.Code)
	}
}

func TestPreflightOverHTTP(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	s.manager.GitProbe = func(context.Context, string) preflight.GitResult {
		return preflight.GitResult{State: preflight.GitDirty, Modified: 1, Paths: []string{"main.tf"}}
	}
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	op := s.wait(decode[operations.Operation](t, w).ID)
	path := "/api/v1/plans/" + op.PlanID
	refused := func(w *httptest.ResponseRecorder, code int, name string) {
		t.Helper()
		if w.Code != code || !strings.Contains(w.Body.String(), name) {
			t.Fatalf("want %d %s, got %d %s", code, name, w.Code, w.Body.String())
		}
	}
	refused(s.do("GET", path+"/preflight", ""), 404, "route_not_found")
	refused(s.do("POST", "/api/v1/plans/"+strings.Repeat("0", 32)+"/preflight", `{}`), 404, "plan_not_found")
	refused(s.do("POST", path+"/preflight", `{"verifyIdentity":true,"command":"aws"}`), 400, "invalid_request")
	refused(s.do("POST", path+"/preflight", `{"verifyIdentity":"yes"}`), 400, "invalid_request")
	w = s.do("POST", path+"/preflight", `{}`)
	report := decode[operations.PreflightReport](t, w)
	if w.Code != 200 || report.Blocked || report.Binding == "" || !slices.Contains(report.Acknowledge, preflight.CheckGit) || report.Impact.Total == 0 {
		t.Fatalf("%d %+v", w.Code, report)
	}
	// The binding alone is not enough: each warning is acknowledged by ID.
	refused(s.do("POST", path+"/approval", `{}`), 409, "preflight_required")
	refused(s.do("POST", path+"/approval", `{"preflightBinding":"`+report.Binding+`"}`), 409, "acknowledgement_required")
	refused(s.do("POST", path+"/approval", `{"preflightBinding":"`+report.Binding+`","acknowledged":["*"]}`), 409, "acknowledgement_required")
	refused(s.do("POST", path+"/approval", `{"preflightBinding":"`+strings.Repeat("x", 200)+`"}`), 400, "invalid_request")
	refused(s.do("POST", path+"/approval", `{"preflightBinding":"`+report.Binding+`","approveAll":true}`), 400, "invalid_request")
	body, _ := json.Marshal(map[string]any{"preflightBinding": report.Binding, "acknowledged": report.Acknowledge})
	w = s.do("POST", path+"/approval", string(body))
	approval := decode[operations.Approval](t, w)
	if w.Code != 200 || !slices.Equal(approval.Acknowledged, report.Acknowledge) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = s.do("POST", path+"/apply", `{"confirmationToken":"`+approval.Token+`","confirmation":"apply demo"}`)
	applied := s.wait(decode[operations.Operation](t, w).ID)
	if applied.Outcome != "applied" || applied.Preflight == nil || !slices.Equal(applied.Preflight.Acknowledged, report.Acknowledge) {
		t.Fatalf("%+v", applied)
	}
	// After the apply the plan cannot pass preflight again.
	if r := decode[operations.PreflightReport](t, s.do("POST", path+"/preflight", `{}`)); !r.Blocked || r.Binding != "" {
		t.Fatalf("%+v", r)
	}
}

// M24.01: over HTTP, a plan with unsupported action combinations is reviewable but apply is
// unavailable with a stable code, and approval and apply are refused even with the
// destructive confirmation phrase. The engine's apply never runs and no values leak.
func TestUnknownActionsRefusedOverHTTP(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanUnknownActions})
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	op := s.wait(decode[operations.Operation](t, w).ID)
	if op.Status != operations.Succeeded {
		t.Fatalf("%+v", op)
	}
	raw := s.do("GET", "/api/v1/plans/"+op.PlanID, "")
	if strings.Contains(raw.Body.String(), "DO_NOT_LEAK_HCLARO_TEST_SECRET") {
		t.Fatal("secret in plan detail")
	}
	detail := decode[struct {
		Unsupported int                    `json:"unsupportedActions"`
		Destructive bool                   `json:"destructive"`
		Warnings    []string               `json:"warnings"`
		Apply       operations.ApplyStatus `json:"apply"`
	}](t, raw)
	if detail.Apply.Available || detail.Apply.Code != "unknown_actions" || detail.Apply.ArtifactRetained || detail.Unsupported != 2 || !detail.Destructive || len(detail.Warnings) == 0 {
		t.Fatalf("detail %+v", detail)
	}
	preflight := s.do("POST", "/api/v1/plans/"+op.PlanID+"/preflight", `{}`)
	if preflight.Code != 200 || !strings.Contains(preflight.Body.String(), `"blocked":true`) || strings.Contains(preflight.Body.String(), `"binding"`) {
		t.Fatalf("preflight %d %s", preflight.Code, preflight.Body.String())
	}
	w = s.do("POST", "/api/v1/plans/"+op.PlanID+"/approval", `{"preflightBinding":"forged","acknowledged":["plan.destructive"]}`)
	if w.Code != 409 || decode[map[string]string](t, w)["error"] != "unknown_actions" {
		t.Fatalf("approval: %d %s", w.Code, w.Body.String())
	}
	w = s.do("POST", "/api/v1/plans/"+op.PlanID+"/apply", `{"confirmationToken":"x","confirmation":"apply demo"}`)
	if w.Code != 409 {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.Join(s.fake.Calls(), ","), "apply") {
		t.Fatalf("engine apply ran: %v", s.fake.Calls())
	}
}
