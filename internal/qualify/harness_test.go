package qualify

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
)

func TestGuardFixture(t *testing.T) {
	ok := map[string]string{
		"main.tf": `terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "= 3.9.1" }
  }
}
resource "terraform_data" "a" {}
resource "random_password" "p" { length = 8 }
data "terraform_remote_state" "r" {
  backend = "local"
  config = { path = "remote.tfstate" }
}
module "m" { source = "./mod" }
`,
		".terraform.lock.hcl": "provider \"registry.terraform.io/hashicorp/random\" {\n  version = \"3.9.1\"\n}\n",
	}
	bad := map[string]map[string]string{
		"s3 backend":       {"main.tf": "terraform {\n  backend \"s3\" {}\n}\n"},
		"cloud block":      {"main.tf": "terraform {\n  cloud {\n  }\n}\n"},
		"remote state s3":  {"main.tf": "data \"terraform_remote_state\" \"r\" {\n  backend = \"s3\"\n}\n"},
		"aws provider":     {"main.tf": "provider \"aws\" {}\n"},
		"implicit aws":     {"main.tf": "resource \"aws_s3_bucket\" \"b\" {}\n"},
		"registry module":  {"main.tf": "module \"m\" {\n  source = \"hashicorp/consul/aws\"\n}\n"},
		"git module":       {"main.tf": "module \"m\" {\n  source = \"git::https://example.invalid/x\"\n}\n"},
		"null provider":    {"main.tf": "terraform {\n  required_providers {\n    null = { source = \"hashicorp/null\" }\n  }\n}\n"},
		"json config":      {"main.tf.json": "{}"},
		"override file":    {"main_override.tf": "resource \"terraform_data\" \"a\" {}\n"},
		"locked aws":       {".terraform.lock.hcl": "provider \"registry.terraform.io/hashicorp/aws\" {\n}\n"},
		"module s3 nested": {"main.tf": "", "mod/main.tf": "terraform {\n  backend \"s3\" {}\n}\n"},
		// OD-8: local-exec only in the approved gate fixture, byte-identical.
		"local-exec":  {"main.tf": "resource \"terraform_data\" \"x\" {\n  provisioner \"local-exec\" {\n    command = \"true\"\n  }\n}\n"},
		"edited gate": gateFixture(t, "exit 0"),
	}
	write := func(files map[string]string) string {
		dir := t.TempDir()
		for name, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	if err := GuardFixture(write(gateFixture(t, ""))); err != nil {
		t.Errorf("approved gate fixture refused: %v", err)
	}
	if err := GuardFixture(write(ok)); err != nil {
		t.Errorf("approved fixture refused: %v", err)
	}
	for name, files := range bad {
		if err := GuardFixture(write(files)); err == nil || !strings.HasPrefix(err.Error(), "fixture_scope") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// Every committed template passes the guard.
	entries, err := os.ReadDir("../../fixtures/qualify")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := GuardFixture(filepath.Join("../../fixtures/qualify", e.Name())); err != nil {
			t.Errorf("template %s: %v", e.Name(), err)
		}
	}
}

func TestEnvironmentAllowlist(t *testing.T) {
	dirs := ProcessDirs{Bin: "/r/bin", Home: "/r/home", Tmp: "/r/tmp", Config: "/r/config"}
	env := Environment(dirs, "/r/cli.tfrc", nil)
	if err := CheckEnvironment(env, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckEnvironment(append(env, "TF_VAR_x=1"), nil); err == nil {
		t.Error("TF_VAR_x accepted")
	}
	extra := map[string]string{"TF_CLI_ARGS_workspace_delete": "-force"}
	if err := CheckEnvironment(Environment(dirs, "/r/cli.tfrc", extra), extra); err != nil {
		t.Errorf("declared extra refused: %v", err)
	}
	if err := CheckEnvironment(Environment(dirs, "/r/cli.tfrc", extra), nil); err == nil {
		t.Error("undeclared extra accepted")
	}
	for _, kv := range env {
		for _, forbidden := range []string{"TF_VAR_", "TF_CLI_ARGS", "TF_WORKSPACE", "TF_DATA_DIR", "TF_PLUGIN_CACHE_DIR", "TF_TOKEN_", "TF_LOG", "SSH_AUTH_SOCK", "KUBECONFIG", "TF_IN_AUTOMATION"} {
			if strings.HasPrefix(kv, forbidden) {
				t.Errorf("%s in the allowlist environment", kv)
			}
		}
	}
	hits := CredentialVariables([]string{"PATH=/bin", "AWS_PROFILE=x", "GITHUB_TOKEN=x", "MY_Secret=x", "TF_TOKEN_app=x", "ARM_CLIENT_ID=x", "LANG=C"})
	if strings.Join(hits, ",") != "ARM_CLIENT_ID,AWS_PROFILE,GITHUB_TOKEN,MY_Secret,TF_TOKEN_app" {
		t.Errorf("credential names: %v", hits)
	}
}

func TestSanitizer(t *testing.T) {
	s := NewSanitizer("HCLARO-QUAL-CANARY-0123456789abcdef", "/tmp/run", "/Users/alice", "/Users/alice/src/hclaro", "alice", "alice-mbp")
	in := `{"a":"/tmp/run/s-1/x","b":"/Users/alice/src/hclaro/bin","c":"/Users/alice/.terraformrc","d":"HCLARO-QUAL-CANARY-0123456789abcdef","e":"alice on alice-mbp","runnerImage":"x","f":"malice"}`
	got := s.String(in)
	want := `{"a":"<TMP>/s-1/x","b":"<REPO>/bin","c":"<HOME>/.terraformrc","d":"<CANARY>","e":"<USER> on <HOST>","runnerImage":"x","f":"malice"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(`{"runnerImage":"runner"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits, err := ScanOut(dir, "runner"); err != nil || len(hits) != 1 {
		t.Errorf("username as a whole word not found: %v %v", hits, err)
	}
	if hits, err := ScanOut(dir, "alice", "/Users/alice"); err != nil || len(hits) != 0 {
		t.Errorf("clean dir: %v %v", hits, err)
	}
}

func TestRouteOf(t *testing.T) {
	if got := routeOf("plans/0123456789abcdef0123456789abcdef/changes/fedcba9876543210fedcba9876543210?x=1"); got != "plans/{id}/changes/{id}" {
		t.Error(got)
	}
}

// TestDirectCallsAllowlisted is the M27.02 grep test: no mutating subcommand may appear in
// the harness's direct-call code, and the allowlist holds only the documented calls.
func TestDirectCallsAllowlisted(t *testing.T) {
	b, err := os.ReadFile("direct.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(b), "")
	for _, sub := range []string{"apply", "import", "destroy", "refresh", "state mv", "state rm", "state push", "state replace-provider", "force-unlock", "workspace new", "workspace select", "workspace delete", "taint", "untaint"} {
		if regexp.MustCompile(`"[^"]*\b` + regexp.QuoteMeta(sub) + `\b[^"]*"`).MatchString(code) {
			t.Errorf("direct.go names %q in a string", sub)
		}
	}
	want := map[string]bool{"version -json": true, "init -input=false -no-color": true, "show -json": true, "state list": true, "workspace show": true, "workspace list": true}
	for sub := range directAllowed {
		if !want[sub] {
			t.Errorf("%q is not a documented direct call", sub)
		}
	}
	if len(holderAllowed) != 2 || !holderAllowed["console -no-color"] || !holderAllowed["plan -input=false -no-color"] {
		t.Errorf("lock holders %v are not the F-2 candidates", holderAllowed)
	}
	// Every engine exec in the package goes through direct.go.
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "direct.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		if regexp.MustCompile(`exec\.Command(Context)?\([^)]*(engine|terraform|tofu)`).Match(src) {
			t.Errorf("%s starts an engine outside direct.go", f)
		}
	}
	for _, sub := range []string{"apply -auto-approve", "workspace new x", "init -upgrade"} {
		if _, err := direct("/bin/false", t.TempDir(), nil, sub); err == nil {
			t.Errorf("%q accepted", sub)
		}
	}
	if _, err := direct("/bin/false", t.TempDir(), nil, "state list", "-state=x"); err == nil {
		t.Error("extra arguments accepted")
	}
}

func TestIsolationLevel(t *testing.T) {
	control := []byte(`{"mode":"control","checks":[{"name":"ipv4_tcp","outcome":"connected","ok":true},{"name":"ipv6_tcp","outcome":"failed","ok":false},{"name":"udp_dns","outcome":"answered","ok":true}]}`)
	denied := `[{"name":"loopback_tcp4","outcome":"connected","expected":"reached","ok":true},{"name":"ipv4_tcp","outcome":"failed","expected":"denied","ok":true},{"name":"udp_dns","outcome":"failed","expected":"denied","ok":true}`
	isolated := []byte(`{"mode":"isolated","checks":` + denied + `,{"name":"system_resolver","outcome":"failed"},{"name":"home_read","outcome":"failed"}],"grandchild":{"mode":"isolated","checks":` + denied + `]}}`)
	leaky := []byte(strings.Replace(string(isolated), `"outcome":"failed","expected":"denied","ok":true`, `"outcome":"connected","expected":"denied","ok":false`, 1))
	noLoopback := []byte(strings.Replace(string(isolated), `"outcome":"connected","expected":"reached","ok":true`, `"outcome":"failed","expected":"reached","ok":false`, 1))
	homeRead := []byte(strings.Replace(string(isolated), `{"name":"home_read","outcome":"failed"}`, `{"name":"home_read","outcome":"read"}`, 1))
	for _, c := range []struct {
		name, mechanism        string
		control, before, after []byte
		want                   string
	}{
		{"enforced", "sandbox-exec", control, isolated, isolated, "enforced"},
		{"no mechanism", "none", control, isolated, isolated, "none"},
		{"proxy", "proxy", control, isolated, isolated, "weak-proxy"},
		{"control unreachable", "sandbox-exec", []byte(`{"mode":"control","checks":[]}`), isolated, isolated, "weak-proxy"},
		{"outbound reached", "sandbox-exec", control, leaky, isolated, "weak-proxy"},
		{"after missing", "sandbox-exec", control, isolated, nil, "weak-proxy"},
		{"after leaky", "sandbox-exec", control, isolated, leaky, "weak-proxy"},
		{"loopback never up (F0-LB)", "sandbox-exec", control, noLoopback, isolated, "weak-proxy"},
		{"home readable", "sandbox-exec", control, homeRead, isolated, "weak-proxy"},
	} {
		if got, reason, _ := IsolationLevel(c.mechanism, c.control, c.before, c.after); got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, got, reason, c.want)
		}
	}
	if _, _, r := IsolationLevel("sandbox-exec", control, isolated, isolated); r == nil || *r {
		t.Error("credentialFilesReachable should be false")
	}
}

