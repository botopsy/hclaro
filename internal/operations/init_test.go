package operations

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/rootlock"
	"hclaro/internal/workspace"
)

// versionErr is an Initer whose version read fails, as with injected TF_CLI_ARGS.
type versionErr struct {
	err   error
	inits atomic.Int64
}

func (v *versionErr) Version(context.Context) (string, error) { return "", v.err }
func (v *versionErr) Init(context.Context, engine.InitRequest, func(string, []byte)) error {
	v.inits.Add(1)
	return nil
}

type initHarness struct {
	runner   *Inits
	root     string
	released atomic.Int64
	finished atomic.Int64
}

func newInitHarness(t *testing.T) *initHarness {
	t.Helper()
	h := &initHarness{runner: NewInits(), root: t.TempDir()}
	t.Cleanup(func() { h.runner.Close(5 * time.Second) })
	return h
}

func (h *initHarness) request(engineName string, initer engine.Initer) InitRequest {
	return InitRequest{
		ProjectID: "p1", Root: h.root, Engine: engineName, Initer: initer, Environ: os.Environ(),
		Revalidate:  func() string { return "" },
		Initialized: func() bool { _, err := os.Stat(filepath.Join(h.root, ".terraform")); return err == nil },
		Lockfile: func() []byte {
			b, _ := os.ReadFile(filepath.Join(h.root, ".terraform.lock.hcl"))
			return b
		},
		Finished: func() { h.finished.Add(1) },
		Release:  func() { h.released.Add(1) },
	}
}

func waitInit(t *testing.T, r *Inits, id string) (Operation, []Event) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op, log, ok := r.Operation(id)
		if !ok {
			t.Fatal("operation disappeared")
		}
		if op.Status != Queued && op.Status != Running && op.Status != Cancelling {
			events, _, done, _ := log.Since(0)
			if done {
				return op, events
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("init did not finish")
	return Operation{}, nil
}

func joined(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Level + " " + e.Text + "\n")
	}
	return b.String()
}

func TestInitSucceedsWithSanitizedHumanOutput(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMIK7MDENGbPxRfiCYsecretvalue")
	h := newInitHarness(t)
	fake := &engine.Fake{VersionValue: "1.8.3", InitDataDir: filepath.Join(h.root, ".terraform"), InitLines: [][]byte{
		[]byte("\x1b[1mInitializing the backend...\x1b[0m"),
		[]byte("Backend password=" + canary),
		[]byte("Using key wJalrXUtnFEMIK7MDENGbPxRfiCYsecretvalue now"),
		[]byte("OpenTofu has been successfully initialized!"),
	}}
	op, err := h.runner.Start(h.request("tofu", fake))
	if err != nil || op.Status != Queued || op.Kind != "init" || op.ProjectID != "p1" {
		t.Fatalf("%+v %v", op, err)
	}
	op, events := waitInit(t, h.runner, op.ID)
	if op.Status != Succeeded || op.ExitCode == nil || *op.ExitCode != 0 || op.Diagnostic != nil {
		t.Fatalf("%+v", op)
	}
	if !slices.Equal(op.Command, []string{"tofu", "init", "-input=false", "-no-color"}) {
		t.Fatalf("command %v", op.Command)
	}
	if calls := fake.InitCalls(); len(calls) != 1 || !slices.Equal(calls[0], []string{"init", "-input=false", "-no-color"}) {
		t.Fatalf("init calls %v", calls)
	}
	text := joined(events)
	for _, leak := range []string{canary, "wJalrXUtnFEMIK7MDENGbPxRfiCYsecretvalue", "\x1b"} {
		if strings.Contains(text, leak) {
			t.Fatalf("leaked %q in\n%s", leak, text)
		}
	}
	for _, want := range []string{"Initializing the backend...", "[line withheld: looks like a secret]", "successfully initialized", "Initialization finished"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if h.released.Load() != 1 || h.finished.Load() != 1 {
		t.Fatalf("released %d finished %d", h.released.Load(), h.finished.Load())
	}
	if op.LockfileChanged == nil || *op.LockfileChanged {
		t.Fatalf("lockfile %v", op.LockfileChanged)
	}
}

func TestInitUsesJSONForTerraform19AndClassifiesDiagnostics(t *testing.T) {
	h := newInitHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", InitErr: &engine.ExitError{Code: 1}, InitLines: [][]byte{
		[]byte(`{"type":"version","terraform":"1.9.8"}`),
		[]byte(`{"type":"init_output","@level":"info","@message":"Initializing the backend..."}`),
		[]byte(`{"type":"diagnostic","@level":"error","diagnostic":{"severity":"error","summary":"Backend configuration changed","detail":"Use \"init -migrate-state\" or \"init -reconfigure\". token=` + canary + `"}}`),
		[]byte(`not json ` + canary),
	}}
	op, _ := h.runner.Start(h.request("terraform", fake))
	op, events := waitInit(t, h.runner, op.ID)
	if op.Status != Failed || op.Diagnostic == nil || op.Diagnostic.Code != "backend_migration" || *op.ExitCode != 1 {
		t.Fatalf("%+v", op)
	}
	if !slices.Equal(op.Command, []string{"terraform", "init", "-input=false", "-json"}) {
		t.Fatalf("command %v", op.Command)
	}
	if len(fake.InitCalls()) != 1 || slices.Contains(fake.Calls(), "apply") {
		t.Fatalf("hidden retry or other command: %v", fake.Calls())
	}
	text := joined(events)
	if strings.Contains(text, canary) || strings.Contains(op.Diagnostics[0].Detail, canary) {
		t.Fatalf("canary leaked:\n%s %+v", text, op.Diagnostics)
	}
	if !strings.Contains(text, "withheld") || !strings.Contains(text, "never runs init with -migrate-state") {
		t.Fatalf("log:\n%s", text)
	}
	if op.LockfileChanged == nil {
		t.Fatal("lockfile status missing after the engine ran")
	}
}

