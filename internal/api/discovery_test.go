package api

import (
	"bytes"
	"context"
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
	"hclaro/internal/discovery"
	"hclaro/internal/discovery/discoverytest"
	"hclaro/internal/engine"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

func newDiscoveryServer(t *testing.T) (*planServer, *discoverytest.Runner, string) {
	t.Helper()
	// The developer's own AWS settings never influence these tests.
	for _, name := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(name, "")
	}
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanImport})
	runner := &discoverytest.Runner{}
	svc := discovery.New(runner)
	svc.Sleep = func(context.Context, time.Duration) error { return nil }
	s.manager.Discovery = svc
	t.Cleanup(func() { svc.Close(time.Second) })
	return s, runner, root
}

type apiScan struct {
	discovery.ScanView
}

func (s *planServer) discoveryScan(id string) apiScan {
	s.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		w := s.do("GET", "/api/v1/discovery/scans/"+id, "")
		if w.Code != 200 {
			s.t.Fatalf("%d %s", w.Code, w.Body)
		}
		v := decode[apiScan](s.t, w)
		if v.Status != discovery.ScanRunning {
			return v
		}
	}
	s.t.Fatal("scan did not finish")
	return apiScan{}
}

func candidate(t *testing.T, v apiScan, typ, id string) discovery.CandidateView {
	t.Helper()
	for _, c := range v.Results {
		if c.Type == typ && c.ResourceID == id {
			return c
		}
	}
	t.Fatalf("no %s %s", typ, id)
	return discovery.CandidateView{}
}

