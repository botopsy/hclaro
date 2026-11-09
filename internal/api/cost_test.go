package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/cost"
	"hclaro/internal/cost/costtest"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func newCostServer(t *testing.T, strict bool) (*planServer, *costtest.Runner, string) {
	t.Helper()
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	// The fixture project declares no import, so the fake engine returns the all-actions plan.
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_instance\" \"web\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := costtest.New()
	svc := cost.New(runner, strict)
	svc.TempDir = t.TempDir()
	svc.Store = cost.OpenStore(t.TempDir())
	s.manager.Cost = svc
	t.Cleanup(func() { svc.Close(5 * time.Second) })
	return s, runner, root
}

func (s *planServer) plan() string {
	s.t.Helper()
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	if w.Code != 202 {
		s.t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := s.wait(decode[operations.Operation](s.t, w).ID)
	if op.Status != operations.Succeeded || op.PlanID == "" {
		s.t.Fatalf("plan %+v", op)
	}
	return op.PlanID
}

func (s *planServer) estimate(id string) cost.CostEstimate {
	s.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		w := s.do("GET", "/api/v1/cost-estimates/"+id, "")
		if w.Code != 200 {
			s.t.Fatalf("%d %s", w.Code, w.Body)
		}
		if e := decode[cost.CostEstimate](s.t, w); e.Status != cost.Running {
			return e
		}
	}
	s.t.Fatal("estimate did not finish")
	return cost.CostEstimate{}
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[map[string]string](t, w)["error"]
}

