package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
	"hclaro/internal/azcli/azclitest"
	"hclaro/internal/imports"
)

const subscription = azclitest.Subscription

func azurePin() Pin {
	return Pin{Provider: "azure", ContextFingerprint: "inherited", Workspace: "default", Engine: "terraform"}
}

func azureService(r *azclitest.Runner) *Service {
	s := newService(&fakeRunner{})
	s.AddProvider(Azure(r))
	return s
}

func azureTypes() []string {
	out := []string{}
	for _, t := range Types {
		if t.Provider == "azure" {
			out = append(out, t.Type)
		}
	}
	return out
}

func azureScan(t *testing.T, s *Service, regions ...string) ScanView {
	t.Helper()
	id := verify(t, s, azurePin())
	scan, err := s.Start(azurePin(), nil, id.ID, DiscoveryScope{Regions: regions, Types: azureTypes()})
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, s, scan.ID, azurePin(), snapshot())
}

func TestAzureVerifyKeepsOnlySafeIdentity(t *testing.T) {
	s := azureService(&azclitest.Runner{})
	id := verify(t, s, azurePin())
	if id.Provider != "azure" || id.Account != subscription || id.Tenant != "22222222-2222-2222-2222-222222222222" || id.PrincipalType != "user" || id.Region != "eastus" || id.RegionSource != "default" || id.CLIVersion != "2.60.0" {
		t.Fatalf("identity = %+v", id)
	}
	if strings.Contains(id.ARN, "alice") || id.ARN != "a…@example.com" {
		t.Fatalf("the person's address must be masked: %q", id.ARN)
	}
	if got, ok := s.Identity(azurePin()); !ok || got.ID != id.ID {
		t.Fatal("verification not found for the pin it was made for")
	}
	// The same context for AWS, or another subscription selection, never shares the verification.
	if _, ok := s.Identity(pin()); ok {
		t.Fatal("AWS pin found an Azure verification")
	}
	other := azurePin()
	other.AzureSubscription = "33333333-3333-3333-3333-333333333333"
	if _, ok := s.Identity(other); ok {
		t.Fatal("another subscription selection shares a verification")
	}
	other = azurePin()
	other.AzureLocation = "westeurope"
	if r := verify(t, s, other); r.Region != "westeurope" || r.RegionSource != "profile" {
		t.Fatalf("profile location: %+v", r)
	}
	if r, err := s.Verify(context.Background(), azurePin(), []string{"AZURE_DEFAULTS_LOCATION=northeurope"}); err != nil || r.Region != "northeurope" || r.RegionSource != "azure" {
		t.Fatalf("az default location: %+v %v", r, err)
	}
}

