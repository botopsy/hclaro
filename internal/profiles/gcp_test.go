package profiles

import (
	"slices"
	"strings"
	"testing"

	"hclaro/internal/ids"
)

func TestGCPSelectionValidation(t *testing.T) {
	for name, in := range map[string]Input{
		"configuration option": {Name: "x", GCPConfiguration: "--project=x"},
		"configuration case":   {Name: "x", GCPConfiguration: "Prod"},
		"configuration key":    {Name: "x", GCPConfiguration: "AIzaSyA-1234567890123456789012345678901"},
		"project name":         {Name: "x", GCPProject: "My Project"},
		"project number":       {Name: "x", GCPProject: "123456789012"},
		"project short":        {Name: "x", GCPProject: "abc"},
		"project injection":    {Name: "x", GCPProject: "hclaro-demo-1;rm"},
		"region":               {Name: "x", GCPRegion: "Europe West1"},
		"aws region":           {Name: "x", GCPRegion: "eu-west-1-x"},
	} {
		if err := check(normalize(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := check(normalize(Input{Name: "x", GCPConfiguration: "prod-eu", GCPProject: "hclaro-demo-1", GCPRegion: "europe-west1"})); err != nil {
		t.Fatal(err)
	}
}

func TestComposeSelectsGCloudConfigurationAndDropsCompetingCredentials(t *testing.T) {
	base := []string{"PATH=/bin", "GOOGLE_APPLICATION_CREDENTIALS=/keys/other.json", "GOOGLE_OAUTH_ACCESS_TOKEN=" + canary, "CLOUDSDK_AUTH_ACCESS_TOKEN=" + canary,
		"CLOUDSDK_CORE_PROJECT=other-project-9", "GOOGLE_PROJECT=other-project-9", "CLOUDSDK_COMPUTE_REGION=us-east1", "CLOUDSDK_CONFIG=/home/me/.config/gcloud", "AWS_PROFILE=keep"}
	p := Profile{ID: ids.New(), ProjectID: projectA, Name: "gcp", Revision: 1, GCPConfiguration: "prod", GCPProject: "hclaro-demo-1", GCPRegion: "europe-west1"}
	r := Resolve(p, t.TempDir(), base)
	env, err := Compose(base, r)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "CLOUDSDK_CONFIG=/home/me/.config/gcloud", "AWS_PROFILE=keep", "CLOUDSDK_ACTIVE_CONFIG_NAME=prod", "CLOUDSDK_CORE_PROJECT=hclaro-demo-1", "CLOUDSDK_COMPUTE_REGION=europe-west1"}
	if !slices.Equal(env, want) {
		t.Fatalf("env\n got %v\nwant %v", env, want)
	}
	notices := strings.Join(r.Notices, "\n")
	for _, bad := range []string{canary, "AWS profile"} {
		if strings.Contains(notices, bad) {
			t.Errorf("notices mention %q: %q", bad, notices)
		}
	}
	for _, want := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CORE_PROJECT", "Google Cloud configuration, project and region are names"} {
		if !strings.Contains(notices, want) {
			t.Errorf("notices lack %q: %q", want, notices)
		}
	}
	// A project-only selection keeps the inherited credentials: only a named configuration
	// is a credential source.
	only := Resolve(Profile{ID: ids.New(), ProjectID: projectA, Name: "p", Revision: 1, GCPProject: "hclaro-demo-1"}, t.TempDir(), base)
	env, _ = Compose(base, only)
	if !slices.Contains(env, "GOOGLE_APPLICATION_CREDENTIALS=/keys/other.json") || slices.Contains(env, "CLOUDSDK_CORE_PROJECT=other-project-9") || !slices.Contains(env, "CLOUDSDK_CORE_PROJECT=hclaro-demo-1") || !slices.Contains(env, "CLOUDSDK_COMPUTE_REGION=us-east1") {
		t.Fatalf("project-only selection: %v", env)
	}
}

func TestGCPSelectionChangesFingerprintOnlyWhenSet(t *testing.T) {
	plain := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1}, t.TempDir(), nil)
	again := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1}, t.TempDir(), nil)
	gcp := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1, GCPProject: "hclaro-demo-1"}, t.TempDir(), nil)
	other := Resolve(Profile{ID: "a", ProjectID: projectA, Name: "p", Revision: 1, GCPProject: "hclaro-demo-2"}, t.TempDir(), nil)
	if plain.Fingerprint != again.Fingerprint || plain.Fingerprint == gcp.Fingerprint || gcp.Fingerprint == other.Fingerprint {
		t.Fatalf("%s %s %s", plain.Fingerprint, gcp.Fingerprint, other.Fingerprint)
	}
}