func TestCostAPIEndToEnd(t *testing.T) {
	s, runner, root := newCostServer(t, false)
	caps := decode[cost.CostProviderCapabilities](t, s.do("GET", "/api/v1/cost/provider", ""))
	if caps.State != cost.StateOff || !caps.Installed || caps.Enabled || caps.CanEstimate || caps.Disclosure.Endpoint == "" || len(caps.Disclosure.NeverSent) == 0 {
		t.Fatalf("caps = %+v", caps)
	}
	planID := s.plan()
	// Nothing runs before opt-in.
	if w := s.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", `{}`); w.Code != 409 || errorCode(t, w) != cost.KindOptIn {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("GET", "/api/v1/plans/"+planID+"/cost-estimate", ""); w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for body, want := range map[string]string{
		`{}`: "acknowledgement_required",
		`{"acknowledged":false,"disclosureVersion":"1"}`: "acknowledgement_required",
		`{"acknowledged":true,"apiKey":"x"}`:             "invalid_request",
	} {
		if w := s.do("POST", "/api/v1/cost/opt-in", body); w.Code != 400 || errorCode(t, w) != want {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := s.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"0"}`); w.Code != 409 || errorCode(t, w) != cost.KindOptIn {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("provider ran before opt-in: %v", calls)
	}
	w := s.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"`+cost.DisclosureVersion+`"}`)
	if caps = decode[cost.CostProviderCapabilities](t, w); w.Code != 200 || caps.State != cost.StateReady || caps.CLIVersion != "0.10.39" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// The browser names only a plan: paths, files, flags and credentials are refused.
	for _, body := range []string{`{"path":"/etc/passwd"}`, `{"planFile":"plan.json"}`, `{"args":["upload"]}`, `{"apiKey":"x"}`, `[]`} {
		if w := s.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	for _, id := range []string{strings.Repeat("0", 32), "..%2f..%2fetc"} {
		// Unknown IDs are not found; malformed ones never reach the handler.
		if w := s.do("POST", "/api/v1/plans/"+id+"/cost-estimates", `{}`); w.Code != 404 && w.Code != 405 {
			t.Errorf("%s: %d", id, w.Code)
		}
	}
	w = s.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", `{}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	started := decode[cost.CostEstimate](t, w)
	e := s.estimate(started.ID)
	b := e.Breakdown
	if e.Status != cost.Completed || e.Stale || b == nil || b.Currency != "USD" || b.MonthlyDelta != "9.89" || b.Coverage.Percent == nil || *b.Coverage.Percent != 50 || e.PlanID != planID {
		t.Fatalf("estimate = %+v", e)
	}
	if latest := decode[cost.CostEstimate](t, s.do("GET", "/api/v1/plans/"+planID+"/cost-estimate", "")); latest.ID != e.ID {
		t.Fatalf("latest = %+v", latest)
	}
	for _, input := range runner.Inputs() {
		if bytes.Contains(input, []byte(canary)) || bytes.Contains(input, []byte(root)) {
			t.Fatal("cost input holds a canary or the project path")
		}
	}
	for _, env := range runner.Environs() {
		for _, item := range env {
			if strings.HasPrefix(item, "AWS_") || strings.HasPrefix(item, "TF_VAR_") {
				t.Fatalf("cloud setting passed to the provider: %s", item)
			}
		}
	}
	history := decode[struct {
		Records   []cost.AggregateRecord `json:"records"`
		Persisted bool                   `json:"persisted"`
	}](t, s.do("GET", "/api/v1/cost/history", ""))
	if !history.Persisted || len(history.Records) != 1 || history.Records[0].MonthlyDelta != "9.89" || history.Records[0].ProjectName != filepath.Base(root) {
		t.Fatalf("history = %+v", history)
	}
	// A context change invalidates the estimate and refuses a new one for this plan.
	if err := os.WriteFile(filepath.Join(root, "extra.tf"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale := decode[cost.CostEstimate](t, s.do("GET", "/api/v1/cost-estimates/"+e.ID, "")); !stale.Stale || stale.StaleReason == "" {
		t.Fatalf("not stale after a context change: %+v", stale)
	}
	if w := s.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", `{}`); w.Code != 409 || errorCode(t, w) != cost.KindContextChanged {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// A new plan has no estimate until asked; the old estimate stays bound to its plan.
	newPlan := s.plan()
	if w := s.do("GET", "/api/v1/plans/"+newPlan+"/cost-estimate", ""); w.Code != 404 {
		t.Fatalf("new plan inherited an estimate: %d", w.Code)
	}
	if w := s.do("POST", "/api/v1/cost/history/clear", `{}`); w.Code != 200 || decode[map[string]int](t, w)["removed"] != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/cost/opt-out", `{}`); decode[cost.CostProviderCapabilities](t, w).Enabled {
		t.Fatal("still enabled after opt-out")
	}
	if w := s.do("POST", "/api/v1/plans/"+newPlan+"/cost-estimates", `{}`); w.Code != 409 || errorCode(t, w) != cost.KindOptIn {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestCostEstimateNeverBlocksPlanOrApply(t *testing.T) {
	s, runner, _ := newCostServer(t, false)
	if w := s.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	runner.Set(func(r *costtest.Runner) { r.Mode = "block" })
	first := s.plan()
	w := s.do("POST", "/api/v1/plans/"+first+"/cost-estimates", `{}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	running := decode[cost.CostEstimate](t, w)
	<-runner.Started
	// The plan stays applicable while the estimate runs, and the apply goes ahead.
	approval := s.approve(first)
	w = s.do("POST", "/api/v1/plans/"+first+"/apply", `{"confirmationToken":"`+approval.Token+`","confirmation":"`+approval.Phrase+`"}`)
	if w.Code != 202 {
		t.Fatalf("apply while estimating: %d %s", w.Code, w.Body)
	}
	if applied := s.wait(decode[operations.Operation](t, w).ID); applied.Outcome != "applied" {
		t.Fatalf("%+v", applied)
	}
	if w := s.do("POST", "/api/v1/cost-estimates/"+running.ID+"/cancel", `{}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if e := s.estimate(running.ID); e.Status != cost.Cancelled || e.Breakdown != nil {
		t.Fatalf("%+v", e)
	}
	// A failed estimate leaves the next plan applicable.
	runner.Set(func(r *costtest.Runner) { r.Mode = "pricing" })
	second := s.plan()
	w = s.do("POST", "/api/v1/plans/"+second+"/cost-estimates", `{}`)
	if e := s.estimate(decode[cost.CostEstimate](t, w).ID); e.Status != cost.Failed || e.Error.Code != cost.KindPricing {
		t.Fatalf("%+v", e)
	}
	approval = s.approve(second)
	w = s.do("POST", "/api/v1/plans/"+second+"/apply", `{"confirmationToken":"`+approval.Token+`","confirmation":"`+approval.Phrase+`"}`)
	if w.Code != 202 {
		t.Fatalf("apply after a failed estimate: %d %s", w.Code, w.Body)
	}
	if applied := s.wait(decode[operations.Operation](t, w).ID); applied.Outcome != "applied" {
		t.Fatalf("%+v", applied)
	}
}

func TestCostUnavailableStatesAndGuards(t *testing.T) {
	// Strict local-only mode.
	strict, sr, _ := newCostServer(t, true)
	if caps := decode[cost.CostProviderCapabilities](t, strict.do("GET", "/api/v1/cost/provider", "")); caps.State != cost.StateStrictLocal || caps.CanEstimate || !caps.StrictLocal {
		t.Fatalf("%+v", caps)
	}
	if w := strict.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 409 || errorCode(t, w) != cost.KindStrictLocal {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	planID := strict.plan()
	if w := strict.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", `{}`); w.Code != 409 || errorCode(t, w) != cost.KindStrictLocal {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(sr.Calls()) != 0 {
		t.Fatal("provider ran in strict mode")
	}
	// Missing CLI.
	missing, mr, _ := newCostServer(t, false)
	mr.Set(func(r *costtest.Runner) { r.Installed = false })
	if caps := decode[cost.CostProviderCapabilities](t, missing.do("GET", "/api/v1/cost/provider", "")); caps.State != cost.StateMissing || caps.Installed {
		t.Fatalf("%+v", caps)
	}
	if w := missing.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 409 || errorCode(t, w) != cost.KindMissingCLI {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Unsupported version.
	mr.Set(func(r *costtest.Runner) { r.Installed, r.Version = true, "Infracost v0.9.24" })
	if w := missing.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 409 || errorCode(t, w) != cost.KindUnsupported {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// No cost service, and no launch project.
	off, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	if caps := decode[map[string]any](t, off.do("GET", "/api/v1/cost/provider", "")); caps["state"] != "unavailable" || caps["canEstimate"] != false {
		t.Fatalf("%v", caps)
	}
	if w := off.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 409 || errorCode(t, w) != "cost_unavailable" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	store := workspace.New(workspace.Info{})
	manager := operations.NewManager(store, &engine.Fake{}, "")
	hr := costtest.New()
	manager.Cost = cost.New(hr, false)
	t.Cleanup(func() { manager.Close(time.Second); manager.Cost.Close(time.Second) })
	hubless := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), nil, manager}
	if caps := decode[cost.CostProviderCapabilities](t, hubless.do("GET", "/api/v1/cost/provider", "")); caps.CanEstimate {
		t.Fatalf("%+v", caps)
	}
	if w := hubless.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`); w.Code != 409 || errorCode(t, w) != "no_launch_project" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Browser guards: session, Origin and CSRF.
	gs, gr, _ := newCostServer(t, false)
	id := strings.Repeat("0", 32)
	for _, tc := range []struct {
		name, method, path, origin, csrfHeader, auth string
		code                                         int
	}{
		{"no session", "GET", "/api/v1/cost/provider", "", "", "", 401},
		{"no origin", "POST", "/api/v1/cost/opt-in", "", csrf, session, 403},
		{"foreign origin", "POST", "/api/v1/plans/" + id + "/cost-estimates", "http://evil.example", csrf, session, 403},
		{"no csrf", "POST", "/api/v1/cost/opt-in", "http://" + host, "", session, 403},
		{"no csrf cancel", "POST", "/api/v1/cost-estimates/" + id + "/cancel", "http://" + host, "", session, 403},
		{"no csrf clear", "POST", "/api/v1/cost/history/clear", "http://" + host, "", session, 403},
		{"no get opt-in", "GET", "/api/v1/cost/opt-in", "", "", session, 404},
		{"no delete", "DELETE", "/api/v1/cost-estimates/" + id, "http://" + host, csrf, session, 405},
	} {
		r := httptest.NewRequest(tc.method, "http://"+host+tc.path, strings.NewReader(`{"acknowledged":true,"disclosureVersion":"1"}`))
		if tc.method == "GET" {
			r = httptest.NewRequest(tc.method, "http://"+host+tc.path, nil)
		}
		if tc.auth != "" {
			r.Header.Set("Authorization", "Bearer "+tc.auth)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.csrfHeader != "" {
			r.Header.Set("X-Hclaro-CSRF", tc.csrfHeader)
		}
		w := httptest.NewRecorder()
		gs.handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.name, w.Code)
		}
	}
	if len(gr.Calls()) != 0 || len(hr.Calls()) != 0 {
		t.Fatal("refused requests ran the provider")
	}
}

// TestCostGolden pins the provider and estimate responses the UI consumes for the synthetic
// Infracost report. IDs and times are fixed; the context fingerprint and plan digest depend
// on the temporary project and are replaced with placeholders.
func TestCostGolden(t *testing.T) {
	s, _, _ := newCostServer(t, false)
	svc := s.manager.Cost
	n := 0
	var mu sync.Mutex
	next := func() string { mu.Lock(); defer mu.Unlock(); n++; return fmt.Sprintf("%032x", n) }
	s.manager.NewID, svc.NewID = next, next
	fixed := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }
	svc.Environ = func() []string { return []string{"PATH=/usr/bin"} }
	off := s.do("GET", "/api/v1/cost/provider", "").Body.Bytes()
	ready := s.do("POST", "/api/v1/cost/opt-in", `{"acknowledged":true,"disclosureVersion":"1"}`).Body.Bytes()
	planID := s.plan()
	started := decode[cost.CostEstimate](t, s.do("POST", "/api/v1/plans/"+planID+"/cost-estimates", `{}`))
	s.estimate(started.ID)
	var estimate map[string]any
	if err := json.Unmarshal(s.do("GET", "/api/v1/plans/"+planID+"/cost-estimate", "").Body.Bytes(), &estimate); err != nil {
		t.Fatal(err)
	}
	estimate["fingerprint"], estimate["artifactDigest"] = "<fingerprint>", "<digest>"
	body, _ := json.Marshal(estimate)
	got, err := json.MarshalIndent(map[string]json.RawMessage{"providerOff": off, "providerReady": ready, "estimate": body}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(canary)) || bytes.Contains(got, []byte("aws_nat_gateway.ghost")) {
		t.Fatal("golden contains a canary or an unattributed address")
	}
	path := filepath.Join("..", "..", "fixtures", "cost", "estimate.golden.json")
	if *updateGolden {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), got) {
		t.Fatalf("cost response differs from golden; rerun with -update and review the diff\n%s", got)
	}
}
