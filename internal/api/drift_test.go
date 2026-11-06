package api

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func lines(b []byte) [][]byte { return bytes.Split(bytes.TrimSpace(b), []byte("\n")) }

type scanView struct {
	operations.DriftScanView
	Resources []struct {
		ID             string `json:"id"`
		Address        string `json:"address"`
		Classification string `json:"classification"`
		ResourceID     string `json:"resourceId"`
	} `json:"resources"`
}

func startScan(t *testing.T, s *planServer) (operations.Operation, scanView) {
	t.Helper()
	w := s.do("POST", "/api/v1/drift-scans", `{"acknowledgeProviderAccess":true}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	started := decode[struct {
		Operation operations.Operation `json:"operation"`
		Scan      scanView             `json:"scan"`
	}](t, w)
	done := s.wait(started.Operation.ID)
	return done, decode[scanView](t, s.do("GET", "/api/v1/drift-scans/"+started.Scan.ID, ""))
}

// newDriftServer is newPlanServer that also returns the launch store.
func newDriftServer(t *testing.T, fake *engine.Fake) (*planServer, *workspace.Store) {
	t.Helper()
	fake.VersionValue, fake.WorkspaceValue, fake.StateJSON = "1.9.8", "demo", fixtures.State
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, t.TempDir())
	manager.TempDir = t.TempDir()
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	return &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}, store
}

func TestDriftScanAPI(t *testing.T) {
	fake := &engine.Fake{Lines: lines(fixtures.DriftLog), PlanJSON: fixtures.DriftMixed}
	s, store := newDriftServer(t, fake)
	options := decode[struct {
		CanScan   bool   `json:"canScan"`
		Workspace string `json:"workspace"`
	}](t, s.do("GET", "/api/v1/drift-scans/options", ""))
	if !options.CanScan || options.Workspace != "demo" {
		t.Fatalf("%+v", options)
	}
	// Browsing runs nothing.
	for _, path := range []string{"/api/v1/drift-scans/options", "/api/v1/drift-scans", "/api/v1/workspace"} {
		s.do("GET", path, "")
	}
	if calls := strings.Join(fake.Calls(), ","); strings.Contains(calls, "plan") {
		t.Fatalf("GET ran an engine command: %s", calls)
	}
	// The acknowledgement is required, and unknown fields are refused.
	for body, code := range map[string]int{`{}`: 400, `{"acknowledgeProviderAccess":false}`: 400, `{"acknowledgeProviderAccess":true,"refresh":true}`: 400, `{"acknowledgeProviderAccess":true,"varFiles":["../x.tfvars"]}`: 400, `{"acknowledgeProviderAccess":true,"profileId":"../x"}`: 400} {
		if w := s.do("POST", "/api/v1/drift-scans", body); w.Code != code {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	op, scan := startScan(t, s)
	if op.Kind != "drift" || scan.Status != "completed" || scan.Outcome != "drift_detected" || !scan.Current || len(scan.Resources) != 4 {
		t.Fatalf("%+v %+v", op, scan)
	}
	var bucket, group string
	for _, r := range scan.Resources {
		switch r.Address {
		case "aws_s3_bucket.artifacts":
			bucket = r.ID
			if r.ResourceID == "" {
				t.Fatal("no inspector link for a recorded object")
			}
			if w := s.do("GET", "/api/v1/resources/"+r.ResourceID, ""); w.Code != 200 {
				t.Fatal("inspector link does not resolve")
			}
		case "aws_security_group.web":
			group = r.ID
		}
	}
	detail := s.do("GET", "/api/v1/drift-scans/"+scan.ID+"/resources/"+group, "")
	if detail.Code != 200 || strings.Contains(detail.Body.String(), "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(detail.Body.String(), `"observed":"0.0.0.0/0"`) {
		t.Fatalf("%d %s", detail.Code, detail.Body)
	}
	if w := s.do("GET", "/api/v1/drift-scans/"+scan.ID+"/resources/"+bucket, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"classification":"changed"`) {
		t.Fatalf("%s", w.Body)
	}
	// No plan, no apply, no second engine run.
	if plans := s.do("GET", "/api/v1/plans", ""); !strings.Contains(plans.Body.String(), `"plans":[]`) {
		t.Fatalf("scan listed as a plan: %s", plans.Body)
	}
	for _, call := range fake.Calls() {
		if call == "apply" {
			t.Fatal("apply ran")
		}
	}
	if n := len(fake.Requests()); n != 1 || !fake.Requests()[0].RefreshOnly {
		t.Fatalf("requests %+v", fake.Requests())
	}
	// SSE progress is the operation's sanitized log.
	events := s.do("GET", "/api/v1/operations/"+op.ID+"/events", "")
	if !strings.Contains(events.Body.String(), "Drift scan started") || !strings.Contains(events.Body.String(), "event: end") {
		t.Fatalf("%s", events.Body)
	}
	for path, code := range map[string]int{
		"/api/v1/drift-scans/" + strings.Repeat("0", 32):                                           404,
		"/api/v1/drift-scans/..%2f..":                                                              404,
		"/api/v1/drift-scans/" + scan.ID + "/resources/" + strings.Repeat("0", 32):                 404,
		"/api/v1/drift-scans/" + scan.ID + "/resources/x":                                          404,
		"/api/v1/drift-scans/" + strings.Repeat("0", 32) + "/resources/" + strings.Repeat("0", 32): 404,
	} {
		if w := s.do("GET", path, ""); w.Code != code {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	// Another workspace: the scan is withheld and its details refused.
	fake.SetWorkspace("other")
	store.Load(context.Background(), fake, "terraform")
	stale := decode[scanView](t, s.do("GET", "/api/v1/drift-scans/"+scan.ID, ""))
	if stale.Current || len(stale.Resources) != 0 || !stale.Withheld {
		t.Fatalf("%+v", stale)
	}
	if w := s.do("GET", "/api/v1/drift-scans/"+scan.ID+"/resources/"+bucket, ""); w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if list := s.do("GET", "/api/v1/drift-scans", ""); strings.Contains(list.Body.String(), "aws_s3_bucket.artifacts") {
		t.Fatalf("list leaked another workspace's resources: %s", list.Body)
	}
}

func TestDriftScanAPIFailuresAndUnsupported(t *testing.T) {
	fake := &engine.Fake{Lines: lines(fixtures.DriftLogSSO), PlanErr: &engine.ExitError{Code: 1}}
	s := newPlanServer(t, fake)
	op, scan := startScan(t, s)
	if op.Status != "failed" || scan.Status != "failed" || scan.Outcome != "" || scan.Exhaustive || scan.Diagnostic == nil || scan.Diagnostic.Code != "credentials" {
		t.Fatalf("%+v", scan)
	}

	// Remote execution: refused with a reason, nothing runs.
	remote := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"})
	store.Load(context.Background(), remote, "terraform")
	manager := operations.NewManager(store, remote, t.TempDir())
	t.Cleanup(func() { manager.Close(time.Second) })
	rs := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), remote, manager}
	options := decode[struct {
		CanScan bool   `json:"canScan"`
		Reason  string `json:"reason"`
	}](t, rs.do("GET", "/api/v1/drift-scans/options", ""))
	if options.CanScan || !strings.Contains(options.Reason, "remote") {
		t.Fatalf("%+v", options)
	}
	w := rs.do("POST", "/api/v1/drift-scans", `{"acknowledgeProviderAccess":true}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "drift_unsupported") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, call := range remote.Calls() {
		if call == "plan" {
			t.Fatal("unsupported scan ran the engine")
		}
	}
}

func TestDriftScanRouteGuard(t *testing.T) {
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.DriftNone})
	for _, tc := range []struct {
		name, method, path, origin, csrfHeader string
		code                                   int
	}{
		{"no origin", "POST", "/api/v1/drift-scans", "", csrf, 403},
		{"foreign origin", "POST", "/api/v1/drift-scans", "http://evil.example", csrf, 403},
		{"no csrf", "POST", "/api/v1/drift-scans", "http://" + host, "", 403},
		{"no post on a scan", "POST", "/api/v1/drift-scans/" + strings.Repeat("0", 32), "http://" + host, csrf, 405},
		{"no apply of a scan", "POST", "/api/v1/drift-scans/" + strings.Repeat("0", 32) + "/apply", "http://" + host, csrf, 405},
		{"no delete", "DELETE", "/api/v1/drift-scans/" + strings.Repeat("0", 32), "http://" + host, csrf, 405},
	} {
		r := httptest.NewRequest(tc.method, "http://"+host+tc.path, strings.NewReader(`{"acknowledgeProviderAccess":true}`))
		r.Header.Set("Authorization", "Bearer "+session)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.csrfHeader != "" {
			r.Header.Set("X-Hclaro-CSRF", tc.csrfHeader)
		}
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.name, w.Code)
		}
	}
	if calls := s.fake.Calls(); len(calls) > 0 && strings.Contains(strings.Join(calls, ","), "plan") {
		t.Fatal("refused request ran the engine")
	}
	// No session: refused before routing.
	r := httptest.NewRequest("GET", "http://"+host+"/api/v1/drift-scans", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// TestDriftGolden pins the scan and detail responses the UI consumes for the mixed fixture.
// Times and IDs are fixed so the file is deterministic.
func TestDriftGolden(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftMixed}
	s, _ := newDriftServer(t, fake)
	n := 0
	s.manager.NewID = func() string { n++; return fmt.Sprintf("%032x", n) }
	_, scan := startScan(t, s)
	body := s.do("GET", "/api/v1/drift-scans/"+scan.ID, "").Body.Bytes()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	doc["startedAt"], doc["completedAt"] = "2026-11-06T09:00:00Z", "2026-11-06T09:00:04Z"
	details := map[string]any{}
	for _, r := range scan.Resources {
		var d any
		if err := json.Unmarshal(s.do("GET", "/api/v1/drift-scans/"+scan.ID+"/resources/"+r.ID, "").Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		details[r.ID] = d
	}
	got, err := json.MarshalIndent(map[string]any{"scan": doc, "details": details}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(canary)) || bytes.Contains(got, []byte("AKIAABCDEFGHIJKLMNOP")) {
		t.Fatal("secret in golden")
	}
	path := filepath.Join("..", "..", "fixtures", "drift", "mixed.golden.json")
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
		t.Fatalf("drift response differs from golden; rerun with -update and review the diff\n%s", got)
	}
}