// TestLeftoverProcesses checks the sampler sees a process started from under a directory, as
// an engine left running by Hclaro would be. The sleeper is a copy of this test binary, so the
// check runs on every platform (Windows uses the Win32_Process sampler).
func TestLeftoverProcesses(t *testing.T) {
	if os.Getenv("QUAL_SLEEPER") == "1" {
		return
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", exeName("engine-sleeper"))
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(os.Args[0], bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestSleeperChild$")
	cmd.Env = append(os.Environ(), "QUAL_SLEEPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Wait for the child to report it is running (observable state, not a timer).
	line := make([]byte, 8)
	if _, err := io.ReadFull(stdout, line); err != nil || string(line) != "sleeping" {
		t.Fatalf("sleeper did not start: %q %v", line, err)
	}
	left, err := leftoverProcesses(dir, nil)
	if err != nil || len(left) != 1 || !strings.Contains(left[0], "engine-sleeper") {
		t.Fatalf("got %v %v", left, err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if left, _ := leftoverProcesses(dir, nil); len(left) != 0 {
		t.Fatalf("after kill: %v", left)
	}
}

// TestSleeperChild is the child of TestLeftoverProcesses.
func TestSleeperChild(t *testing.T) {
	if os.Getenv("QUAL_SLEEPER") != "1" {
		t.Skip("child of TestLeftoverProcesses")
	}
	os.Stdout.WriteString("sleeping")
	time.Sleep(60 * time.Second)
}

// TestRealEngineTagNotInCI: the realengine tag is never set in PR or push CI.
func TestRealEngineTagNotInCI(t *testing.T) {
	var files []string
	_ = filepath.WalkDir("../../.github", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")) {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Skip("no workflows")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "realengine") || strings.Contains(string(b), "make qualify") {
			t.Errorf("%s mentions the realengine harness", f)
		}
	}
}

// TestFinishFailsClosed: a forbidden value in the result directory, or a non-zero exit with no
// FAIL scenario, must leave a run.json that records FAIL and goTestExit 1.
func TestFinishFailsClosed(t *testing.T) {
	newRun := func() *Run {
		out, home := t.TempDir(), t.TempDir()
		res := &RunResult{SchemaVersion: 1, Partial: true}
		r := &Run{Cfg: Config{Out: out, Home: home}, Canary: "HCLARO-QUAL-CANARY-0123456789abcdef", Result: res, user: "qualify-test-user"}
		r.San = NewSanitizer(r.Canary, "", "", "", "", "")
		r.W = NewWriter(out, res, r.San)
		return r
	}
	read := func(r *Run) RunResult {
		var got RunResult
		b, err := os.ReadFile(filepath.Join(r.Cfg.Out, "run.json"))
		if err != nil || json.Unmarshal(b, &got) != nil {
			t.Fatalf("run.json: %v", err)
		}
		return got
	}
	r := newRun()
	if err := r.W.Add(ScenarioResult{ID: "a", Status: Pass}); err != nil {
		t.Fatal(err)
	}
	leak := filepath.Join(r.Cfg.Out, "artifacts", "a", "log.txt")
	if err := os.MkdirAll(filepath.Dir(leak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leak, []byte("read "+r.Cfg.Home+"/.terraformrc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(0); err == nil {
		t.Fatal("Finish accepted a home path in the results")
	}
	got := read(r)
	if got.GoTestExit != 1 || got.Scenarios[len(got.Scenarios)-1].ID != "result-scan" || !strings.Contains(got.Scenarios[len(got.Scenarios)-1].Reason, "artifacts/a/log.txt") {
		t.Errorf("run.json after a scan hit: %+v", got)
	}
	if _, err := os.Stat(leak); err == nil {
		t.Error("leaking file kept")
	}
	r = newRun()
	if err := r.W.Add(ScenarioResult{ID: "a", Status: Pass}); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(2); err != nil {
		t.Fatal(err)
	}
	if got := read(r); got.GoTestExit != 2 || got.Scenarios[len(got.Scenarios)-1].Status != Fail {
		t.Errorf("non-zero exit without FAIL: %+v", got)
	}
}

// gateFixture returns the approved gated fixture, with gate.sh changed when edit is set.
func gateFixture(t *testing.T, edit string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"main.tf", "gate.sh", "launch.tfvars"} {
		b, err := fs.ReadFile(fixtures.Qualify, "qualify/launch-gate/"+name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	if edit != "" {
		files["gate.sh"] += edit + "\n"
	}
	return files
}