func TestAzureVerifyChecksTheContextAgainstAz(t *testing.T) {
	s := azureService(&azclitest.Runner{})
	for name, mutate := range map[string]func(*Pin){
		"another subscription": func(p *Pin) { p.AzureSubscription = "33333333-3333-3333-3333-333333333333" },
		"another tenant":       func(p *Pin) { p.AzureTenant = "44444444-4444-4444-4444-444444444444" },
	} {
		p := azurePin()
		mutate(&p)
		if _, err := s.Verify(context.Background(), p, nil); awscli.KindOf(err) != awscli.KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The expectation matches case-insensitively.
	p := azurePin()
	p.AzureSubscription, p.AzureTenant = strings.ToUpper(subscription), "22222222-2222-2222-2222-222222222222"
	if _, err := s.Verify(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAzureVerifyFailuresUseAzWording(t *testing.T) {
	for name, kind := range map[string]string{"expired": awscli.KindExpired, "no az": awscli.KindMissingCLI, "no login": awscli.KindNoCredentials} {
		r := &azclitest.Runner{Fail: func([]string) error { return &awscli.CallError{Kind: kind} }}
		_, err := azureService(r).Verify(context.Background(), azurePin(), nil)
		if awscli.KindOf(err) != kind {
			t.Errorf("%s: %v", name, err)
		}
		if m := Message("azure", kind); strings.Contains(m, "AWS") || strings.Contains(m, "gcloud") || !strings.Contains(m, "az") && !strings.Contains(m, "Azure") {
			t.Errorf("%s: message %q", name, m)
		}
	}
	s := newService(&fakeRunner{})
	s.AddProvider(Azure(oldAz{}))
	if _, err := s.Verify(context.Background(), azurePin(), nil); awscli.KindOf(err) != awscli.KindUnsupportedCLI {
		t.Errorf("old cli: %v", err)
	}
}

type oldAz struct{}

func (oldAz) Run(context.Context, []string, []string) ([]byte, error) {
	return []byte("azure-cli                         2.10.0\n"), nil
}

func TestAzureVerifyRefusesUnusableAccounts(t *testing.T) {
	const base = `"id":"11111111-1111-1111-1111-111111111111","tenantId":"22222222-2222-2222-2222-222222222222","user":"alice@example.com","userType":"user"`
	for name, tc := range map[string]struct{ doc, kind string }{
		"sovereign cloud":   {`{` + base + `,"cloud":"AzureUSGovernment","state":"Enabled"}`, azcli.KindUnsupportedCloud},
		"disabled":          {`{` + base + `,"cloud":"AzureCloud","state":"Disabled"}`, awscli.KindAccessDenied},
		"subscription name": {`{"id":"My Subscription","tenantId":"22222222-2222-2222-2222-222222222222","user":"a@b.c","userType":"user","cloud":"AzureCloud","state":"Enabled"}`, awscli.KindMalformed},
		"not json":          {`ERROR`, awscli.KindMalformed},
		"tenant not a GUID": {`{"id":"11111111-1111-1111-1111-111111111111","tenantId":"x","user":"a@b.c","userType":"user","cloud":"AzureCloud","state":"Enabled"}`, awscli.KindMalformed},
	} {
		r := &azclitest.Runner{Override: map[string][]byte{"account show": []byte(tc.doc)}}
		if _, err := azureService(r).Verify(context.Background(), azurePin(), nil); awscli.KindOf(err) != tc.kind {
			t.Errorf("%s: %v", name, err)
		}
	}
	if m := azcli.Message(azcli.KindUnsupportedCloud); !strings.Contains(m, "AzureCloud") {
		t.Errorf("message %q", m)
	}
	// A service principal is shown whole; a managed identity or odd name is not trusted.
	sp := `{"id":"11111111-1111-1111-1111-111111111111","tenantId":"22222222-2222-2222-2222-222222222222","user":"44444444-4444-4444-4444-444444444444","userType":"servicePrincipal","cloud":"AzureCloud","state":"Enabled"}`
	r := &azclitest.Runner{Override: map[string][]byte{"account show": []byte(sp)}}
	if id := verify(t, azureService(r), azurePin()); id.PrincipalType != "service-principal" || id.ARN != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("%+v", id)
	}
	odd := strings.Replace(sp, `"user":"44444444-4444-4444-4444-444444444444"`, `"user":"AKIAIOSFODNN7EXAMPLE"`, 1)
	r = &azclitest.Runner{Override: map[string][]byte{"account show": []byte(odd)}}
	if id := verify(t, azureService(r), azurePin()); id.ARN != "Principal withheld" {
		t.Fatalf("%+v", id)
	}
}

func TestAzureScanCandidatesImportIDsAndRedaction(t *testing.T) {
	v := azureScan(t, azureService(&azclitest.Runner{}), "westeurope", "eastus")
	if v.Status != ScanCompleted {
		t.Fatalf("%s %v", v.Status, v.Tasks)
	}
	if len(v.Tasks) != 8 || v.Tasks[0].Region != "global" {
		t.Fatalf("one task per type expected: %+v", v.Tasks)
	}
	base := "/subscriptions/" + subscription + "/resourceGroups/"
	want := map[string]string{
		"azurerm_resource_group:rg-prod":                   base + "rg-prod",
		"azurerm_virtual_network:rg-prod/prod-vnet":        base + "rg-prod/providers/Microsoft.Network/virtualNetworks/prod-vnet",
		"azurerm_subnet:rg-prod/prod-vnet/app":             base + "rg-prod/providers/Microsoft.Network/virtualNetworks/prod-vnet/subnets/app",
		"azurerm_network_security_group:rg-prod/allow-ssh": base + "rg-prod/providers/Microsoft.Network/networkSecurityGroups/allow-ssh",
		"azurerm_linux_virtual_machine:rg-prod/web-1":      base + "RG-PROD/providers/Microsoft.Compute/virtualMachines/web-1",
		"azurerm_windows_virtual_machine:rg-batch/win-1":   base + "rg-batch/providers/Microsoft.Compute/virtualMachines/win-1",
		"azurerm_managed_disk:rg-prod/web-1-os":            base + "rg-prod/providers/Microsoft.Compute/disks/web-1-os",
		"azurerm_storage_account:rg-prod/hclaroassets":     base + "rg-prod/providers/Microsoft.Storage/storageAccounts/hclaroassets",
	}
	for key, importID := range want {
		typ, id, _ := strings.Cut(key, ":")
		c := find(v, typ, id)
		switch {
		case c == nil:
			t.Errorf("%s missing", key)
		case c.ImportID != importID || c.ImportStatus != ImportIDVerified:
			t.Errorf("%s: import %q (%s)", key, c.ImportID, c.ImportStatus)
		case c.Account != subscription || c.ARN != "" || c.CloudID != importID:
			t.Errorf("%s: account %q arn %q cloudId %q", key, c.Account, c.ARN, c.CloudID)
		}
	}
	// Invalid names have no import ID; a VM is split by OS and an unmanaged one fits neither.
	for typ, id := range map[string]string{"azurerm_resource_group": "bad name", "azurerm_network_security_group": "rg-prod/bad name", "azurerm_storage_account": "rg-batch/Bad_Account"} {
		if c := find(v, typ, id); c == nil || c.ImportStatus != ImportIDUnresolved || c.CloudID != "" {
			t.Errorf("%s %s: %+v", typ, id, c)
		}
	}
	if find(v, "azurerm_linux_virtual_machine", "rg-batch/win-1") != nil || find(v, "azurerm_windows_virtual_machine", "rg-prod/web-1") != nil || find(v, "azurerm_linux_virtual_machine", "rg-batch/legacy-1") != nil || find(v, "azurerm_windows_virtual_machine", "rg-batch/legacy-1") != nil {
		t.Error("virtual machines were not split by operating system")
	}
	if c := find(v, "azurerm_subnet", "rg-prod/prod-vnet/app"); c.Region != "westeurope" || c.Details["cidr"] != "10.0.1.0/24" || c.Details["virtualNetwork"] != "prod-vnet" {
		t.Errorf("subnet: %+v", c)
	}
	if c := find(v, "azurerm_linux_virtual_machine", "rg-prod/web-1"); c.Details["size"] != "Standard_D2s_v5" || c.Details["resourceGroup"] != "RG-PROD" {
		t.Errorf("web-1: %+v", c)
	}
	// Tags: credential-shaped and risky values are replaced; nothing from fields outside the
	// projection (rules, DNS servers, passwords, endpoints, managedBy) is shown.
	tags := map[string]string{}
	for _, tag := range find(v, "azurerm_resource_group", "rg-prod").Tags {
		tags[tag.Key] = tag.Value
	}
	if tags["env"] != "prod" || tags["api_token"] != "[redacted]" || strings.Contains(tags["owner"], "DO_NOT_LEAK") {
		t.Errorf("tags = %v", tags)
	}
	b, _ := json.Marshal(v)
	for _, leak := range []string{"DO_NOT_LEAK", "AKIA", "203.0.113", "0.0.0.0/0", "adminPassword", "customData", "blob.core.windows.net", "alice@"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("scan JSON contains %q", leak)
		}
	}
}

func TestAzureLocationSelectionFiltersRegionalObjects(t *testing.T) {
	v := azureScan(t, azureService(&azclitest.Runner{}), "eastus")
	if find(v, "azurerm_resource_group", "rg-prod") != nil || find(v, "azurerm_linux_virtual_machine", "rg-prod/web-1") != nil || find(v, "azurerm_subnet", "rg-prod/prod-vnet/app") != nil || find(v, "azurerm_storage_account", "rg-prod/hclaroassets") != nil {
		t.Error("objects of an unselected location were kept")
	}
	if find(v, "azurerm_resource_group", "rg-batch") == nil || find(v, "azurerm_windows_virtual_machine", "rg-batch/win-1") == nil || find(v, "azurerm_subnet", "rg-batch/batch-vnet/app") == nil {
		t.Error("objects of the selected location must be kept")
	}
}

func TestAzureOnlyListedReadOnlyCallsAreMade(t *testing.T) {
	r := &azclitest.Runner{}
	azureScan(t, azureService(r), "westeurope")
	seen := map[string]bool{}
	for _, args := range r.Calls() {
		if !azcli.Allowed(args) {
			t.Errorf("not allowlisted: %v", args)
		}
		for _, bad := range []string{"token", "--ids", "--debug", "--resource-group"} {
			if strings.Contains(strings.Join(args, " "), bad) {
				t.Errorf("%v mentions %s", args, bad)
			}
		}
		seen[strings.Join(args[:min(2, len(args))], " ")] = true
	}
	for _, want := range []string{"--version", "account show", "group list", "network vnet", "network nsg", "vm list", "disk list", "storage account"} {
		found := false
		for k := range seen {
			found = found || strings.HasPrefix(k, want)
		}
		if !found {
			t.Errorf("no call for %s: %v", want, seen)
		}
	}
}

func TestAzureFailuresArePerTypeAndUseAzWording(t *testing.T) {
	r := &azclitest.Runner{Fail: func(args []string) error {
		if args[0] == "disk" {
			return &awscli.CallError{Kind: awscli.KindAccessDenied}
		}
		return nil
	}}
	v := azureScan(t, azureService(r), "westeurope")
	if v.Status != ScanPartial {
		t.Fatalf("status %s", v.Status)
	}
	for _, task := range v.Tasks {
		if task.Type == "azurerm_managed_disk" {
			if task.Status != TaskFailed || task.Error != awscli.KindAccessDenied || strings.Contains(task.Message, "AWS") || !strings.Contains(task.Message, "Azure") {
				t.Errorf("disk task: %+v", task)
			}
		} else if task.Status != TaskDone {
			t.Errorf("%s: %s", task.Type, task.Status)
		}
	}
	// An expired login stops the whole scan.
	r = &azclitest.Runner{Fail: func(args []string) error {
		if len(args) > 1 && args[1] == "list" {
			return &awscli.CallError{Kind: awscli.KindExpired}
		}
		return nil
	}}
	if v := azureScan(t, azureService(r), "westeurope"); v.Status != ScanFailed || v.Error != awscli.KindExpired || !strings.Contains(v.Message, "az login") {
		t.Fatalf("expired: %s %s %q", v.Status, v.Error, v.Message)
	}
}

func TestAzureSubscriptionChangeBetweenVerifyAndScanStopsIt(t *testing.T) {
	r := &azclitest.Runner{}
	s := azureService(r)
	id := verify(t, s, azurePin())
	r.SetSubscription("33333333-3333-3333-3333-333333333333")
	scan, err := s.Start(azurePin(), nil, id.ID, DiscoveryScope{Regions: []string{"westeurope"}, Types: azureTypes()})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, scan.ID, azurePin(), snapshot())
	if v.Status != ScanFailed || v.Error != awscli.KindIdentityChanged || strings.Contains(v.Message, "AWS") {
		t.Fatalf("%s %s %q", v.Status, v.Error, v.Message)
	}
	for _, args := range r.Calls() {
		if args[len(args)-1] == "--only-show-errors" && args[1] == "list" {
			t.Fatalf("listed after the subscription changed: %v", args)
		}
	}
}

