package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/internal/discovery"
	"hclaro/internal/discovery/discoverytest"
	"hclaro/internal/profiles"
)

// Tests for Hub-scoped AWS discovery (M24.09). AWS answers come from the synthetic runner
// (fixtures/aws); no process runs and nothing contacts AWS.

// envRunner records the AWS_PROFILE each call ran with.
type envRunner struct {
	discoverytest.Runner
	mu       sync.Mutex
	profiles []string
}

func (r *envRunner) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	profile := "(inherited)"
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "AWS_PROFILE="); ok {
			profile = v
		}
	}
	r.mu.Lock()
	r.profiles = append(r.profiles, profile)
	r.mu.Unlock()
	return r.Runner.Run(ctx, environ, args)
}

func (r *envRunner) usedProfiles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.profiles)
	r.profiles = nil
	return out
}

func newScopedDiscovery(t *testing.T) (*scopedServer, *envRunner) {
	t.Helper()
	for _, name := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(name, "")
	}
	s := newScopedServer(t)
	runner := &envRunner{}
	svc := discovery.New(runner)
	svc.Sleep = func(context.Context, time.Duration) error { return nil }
	s.hub.Discovery, s.launch.Discovery = svc, svc
	t.Cleanup(func() { svc.Close(time.Second) })
	return s, runner
}

type scopedDiscoveryOptions struct {
	CanScan            bool                     `json:"canScan"`
	Code               string                   `json:"code"`
	Reason             string                   `json:"reason"`
	Workspace          string                   `json:"workspace"`
	DefaultRegion      string                   `json:"defaultRegion"`
	RegionSource       string                   `json:"regionSource"`
	Identity           *discovery.CloudIdentity `json:"identity"`
	ContextFingerprint string                   `json:"contextFingerprint"`
	Scope              *struct {
		ContextID string `json:"contextId"`
	} `json:"scope"`
}

func (s *scopedServer) discoveryOptions(cid string) scopedDiscoveryOptions {
	s.t.Helper()
	w := s.do("GET", "/api/v1/contexts/"+cid+"/discovery/options", "")
	if w.Code != 200 {
		s.t.Fatalf("discovery options: %d %s", w.Code, w.Body)
	}
	return decode[scopedDiscoveryOptions](s.t, w)
}

func (s *scopedServer) waitDiscovery(cid, id string) discovery.ScanView {
	s.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		w := s.do("GET", "/api/v1/contexts/"+cid+"/discovery/scans/"+id, "")
		if w.Code != 200 {
			s.t.Fatalf("%d %s", w.Code, w.Body)
		}
		if v := decode[discovery.ScanView](s.t, w); v.Status != discovery.ScanRunning {
			return v
		}
	}
	s.t.Fatal("scan did not finish")
	return discovery.ScanView{}
}

const discoveryScope = `"scope":{"regions":["eu-west-1"],"types":["aws_vpc","aws_s3_bucket"]}`

