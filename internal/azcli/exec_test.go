package azcli_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
	"hclaro/internal/discovery"
)

// fakeAz builds scripts/fake-az as the only executable named az on PATH, so the real Exec,
// allowlist, environment and error classification run against synthetic output.
func fakeAz(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the synthetic az")
	}
	bin := t.TempDir()
	name := "az"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, name), "hclaro/scripts/fake-az")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("PATH", bin)
}

func TestExecAgainstFakeAz(t *testing.T) {
	fakeAz(t)
	spy := filepath.Join(t.TempDir(), "spy")
	var observed [][]string
	runner := azcli.Exec{Observe: func(a []string) { observed = append(observed, a) }}
	svc := discovery.New(&noAWS{})
	svc.AddProvider(discovery.Azure(runner))
	const other = "33333333-3333-3333-3333-333333333333"
	environ := []string{"ARM_SUBSCRIPTION_ID=" + other, "HCLARO_FAKE_AZ_SPY=" + spy, "PATH=" + os.Getenv("PATH")}
	pin := discovery.Pin{Provider: "azure", ContextFingerprint: "inherited", Workspace: "default", Engine: "terraform"}
	id, err := svc.Verify(context.Background(), pin, environ)
	if err != nil || id.Account != other || id.CLIVersion != "2.60.0" || id.Tenant == "" {
		t.Fatalf("%+v %v", id, err)
	}
	// The composed subscription reaches az as the flag, and the environment as it is.
	b, _ := os.ReadFile(spy)
	if !strings.Contains(string(b), "SUB="+other+"\x00SECRET=false") || !strings.Contains(string(b), "--subscription\x00"+other) {
		t.Fatalf("composed subscription not passed: %q", b)
	}
	for _, args := range observed {
		if args[0] != "--version" && !azcli.Allowed(args) {
			t.Errorf("ran %v", args)
		}
	}
	// Refused commands never start a process.
	n := len(observed)
	if _, err := runner.Run(context.Background(), environ, []string{"account", "get-access-token"}); awscli.KindOf(err) != awscli.KindRefused || len(observed) != n {
		t.Fatalf("get-access-token: %v", err)
	}
	// Failures are classified from stderr and the text never reaches the error.
	for mode, kind := range map[string]string{"expired": awscli.KindExpired, "throttle": awscli.KindThrottled, "denied": awscli.KindAccessDenied, "old": awscli.KindUnsupportedCLI} {
		env := append(environ, "HCLARO_FAKE_AZ="+mode)
		args := []string{"vm", "list", "--query", azcli.Queries["vm list"], "-o", "json", "--only-show-errors"}
		if mode == "denied" {
			args = []string{"disk", "list", "--query", azcli.Queries["disk list"], "-o", "json", "--only-show-errors"}
		}
		var err error
		if mode == "old" {
			_, err = svc.Verify(context.Background(), pin, env)
		} else {
			_, err = runner.Run(context.Background(), env, args)
		}
		if awscli.KindOf(err) != kind {
			t.Errorf("%s: kind %s (%v)", mode, awscli.KindOf(err), err)
		}
		if err != nil && strings.Contains(err.Error(), "DO_NOT_LEAK") {
			t.Errorf("%s: error carries CLI output", mode)
		}
	}
	// No az on PATH.
	t.Setenv("PATH", t.TempDir())
	if _, err := runner.Run(context.Background(), nil, []string{"--version"}); awscli.KindOf(err) != awscli.KindMissingCLI {
		t.Fatalf("missing: %v", err)
	}
}

// noAWS is an AWS runner that fails: this test never reaches AWS.
type noAWS struct{}

func (noAWS) Run(context.Context, []string, []string) ([]byte, error) {
	return nil, &awscli.CallError{Kind: awscli.KindRefused}
}
