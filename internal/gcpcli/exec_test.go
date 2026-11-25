package gcpcli_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hclaro/internal/awscli"
	"hclaro/internal/discovery"
	"hclaro/internal/gcpcli"
)

// fakeGCloud builds scripts/fake-gcloud as the only executable named gcloud on PATH, so the
// real Exec, allowlist, environment and error classification run against synthetic output.
func fakeGCloud(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the synthetic gcloud")
	}
	bin := t.TempDir()
	name := "gcloud"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, name), "hclaro/scripts/fake-gcloud")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("PATH", bin)
	return bin
}

func TestExecAgainstFakeGCloud(t *testing.T) {
	fakeGCloud(t)
	spy := filepath.Join(t.TempDir(), "spy")
	var observed [][]string
	runner := gcpcli.Exec{Observe: func(a []string) { observed = append(observed, a) }}
	svc := discovery.New(&noAWS{})
	svc.AddProvider(discovery.GCP(runner))
	// Credentials the profile replaced never reach gcloud; the selection does.
	environ := []string{"CLOUDSDK_ACTIVE_CONFIG_NAME=prod", "CLOUDSDK_CORE_PROJECT=hclaro-demo-1", "HCLARO_FAKE_GCLOUD_SPY=" + spy, "PATH=" + os.Getenv("PATH")}
	pin := discovery.Pin{Provider: "gcp", ContextFingerprint: "inherited", Workspace: "default", Engine: "terraform"}
	id, err := svc.Verify(context.Background(), pin, environ)
	if err != nil || id.Account != "hclaro-demo-1" || id.CLIVersion != "480.0.0" {
		t.Fatalf("%+v %v", id, err)
	}
	b, _ := os.ReadFile(spy)
	if !strings.Contains(string(b), "CONFIG=prod\x00PROJECT=hclaro-demo-1\x00KEYS=false") {
		t.Fatalf("composed environment not passed: %q", b)
	}
	for _, args := range observed {
		if !gcpcli.Allowed(args) {
			t.Errorf("ran %v", args)
		}
	}
	// Refused commands never start a process.
	n := len(observed)
	if _, err := runner.Run(context.Background(), environ, []string{"auth", "print-access-token"}); awscli.KindOf(err) != awscli.KindRefused || len(observed) != n {
		t.Fatalf("print-access-token: %v", err)
	}
	// Failures are classified from stderr and the text never reaches the error.
	for mode, kind := range map[string]string{"expired": awscli.KindExpired, "throttle": awscli.KindThrottled, "denied": awscli.KindAccessDenied, "old": awscli.KindUnsupportedCLI} {
		env := append(environ, "HCLARO_FAKE_GCLOUD="+mode)
		args := []string{"compute", "networks", "list", "--format=" + gcpcli.Formats["compute networks list"], "--limit", "5", "--quiet"}
		if mode == "denied" {
			args = []string{"sql", "instances", "list", "--format=" + gcpcli.Formats["sql instances list"], "--limit", "5", "--quiet"}
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
	// No gcloud on PATH.
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
