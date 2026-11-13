package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Go test binary is the fake executable: no shell or real IaC engine is involved.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("HCLARO_PLAN_HELPER") == "1" {
		planHelper()
	}
	if os.Getenv("HCLARO_INIT_HELPER") == "1" {
		initHelper()
	}
	if os.Getenv("HCLARO_TEST_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args[1:], " ")
	if os.Getenv("HCLARO_TEST_DELAY") == "1" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if text := os.Getenv("HCLARO_TEST_STDERR"); text != "" {
		fmt.Fprintln(os.Stderr, text)
		os.Exit(1)
	}
	if os.Getenv("HCLARO_TEST_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "DO_NOT_LEAK_HCLARO_TEST_SECRET")
		os.Exit(1)
	}
	if os.Getenv("TF_LOG_PATH") != "" || os.Getenv("TF_LOG") != "OFF" {
		os.Exit(3)
	}
	switch args {
	case "version -json":
		if out := os.Getenv("HCLARO_TEST_VERSION_JSON"); out != "" {
			fmt.Println(out)
			break
		}
		fmt.Println(`{"terraform_version":"1.9.8"}`)
	case "workspace show":
		fmt.Println("staging")
	case "show -json":
		fmt.Println(`{"format_version":"1.0"}`)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
func TestFakeExecutable(t *testing.T) {
	t.Setenv("HCLARO_TEST_HELPER", "1")
	t.Setenv("TF_LOG_PATH", filepath.Join(t.TempDir(), "never.log"))
	t.Setenv("TF_LOG", "TRACE")
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_version", "TF_CLI_ARGS_workspace", "TF_CLI_ARGS_show"} {
		t.Setenv(key, "")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &CLI{Name: "terraform", Executable: exe, Root: t.TempDir()}
	if v, err := c.Version(context.Background()); err != nil || v != "1.9.8" {
		t.Fatalf("version %s %v", v, err)
	}
	if w, err := c.Workspace(context.Background()); err != nil || w != "staging" {
		t.Fatalf("workspace %s %v", w, err)
	}
	if _, err := c.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_CLI_ARGS_show", "-json-into=never-write.json")
	if _, err := c.State(context.Background()); !errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("output override was accepted")
	}
	t.Setenv("TF_CLI_ARGS_show", "")
	t.Setenv("HCLARO_TEST_FAIL", "1")
	if _, err := c.State(context.Background()); err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatal("unsafe diagnostic")
	}
	t.Setenv("HCLARO_TEST_FAIL", "")
	t.Setenv("HCLARO_TEST_DELAY", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.State(ctx); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("cancellation failed")
	}
}

