package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/gcpcli"
	"hclaro/internal/gcpcli/gcpclitest"
	"hclaro/internal/imports"
)

const project = "hclaro-demo-1"

func gcpPin() Pin {
	return Pin{Provider: "gcp", ContextFingerprint: "inherited", Workspace: "default", Engine: "terraform"}
}

func gcpService(r *gcpclitest.Runner) *Service {
	s := newService(&fakeRunner{})
	s.AddProvider(GCP(r))
	return s
}

func gcpTypes() []string {
	out := []string{}
	for _, t := range Types {
		if t.Provider == "gcp" {
			out = append(out, t.Type)
		}
	}
	return out
}

func gcpScan(t *testing.T, s *Service, regions ...string) ScanView {
	t.Helper()
	id := verify(t, s, gcpPin())
	scan, err := s.Start(gcpPin(), nil, id.ID, DiscoveryScope{Regions: regions, Types: gcpTypes()})
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, s, scan.ID, gcpPin(), snapshot())
}

func TestGCPVerifyKeepsOnlySafeIdentity(t *testing.T) {
	s := gcpService(&gcpclitest.Runner{})
	id := verify(t, s, gcpPin())
	if id.Provider != "gcp" || id.Account != project || id.PrincipalType != "user" || id.Region != "europe-west1" || id.RegionSource != "gcloud" || id.CLIVersion != "480.0.0" {
		t.Fatalf("identity = %+v", id)
	}
	if strings.Contains(id.ARN, "alice") || id.ARN != "a…@example.com" {
		t.Fatalf("the person's address must be masked: %q", id.ARN)
	}
	if got, ok := s.Identity(gcpPin()); !ok || got.ID != id.ID {
		t.Fatal("verification not found for the pin it was made for")
	}
	// The same context for AWS, or another project selection, never shares the verification.
	if _, ok := s.Identity(pin()); ok {
		t.Fatal("AWS pin found a GCP verification")
	}
	other := gcpPin()
	other.GCPProject = "other-project-1"
	if _, ok := s.Identity(other); ok {
		t.Fatal("another GCP project selection shares a verification")
	}
	other = gcpPin()
	other.GCPRegion = "us-central1"
	if r := verify(t, s, other); r.Region != "us-central1" || r.RegionSource != "profile" {
		t.Fatalf("profile region: %+v", r)
	}
}