func TestDiscoveryAPIEndToEnd(t *testing.T) {
	s, runner, root := newDiscoveryServer(t)
	bodies := []string{}
	record := func(w *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		bodies = append(bodies, w.Body.String())
		return w
	}
	// Opening the page reads options and the scan list: nothing runs, AWS is not contacted.
	options := decode[struct {
		CanScan   bool                     `json:"canScan"`
		Types     []discovery.ResourceType `json:"types"`
		Workspace string                   `json:"workspace"`
		Identity  *discovery.CloudIdentity `json:"identity"`
	}](t, record(s.do("GET", "/api/v1/discovery/options", "")))
	record(s.do("GET", "/api/v1/discovery/scans", ""))
	if !options.CanScan || len(options.Types) != 5 || options.Workspace != "demo" || options.Identity != nil {
		t.Fatalf("%+v", options)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("browsing contacted AWS: %v", calls)
	}
	// Scanning requires a verification first, and an acknowledgement.
	scope := `"scope":{"regions":["eu-west-1"],"types":["aws_vpc","aws_subnet","aws_s3_bucket","aws_db_instance","aws_security_group"]}`
	if w := s.do("POST", "/api/v1/discovery/scans", `{"identityId":"`+strings.Repeat("0", 32)+`",`+scope+`,"acknowledgeApiCalls":true}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "identity_required" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := record(s.do("POST", "/api/v1/discovery/identity", `{}`))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	identity := decode[discovery.CloudIdentity](t, w)
	if identity.Account != "111122223333" || identity.Pin.Workspace != "demo" {
		t.Fatalf("%+v", identity)
	}
	for body, want := range map[string]string{
		`{"identityId":"` + identity.ID + `",` + scope + `}`:                                                                         "acknowledgement_required",
		`{"identityId":"` + identity.ID + `",` + scope + `,"acknowledgeApiCalls":false}`:                                             "acknowledgement_required",
		`{"identityId":"` + identity.ID + `",` + scope + `,"acknowledgeApiCalls":true,"command":"ec2 delete-vpc"}`:                   "invalid_request",
		`{"identityId":"` + identity.ID + `","scope":{"regions":["mars-1"],"types":["aws_vpc"]},"acknowledgeApiCalls":true}`:         "invalid_scope",
		`{"identityId":"` + identity.ID + `","scope":{"regions":["eu-west-1"],"types":["aws_iam_role"]},"acknowledgeApiCalls":true}`: "invalid_scope",
		`{"identityId":"` + identity.ID + `",` + scope + `,"acknowledgeApiCalls":true,"profileId":"../x"}`:                           "invalid_request",
	} {
		if w := s.do("POST", "/api/v1/discovery/scans", body); w.Code != 400 || decode[map[string]string](t, w)["error"] != want {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	w = record(s.do("POST", "/api/v1/discovery/scans", `{"identityId":"`+identity.ID+`",`+scope+`,"acknowledgeApiCalls":true}`))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	started := decode[discovery.Scan](t, w)
	v := s.discoveryScan(started.ID)
	record(s.do("GET", "/api/v1/discovery/scans/"+started.ID, ""))
	if v.Status != discovery.ScanCompleted || !v.Current {
		t.Fatalf("%s %+v", v.Status, v.Tasks)
	}
	// Labels against the launch state fixture.
	for _, tc := range []struct{ typ, id, want string }{
		{"aws_s3_bucket", "hclaro-demo-artifacts", discovery.MatchTracked},
		{"aws_s3_bucket", "hclaro-legacy-logs", discovery.MatchPossiblyUntracked},
		{"aws_db_instance", "demo-database-000", discovery.MatchTracked},
		// The state fixture's subnets have IDs in another format: never "untracked".
		{"aws_subnet", "subnet-0f1e2d3c4b5a60001", discovery.MatchUnknown},
		{"aws_vpc", "vpc-0a1b2c3d4e5f60001", discovery.MatchPossiblyUntracked},
	} {
		if c := candidate(t, v, tc.typ, tc.id); c.Match.Status != tc.want {
			t.Errorf("%s %s: %s", tc.typ, tc.id, c.Match.Status)
		}
	}
	tracked := candidate(t, v, "aws_s3_bucket", "hclaro-demo-artifacts")
	if w := s.do("POST", "/api/v1/discovery/scans/"+v.ID+"/candidates/"+tracked.ID+"/prepare", `{}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "not_importable" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	subnet := candidate(t, v, "aws_subnet", "subnet-0f1e2d3c4b5a60001")
	if w := s.do("POST", "/api/v1/discovery/scans/"+v.ID+"/candidates/"+subnet.ID+"/prepare", `{}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "acknowledgement_required" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	logs := candidate(t, v, "aws_s3_bucket", "hclaro-legacy-logs")
	w = record(s.do("POST", "/api/v1/discovery/scans/"+v.ID+"/candidates/"+logs.ID+"/prepare", `{}`))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	prefill := decode[discovery.Prefill](t, w)
	if prefill.ImportID != "hclaro-legacy-logs" || prefill.Type != "aws_s3_bucket" || prefill.SuggestedAddress != "aws_s3_bucket.hclaro_legacy_logs" {
		t.Fatalf("%+v", prefill)
	}
	// The import wizard flow, linked to the prepared candidate.
	request := `"address":"aws_s3_bucket.legacy_logs","id":"hclaro-legacy-logs","provider":"","file":"imports.tf","generateConfig":false`
	preview := decode[imports.Preview](t, s.do("POST", "/api/v1/imports/preview", "{"+request+"}"))
	acks := `,"previewSha256":"` + preview.SHA256 + `","acknowledgeWrite":true,"acknowledgeBackendAccess":true`
	for name, body := range map[string]string{
		"other id":      `{"address":"aws_s3_bucket.legacy_logs","id":"hclaro-other","provider":"","file":"imports.tf","generateConfig":false` + acks + `,"discoveryToken":"` + prefill.Token + `"}`,
		"other type":    `{"address":"aws_s3_bucket_policy.legacy_logs","id":"hclaro-legacy-logs","provider":"","file":"imports.tf","generateConfig":false` + acks + `,"discoveryToken":"` + prefill.Token + `"}`,
		"unknown token": `{` + request + acks + `,"discoveryToken":"` + strings.Repeat("0", 32) + `"}`,
		"bad token":     `{` + request + acks + `,"discoveryToken":"../x"}`,
	} {
		if w := s.do("POST", "/api/v1/imports", body); w.Code != 409 || decode[map[string]string](t, w)["error"] != "discovery_stale" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); err == nil {
		t.Fatal("a refused discovery import wrote a file")
	}
	w = s.do("POST", "/api/v1/imports", `{`+request+acks+`,"discoveryToken":"`+prefill.Token+`"}`)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if op := s.wait(decode[operations.Operation](t, w).ID); op.Status != operations.Succeeded {
		t.Fatalf("import plan %s", op.Status)
	}
	// A prepared import is used once.
	if w := s.do("POST", "/api/v1/imports", `{`+request+acks+`,"discoveryToken":"`+prefill.Token+`"}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "discovery_stale" {
		t.Fatalf("token reused: %d %s", w.Code, w.Body)
	}
	// The credentials now belong to another account: prepare and new scans are refused.
	runner.SetAccount("999999999999")
	if w := s.do("POST", "/api/v1/discovery/scans/"+v.ID+"/candidates/"+logs.ID+"/prepare", `{}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != discovery.KindIdentityChanged {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Every call was an allowlisted read; no response carried a canary or CLI internals.
	for _, args := range runner.Calls() {
		if !discovery.Allowed(args) || (args[0] != "--version" && !(strings.HasPrefix(args[1], "describe-") || strings.HasPrefix(args[1], "list-") || strings.HasPrefix(args[1], "get-"))) {
			t.Fatalf("non-read call %v", args)
		}
	}
	for _, body := range bodies {
		for _, leak := range []string{"DO_NOT_LEAK", "hunter2", "MasterUsername", "IpPermissions", "demo.user@example.com", "AROASYNTHETIC"} {
			if strings.Contains(body, leak) {
				t.Fatalf("response leaks %q", leak)
			}
		}
	}
}

func TestDiscoveryErrorsAreSanitized(t *testing.T) {
	s, runner, _ := newDiscoveryServer(t)
	runner.Fail = func(args []string) error {
		if args[0] == "sts" {
			return &discovery.CallError{Kind: discovery.Classify("Error loading SSO Token: Token for corp-sso at /Users/me/.aws/sso/cache/abc.json has expired")}
		}
		return nil
	}
	w := s.do("POST", "/api/v1/discovery/identity", `{}`)
	body := w.Body.String()
	if w.Code != 502 || !strings.Contains(body, discovery.KindExpired) || strings.Contains(body, "corp-sso") || strings.Contains(body, ".aws") {
		t.Fatalf("%d %s", w.Code, body)
	}
}

func TestDiscoveryAccessDeniedIsPartialNotEmpty(t *testing.T) {
	s, runner, _ := newDiscoveryServer(t)
	runner.Fail = func(args []string) error {
		if args[0] == "rds" {
			return &discovery.CallError{Kind: discovery.KindAccessDenied}
		}
		return nil
	}
	identity := decode[discovery.CloudIdentity](t, s.do("POST", "/api/v1/discovery/identity", `{}`))
	w := s.do("POST", "/api/v1/discovery/scans", `{"identityId":"`+identity.ID+`","scope":{"regions":["eu-west-1"],"types":["aws_vpc","aws_db_instance"]},"acknowledgeApiCalls":true}`)
	v := s.discoveryScan(decode[discovery.Scan](t, w).ID)
	if v.Status != discovery.ScanPartial || len(v.Results) == 0 {
		t.Fatalf("%s %d", v.Status, len(v.Results))
	}
	for _, task := range v.Tasks {
		if task.Type == "aws_db_instance" && task.Error != discovery.KindAccessDenied {
			t.Fatalf("%+v", task)
		}
	}
}

func TestDiscoveryUnavailableAndRouteGuard(t *testing.T) {
	// Without a service, or without a launch project, nothing runs.
	s := newPlanServer(t, &engine.Fake{PlanJSON: fixtures.PlanImport})
	if w := s.do("POST", "/api/v1/discovery/identity", `{}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "discovery_unavailable" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	store := workspace.New(workspace.Info{})
	manager := operations.NewManager(store, &engine.Fake{}, "")
	runner := &discoverytest.Runner{}
	manager.Discovery = discovery.New(runner)
	t.Cleanup(func() { manager.Close(time.Second); manager.Discovery.Close(time.Second) })
	hubless := &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), nil, manager}
	if w := hubless.do("POST", "/api/v1/discovery/identity", `{}`); w.Code != 409 || decode[map[string]string](t, w)["error"] != "no_launch_project" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if options := decode[map[string]any](t, hubless.do("GET", "/api/v1/discovery/options", "")); options["canScan"] != false {
		t.Fatalf("%v", options)
	}
	ds, dr, _ := newDiscoveryServer(t)
	id := strings.Repeat("0", 32)
	for _, tc := range []struct {
		name, method, path, origin, csrfHeader string
		code                                   int
	}{
		{"no origin", "POST", "/api/v1/discovery/identity", "", csrf, 403},
		{"foreign origin", "POST", "/api/v1/discovery/scans", "http://evil.example", csrf, 403},
		{"no csrf", "POST", "/api/v1/discovery/scans/" + id + "/candidates/" + id + "/prepare", "http://" + host, "", 403},
		{"no csrf cancel", "POST", "/api/v1/discovery/scans/" + id + "/cancel", "http://" + host, "", 403},
		{"no delete", "DELETE", "/api/v1/discovery/scans/" + id, "http://" + host, csrf, 405},
		{"no arbitrary post", "POST", "/api/v1/discovery/run", "http://" + host, csrf, 405},
		{"no identity get", "GET", "/api/v1/discovery/identity", "", "", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://"+host+tc.path, strings.NewReader(`{}`))
		if tc.method == "GET" {
			r = httptest.NewRequest(tc.method, "http://"+host+tc.path, nil)
		}
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
		ds.handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.name, w.Code)
		}
	}
	if calls := dr.Calls(); len(calls) != 0 {
		t.Fatalf("refused requests contacted AWS: %v", calls)
	}
	r := httptest.NewRequest("GET", "http://"+host+"/api/v1/discovery/options", nil)
	w := httptest.NewRecorder()
	ds.handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

// TestDiscoveryGolden pins the options, identity, scan and prefill responses the UI consumes
// for the synthetic AWS fixtures. IDs and times are fixed so the file is deterministic.
func TestDiscoveryGolden(t *testing.T) {
	s, _, _ := newDiscoveryServer(t)
	svc := s.manager.Discovery
	n := 0
	var mu sync.Mutex
	svc.NewID = func() string { mu.Lock(); defer mu.Unlock(); n++; return fmt.Sprintf("%032x", n) }
	fixed := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }
	identity := decode[discovery.CloudIdentity](t, s.do("POST", "/api/v1/discovery/identity", `{}`))
	options := s.do("GET", "/api/v1/discovery/options", "").Body.Bytes()
	w := s.do("POST", "/api/v1/discovery/scans", `{"identityId":"`+identity.ID+`","scope":{"regions":["eu-west-1","us-east-1"],"types":["aws_vpc","aws_subnet","aws_security_group","aws_s3_bucket","aws_db_instance"]},"acknowledgeApiCalls":true}`)
	v := s.discoveryScan(decode[discovery.Scan](t, w).ID)
	scanBody := s.do("GET", "/api/v1/discovery/scans/"+v.ID, "").Body.Bytes()
	logs := candidate(t, v, "aws_s3_bucket", "hclaro-legacy-logs")
	prefill := s.do("POST", "/api/v1/discovery/scans/"+v.ID+"/candidates/"+logs.ID+"/prepare", `{}`).Body.Bytes()
	doc := map[string]json.RawMessage{"options": options, "scan": scanBody, "prefill": prefill}
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{canary, "hunter2", "demo.user@example.com", "MasterUsername"} {
		if bytes.Contains(got, []byte(leak)) {
			t.Fatalf("golden contains %q", leak)
		}
	}
	path := filepath.Join("..", "..", "fixtures", "discovery", "scan.golden.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
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
		t.Fatalf("discovery response differs from golden; rerun with -update and review the diff\n%s", got)
	}
}
