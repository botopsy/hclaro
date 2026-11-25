package api

import (
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/discovery"
	"hclaro/internal/gcpcli"
	"hclaro/internal/gcpcli/gcpclitest"
)

func TestDiscoveryAPIGCP(t *testing.T) {
	s, awsRunner, _ := newDiscoveryServer(t)
	gcp := &gcpclitest.Runner{}
	s.svc.Discovery.AddProvider(discovery.GCP(gcp))
	s.svc.Discovery.Detect = func(cli string) bool { return cli == "gcloud" }
	bodies := []string{}
	do := func(method, path, body string) (int, string) {
		w := s.do(method, path, body)
		bodies = append(bodies, w.Body.String())
		return w.Code, w.Body.String()
	}
	type options struct {
		Provider  string `json:"provider"`
		Providers []struct {
			Name     string `json:"name"`
			Detected bool   `json:"detected"`
		} `json:"providers"`
		Types         []discovery.ResourceType `json:"types"`
		Regions       []string                 `json:"regions"`
		DefaultRegion string                   `json:"defaultRegion"`
	}
	var o options
	code, body := do("GET", "/api/v1/discovery/options?provider=gcp", "")
	o = decode[options](t, s.do("GET", "/api/v1/discovery/options?provider=gcp", ""))
	if code != 200 || o.Provider != "gcp" || len(o.Providers) != 2 || o.DefaultRegion != "us-central1" || len(o.Types) != 7 || !contains(o.Regions, "europe-west1") {
		t.Fatalf("%d %s", code, body)
	}
	for _, typ := range o.Types {
		if typ.Provider != "gcp" || !strings.HasPrefix(typ.Type, "google_") {
			t.Errorf("non-GCP type %s", typ.Type)
		}
	}
	for _, p := range o.Providers {
		if p.Name == "gcp" && !p.Detected {
			t.Error("gcloud on PATH was not detected")
		}
	}
	if code, _ := do("GET", "/api/v1/discovery/options?provider=oracle", ""); code != 400 {
		t.Fatalf("unknown provider: %d", code)
	}
	if aws := decode[options](t, s.do("GET", "/api/v1/discovery/options", "")); aws.Provider != "aws" || len(aws.Types) != len(typesOf("aws")) {
		t.Fatalf("default is AWS: %+v", aws)
	}
	if len(gcp.Calls()) != 0 || len(awsRunner.Calls()) != 0 {
		t.Fatal("browsing contacted a cloud")
	}
	// Verify, scan, read.
	if code, _ := do("POST", "/api/v1/discovery/identity", `{"provider":"oracle"}`); code != 400 {
		t.Fatalf("identity for an unknown provider: %d", code)
	}
	code, body = do("POST", "/api/v1/discovery/identity", `{"provider":"gcp"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	id := decode[discovery.CloudIdentity](t, s.do("POST", "/api/v1/discovery/identity", `{"provider":"gcp"}`))
	if id.Provider != "gcp" || id.Account != "hclaro-demo-1" || id.Pin.Provider != "gcp" {
		t.Fatalf("%+v", id)
	}
	scope := `"scope":{"regions":["europe-west1"],"types":["google_compute_instance","google_storage_bucket","google_compute_network"]}`
	// AWS types with a GCP identity, or GCP types with the AWS provider, are an invalid scope.
	for name, req := range map[string]string{
		"aws types":      `{"provider":"gcp","identityId":"` + id.ID + `","scope":{"regions":["europe-west1"],"types":["aws_vpc"]},"acknowledgeApiCalls":true}`,
		"gcp for aws":    `{"identityId":"` + id.ID + `",` + scope + `,"acknowledgeApiCalls":true}`,
		"aws region":     `{"provider":"gcp","identityId":"` + id.ID + `","scope":{"regions":["eu-west-1"],"types":["google_compute_network"]},"acknowledgeApiCalls":true}`,
		"no ack":         `{"provider":"gcp","identityId":"` + id.ID + `",` + scope + `}`,
		"unknown prov":   `{"provider":"oracle","identityId":"` + id.ID + `",` + scope + `,"acknowledgeApiCalls":true}`,
		"gcloud command": `{"provider":"gcp","identityId":"` + id.ID + `",` + scope + `,"acknowledgeApiCalls":true,"command":"auth print-access-token"}`,
	} {
		if code, body := do("POST", "/api/v1/discovery/scans", req); code != 400 && code != 409 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	code, body = do("POST", "/api/v1/discovery/scans", `{"provider":"gcp","identityId":"`+id.ID+`",`+scope+`,"acknowledgeApiCalls":true}`)
	if code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	started := decode[discovery.Scan](t, s.do("POST", "/api/v1/discovery/scans", `{"provider":"gcp","identityId":"`+id.ID+`",`+scope+`,"acknowledgeApiCalls":true}`))
	var v apiScan
	for i := 0; i < 500; i++ {
		_, body := do("GET", "/api/v1/discovery/scans/"+started.ID+"?provider=gcp", "")
		v = decode[apiScan](t, s.do("GET", "/api/v1/discovery/scans/"+started.ID+"?provider=gcp", ""))
		_ = body
		if v.Status != discovery.ScanRunning {
			break
		}
	}
	if v.Status != discovery.ScanCompleted || !v.Current {
		t.Fatalf("%s %+v", v.Status, v.Tasks)
	}
	// Without the provider the scan belongs to another context: withheld.
	if view := decode[apiScan](t, s.do("GET", "/api/v1/discovery/scans/"+started.ID, "")); view.Current || !view.Withheld || len(view.Results) != 0 {
		t.Fatalf("a GCP scan was readable as AWS: %+v", view.StaleReason)
	}
	vm := candidate(t, v, "google_compute_instance", "web-1")
	if vm.ImportID != "projects/hclaro-demo-1/zones/europe-west1-b/instances/web-1" || vm.CloudID == "" || vm.ARN != "" {
		t.Fatalf("%+v", vm.CloudResourceCandidate)
	}
	prep := "/api/v1/discovery/scans/" + v.ID + "/candidates/" + vm.ID + "/prepare"
	if code, _ := do("POST", prep, `{}`); code != 409 {
		t.Fatalf("prepare as AWS: %d", code)
	}
	code, body = do("POST", prep, `{"provider":"gcp"}`)
	if code != 200 || !strings.Contains(body, `"importId":"projects/hclaro-demo-1/zones/europe-west1-b/instances/web-1"`) {
		t.Fatalf("%d %s", code, body)
	}
	// An expired login is explained in gcloud's words, without CLI output.
	gcp.Fail = func([]string) error { return &awscli.CallError{Kind: awscli.KindExpired} }
	if code, body := do("POST", "/api/v1/discovery/identity", `{"provider":"gcp"}`); code != 502 || !strings.Contains(body, "gcloud auth login") || strings.Contains(body, "aws sso") {
		t.Fatalf("%d %s", code, body)
	}
	gcp.Fail = nil
	if len(awsRunner.Calls()) != 0 {
		t.Fatalf("a GCP request ran the AWS CLI: %v", awsRunner.Calls())
	}
	for _, args := range gcp.Calls() {
		if !gcpcli.Allowed(args) {
			t.Fatalf("not allowlisted: %v", args)
		}
	}
	for _, b := range bodies {
		for _, leak := range []string{"DO_NOT_LEAK", "AKIA", "alice@", "startup-script", "0.0.0.0/0"} {
			if strings.Contains(b, leak) {
				t.Fatalf("response leaks %q: %s", leak, b)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