func TestGCPVerifyFailuresUseGCloudWording(t *testing.T) {
	cases := map[string]struct {
		fail func([]string) error
		kind string
	}{
		"old sdk":   {func(a []string) error { return nil }, awscli.KindUnsupportedCLI},
		"expired":   {func(a []string) error { return &awscli.CallError{Kind: awscli.KindExpired} }, awscli.KindExpired},
		"no gcloud": {func(a []string) error { return &awscli.CallError{Kind: awscli.KindMissingCLI} }, awscli.KindMissingCLI},
	}
	for name, c := range cases {
		r := &gcpclitest.Runner{Fail: c.fail}
		if name == "old sdk" {
			r.Fail = nil
			s := newService(&fakeRunner{})
			s.AddProvider(GCP(oldSDK{}))
			_, err := s.Verify(context.Background(), gcpPin(), nil)
			if awscli.KindOf(err) != c.kind {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		_, err := gcpService(r).Verify(context.Background(), gcpPin(), nil)
		if awscli.KindOf(err) != c.kind {
			t.Errorf("%s: %v", name, err)
		}
		if m := Message("gcp", awscli.KindOf(err)); strings.Contains(m, "AWS") || !strings.Contains(m, "gcloud") && !strings.Contains(m, "Google") {
			t.Errorf("%s: message %q", name, m)
		}
	}
}

type oldSDK struct{}

func (oldSDK) Run(context.Context, []string, []string) ([]byte, error) {
	return []byte("Google Cloud SDK 120.0.0\n"), nil
}

func TestGCPVerifyNeedsProjectAndActiveProject(t *testing.T) {
	for name, cfg := range map[string]string{
		"no project":  `{"core":{"account":"alice@example.com"}}`,
		"no account":  `{"core":{"project":"hclaro-demo-1"}}`,
		"bad project": `{"core":{"account":"alice@example.com","project":"Bad Project; x"}}`,
	} {
		r := &gcpclitest.Runner{Override: map[string][]byte{"config list": []byte(cfg), "auth list": []byte(`[]`)}}
		if _, err := gcpService(r).Verify(context.Background(), gcpPin(), nil); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	r := &gcpclitest.Runner{Override: map[string][]byte{"projects describe": []byte(`{"projectId":"hclaro-demo-1","lifecycleState":"DELETE_REQUESTED"}`)}}
	if _, err := gcpService(r).Verify(context.Background(), gcpPin(), nil); awscli.KindOf(err) != awscli.KindAccessDenied {
		t.Fatalf("deleted project: %v", err)
	}
	// With no account in the configuration the active account of `auth list` is used.
	r = &gcpclitest.Runner{Override: map[string][]byte{"config list": []byte(`{"core":{"project":"hclaro-demo-1"}}`)}}
	if id := verify(t, gcpService(r), gcpPin()); id.PrincipalType != "user" {
		t.Fatalf("%+v", id)
	}
	r = &gcpclitest.Runner{Override: map[string][]byte{"config list": []byte(`{"core":{"project":"hclaro-demo-1","account":"deploy@hclaro-demo-1.iam.gserviceaccount.com"}}`)}}
	if id := verify(t, gcpService(r), gcpPin()); id.PrincipalType != "service-account" || id.ARN != "deploy@hclaro-demo-1.iam.gserviceaccount.com" {
		t.Fatalf("%+v", id)
	}
}

func TestGCPScanCandidatesImportIDsAndRedaction(t *testing.T) {
	r := &gcpclitest.Runner{}
	v := gcpScan(t, gcpService(r), "europe-west1", "us-central1")
	if v.Status != ScanCompleted {
		t.Fatalf("%s %v", v.Status, v.Tasks)
	}
	if len(v.Tasks) != 7 || v.Tasks[0].Region != "global" {
		t.Fatalf("one task per type expected: %+v", v.Tasks)
	}
	want := map[string]string{
		"google_compute_network:prod-vpc":          "projects/hclaro-demo-1/global/networks/prod-vpc",
		"google_compute_subnetwork:prod-euw1":      "projects/hclaro-demo-1/regions/europe-west1/subnetworks/prod-euw1",
		"google_compute_firewall:allow-ssh":        "projects/hclaro-demo-1/global/firewalls/allow-ssh",
		"google_compute_instance:web-1":            "projects/hclaro-demo-1/zones/europe-west1-b/instances/web-1",
		"google_compute_disk:web-1":                "projects/hclaro-demo-1/zones/europe-west1-b/disks/web-1",
		"google_storage_bucket:hclaro-demo-assets": "hclaro-demo-assets",
		"google_sql_database_instance:orders-db":   "projects/hclaro-demo-1/instances/orders-db",
	}
	for key, importID := range want {
		typ, id, _ := strings.Cut(key, ":")
		c := find(v, typ, id)
		switch {
		case c == nil:
			t.Errorf("%s missing", key)
		case c.ImportID != importID || c.ImportStatus != ImportIDVerified:
			t.Errorf("%s: import %q (%s)", key, c.ImportID, c.ImportStatus)
		case c.Account != project || c.ARN != "" || c.CloudID == "":
			t.Errorf("%s: account %q arn %q cloudId %q", key, c.Account, c.ARN, c.CloudID)
		}
	}
	// Invalid names have no import ID; regional disks are another Terraform type.
	if c := find(v, "google_compute_network", "Bad_Name"); c == nil || c.ImportStatus != ImportIDUnresolved || c.CloudID != "" {
		t.Errorf("invalid network name: %+v", c)
	}
	if c := find(v, "google_storage_bucket", "Bad..Bucket"); c == nil || c.ImportStatus != ImportIDUnresolved {
		t.Errorf("invalid bucket name: %+v", c)
	}
	if find(v, "google_compute_disk", "regional-1") != nil {
		t.Error("a regional disk is not a google_compute_disk")
	}
	// Region and details.
	if c := find(v, "google_compute_instance", "batch-1"); c == nil || c.Region != "us-central1" || c.Details["machineType"] != "n2-standard-4" || c.Details["zone"] != "us-central1-a" {
		t.Errorf("batch-1: %+v", c)
	}
	if c := find(v, "google_compute_network", "prod-vpc"); c.Region != "global" || c.Details["routingMode"] != "GLOBAL" {
		t.Errorf("prod-vpc: %+v", c)
	}
	if c := find(v, "google_storage_bucket", "hclaro-demo-assets"); c.Region != "europe-west1" || c.CloudID != "projects/_/buckets/hclaro-demo-assets" {
		t.Errorf("bucket: %+v", c)
	}
	// Labels: credential-shaped and risky values are replaced; nothing from fields outside the
	// projection (descriptions, metadata, rules, passwords) is shown.
	web := find(v, "google_compute_instance", "web-1")
	labels := map[string]string{}
	for _, tag := range web.Tags {
		labels[tag.Key] = tag.Value
	}
	if labels["env"] != "prod" || labels["api_token"] != "[redacted]" || strings.Contains(labels["owner"], "DO_NOT_LEAK") {
		t.Errorf("labels = %v", labels)
	}
	b, _ := json.Marshal(v)
	for _, leak := range []string{"DO_NOT_LEAK", "AKIA", "203.0.113", "0.0.0.0/0", "startup-script", "rootPassword"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("scan JSON contains %q", leak)
		}
	}
}

func TestGCPRegionSelectionFiltersRegionalObjects(t *testing.T) {
	v := gcpScan(t, gcpService(&gcpclitest.Runner{}), "us-central1")
	if find(v, "google_compute_instance", "web-1") != nil || find(v, "google_sql_database_instance", "orders-db") != nil || find(v, "google_compute_subnetwork", "prod-euw1") != nil || find(v, "google_storage_bucket", "hclaro-demo-assets") != nil {
		t.Error("objects of an unselected region were kept")
	}
	if find(v, "google_compute_instance", "batch-1") == nil || find(v, "google_compute_network", "prod-vpc") == nil || find(v, "google_storage_bucket", "hclaro-demo-logs") == nil {
		t.Error("selected-region, global and multi-region objects must be kept")
	}
}

func TestGCPOnlyListedReadOnlyCallsAreMade(t *testing.T) {
	r := &gcpclitest.Runner{}
	gcpScan(t, gcpService(r), "europe-west1")
	seen := map[string]bool{}
	for _, args := range r.Calls() {
		if !gcpcli.Allowed(args) {
			t.Errorf("not allowlisted: %v", args)
		}
		for _, bad := range []string{"token", "--project", "--impersonate"} {
			if strings.Contains(strings.Join(args, " "), bad) {
				t.Errorf("%v mentions %s", args, bad)
			}
		}
		seen[strings.Join(args[:min(2, len(args))], " ")] = true
	}
	for _, want := range []string{"--version", "config list", "projects describe", "compute networks", "compute firewall-rules", "compute instances", "compute disks", "storage buckets", "sql instances"} {
		found := false
		for k := range seen {
			found = found || strings.HasPrefix(k, want)
		}
		if !found {
			t.Errorf("no call for %s: %v", want, seen)
		}
	}
}

func TestGCPFailuresArePerTypeAndUseGCloudWording(t *testing.T) {
	r := &gcpclitest.Runner{Fail: func(args []string) error {
		if args[0] == "sql" {
			return &awscli.CallError{Kind: awscli.KindAccessDenied}
		}
		return nil
	}}
	v := gcpScan(t, gcpService(r), "europe-west1")
	if v.Status != ScanPartial {
		t.Fatalf("status %s", v.Status)
	}
	for _, task := range v.Tasks {
		if task.Type == "google_sql_database_instance" {
			if task.Status != TaskFailed || task.Error != awscli.KindAccessDenied || strings.Contains(task.Message, "AWS") || !strings.Contains(task.Message, "Google Cloud") {
				t.Errorf("sql task: %+v", task)
			}
		} else if task.Status != TaskDone {
			t.Errorf("%s: %s", task.Type, task.Status)
		}
	}
	// An expired login stops the whole scan.
	r = &gcpclitest.Runner{Fail: func(args []string) error {
		if len(args) > 2 && args[len(args)-1] == "--quiet" {
			return &awscli.CallError{Kind: awscli.KindExpired}
		}
		return nil
	}}
	if v := gcpScan(t, gcpService(r), "europe-west1"); v.Status != ScanFailed || v.Error != awscli.KindExpired || !strings.Contains(v.Message, "gcloud auth login") {
		t.Fatalf("expired: %s %s %q", v.Status, v.Error, v.Message)
	}
}

func TestGCPProjectChangeBetweenVerifyAndScanStopsIt(t *testing.T) {
	r := &gcpclitest.Runner{}
	s := gcpService(r)
	id := verify(t, s, gcpPin())
	r.SetProject("other-project-1")
	scan, err := s.Start(gcpPin(), nil, id.ID, DiscoveryScope{Regions: []string{"europe-west1"}, Types: gcpTypes()})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, scan.ID, gcpPin(), snapshot())
	if v.Status != ScanFailed || v.Error != awscli.KindIdentityChanged || strings.Contains(v.Message, "AWS") {
		t.Fatalf("%s %s %q", v.Status, v.Error, v.Message)
	}
	for _, args := range r.Calls() {
		if strings.HasSuffix(args[len(args)-1], "--quiet") && args[0] != "config" {
			t.Fatalf("listed after the project changed: %v", args)
		}
	}
}

func TestGCPTruncatedListIsMarkedPartial(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 301; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"vm-` + itoa(i+1) + `","zone":"projects/p/zones/europe-west1-b","machineType":"x/e2-small","status":"RUNNING"}`)
	}
	sb.WriteString("]")
	r := &gcpclitest.Runner{Override: map[string][]byte{"compute instances list": []byte(sb.String())}}
	s := gcpService(r)
	id := verify(t, s, gcpPin())
	scan, err := s.Start(gcpPin(), nil, id.ID, DiscoveryScope{Regions: []string{"europe-west1"}, Types: []string{"google_compute_instance"}, MaxPerType: 100})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, scan.ID, gcpPin(), snapshot())
	if v.Status != ScanPartial || !v.Tasks[0].Truncated || v.Tasks[0].Count != 100 || len(v.Results) != 100 {
		t.Fatalf("%s %+v results=%d", v.Status, v.Tasks[0], len(v.Results))
	}
}

func TestGCPMatchIsExactAgainstState(t *testing.T) {
	tracked := res("google_compute_instance", "google_compute_instance.web", map[string]any{"id": "projects/hclaro-demo-1/zones/europe-west1-b/instances/web-1"})
	otherProject := res("google_compute_network", "google_compute_network.prod", map[string]any{"id": "projects/other-project-1/global/networks/prod-vpc"})
	bareBucket := res("google_storage_bucket", "google_storage_bucket.assets", map[string]any{"id": "hclaro-demo-assets", "name": "hclaro-demo-assets"})
	// An older provider recorded the bare name as the SQL instance ID: not comparable.
	oldSQL := res("google_sql_database_instance", "google_sql_database_instance.db", map[string]any{"id": "orders-db"})
	s := gcpService(&gcpclitest.Runner{})
	id := verify(t, s, gcpPin())
	scan, _ := s.Start(gcpPin(), nil, id.ID, DiscoveryScope{Regions: []string{"europe-west1", "us-central1"}, Types: gcpTypes()})
	snap := snapshot(tracked, otherProject, bareBucket, oldSQL)
	v := wait(t, s, scan.ID, gcpPin(), snap)
	for key, want := range map[string]string{
		"google_compute_instance:web-1":            MatchTracked,
		"google_compute_instance:batch-1":          MatchPossiblyUntracked,
		"google_compute_network:prod-vpc":          MatchPossiblyUntracked,
		"google_storage_bucket:hclaro-demo-assets": MatchTracked,
		"google_sql_database_instance:orders-db":   MatchUnknown,
	} {
		typ, rid, _ := strings.Cut(key, ":")
		if c := find(v, typ, rid); c == nil || c.Match.Status != want {
			t.Errorf("%s: %+v, want %s", key, c, want)
		}
	}
}

func TestGCPPrepareChecksProjectAndWarns(t *testing.T) {
	r := &gcpclitest.Runner{}
	s := gcpService(r)
	v := gcpScan(t, s, "europe-west1")
	c := find(v, "google_compute_instance", "web-1")
	p, err := s.Prepare(context.Background(), v.ID, c.ID, gcpPin(), nil, snapshot(), false)
	if err != nil {
		t.Fatal(err)
	}
	if p.ImportID != "projects/hclaro-demo-1/zones/europe-west1-b/instances/web-1" || p.Account != project || !strings.Contains(strings.Join(p.Warnings, " "), "project hclaro-demo-1") || strings.Contains(strings.Join(p.Warnings, " "), "aliased") {
		t.Fatalf("%+v", p)
	}
	// A prepared GCP import is bound to the GCP pin only.
	other := gcpPin()
	other.GCPProject = "other-project-1"
	if _, err := s.Prepare(context.Background(), v.ID, c.ID, other, nil, snapshot(), false); !errors.Is(err, ErrScanStale) {
		t.Fatalf("another project selection: %v", err)
	}
	if _, err := s.Prepare(context.Background(), v.ID, c.ID, pin(), nil, snapshot(), false); !errors.Is(err, ErrScanStale) {
		t.Fatalf("AWS pin: %v", err)
	}
	r.SetProject("other-project-1")
	_, err = s.Prepare(context.Background(), v.ID, c.ID, gcpPin(), nil, snapshot(), false)
	if awscli.KindOf(err) != awscli.KindIdentityChanged {
		t.Fatalf("project changed before prepare: %v", err)
	}
}

func TestScopeValidationIsPerProvider(t *testing.T) {
	gcp := DiscoveryScope{Regions: []string{"europe-west1"}, Types: []string{"google_compute_network"}}
	if err := gcp.ValidateFor("gcp"); err != nil {
		t.Fatal(err)
	}
	aws := DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}}
	if aws.ValidateFor("gcp") == nil || gcp.ValidateFor("") == nil {
		t.Error("a scope of one provider validated for the other")
	}
	mixed := DiscoveryScope{Regions: []string{"europe-west1"}, Types: []string{"google_compute_network", "aws_vpc"}}
	if mixed.ValidateFor("gcp") == nil {
		t.Error("mixed providers validated")
	}
	bad := DiscoveryScope{Regions: []string{"europe-west1; rm"}, Types: []string{"google_compute_network"}}
	if bad.ValidateFor("gcp") == nil {
		t.Error("bad region validated")
	}
}

func TestGCPListArgsAreAllowlisted(t *testing.T) {
	for _, name := range gcpTypes() {
		typ, _ := TypeByName(name)
		if args := (gcpProvider{}).ListArgs(typ, "", 100, ""); !gcpcli.Allowed(args) {
			t.Errorf("%s: %v", name, args)
		}
	}
}

func TestGCPPreparedImportChecksWithoutNamingTheProvider(t *testing.T) {
	s := gcpService(&gcpclitest.Runner{})
	v := gcpScan(t, s, "europe-west1")
	c := find(v, "google_compute_instance", "web-1")
	p, err := s.Prepare(context.Background(), v.ID, c.ID, gcpPin(), nil, snapshot(), false)
	if err != nil {
		t.Fatal(err)
	}
	// The import route builds its pin from the context alone.
	ctxPin := gcpPin()
	ctxPin.Provider = ""
	req := imports.Request{Address: p.SuggestedAddress, ID: p.ImportID}
	if err := s.CheckImport(p.Token, ctxPin, req); err != nil {
		t.Fatalf("prepared GCP import refused: %v", err)
	}
	ctxPin.GCPProject = "other-project-1"
	if err := s.CheckImport(p.Token, ctxPin, req); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("another project: %v", err)
	}
	req.Address = "google_compute_disk.web"
	ctxPin.GCPProject = ""
	if err := s.CheckImport(p.Token, ctxPin, req); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("another type: %v", err)
	}
}
