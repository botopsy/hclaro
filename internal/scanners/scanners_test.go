package scanners_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hclaro/internal/scanners"
	"hclaro/internal/scanners/scannerstest"
)

func TestArgvIsFixed(t *testing.T) {
	cases := map[string][]string{
		"tflint":  {"--format=json", "--no-color"},
		"trivy":   {"config", "--format", "json", "--quiet", "--skip-check-update", "."},
		"checkov": {"-d", ".", "-o", "json", "--framework", "terraform", "--quiet", "--compact", "--skip-download"},
	}
	for name, want := range cases {
		if got := scanners.Argv(name, false); !slices.Equal(got, want) {
			t.Errorf("%s argv = %v", name, got)
		}
	}
	if got := scanners.Argv("tflint", true); got[len(got)-1] != "--recursive" {
		t.Errorf("recursive tflint argv = %v", got)
	}
	if scanners.Argv("terraform", false) != nil {
		t.Error("an unknown scanner has an argv")
	}
}

func TestParseVersion(t *testing.T) {
	for _, c := range []struct {
		name, out, version string
		supported          bool
	}{
		{"tflint", "TFLint version 0.47.0\n+ ruleset.terraform (0.4.0-bundled)\n", "0.47.0", true},
		{"tflint", "TFLint version 0.46.1\n", "0.46.1", false},
		{"trivy", "Version: 0.58.1\nVulnerability DB:\n  Version: 2\n", "0.58.1", true},
		{"trivy", "Version: 0.40.0\n", "0.40.0", false},
		{"checkov", "3.2.100\n", "3.2.100", true},
		{"checkov", "2.5.0\n", "2.5.0", false},
		{"checkov", "garbage", "", false},
	} {
		v, ok := scanners.ParseVersion(c.name, []byte(c.out))
		if v != c.version || ok != c.supported {
			t.Errorf("%s %q = %q, %v", c.name, c.out, v, ok)
		}
	}
}

func TestNotInstalledRunsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := &scanners.Runner{}
	for _, name := range scanners.Names {
		info := r.Detect(context.Background(), name, t.TempDir())
		if info.Installed || info.Reason == "" {
			t.Errorf("%s: %+v", name, info)
		}
		if _, _, err := r.Run(context.Background(), name, t.TempDir(), false); !errors.Is(err, scanners.ErrNotRunnable) {
			t.Errorf("%s run: %v", name, err)
		}
	}
}

func TestRunUsesFixedArgvAndMinimalEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFAKEFAKEFAKE0000")
	t.Setenv("AWS_PROFILE", "prod")
	t.Setenv("TF_VAR_password", "hunter2-hunter2")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	fake := scannerstest.Install(t, "trivy", "", `{"Results":[]}`, "", 0, false)
	r := &scanners.Runner{Paths: map[string]string{"trivy": fake.Path}}
	out, version, err := r.Run(context.Background(), "trivy", root, false)
	if err != nil || version != "0.58.1" || out.ExitCode != 0 || string(out.Stdout) != `{"Results":[]}` {
		t.Fatalf("run = %+v %q %v", out, version, err)
	}
	if got := fake.Args(); !slices.Equal(got, scanners.Argv("trivy", false)) {
		t.Errorf("argv = %v", got)
	}
	if want, _ := filepath.EvalSymlinks(root); fake.Cwd() != want && fake.Cwd() != root {
		t.Errorf("cwd = %q, want %q", fake.Cwd(), root)
	}
	env := fake.Env()
	for _, banned := range []string{"AWS_", "TF_VAR_", "hunter2", "AKIA"} {
		if strings.Contains(env, banned) {
			t.Errorf("scanner environment contains %q:\n%s", banned, env)
		}
	}
	if !strings.Contains(env, "HTTPS_PROXY=http://proxy.invalid:3128") {
		t.Errorf("proxy setting was not passed:\n%s", env)
	}
}

func TestUnsupportedVersionRunsNothing(t *testing.T) {
	fake := scannerstest.Install(t, "checkov", "2.5.0\n", "", "", 0, false)
	r := &scanners.Runner{Paths: map[string]string{"checkov": fake.Path}}
	info := r.Detect(context.Background(), "checkov", t.TempDir())
	if !info.Installed || info.Supported || info.Reason == "" {
		t.Fatalf("info = %+v", info)
	}
	if _, _, err := r.Run(context.Background(), "checkov", t.TempDir(), false); !errors.Is(err, scanners.ErrNotRunnable) || fake.Ran() {
		t.Fatalf("run err=%v ran=%v", err, fake.Ran())
	}
}

func TestFoundConfig(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".tflint.hcl"), nil, 0o644)
	os.Symlink(filepath.Join(root, ".tflint.hcl"), filepath.Join(root, ".trivyignore"))
	if got := scanners.FoundConfig("tflint", root); !slices.Equal(got, []string{".tflint.hcl"}) {
		t.Errorf("tflint config = %v", got)
	}
	if got := scanners.FoundConfig("trivy", root); len(got) != 0 {
		t.Errorf("a symlinked config was reported: %v", got)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct{ name, stderr, want string }{
		{"tflint", "Plugin `aws` not found. Did you run \"tflint --init\"?", "scanner_plugins"},
		{"trivy", "FATAL failed to download checks: dial tcp: no such host", "scanner_download"},
		{"checkov", "boom", "scanner_failed"},
	} {
		if got := scanners.Classify(c.name, c.stderr); got != c.want {
			t.Errorf("%s %q = %s", c.name, c.stderr, got)
		}
	}
}