// A failed read is classified for the user; the stderr text never leaves the adapter.
func TestReadFailureIsClassifiedWithoutStderr(t *testing.T) {
	t.Setenv("HCLARO_TEST_HELPER", "1")
	t.Setenv("TF_LOG_PATH", "")
	t.Setenv("TF_LOG", "")
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_version", "TF_CLI_ARGS_workspace", "TF_CLI_ARGS_show"} {
		t.Setenv(key, "")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &CLI{Name: "terraform", Executable: exe, Root: t.TempDir()}
	for _, tc := range []struct {
		stderr string
		want   error
		advice string
	}{
		{"Error: the SSO session has expired for account 123456789012 SECRET_TOKEN_x", ErrReadCredentials, "expired credentials"},
		{"Error: dial tcp: lookup bucket.example: no such host SECRET_TOKEN_x", ErrReadNetwork, "could not reach"},
		{"Error: Backend initialization required, please run \"terraform init\" SECRET_TOKEN_x", ErrReadInitRequired, "needs init"},
		{"Error: something else entirely SECRET_TOKEN_x", nil, "fallback"},
	} {
		t.Setenv("HCLARO_TEST_STDERR", tc.stderr)
		for _, read := range []func() error{
			func() error { _, err := c.State(context.Background()); return err },
			func() error { _, err := c.ListWorkspaces(context.Background()); return err },
		} {
			err := read()
			if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "123456789012") {
				t.Fatalf("%q: unsafe or missing error %v", tc.stderr, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("%q: got %v", tc.stderr, err)
			}
			if advice := ReadAdvice(err, "fallback"); !strings.Contains(advice, tc.advice) || strings.Contains(advice, "SECRET") {
				t.Fatalf("%q: advice %q", tc.stderr, advice)
			}
		}
	}
}
func TestDetection(t *testing.T) {
	dir := t.TempDir()
	suffix := ""
	if os.PathSeparator == '\\' {
		suffix = ".exe"
	}
	for _, name := range []string{"terraform", "tofu"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), []byte("synthetic"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	if c, err := Detect(dir, ""); err != nil || c.Name != "terraform" {
		t.Fatal(c, err)
	}
	if c, err := Detect(dir, "tofu"); err != nil || c.Name != "tofu" {
		t.Fatal(c, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(dir, ""); err == nil {
		t.Fatal("missing CLI accepted")
	}
}
func TestVersionCompatibility(t *testing.T) {
	for _, v := range []string{"0.12.0", "2.0.0", "1.18.0", "1.9.0-beta", "bogus"} {
		if Supported("terraform", v) {
			t.Fatal(v)
		}
	}
	if !Supported("tofu", "1.6.0") || !Supported("terraform", "1.5.0") || Supported("tofu", "1.5.0") {
		t.Fatal("incorrect compatibility")
	}
	// Exact accepted edges, documented in docs/support-matrix.md, README.md and the version
	// error. Widening either range needs real-engine evidence in the support matrix first.
	for v, want := range map[string]bool{
		"terraform 1.4.7": false, "terraform 1.5.0": true, "terraform 1.17.99": true, "terraform 1.18.0": false,
		"tofu 1.5.9": false, "tofu 1.6.0": true, "tofu 1.13.99": true, "tofu 1.14.0": false,
		"terraform 1.5.0-rc1": false, "tofu 1.9.0-beta1": false, "opentofu 1.6.0": false,
		"terraform v1.5.0": false, "terraform 1.5": false, "terraform 1.5.0.1": false,
	} {
		name, version, _ := strings.Cut(v, " ")
		if Supported(name, version) != want {
			t.Errorf("Supported(%s) = %v", v, !want)
		}
	}
}

// Version edges through the fake CLI's `version -json`, the path Hclaro actually uses.
func TestVersionBoundariesThroughCLI(t *testing.T) {
	t.Setenv("HCLARO_TEST_HELPER", "1")
	t.Setenv("TF_LOG", "OFF")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, json, want string
	}{
		{"terraform", `{"terraform_version":"1.5.0"}`, "1.5.0"},
		{"terraform", `{"terraform_version":"1.17.99"}`, "1.17.99"},
		{"terraform", `{"terraform_version":"1.4.7"}`, ""},
		{"terraform", `{"terraform_version":"1.18.0"}`, ""},
		{"terraform", `{"terraform_version":"2.0.0"}`, ""},
		{"terraform", `{"terraform_version":"1.10.0-rc1"}`, ""},
		{"terraform", `{"terraform_version":"1.9.0-alpha20240101"}`, ""},
		{"terraform", `{"tofu_version":"1.9.0"}`, ""},
		{"terraform", `not json`, ""},
		{"tofu", `{"terraform_version":"1.6.0"}`, "1.6.0"},
		{"tofu", `{"tofu_version":"1.13.99"}`, "1.13.99"},
		{"tofu", `{"terraform_version":"1.5.9"}`, ""},
		{"tofu", `{"terraform_version":"1.14.0"}`, ""},
		{"tofu", `{"terraform_version":"1.9.0-beta1"}`, ""},
		{"tofu", `{"terraform_version":"2.0.0"}`, ""},
	} {
		t.Setenv("HCLARO_TEST_VERSION_JSON", c.json)
		cli := &CLI{Name: c.name, Executable: exe, Root: t.TempDir()}
		v, err := cli.Version(context.Background())
		if c.want == "" && (err == nil || v != "") || c.want != "" && (err != nil || v != c.want) {
			t.Errorf("%s %s: got %q %v", c.name, c.json, v, err)
		}
	}
}

func TestBoundedOutput(t *testing.T) {
	b := &boundedBuffer{max: 8}
	if _, err := b.Write([]byte("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("excess-output")); err == nil || string(b.b) != "safe" {
		t.Fatal("output limit failed")
	}
}

func TestImportSupport(t *testing.T) {
	for _, c := range []struct {
		name, version string
		want          bool
	}{
		{"terraform", "1.5.0", true}, {"terraform", "1.9.8", true}, {"tofu", "1.6.0", true},
		{"terraform", "1.4.6", false}, {"tofu", "1.5.0", false}, {"terraform", "1.10.0-rc1", false}, {"terraform", "2.0.0", false},
	} {
		if blocks, generate := ImportSupport(c.name, c.version); blocks != c.want || generate != c.want {
			t.Errorf("%s %s: %v %v", c.name, c.version, blocks, generate)
		}
	}
}

func TestUnsafeArgs(t *testing.T) {
	for _, c := range []struct {
		env      string
		commands []string
		want     bool
	}{
		{"TF_CLI_ARGS=-lock=false", []string{"plan"}, true},
		{"TF_CLI_ARGS_plan=-target=x", []string{"plan"}, true},
		{"tf_cli_args_plan=-target=x", []string{"plan"}, true},
		{"TF_CLI_ARGS_apply=-auto-approve", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=-no-color", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=  -no-color  -no-color ", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=-no-color -refresh=false", []string{"plan"}, true},
		{"TF_CLI_ARGS_plan=", []string{"plan"}, false},
		{"TF_CLI_ARGSX=-x", []string{"plan"}, false},
		{"TF_CLI_ARGS_workspace=-force", []string{"workspace delete"}, true},
		{"TF_CLI_ARGS_workspace_delete=-force", []string{"workspace delete"}, true},
		{"TF_CLI_ARGS_workspace_delete=-force", []string{"workspace new", "show"}, false},
		{"TF_CLI_ARGS_workspace_deletex=-force", []string{"workspace delete"}, false},
		{"TF_CLI_ARGS_providers_lock=-x", []string{"providers lock"}, true},
		{"TF_CLI_ARGS_force_unlock=-force", []string{"force-unlock"}, true},
		{"TF_CLI_ARGS_show=-x", []string{""}, false},
		// No commands means every TF_CLI_ARGS* variable.
		{"TF_CLI_ARGS_anything=-x", nil, true},
		{"TF_CLI_ARGSX=-x", nil, true},
		{"TF_CLI_ARGS_anything=-no-color", nil, false},
		{"TF_LOG=-x", nil, false},
	} {
		if got := UnsafeArgs([]string{c.env}, c.commands...); got != c.want {
			t.Errorf("UnsafeArgs(%q, %q) = %v", c.env, c.commands, got)
		}
		// The help names exactly the variables the check refuses.
		if got := UnsafeArgsHelp([]string{c.env}, c.commands...) != ""; got != c.want {
			t.Errorf("UnsafeArgsHelp(%q, %q) non-empty = %v", c.env, c.commands, got)
		}
	}
}

// Secret-like names are ordinary Terraform/OpenTofu workspace names: only their values are
// hidden elsewhere, the names are not rejected.
func TestRiskyNamesAreValidWorkspaces(t *testing.T) {
	for _, name := range []string{"user_data", "authorizationType", "credential", "kubeconfig", "sas_token"} {
		if !ValidWorkspace(name) {
			t.Errorf("%q rejected", name)
		}
	}
}