func TestAzureTruncatedListIsMarkedPartial(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 301; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		n := "vm-" + itoa(i+1)
		sb.WriteString(`{"id":"/subscriptions/` + subscription + `/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/` + n + `","name":"` + n + `","location":"westeurope","osType":"Linux","size":"Standard_B1s","tags":null}`)
	}
	sb.WriteString("]")
	r := &azclitest.Runner{Override: map[string][]byte{"vm list": []byte(sb.String())}}
	s := azureService(r)
	id := verify(t, s, azurePin())
	scan, err := s.Start(azurePin(), nil, id.ID, DiscoveryScope{Regions: []string{"westeurope"}, Types: []string{"azurerm_linux_virtual_machine"}, MaxPerType: 100})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, s, scan.ID, azurePin(), snapshot())
	if v.Status != ScanPartial || !v.Tasks[0].Truncated || v.Tasks[0].Count != 100 || len(v.Results) != 100 {
		t.Fatalf("%s %+v results=%d", v.Status, v.Tasks[0], len(v.Results))
	}
}

func TestAzureAnotherSubscriptionsIDIsNotImportable(t *testing.T) {
	other := `[{"id":"/subscriptions/33333333-3333-3333-3333-333333333333/resourceGroups/rg/providers/Microsoft.Compute/disks/d","name":"d","location":"westeurope","sizeGb":1,"sku":"x","state":"x","tags":null}]`
	r := &azclitest.Runner{Override: map[string][]byte{"disk list": []byte(other)}}
	s := azureService(r)
	id := verify(t, s, azurePin())
	scan, _ := s.Start(azurePin(), nil, id.ID, DiscoveryScope{Regions: []string{"westeurope"}, Types: []string{"azurerm_managed_disk"}})
	v := wait(t, s, scan.ID, azurePin(), snapshot())
	if c := find(v, "azurerm_managed_disk", "rg/d"); c == nil || c.ImportStatus != ImportIDUnresolved {
		t.Fatalf("%+v", c)
	}
}

