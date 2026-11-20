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
// lockedRead is the stderr of `tofu show -json` captured under the L1 holder on Windows
// (FU-WIN-STATE-READ step 0), with its colour codes.
const lockedRead = "\x1b[31m╷\x1b[0m\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\x1b[1m\x1b[31mError: \x1b[0m\x1b[0m\x1b[1mfailed to load state: Failed to read state file: The state file could not be read: read terraform.tfstate: The process cannot access the file because another process has locked a portion of the file.\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\n\x1b[31m╵\x1b[0m\x1b[0m\n"

// lockedReadTerraform is Terraform 1.17.2's `show -json` stderr under the Windows holder (run
// 43871517546): the same text, wrapped across "│ " lines with colour codes.
const lockedReadTerraform = "\x1b[31m╷\x1b[0m\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\x1b[1m\x1b[31mError: \x1b[0m\x1b[0m\x1b[1mFailed to read state file\x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\n\x1b[31m│\x1b[0m \x1b[0m\x1b[0mThe state file could not be read: read terraform.tfstate: The process\n\x1b[31m│\x1b[0m \x1b[0mcannot access the file because another process has locked a portion of the\n\x1b[31m│\x1b[0m \x1b[0mfile.\n\x1b[31m╵\x1b[0m\x1b[0m\n"

// The Windows lock-violation text is a lock only with a state-file read; anything else keeps
// today's class. Windows "Access is denied." matches no class (not `access ?denied`), so it
// stays generic rather than becoming a lock.
func TestClassifyLockedStateRead(t *testing.T) {
	for text, want := range map[string]string{
		lockedRead:          "state_locked",
		lockedReadTerraform: "state_locked",
		"Error: Failed to read state file: The state file could not be read: read terraform.tfstate.d/qa/terraform.tfstate: The process cannot access the file because another process has locked a portion of the file.": "state_locked",
		"Error: AccessDenied: not authorized to read the state bucket": "credentials",
		"Error: Access is denied.":                                     "",
		"Error: read terraform.tfstate: Access is denied.":             "",
		"Error: open .terraform.lock.hcl: The process cannot access the file because another process has locked a portion of the file.": "",
		"Error: The process cannot access the file because another process has locked a portion of the file.":                           "",
		// Localised Windows text is not recognised: it stays generic and fail-closed.
		"Error: Failed to read state file: read terraform.tfstate: Der Prozess kann nicht auf die Datei zugreifen, da ein anderer Prozess einen Teil der Datei gesperrt hat.": "",
	} {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%q) = %q, want %q", text, got, want)
		}
	}
}

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
		// OpenTofu 1.13.9 on Windows under another process's state lock (run 43834356151).
		{lockedRead, ErrReadStateLocked, "holds the state lock"},
		{lockedReadTerraform, ErrReadStateLocked, "holds the state lock"},
		// Other lock text on a read keeps the generic failure (unchanged outside Windows).
		{"Error: Error acquiring the state lock\nLock Info: SECRET_TOKEN_x", nil, "fallback"},
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

// Secret-like names are ordinary Terraform/OpenTofu workspace names: only their values are
// hidden elsewhere, the names are not rejected.
func TestRiskyNamesAreValidWorkspaces(t *testing.T) {
	for _, name := range []string{"user_data", "authorizationType", "credential", "kubeconfig", "sas_token"} {
		if !ValidWorkspace(name) {
			t.Errorf("%q rejected", name)
		}
	}
}
