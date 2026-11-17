package projects

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/internal/awsauth"
	"hclaro/internal/profiles"
)

// fakeVault builds scripts/fake-aws-vault as the only executable on PATH (the real aws-vault
// on a developer machine must never run in tests) and returns its spy file.
func fakeVault(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	name := "aws-vault"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, name), "hclaro/scripts/fake-aws-vault")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("PATH", bin)
	spy := filepath.Join(t.TempDir(), "vault-spy")
	t.Setenv("HCLARO_FAKE_VAULT_SPY", spy)
	t.Setenv("HCLARO_FAKE_VAULT", "")
	return spy
}

func vaultCalls(t *testing.T, spy string) []string {
	t.Helper()
	b, _ := os.ReadFile(spy)
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, strings.Join(strings.Split(line, "\x00")[3:], " "))
		}
	}
	return out
}

// Opening a Hub workspace whose profile uses aws-vault never prompts on its own (M20): a local
// backend is read without aws-vault, and a remote backend's read waits for Read again, which
// then runs through aws-vault as selected.
func TestAWSVaultProfileDoesNotPromptOnOpen(t *testing.T) {
	hub, ids, _ := hubFixture(t)
	spy := fakeVault(t)
	hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	network := ids["network"]
	p, err := hub.Profiles.Create(network, profiles.Input{Name: "prod", AWSProfile: "prod", AWSAuth: profiles.AWSVault})
	if err != nil {
		t.Fatal(err)
	}
	// Local backend: read normally, aws-vault never runs.
	c, _, err := hub.OpenWith(network, "dev", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if s := waitLoaded(t, c); s.Status != "ready" {
		t.Fatalf("local backend: %s %+v", s.Status, s.Diagnostic)
	}
	if calls := vaultCalls(t, spy); len(calls) != 0 {
		t.Fatalf("aws-vault ran for a local backend read: %q", calls)
	}
	// Remote backend: the automatic read is deferred and runs nothing that needs credentials.
	root := rootOf(t, hub, network)
	if err := os.WriteFile(filepath.Join(root, ".terraform", "terraform.tfstate"), []byte(`{"version":3,"backend":{"type":"s3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _, err = hub.OpenWith(network, "dev", p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// reload=true is the user's Read again: it runs through aws-vault.
	if s := waitLoaded(t, c); s.Status != "ready" {
		t.Fatalf("explicit read: %s %+v", s.Status, s.Diagnostic)
	}
	calls := vaultCalls(t, spy)
	if len(calls) == 0 || !strings.Contains(strings.Join(calls, ","), "show -json") {
		t.Fatalf("explicit read did not use aws-vault: %q", calls)
	}
	hub.ProfileChanged(network, p.ID, "test") // close it so the next open is a fresh, automatic one
	before := len(calls)
	c, _, err = hub.OpenWith(network, "dev", p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	s := waitLoaded(t, c)
	if s.Status != "error" || s.Diagnostic == nil || s.Diagnostic.Code != AWSVaultDeferred || !strings.Contains(s.Diagnostic.Message, "Read again") {
		t.Fatalf("automatic remote read: %s %+v", s.Status, s.Diagnostic)
	}
	if calls := vaultCalls(t, spy); len(calls) != before {
		t.Fatalf("aws-vault ran on open: %q", calls[before:])
	}
}

// Removing a project cancels its profiles' running sign-ins and drops their checks.
func TestRemovingAProjectCancelsItsSignIns(t *testing.T) {
	hub, ids, _ := hubFixture(t)
	hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	auth := awsauth.New()
	auth.Run = func(ctx context.Context, _ []string, _ string, args ...string) ([]byte, []byte, error) {
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("aws-cli/2.17.0 Python/3.12 (synthetic)"), nil, nil
		case "configure get sso_session --profile dev-sso":
			return []byte("corp"), nil, nil
		case "sso login --profile dev-sso":
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("exit status 1")
	}
	hub.Auth = auth
	network := ids["network"]
	p, err := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := hub.AuthTarget(network, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StartLogin(context.Background(), target, awsauth.ModeSSO); err != nil {
		t.Fatal(err)
	}
	if err := hub.Remove(network); err != nil {
		t.Fatal(err)
	}
	eventually(t, "sign-in not cancelled", func() bool {
		l := auth.Status(context.Background(), target).Login
		return l != nil && l.State == awsauth.LoginCanceled
	})
}