func TestAzureMatchIsCaseInsensitiveAgainstState(t *testing.T) {
	base := "/subscriptions/" + subscription + "/resourceGroups/"
	// State and the CLI may spell the resource group in different case.
	tracked := res("azurerm_linux_virtual_machine", "azurerm_linux_virtual_machine.web", map[string]any{"id": strings.ToLower(base) + "rg-prod/providers/microsoft.compute/virtualmachines/web-1"})
	otherSub := res("azurerm_virtual_network", "azurerm_virtual_network.prod", map[string]any{"id": "/subscriptions/33333333-3333-3333-3333-333333333333/resourceGroups/rg-prod/providers/Microsoft.Network/virtualNetworks/prod-vnet"})
	// An instance whose identifier is in another format cannot be compared.
	odd := res("azurerm_storage_account", "azurerm_storage_account.assets", map[string]any{"id": "hclaroassets"})
	s := azureService(&azclitest.Runner{})
	id := verify(t, s, azurePin())
	scan, _ := s.Start(azurePin(), nil, id.ID, DiscoveryScope{Regions: []string{"westeurope", "eastus"}, Types: azureTypes()})
	v := wait(t, s, scan.ID, azurePin(), snapshot(tracked, otherSub, odd))
	for key, want := range map[string]string{
		"azurerm_linux_virtual_machine:rg-prod/web-1":  MatchTracked,
		"azurerm_virtual_network:rg-prod/prod-vnet":    MatchPossiblyUntracked,
		"azurerm_storage_account:rg-prod/hclaroassets": MatchUnknown,
		"azurerm_managed_disk:rg-prod/web-1-os":        MatchPossiblyUntracked,
	} {
		typ, rid, _ := strings.Cut(key, ":")
		if c := find(v, typ, rid); c == nil || c.Match.Status != want {
			t.Errorf("%s: %+v, want %s", key, c, want)
		}
	}
	// AWS matching stays exact: a differently cased ID is another object.
	if got := (bindingIndex{}).match(CloudResourceCandidate{Type: "aws_vpc", ImportStatus: ImportIDVerified, ImportID: "vpc-1"}); got.Status != MatchUnknown {
		t.Fatalf("%+v", got)
	}
}