// network/dev and network/prod are one project and root; prod uses an execution profile with
// its own AWS profile. A verification, scan or prepared import of one workspace is never
// visible, usable or cancellable from the other, from another project or from the launch
// project; each runs with its own context's AWS profile; browsing contacts nothing; and
// closing the scope drops it.
func TestScopedDiscoveryStaysInItsContext(t *testing.T) {
	s, runner := newScopedDiscovery(t)
	if err := os.WriteFile(filepath.Join(s.roots["network"], "prod.tfvars"), []byte("# synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prod, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "prod", AWSProfile: "prod-sso", AWSRegion: "eu-west-1", VarFiles: []string{"prod.tfvars"}})
	if err != nil {
		t.Fatal(err)
	}
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("network", "prod", prod.ID)
	c := s.openLoaded("platform", "default", "")

	// Opening the page reads options and the scan list only.
	oa, ob := s.discoveryOptions(a), s.discoveryOptions(b)
	for _, cid := range []string{a, b, c} {
		if w := s.do("GET", "/api/v1/contexts/"+cid+"/discovery/scans", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"scans":[]`) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if !oa.CanScan || oa.Workspace != "dev" || oa.Scope == nil || oa.Scope.ContextID != a || oa.Identity != nil || oa.RegionSource != "default" {
		t.Fatalf("%+v", oa)
	}
	if !ob.CanScan || ob.Workspace != "prod" || ob.ContextFingerprint == "" || ob.DefaultRegion != "eu-west-1" || ob.RegionSource != "profile" {
		t.Fatalf("%+v", ob)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("browsing contacted AWS: %v", calls)
	}

	// The verification must name the reviewed context; a refusal runs nothing.
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/discovery/identity", `{"contextFingerprint":""}`), 409, "context_changed")
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/discovery/identity", `{"profileId":"`+prod.ID+`"}`), 400, "invalid_request")
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("refused verification contacted AWS: %v", calls)
	}
	w := s.do("POST", "/api/v1/contexts/"+a+"/discovery/identity", `{"contextFingerprint":""}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	idA := decode[discovery.CloudIdentity](t, w)
	if idA.Pin.Context != a || idA.Pin.Workspace != "dev" {
		t.Fatalf("%+v", idA)
	}
	if got := runner.usedProfiles(); len(got) != 2 || got[0] != "(inherited)" || got[1] != "(inherited)" {
		t.Fatalf("dev verified with %v", got)
	}
	w = s.do("POST", "/api/v1/contexts/"+b+"/discovery/identity", fmt.Sprintf(`{"contextFingerprint":%q}`, ob.ContextFingerprint))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	idB := decode[discovery.CloudIdentity](t, w)
	if got := runner.usedProfiles(); len(got) != 2 || got[1] != "prod-sso" || idB.Pin.AWSProfile != "prod-sso" {
		t.Fatalf("prod verified with %v %+v", got, idB.Pin)
	}
	// Each context sees only its own verification; the launch project sees none.
	if o := s.discoveryOptions(a); o.Identity == nil || o.Identity.ID != idA.ID {
		t.Fatalf("%+v", o.Identity)
	}
	if o := s.discoveryOptions(c); o.Identity != nil {
		t.Fatalf("platform sees %+v", o.Identity)
	}
	if launch := decode[scopedDiscoveryOptions](t, s.do("GET", "/api/v1/discovery/options", "")); launch.Identity != nil {
		t.Fatalf("launch sees %+v", launch.Identity)
	}
	// A verification of one context cannot start a scan in another.
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/discovery/scans", fmt.Sprintf(`{"identityId":%q,%s,"acknowledgeApiCalls":true,"contextFingerprint":%q}`, idA.ID, discoveryScope, ob.ContextFingerprint)), 409, "identity_required")
	wantError(t, s.do("POST", "/api/v1/discovery/scans", fmt.Sprintf(`{"identityId":%q,%s,"acknowledgeApiCalls":true}`, idA.ID, discoveryScope)), 409, "identity_required")
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/discovery/scans", fmt.Sprintf(`{"identityId":%q,%s}`, idA.ID, discoveryScope)), 400, "acknowledgement_required")

	w = s.do("POST", "/api/v1/contexts/"+a+"/discovery/scans", fmt.Sprintf(`{"identityId":%q,%s,"acknowledgeApiCalls":true,"contextFingerprint":""}`, idA.ID, discoveryScope))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	scan := s.waitDiscovery(a, decode[discovery.Scan](t, w).ID)
	if !scan.Current || scan.Status != discovery.ScanCompleted || scan.Pin.Context != a {
		t.Fatalf("%+v", scan.Scan)
	}
	var cand discovery.CandidateView
	for _, r := range scan.Results {
		if r.Match.Status == discovery.MatchPossiblyUntracked && r.Type == "aws_s3_bucket" {
			cand = r
		}
	}
	if cand.ID == "" {
		t.Fatalf("no importable bucket: %+v", scan.Results)
	}

	// The scan does not exist in the other workspace of the same project, in another project
	// or in the launch project, and cannot be cancelled or prepared there.
	for _, base := range []string{"/api/v1/contexts/" + b, "/api/v1/contexts/" + c, "/api/v1"} {
		if w := s.do("GET", base+"/discovery/scans", ""); strings.Contains(w.Body.String(), scan.ID) {
			t.Fatalf("%s lists the scan: %s", base, w.Body)
		}
		wantError(t, s.do("GET", base+"/discovery/scans/"+scan.ID, ""), 404, "scan_not_found")
		wantError(t, s.do("POST", base+"/discovery/scans/"+scan.ID+"/cancel", `{}`), 404, "scan_not_found")
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/discovery/scans/"+scan.ID+"/candidates/"+cand.ID+"/prepare", fmt.Sprintf(`{"contextFingerprint":%q}`, ob.ContextFingerprint)), 404, "scan_not_found")
	wantError(t, s.do("POST", "/api/v1/discovery/scans/"+scan.ID+"/candidates/"+cand.ID+"/prepare", `{}`), 404, "scan_not_found")

	w = s.do("POST", "/api/v1/contexts/"+a+"/discovery/scans/"+scan.ID+"/candidates/"+cand.ID+"/prepare", `{"contextFingerprint":""}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	prefill := decode[discovery.Prefill](t, w)

	// The prepared import links only to an import in the same context.
	request := fmt.Sprintf(`{"address":"aws_s3_bucket.logs","id":%q,"file":"imports.tf"}`, prefill.ImportID)
	start := func(cid, fingerprint string) string {
		preview := s.previewImport(cid, request)
		return fmt.Sprintf(`{"address":"aws_s3_bucket.logs","id":%q,"file":"imports.tf","previewSha256":%q,"acknowledgeWrite":true,"acknowledgeBackendAccess":true,"contextFingerprint":%q,"discoveryToken":%q}`, prefill.ImportID, preview.SHA256, fingerprint, prefill.Token)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/imports", start(b, ob.ContextFingerprint)), 409, "discovery_stale")
	wantError(t, s.do("POST", "/api/v1/contexts/"+c+"/imports", start(c, "")), 409, "discovery_stale")
	absent(t, filepath.Join(s.roots["network"], "imports.tf"))
	absent(t, filepath.Join(s.roots["platform"], "imports.tf"))
	body := start(a, "")
	w = s.do("POST", "/api/v1/contexts/"+a+"/imports", body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.waitScoped(a, decode[scopedOp](t, w).ID)
	// A prepared import is used once.
	wantError(t, s.do("POST", "/api/v1/contexts/"+a+"/imports", body), 409, "discovery_stale")

	// Every AWS call was an allowlisted read.
	for _, args := range runner.Calls() {
		if !discovery.Allowed(args) || (args[0] != "--version" && !(strings.HasPrefix(args[1], "describe-") || strings.HasPrefix(args[1], "list-") || strings.HasPrefix(args[1], "get-"))) {
			t.Fatalf("non-read call %v", args)
		}
	}

	// Closing the scope (here: its view is reopened, which closes it; a scope that keeps the
	// import's reviewed plan is never evicted) drops its verification and scan.
	verified := s.discoveryOptions(a).Identity
	if verified == nil {
		t.Fatal("no verification")
	}
	calls := len(runner.Calls())
	s.openLoaded("network", "dev", "")
	if got := s.hub.Discovery.Scans(a); len(got) != 0 {
		t.Fatalf("scans kept after close: %+v", got)
	}
	if id, ok := s.hub.Discovery.Identity(verified.Pin); ok {
		t.Fatalf("verification kept after close: %+v", id)
	}
	if len(runner.Calls()) != calls {
		t.Fatal("closing or reopening a scope contacted AWS")
	}
}

// With Hub execution off every scoped discovery route is refused before anything runs.
func TestScopedDiscoveryNeedsTheGate(t *testing.T) {
	s, runner := newScopedDiscovery(t)
	a := s.openLoaded("network", "dev", "")
	s.hub.ScopedExecution = false
	id := strings.Repeat("0", 32)
	for _, r := range [][2]string{
		{"GET", "options"}, {"GET", "scans"}, {"GET", "scans/" + id},
		{"POST", "identity"}, {"POST", "scans"}, {"POST", "scans/" + id + "/cancel"}, {"POST", "scans/" + id + "/candidates/" + id + "/prepare"},
	} {
		body := ""
		if r[0] == "POST" {
			body = `{}`
		}
		if w := s.do(r[0], "/api/v1/contexts/"+a+"/discovery/"+r[1], body); w.Code < 400 {
			t.Errorf("%s %s: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("gate off contacted AWS: %v", calls)
	}
}