func TestInitFailureCategoriesFromHumanStderr(t *testing.T) {
	cases := map[string][]string{
		"backend_migration": {"│ Error: Backend configuration changed", "│ use \"tofu init -migrate-state\""},
		"input_required":    {"Error: No value for required variable", "input is disabled"},
		"credentials":       {"Error: No valid credential sources found"},
		"registry":          {"Error: Failed to query available provider packages"},
		"lockfile_upgrade":  {"Error: Failed to resolve provider packages", "locked provider registry.example/x does not match configured version constraint"},
		"state_locked":      {"Error: Error acquiring the state lock"},
		"version_mismatch":  {"Error: Unsupported OpenTofu Version"},
		"network":           {"Error: dial tcp: lookup backend.example: no such host"},
		"backend_config":    {"Error: Invalid backend configuration"},
		"init_failed":       {"Error: Something unexpected"},
	}
	for want, stderr := range cases {
		t.Run(want, func(t *testing.T) {
			h := newInitHarness(t)
			fake := &engine.Fake{VersionValue: "1.8.3", InitErr: &engine.ExitError{Code: 1}}
			for _, l := range stderr {
				fake.InitStderr = append(fake.InitStderr, []byte(l))
			}
			op, _ := h.runner.Start(h.request("tofu", fake))
			op, _ = waitInit(t, h.runner, op.ID)
			if op.Status != Failed || op.Diagnostic.Code != want {
				t.Fatalf("got %+v", op.Diagnostic)
			}
			if op.Diagnostic.Message == "" || len(fake.InitCalls()) != 1 {
				t.Fatalf("message or retries: %+v %v", op.Diagnostic, fake.InitCalls())
			}
		})
	}
}

func TestInitZeroExitWithoutDataDirectoryIsUnknown(t *testing.T) {
	h := newInitHarness(t)
	fake := &engine.Fake{VersionValue: "1.8.3"}
	op, _ := h.runner.Start(h.request("tofu", fake))
	op, _ = waitInit(t, h.runner, op.ID)
	if op.Status != Unknown || op.Diagnostic.Code != "unverified" {
		t.Fatalf("%+v", op)
	}
}

