package projects

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
)

// initFixture is hubFixture with initialization enabled through the synthetic CLI and the
// real CLI adapter, so each init is a real subprocess.
func initFixture(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	hub, ids, spy := hubFixture(t)
	for _, key := range []string{"TF_CLI_ARGS_init", "TF_WORKSPACE", "HCLARO_FAKE_INIT", "TF_CLI_CONFIG_FILE", "TF_PLUGIN_CACHE_DIR"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	engines := hub.Engines
	hub.Initers = func(root, preference string, environ []string) (string, string, engine.Initer, error) {
		name, executable, adapter, err := engines(root, preference, "", environ)
		if err != nil {
			return "", "", nil, err
		}
		cli := adapter.(*engine.CLI)
		cli.Environ = environ
		return name, executable, cli, nil
	}
	return hub, ids, spy
}

func waitInit(t *testing.T, hub *Hub, id string) (operations.Operation, string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		op, log, ok := hub.Inits.Operation(id)
		if !ok {
			t.Fatal("operation disappeared")
		}
		events, _, done, _ := log.Since(0)
		if done {
			var b strings.Builder
			for _, e := range events {
				b.WriteString(e.Level + " " + e.Text + "\n")
			}
			return op, b.String()
		}
	}
	t.Fatal("init did not finish")
	return operations.Operation{}, ""
}

func initLines(t *testing.T, spy string) []string {
	out := []string{}
	for _, line := range spyLines(t, spy) {
		if strings.Contains(line, "\x00init") {
			out = append(out, line)
		}
	}
	return out
}

func TestInitStatusRunsNothingAndDisclosesContext(t *testing.T) {
	hub, ids, spy := initFixture(t)
	t.Setenv("TF_WORKSPACE", "inherited")
	t.Setenv("TF_PLUGIN_CACHE_DIR", "/tmp/never-used-cache")
	s, err := hub.InitStatus(ids["legacy"])
	if err != nil {
		t.Fatal(err)
	}
	if !s.CanInit || s.Initialized || s.Engine != "terraform" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s.ContextID) {
		t.Fatalf("%+v", s)
	}
	if !slices.Equal(s.Command, []string{"terraform", "init", "-input=false", "-no-color"}) || s.DataDir != ".terraform" || s.Lockfile {
		t.Fatalf("%+v", s)
	}
	notices := strings.Join(s.Notices, "\n")
	if !strings.Contains(notices, "TF_WORKSPACE is not passed") || !strings.Contains(notices, "TF_PLUGIN_CACHE_DIR") || strings.Contains(notices, "/tmp/never-used-cache") {
		t.Fatalf("notices %q", notices)
	}
	if lines := spyLines(t, spy); len(lines) != 1 || lines[0] != "" {
		t.Fatalf("status ran commands: %q", lines)
	}
	if again, _ := hub.InitStatus(ids["legacy"]); again.ContextID != s.ContextID {
		t.Fatal("context ID is not stable")
	}
}

