package api

import (
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
	"hclaro/internal/azcli/azclitest"
	"hclaro/internal/discovery"
)

func TestDiscoveryAPIAzure(t *testing.T) {
	s, awsRunner, _ := newDiscoveryServer(t)
	az := &azclitest.Runner{}
	s.svc.Discovery.AddProvider(discovery.Azure(az))
	s.svc.Discovery.Detect = func(cli string) bool { return cli == "az" }
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
	code, body := do("GET", "/api/v1/discovery/options?provider=azure", "")
	o := decode[options](t, s.do("GET", "/api/v1/discovery/options?provider=azure", ""))
	if code != 200 || o.Provider != "azure" || len(o.Providers) != 2 || o.DefaultRegion != "eastus" || len(o.Types) != 8 || !contains(o.Regions, "westeurope") {
		t.Fatalf("%d %s", code, body)
	}
	for _, typ := range o.Types {
		if typ.Provider != "azure" || !strings.HasPrefix(typ.Type, "azurerm_") {
			t.Errorf("non-Azure type %s", typ.Type)
		}
	}
	for _, p := range o.Providers {
		if p.Name == "azure" && !p.Detected {
			t.Error("az on PATH was not detected")
		}
	}
	if len(az.Calls()) != 0 || len(awsRunner.Calls()) != 0 {
		t.Fatal("browsing contacted a cloud")
	}
	code, body = do("POST", "/api/v1/discovery/identity", `{"provider":"azure"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	id := decode[discovery.CloudIdentity](t, s.do("POST", "/api/v1/discovery/identity", `{"provider":"azure"}`))
	if id.Provider != "azure" || id.Account != azclitest.Subscription || id.Tenant == "" || id.Pin.Provider != "azure" {
		t.Fatalf("%+v", id)
	}
	scope := `"scope":{"regions":["westeurope"],"types":["azurerm_linux_virtual_machine","azurerm_resource_group"]}`
	for name, req := range map[string]string{
		"aws types":     `{"provider":"azure","identityId":"` + id.ID + `","scope":{"regions":["westeurope"],"types":["aws_vpc"]},"acknowledgeApiCalls":true}`,
		"azure for aws": `{"identityId":"` + id.ID + `",` + scope + `,"acknowledgeApiCalls":true}`,
		"aws region":    `{"provider":"azure","identityId":"` + id.ID + `","scope":{"regions":["eu-west-1"],"types":["azurerm_resource_group"]},"acknowledgeApiCalls":true}`,
		"no ack":        `{"provider":"azure","identityId":"` + id.ID + `",` + scope + `}`,
		"az command":    `{"provider":"azure","identityId":"` + id.ID + `",` + scope + `,"acknowledgeApiCalls":true,"command":"account get-access-token"}`,
	} {
		if code, body := do("POST", "/api/v1/discovery/scans", req); code != 400 && code != 409 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	code, body = do("POST", "/api/v1/discovery/scans", `{"provider":"azure","identityId":"`+id.ID+`",`+scope+`,"acknowledgeApiCalls":true}`)
	if code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	started := decode[discovery.Scan](t, s.do("POST", "/api/v1/discovery/scans", `{"provider":"azure","identityId":"`+id.ID+`",`+scope+`,"acknowledgeApiCalls":true}`))
	var v apiScan
	for i := 0; i < 500; i++ {
		v = decode[apiScan](t, s.do("GET", "/api/v1/discovery/scans/"+started.ID+"?provider=azure", ""))
		if v.Status != discovery.ScanRunning {
			break
		}
	}
	if v.Status != discovery.ScanCompleted || !v.Current {
		t.Fatalf("%s %+v", v.Status, v.Tasks)
	}
	// Without the provider the scan belongs to another context: withheld.
	if view := decode[apiScan](t, s.do("GET", "/api/v1/discovery/scans/"+started.ID, "")); view.Current || !view.Withheld || len(view.Results) != 0 {
		t.Fatalf("an Azure scan was readable as AWS: %+v", view.StaleReason)
	}
	vm := candidate(t, v, "azurerm_linux_virtual_machine", "rg-prod/web-1")
	if !strings.HasSuffix(vm.ImportID, "/providers/Microsoft.Compute/virtualMachines/web-1") || vm.CloudID != vm.ImportID || vm.ARN != "" {
		t.Fatalf("%+v", vm.CloudResourceCandidate)
	}
	prep := "/api/v1/discovery/scans/" + v.ID + "/candidates/" + vm.ID + "/prepare"
	if code, _ := do("POST", prep, `{}`); code != 409 {
		t.Fatalf("prepare as AWS: %d", code)
	}
	if code, body = do("POST", prep, `{"provider":"azure"}`); code != 200 || !strings.Contains(body, `"importId":"`+vm.ImportID+`"`) {
		t.Fatalf("%d %s", code, body)
	}
	// Failures are explained in az's words, without CLI output.
	az.Fail = func([]string) error { return &awscli.CallError{Kind: awscli.KindExpired} }
	if code, body := do("POST", "/api/v1/discovery/identity", `{"provider":"azure"}`); code != 502 || !strings.Contains(body, "az login") || strings.Contains(body, "aws sso") {
		t.Fatalf("%d %s", code, body)
	}
	az.Fail = nil
	az.Override = map[string][]byte{"account show": []byte(`{"id":"` + azclitest.Subscription + `","tenantId":"22222222-2222-2222-2222-222222222222","user":"a@b.c","userType":"user","cloud":"AzureUSGovernment","state":"Enabled"}`)}
	if code, body := do("POST", "/api/v1/discovery/identity", `{"provider":"azure"}`); code != 409 || !strings.Contains(body, azcli.KindUnsupportedCloud) {
		t.Fatalf("sovereign cloud: %d %s", code, body)
	}
	if len(awsRunner.Calls()) != 0 {
		t.Fatalf("an Azure request ran the AWS CLI: %v", awsRunner.Calls())
	}
	for _, args := range az.Calls() {
		if !azcli.Allowed(args) {
			t.Fatalf("not allowlisted: %v", args)
		}
	}
	for _, b := range bodies {
		for _, leak := range []string{"DO_NOT_LEAK", "AKIA", "alice@", "adminPassword", "0.0.0.0/0", "203.0.113"} {
			if strings.Contains(b, leak) {
				t.Fatalf("response leaks %q: %s", leak, b)
			}
		}
	}
}
