package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// knownValue is an ordinary-looking variable value: only the known-input scrubbing in the
// operation runner can recognize it.
const knownValue = "orchid-lantern-meadow-4417"

type harness struct {
	t     *testing.T
	fake  *engine.Fake
	m     *operations.Manager
	ws    *workspace.Store
	store *Store
	dir   string
	root  string
}

func newHarness(t *testing.T, fake *engine.Fake, dir string) *harness {
	t.Helper()
	if fake.VersionValue == "" {
		fake.VersionValue = "1.9.8"
	}
	fake.WorkspaceValue = "demo"
	if fake.StateJSON == nil {
		fake.StateJSON = fixtures.State
	}
	ws := workspace.New(workspace.Info{Project: "demo", Engine: "terraform", Version: "1.9.8", Workspace: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	ws.Load(context.Background(), fake, "terraform")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "terraform.tfvars"), []byte(`note = "`+knownValue+`"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := operations.NewManager(ws, fake, root)
	m.TempDir = t.TempDir()
	m.Reload = func(context.Context) {}
	store := Open(dir, time.Now())
	store.Sync = false
	m.Recorder = store
	t.Cleanup(func() { m.Close(5 * time.Second) })
	return &harness{t, fake, m, ws, store, dir, root}
}

func (h *harness) wait(id string) operations.Operation {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op, _, _ := h.m.Operation(id)
		if op.Status != operations.Running {
			// The recorder runs just before the log finishes; wait for the final entry.
			if e, ok := h.store.Get(id); ok && e.Status != StatusRunning {
				return op
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("operation did not finish")
	return operations.Operation{}
}

func (h *harness) waitStage(id, stage string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if op, _, _ := h.m.Operation(id); op.Stage == stage {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("stage %s not reached", stage)
}

func (h *harness) assertClean() {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, fileName))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, secret := range []string{canary, knownValue, h.root, h.m.TempDir} {
		if strings.Contains(string(b), secret) {
			h.t.Fatalf("%q reached the history file", secret)
		}
	}
}

func TestPlanThenRestartNeverReappliesHistory(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, dir)
	op, err := h.m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The start is recorded before the engine runs.
	if e, ok := h.store.Get(op.ID); !ok || e.Kind != "plan" {
		t.Fatalf("start not recorded: %+v", e)
	}
	done := h.wait(op.ID)
	e, _ := h.store.Get(op.ID)
	if e.Status != StatusSucceeded || e.Outcome.Result != "planned" || e.Plan == nil || e.Plan.PlanID != done.PlanID || len(e.Plan.Changes) == 0 || e.EngineVersion != "1.9.8" || e.Workspace != "demo" || len(e.Excerpt) == 0 {
		t.Fatalf("%+v", e)
	}
	h.assertClean()

	// Restart: a new process opens the same history with a fresh operation runner.
	h.m.Close(5 * time.Second)
	restarted := newHarness(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, dir)
	e, ok := restarted.store.Get(op.ID)
	if !ok || e.Plan == nil || e.Plan.PlanID != done.PlanID {
		t.Fatal("history did not survive a restart")
	}
	if _, ok := restarted.m.Plan(done.PlanID); ok {
		t.Fatal("a historical plan is reviewable as a live plan")
	}
	if _, err := approve(restarted.m, done.PlanID); err == nil {
		t.Fatal("a historical plan could be approved")
	}
	if _, err := restarted.m.StartApply(done.PlanID, strings.Repeat("0", 64), ""); err == nil {
		t.Fatal("a historical plan could be applied")
	}
	if strings.Contains(strings.Join(restarted.fake.Calls(), ","), "apply") {
		t.Fatal("apply ran")
	}
}

func TestCancelledReplaceAndFailedPlanAreRecordedHonestly(t *testing.T) {
	h := newHarness(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions, Block: true}, t.TempDir())
	var ids []string
	for _, r := range h.ws.Get().Index.Resources {
		if r.Mode == "managed" {
			ids = append(ids, r.ID)
			break
		}
	}
	op, err := h.m.StartPlan(nil, ids)
	if err != nil {
		t.Fatal(err)
	}
	h.waitStage(op.ID, "Planning")
	if e, _ := h.store.Get(op.ID); e.Status != StatusRunning || e.Kind != "replace" {
		t.Fatalf("%+v", e)
	}
	if _, err := h.m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(op.ID)
	e, _ := h.store.Get(op.ID)
	if e.Kind != "replace" || e.Status != StatusCancelled || e.Outcome.Result != "not_planned" || e.Plan != nil || e.Diagnostic == nil || e.Diagnostic.Code != "cancelled" {
		t.Fatalf("cancelled replace: %+v", e)
	}

	// A failed plan whose output carries a canary in a diagnostic, in an unknown JSON field,
	// in a non-JSON (stderr-like) line, and an ordinary-looking known input value.
	h.fake.Block = false
	h.fake.PlanErr = &engine.ExitError{Code: 1}
	h.fake.Lines = [][]byte{
		[]byte(`{"type":"diagnostic","@level":"error","diagnostic":{"severity":"error","summary":"Invalid value","detail":"value ` + knownValue + ` rejected; password=` + canary + `"}}`),
		[]byte(`{"type":"log","@level":"info","@message":"refreshing","x_unknown_field":"` + canary + `"}`),
		[]byte(`Error: provider said ` + canary + ` in plain text`),
		[]byte(`{"type":"log","@level":"error","@message":"note is ` + knownValue + `"}`),
	}
	op, err = h.m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.wait(op.ID)
	e, _ = h.store.Get(op.ID)
	if e.Status != StatusFailed || e.Outcome.Result != "not_planned" || e.Plan != nil || e.ExitCode == nil || *e.ExitCode != 1 {
		t.Fatalf("failed plan: %+v", e)
	}
	excerpt := ""
	for _, l := range e.Excerpt {
		excerpt += l.Text + "\n"
	}
	if !strings.Contains(excerpt, "withheld") {
		t.Fatalf("excerpt: %s", excerpt)
	}
	h.assertClean()
	b, _ := os.ReadFile(filepath.Join(h.store.path))
	report := NewReport(h.store.List(Filter{}), Filter{}, "test", time.Now())
	if strings.Contains(report.Markdown(), canary) || strings.Contains(report.Markdown(), knownValue) || strings.Contains(string(b), "x_unknown_field") {
		t.Fatal("secret in export")
	}
}

func TestCancelledApplyIsPartialAndLinkedToItsPlan(t *testing.T) {
	h := newHarness(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions, BlockApply: true}, t.TempDir())
	op, err := h.m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	planned := h.wait(op.ID)
	approval, err := approve(h.m, planned.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := h.m.StartApply(planned.PlanID, approval.Token, approval.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	h.waitStage(apply.ID, "Applying")
	if _, err := h.m.Cancel(apply.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(apply.ID)
	e, _ := h.store.Get(apply.ID)
	if e.Kind != "apply" || e.Status != StatusCancelled || e.Outcome.Result != "partial" || e.Outcome.Verified || e.Outcome.PlanID != planned.PlanID || e.Plan == nil {
		t.Fatalf("%+v", e)
	}
	p, _ := h.store.Get(op.ID)
	if p.Plan.Execution == nil || p.Plan.Execution.OperationID != apply.ID || p.Plan.Execution.Result != "partial" || p.Outcome.Result != "planned" {
		t.Fatalf("plan entry: %+v %+v", p.Plan.Execution, p.Outcome)
	}
	h.assertClean()
}

func TestHubInitIsRecordedWithProject(t *testing.T) {
	store := Open(t.TempDir(), time.Now())
	store.Sync = false
	root := t.TempDir()
	store.Project = func(r string) (string, string, bool) { return id(77), "network", r == root }
	inits := operations.NewInits()
	inits.Recorder = store
	t.Cleanup(func() { inits.Close(5 * time.Second) })
	fake := &engine.Fake{VersionValue: "1.9.8", BlockInit: true, InitStderr: [][]byte{[]byte("Error: " + canary)}}
	op, err := inits.Start(operations.InitRequest{
		ProjectID: id(77), Root: root, Engine: "terraform", Initer: fake,
		Revalidate: func() string { return "" }, Initialized: func() bool { return false }, Lockfile: func() []byte { return nil }, Release: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if o, _, _ := inits.Operation(op.ID); o.Stage == "Initializing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("init did not start")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := inits.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	for {
		if e, _ := store.Get(op.ID); e.Status != StatusRunning && e.Status != "" {
			if e.Kind != "init" || e.Status != StatusCancelled || e.Outcome.Result != "partial" || e.ProjectID != id(77) || e.ProjectName != "network" || !e.Registered || e.EngineVersion != "1.9.8" {
				t.Fatalf("%+v", e)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("init outcome not recorded")
		}
		time.Sleep(2 * time.Millisecond)
	}
	b, _ := os.ReadFile(store.path)
	if strings.Contains(string(b), canary) || strings.Contains(string(b), root) {
		t.Fatal("secret or root path in history")
	}
	// Deleting history changes only the history file: no engine call, no project file.
	before, calls := entries(t, root), len(fake.Calls())
	if err := store.Delete(op.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(op.ID); ok {
		t.Fatal("entry not deleted")
	}
	if after := entries(t, root); strings.Join(before, ",") != strings.Join(after, ",") || len(fake.Calls()) != calls {
		t.Fatal("deleting history touched the project or ran the engine")
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}