func TestInitRunsOnceWithStandardArgvAndRefreshesProject(t *testing.T) {
	hub, ids, spy := initFixture(t)
	t.Setenv("TF_WORKSPACE", "inherited")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMIK7MDENGbPxRfiCYsecretvalue")
	t.Setenv("HCLARO_FAKE_INIT", "leak")
	legacy := ids["legacy"]
	if s, _ := hub.Inspect(context.Background(), legacy); s.Inspection.Diagnostic.Code != "uninitialized" {
		t.Fatalf("%+v", s.Inspection)
	}
	status, _ := hub.InitStatus(legacy)
	op, err := hub.StartInit(legacy, status.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	op, log := waitInit(t, hub, op.ID)
	if op.Status != operations.Succeeded || *op.ExitCode != 0 || op.LockfileChanged == nil || !*op.LockfileChanged {
		t.Fatalf("%+v %+v\n%s", op, op.Diagnostic, log)
	}
	for _, leak := range []string{fixtures.Canary, "wJalrXUtnFEMIK7MDENGbPxRfiCYsecretvalue"} {
		if strings.Contains(log, leak) {
			t.Fatalf("leaked %q:\n%s", leak, log)
		}
	}
	// Terraform 1.9.8 gets the machine-readable mode; TF_WORKSPACE never reaches init.
	if got := initLines(t, spy); len(got) != 1 || got[0] != "TF_WORKSPACE=<unset>\x00init\x00-input=false\x00-json" {
		t.Fatalf("init invocations %q", got)
	}
	for _, line := range spyLines(t, spy) {
		if strings.Contains(line, "apply") || strings.Contains(line, "workspace\x00select") || strings.Contains(line, "-upgrade") {
			t.Fatalf("unexpected command %q", line)
		}
	}
	s, _ := hub.Summary(legacy)
	if !s.Initialized || s.Inspection != nil || s.Busy != "" {
		t.Fatalf("project not refreshed: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(s.RootPath, ".terraform.lock.hcl")); err != nil {
		t.Fatal(err)
	}
	// Post-init navigation: workspaces list and state reads now work.
	if s, _ := hub.Inspect(context.Background(), legacy); s.Inspection.Diagnostic != nil {
		t.Fatalf("%+v", s.Inspection.Diagnostic)
	}
	c, _, err := hub.Open(legacy, "default", false)
	if err != nil || waitLoaded(t, c).Status != "ready" {
		t.Fatalf("%v", err)
	}
	// A retry needs a fresh review: the context changed with the new lock file.
	if _, err := hub.StartInit(legacy, status.ContextID); !errors.Is(err, ErrContextChanged) {
		t.Fatalf("stale review accepted: %v", err)
	}
	again, _ := hub.InitStatus(legacy)
	op, _ = hub.StartInit(legacy, again.ContextID)
	if op, _ = waitInit(t, hub, op.ID); op.Status != operations.Succeeded || *op.LockfileChanged {
		t.Fatalf("retry %+v", op)
	}
}

func TestInitOpenTofuUsesHumanOutputAndClassifiesFailures(t *testing.T) {
	hub, ids, spy := initFixture(t)
	platform := ids["platform"]
	for mode, want := range map[string]string{"migrate": "backend_migration", "prompt": "input_required", "credentials": "credentials", "registry": "registry"} {
		t.Setenv("HCLARO_FAKE_INIT", mode)
		status, _ := hub.InitStatus(platform)
		op, err := hub.StartInit(platform, status.ContextID)
		if err != nil {
			t.Fatal(err)
		}
		op, log := waitInit(t, hub, op.ID)
		if op.Status != operations.Failed || op.Diagnostic.Code != want || *op.ExitCode != 1 {
			t.Fatalf("%s: %+v\n%s", mode, op.Diagnostic, log)
		}
		if !slices.Equal(op.Command, []string{"tofu", "init", "-input=false", "-no-color"}) {
			t.Fatalf("%v", op.Command)
		}
	}
	got := initLines(t, spy)
	if len(got) != 4 {
		t.Fatalf("expected exactly one init per request, got %q", got)
	}
	for _, line := range got {
		if line != "TF_WORKSPACE=<unset>\x00init\x00-input=false\x00-no-color" {
			t.Fatalf("argv %q", line)
		}
	}
}

func TestInitRefusalsRunNothing(t *testing.T) {
	hub, ids, spy := initFixture(t)
	legacy := ids["legacy"]
	status, _ := hub.InitStatus(legacy)

	if _, err := hub.StartInit(legacy, strings.Repeat("0", 64)); !errors.Is(err, ErrContextChanged) {
		t.Fatalf("wrong context: %v", err)
	}
	t.Run("hidden flags", func(t *testing.T) {
		for _, env := range [][2]string{{"TF_CLI_ARGS_init", "-upgrade"}, {"TF_CLI_ARGS", "-reconfigure"}} {
			t.Setenv(env[0], env[1])
			s, _ := hub.InitStatus(legacy)
			var refusal *InitRefusal
			if s.CanInit || s.ReasonCode != "unsafe_cli_args" {
				t.Fatalf("%+v", s)
			}
			if _, err := hub.StartInit(legacy, status.ContextID); !errors.As(err, &refusal) || refusal.Code != "unsafe_cli_args" {
				t.Fatalf("%v", err)
			}
			os.Unsetenv(env[0])
		}
	})
	t.Run("shared data directory", func(t *testing.T) {
		t.Setenv("TF_DATA_DIR", t.TempDir())
		if s, _ := hub.InitStatus(legacy); s.CanInit || s.ReasonCode != "shared_data_dir" {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("configuration edited after review", func(t *testing.T) {
		s, _ := hub.InitStatus(legacy)
		if err := os.WriteFile(filepath.Join(s.RootPath, "extra.tf"), []byte("# edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := hub.StartInit(legacy, s.ContextID); !errors.Is(err, ErrContextChanged) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("root replaced by a symlink", func(t *testing.T) {
		s, _ := hub.InitStatus(legacy)
		moved := s.RootPath + "-moved"
		if err := os.Rename(s.RootPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, s.RootPath); err != nil {
			t.Skip("symlinks unavailable")
		}
		_, err := hub.StartInit(legacy, s.ContextID)
		var refusal *InitRefusal
		if !errors.As(err, &refusal) || refusal.Code != StatusPathChanged {
			t.Fatalf("%v", err)
		}
	})
	if _, err := hub.StartInit(strings.Repeat("a", 32), status.ContextID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if got := initLines(t, spy); len(got) != 0 {
		t.Fatalf("refused init ran: %q", got)
	}
}

func TestInitClaimsRootExclusivelyAndCancels(t *testing.T) {
	hub, ids, spy := initFixture(t)
	legacy, network := ids["legacy"], ids["network"]
	t.Setenv("HCLARO_FAKE_INIT", "slow")
	status, _ := hub.InitStatus(legacy)
	op, err := hub.StartInit(legacy, status.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); len(initLines(t, spy)) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("init did not start")
		}
	}
	// The same root refuses another init, inspection and state reads while it runs.
	if _, err := hub.StartInit(legacy, status.ContextID); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent init: %v", err)
	}
	if s, _ := hub.InitStatus(legacy); s.CanInit || s.Busy != "init" || s.Operation == nil || s.Operation.ID != op.ID {
		t.Fatalf("%+v", s)
	}
	if _, err := hub.Inspect(context.Background(), legacy); !errors.Is(err, ErrBusy) {
		t.Fatalf("inspect during init: %v", err)
	}
	if _, _, err := hub.Open(legacy, "default", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("open during init: %v", err)
	}
	// Another project initializes independently with its own pinned environment.
	os.Unsetenv("HCLARO_FAKE_INIT")
	other, _ := hub.InitStatus(network)
	second, err := hub.StartInit(network, other.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if second, _ = waitInit(t, hub, second.ID); second.Status != operations.Succeeded {
		t.Fatalf("%+v", second)
	}
	if running, _, _ := hub.Inits.Operation(op.ID); running.Status != operations.Running {
		t.Fatalf("first init disturbed: %+v", running)
	}
	if _, err := hub.Inits.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	op, log := waitInit(t, hub, op.ID)
	if op.Status != operations.Cancelled || op.Diagnostic.Code != "cancelled" {
		t.Fatalf("%+v\n%s", op, log)
	}
	if s, _ := hub.Summary(legacy); s.Busy != "" || s.Initialized {
		t.Fatalf("after cancel %+v", s)
	}
	if _, err := hub.Inspect(context.Background(), legacy); err != nil {
		t.Fatalf("root still claimed: %v", err)
	}
}

// An init of the folder Hclaro was launched with rereads the launch project once the root
// is free; inits of other projects and refused starts do not.
func TestInitOfTheLaunchProjectRereadsIt(t *testing.T) {
	hub, ids, _ := initFixture(t)
	legacy, network := ids["legacy"], ids["network"]
	hub.Launch = root(t, hub, legacy)
	calls := make(chan bool, 4)
	hub.LaunchChanged = func() {
		_, held := hub.Locks.Holder(hub.Launch)
		calls <- held
	}
	if _, err := hub.StartInit(legacy, strings.Repeat("0", 64)); !errors.Is(err, ErrContextChanged) {
		t.Fatalf("stale review: %v", err)
	}
	status, _ := hub.InitStatus(network)
	op, err := hub.StartInit(network, status.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	waitInit(t, hub, op.ID)
	status, _ = hub.InitStatus(legacy)
	if op, err = hub.StartInit(legacy, status.ContextID); err != nil {
		t.Fatal(err)
	}
	if done, _ := waitInit(t, hub, op.ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	select {
	case held := <-calls:
		if held {
			t.Fatal("launch reread ran while init still held the root")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launch project was not reread")
	}
	if len(calls) != 0 {
		t.Fatalf("%d extra rereads", len(calls))
	}
}