func TestAzurePrepareChecksSubscriptionAndWarns(t *testing.T) {
	r := &azclitest.Runner{}
	s := azureService(r)
	v := azureScan(t, s, "westeurope")
	c := find(v, "azurerm_linux_virtual_machine", "rg-prod/web-1")
	p, err := s.Prepare(context.Background(), v.ID, c.ID, azurePin(), nil, snapshot(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.ImportID, "/virtualMachines/web-1") || p.Account != subscription || !strings.Contains(strings.Join(p.Warnings, " "), "subscription "+subscription) || strings.Contains(strings.Join(p.Warnings, " "), "aliased") {
		t.Fatalf("%+v", p)
	}
	// A prepared Azure import is bound to the Azure pin only.
	other := azurePin()
	other.AzureSubscription = "33333333-3333-3333-3333-333333333333"
	if _, err := s.Prepare(context.Background(), v.ID, c.ID, other, nil, snapshot(), false); !errors.Is(err, ErrScanStale) {
		t.Fatalf("another subscription selection: %v", err)
	}
	if _, err := s.Prepare(context.Background(), v.ID, c.ID, pin(), nil, snapshot(), false); !errors.Is(err, ErrScanStale) {
		t.Fatalf("AWS pin: %v", err)
	}
	r.SetSubscription("33333333-3333-3333-3333-333333333333")
	_, err = s.Prepare(context.Background(), v.ID, c.ID, azurePin(), nil, snapshot(), false)
	if awscli.KindOf(err) != awscli.KindIdentityChanged {
		t.Fatalf("subscription changed before prepare: %v", err)
	}
}

func TestAzureScopeValidationIsPerProvider(t *testing.T) {
	az := DiscoveryScope{Regions: []string{"westeurope"}, Types: []string{"azurerm_resource_group"}}
	if err := az.ValidateFor("azure"); err != nil {
		t.Fatal(err)
	}
	if az.ValidateFor("") == nil || az.ValidateFor("gcp") == nil {
		t.Error("an Azure scope validated for another provider")
	}
	for name, scope := range map[string]DiscoveryScope{
		"aws":       {Regions: []string{"eu-west-1"}, Types: []string{"aws_vpc"}},
		"gcp types": {Regions: []string{"westeurope"}, Types: []string{"google_compute_network"}},
		"mixed":     {Regions: []string{"westeurope"}, Types: []string{"azurerm_resource_group", "aws_vpc"}},
		"bad":       {Regions: []string{"west europe; rm"}, Types: []string{"azurerm_resource_group"}},
		"upper":     {Regions: []string{"WestEurope"}, Types: []string{"azurerm_resource_group"}},
	} {
		if scope.ValidateFor("azure") == nil {
			t.Errorf("%s validated", name)
		}
	}
}

func TestAzureListArgsAreAllowlisted(t *testing.T) {
	for _, name := range azureTypes() {
		typ, _ := TypeByName(name)
		if args := (azureProvider{}).ListArgs(typ, "", 100, ""); !azcli.Allowed(args) {
			t.Errorf("%s: %v", name, args)
		}
	}
}

func TestAzurePreparedImportChecksWithoutNamingTheProvider(t *testing.T) {
	s := azureService(&azclitest.Runner{})
	v := azureScan(t, s, "westeurope")
	c := find(v, "azurerm_linux_virtual_machine", "rg-prod/web-1")
	p, err := s.Prepare(context.Background(), v.ID, c.ID, azurePin(), nil, snapshot(), false)
	if err != nil {
		t.Fatal(err)
	}
	// The import route builds its pin from the context alone.
	ctxPin := azurePin()
	ctxPin.Provider = ""
	req := imports.Request{Address: p.SuggestedAddress, ID: p.ImportID}
	if err := s.CheckImport(p.Token, ctxPin, req); err != nil {
		t.Fatalf("prepared Azure import refused: %v", err)
	}
	ctxPin.AzureTenant = "44444444-4444-4444-4444-444444444444"
	if err := s.CheckImport(p.Token, ctxPin, req); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("another tenant: %v", err)
	}
	req.Address = "azurerm_managed_disk.web"
	ctxPin.AzureTenant = ""
	if err := s.CheckImport(p.Token, ctxPin, req); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("another type: %v", err)
	}
}
