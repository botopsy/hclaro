package profiles

import (
	"slices"
	"strings"
	"testing"

	"hclaro/internal/ids"
)

const (
	azSub    = "11111111-1111-1111-1111-111111111111"
	azTenant = "22222222-2222-2222-2222-222222222222"
)

func TestAzureSelectionValidation(t *testing.T) {
	for name, in := range map[string]Input{
		"subscription name":     {Name: "x", AzureSubscription: "Production"},
		"subscription option":   {Name: "x", AzureSubscription: "--subscription=x"},
		"subscription injected": {Name: "x", AzureSubscription: azSub + ";rm"},
		"tenant domain":         {Name: "x", AzureTenant: "contoso.onmicrosoft.com"},
		"location spaced":       {Name: "x", AzureLocation: "West Europe"},
		"location upper":        {Name: "x", AzureLocation: "WestEurope"},
		"aws region":            {Name: "x", AzureLocation: "eu-west-1"},
	} {
		if err := check(normalize(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := check(normalize(Input{Name: "x", AzureSubscription: azSub, AzureTenant: strings.ToUpper(azTenant), AzureLocation: "westeurope"})); err != nil {
		t.Fatal(err)
	}
}

func TestComposeSelectsAzureSubscriptionAndKeepsTheCredentialChain(t *testing.T) {
	base := []string{"PATH=/bin", "ARM_SUBSCRIPTION_ID=33333333-3333-3333-3333-333333333333", "AZURE_SUBSCRIPTION_ID=33333333-3333-3333-3333-333333333333", "ARM_TENANT_ID=44444444-4444-4444-4444-444444444444",
		"AZURE_DEFAULTS_LOCATION=eastus", "AZURE_CONFIG_DIR=/home/me/.azure", "ARM_CLIENT_ID=app", "ARM_CLIENT_SECRET=" + canary, "AWS_PROFILE=keep"}
	p := Profile{ID: ids.New(), ProjectID: projectA, Name: "azure", Revision: 1, AzureSubscription: azSub, AzureTenant: azTenant, AzureLocation: "westeurope"}
	r := Resolve(p, t.TempDir(), base)
	env, err := Compose(base, r)
	if err != nil {
		t.Fatal(err)
	}
	// The user's credential chain stays; only the selection is replaced.
	want := []string{"PATH=/bin", "AZURE_CONFIG_DIR=/home/me/.azure", "ARM_CLIENT_ID=app", "ARM_CLIENT_SECRET=" + canary, "AWS_PROFILE=keep", "ARM_SUBSCRIPTION_ID=" + azSub, "ARM_TENANT_ID=" + azTenant, "AZURE_DEFAULTS_LOCATION=westeurope"}
	if !slices.Equal(env, want) {
		t.Fatalf("env\n got %v\nwant %v", env, want)
	}
	notices := strings.Join(r.Notices, "\n")
	if strings.Contains(notices, canary) || strings.Contains(notices, "AWS profile") || strings.Contains(notices, "ARM_CLIENT_SECRET") {
		t.Errorf("notices: %q", notices)
	}
	for _, want := range []string{"ARM_SUBSCRIPTION_ID", "AZURE_SUBSCRIPTION_ID", "ARM_TENANT_ID", "AZURE_DEFAULTS_LOCATION", "Azure subscription, tenant and location are IDs and names"} {
		if !strings.Contains(notices, want) {
			t.Errorf("notices lack %q: %q", want, notices)
		}
	}
	// A subscription-only selection keeps the inherited tenant and location.
	only := Resolve(Profile{ID: ids.New(), ProjectID: projectA, Name: "p", Revision: 1, AzureSubscription: azSub}, t.TempDir(), base)
	env, _ = Compose(base, only)
	if !slices.Contains(env, "ARM_TENANT_ID=44444444-4444-4444-4444-444444444444") || !slices.Contains(env, "AZURE_DEFAULTS_LOCATION=eastus") || slices.Contains(env, "ARM_SUBSCRIPTION_ID=33333333-3333-3333-3333-333333333333") {
		t.Fatalf("subscription-only selection: %v", env)
	}
}

func TestAzureSelectionChangesFingerprintOnlyWhenSet(t *testing.T) {
	plain := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1}, t.TempDir(), nil)
	again := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1}, t.TempDir(), nil)
	az := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1, AzureSubscription: azSub}, t.TempDir(), nil)
	other := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1, AzureSubscription: "33333333-3333-3333-3333-333333333333"}, t.TempDir(), nil)
	if plain.Fingerprint != again.Fingerprint || plain.Fingerprint == az.Fingerprint || az.Fingerprint == other.Fingerprint {
		t.Fatalf("%s %s %s", plain.Fingerprint, az.Fingerprint, other.Fingerprint)
	}
}