func TestInitRefusesBeforeRunningEngine(t *testing.T) {
	t.Run("unsafe CLI args", func(t *testing.T) {
		h := newInitHarness(t)
		initer := &versionErr{err: engine.ErrUnsafeArgs}
		op, _ := h.runner.Start(h.request("terraform", initer))
		op, _ = waitInit(t, h.runner, op.ID)
		if op.Status != Failed || op.Diagnostic.Code != "unsafe_cli_args" || initer.inits.Load() != 0 || op.LockfileChanged != nil {
			t.Fatalf("%+v inits %d", op, initer.inits.Load())
		}
	})
	t.Run("unsupported version", func(t *testing.T) {
		h := newInitHarness(t)
		initer := &versionErr{err: os.ErrInvalid}
		op, _ := h.runner.Start(h.request("terraform", initer))
		op, _ = waitInit(t, h.runner, op.ID)
		if op.Diagnostic.Code != "engine_version" || initer.inits.Load() != 0 {
			t.Fatalf("%+v", op)
		}
	})
	t.Run("changed root", func(t *testing.T) {
		h := newInitHarness(t)
		fake := &engine.Fake{VersionValue: "1.9.8"}
		req := h.request("terraform", fake)
		req.Revalidate = func() string { return "path_changed" }
		op, _ := h.runner.Start(req)
		op, _ = waitInit(t, h.runner, op.ID)
		if op.Diagnostic.Code != "path_changed" || len(fake.InitCalls()) != 0 || h.released.Load() != 1 {
			t.Fatalf("%+v %v", op, fake.InitCalls())
		}
	})
}

func TestInitCancelAndShutdown(t *testing.T) {
	h := newInitHarness(t)
	fake := &engine.Fake{VersionValue: "1.9.8", BlockInit: true}
	op, _ := h.runner.Start(h.request("terraform", fake))
	for deadline := time.Now().Add(5 * time.Second); len(fake.InitCalls()) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("init did not start")
		}
	}
	cancelled, err := h.runner.Cancel(op.ID)
	if err != nil || cancelled.Status != Cancelling || !cancelled.CancelRequested {
		t.Fatalf("%+v %v", cancelled, err)
	}
	op, _ = waitInit(t, h.runner, op.ID)
	if op.Status != Cancelled || op.Diagnostic.Code != "cancelled" || h.released.Load() != 1 {
		t.Fatalf("%+v", op)
	}
	if _, err := h.runner.Cancel(op.ID); err != ErrNotRunning {
		t.Fatalf("second cancel %v", err)
	}

	second := &engine.Fake{VersionValue: "1.9.8", BlockInit: true}
	op, _ = h.runner.Start(h.request("terraform", second))
	for deadline := time.Now().Add(5 * time.Second); len(second.InitCalls()) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("init did not start")
		}
	}
	h.runner.Close(5 * time.Second)
	op, _ = waitInit(t, h.runner, op.ID)
	if op.Status != Cancelled || op.Diagnostic.Code != "shutdown" {
		t.Fatalf("%+v", op)
	}
	released := atomic.Int64{}
	req := h.request("terraform", second)
	req.Release = func() { released.Add(1) }
	if _, err := h.runner.Start(req); err != ErrClosed || released.Load() != 1 {
		t.Fatalf("start after close: %v released %d", err, released.Load())
	}
}

func TestInitDetectsLockfileChange(t *testing.T) {
	h := newInitHarness(t)
	lock := filepath.Join(h.root, ".terraform.lock.hcl")
	fake := &engine.Fake{VersionValue: "1.8.3", InitDataDir: filepath.Join(h.root, ".terraform")}
	req := h.request("tofu", fake)
	initialized := req.Initialized
	req.Initialized = func() bool { _ = os.WriteFile(lock, []byte("new"), 0o644); return initialized() }
	op, _ := h.runner.Start(req)
	op, events := waitInit(t, h.runner, op.ID)
	if op.LockfileChanged == nil || !*op.LockfileChanged || !strings.Contains(joined(events), "lock.hcl was created or changed") {
		t.Fatalf("%+v", op)
	}
}

func TestLaunchOperationsRespectRootClaims(t *testing.T) {
	m, root := setup(t, &engine.Fake{}, workspace.Info{})
	m.Locks = rootlock.New()
	release, ok := m.Locks.Acquire(root, "init")
	if !ok {
		t.Fatal("claim failed")
	}
	if _, err := m.StartPlan(nil, nil); err != ErrBusy {
		t.Fatalf("plan during init: %v", err)
	}
	release()
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if holder, held := m.Locks.Holder(root); !held || holder != "plan" {
		t.Fatalf("plan does not claim the root: %q %v", holder, held)
	}
	wait(t, m, op.ID)
	if _, held := m.Locks.Holder(root); held {
		t.Fatal("plan kept the root claimed")
	}
}
