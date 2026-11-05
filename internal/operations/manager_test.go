package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func lines(b []byte) [][]byte { return bytes.Split(bytes.TrimSpace(b), []byte("\n")) }

func setup(t *testing.T, fake *engine.Fake, info workspace.Info) (*Manager, string) {
	t.Helper()
	if fake.VersionValue == "" {
		fake.VersionValue = "1.9.8"
	}
	if fake.WorkspaceValue == "" {
		fake.WorkspaceValue = "demo"
	}
	if fake.StateJSON == nil {
		fake.StateJSON = fixtures.State
	}
	if info.ExecutionMode == "" {
		info = workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"}
	}
	store := workspace.New(info)
	store.Load(context.Background(), fake, "terraform")
	root := t.TempDir()
	m := NewManager(store, fake, root)
	m.TempDir = t.TempDir()
	t.Cleanup(func() { m.Close(5 * time.Second) })
	return m, root
}

func wait(t *testing.T, m *Manager, id string) Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op, _, ok := m.Operation(id)
		if !ok {
			t.Fatal("operation disappeared")
		}
		if op.Status != Running {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return Operation{}
}

func waitStage(t *testing.T, m *Manager, id, stage string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if op, _, _ := m.Operation(id); op.Stage == stage {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("stage %s not reached", stage)
}

func assertNoCanary(t *testing.T, values ...any) {
	t.Helper()
	for _, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(canary)) {
			t.Fatalf("canary leaked: %s", b)
		}
	}
}

func assertTempEmpty(t *testing.T, m *Manager) {
	t.Helper()
	entries, err := os.ReadDir(m.TempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary plan files left behind: %v %v", entries, err)
	}
}

func assertArtifactPrivate(t *testing.T, m *Manager) {
	t.Helper()
	entries, err := os.ReadDir(m.TempDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one retained plan directory: %v %v", entries, err)
	}
	dir := filepath.Join(m.TempDir, entries[0].Name())
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("plan directory mode %v %v", info, err)
	}
	file, err := os.Stat(filepath.Join(dir, "plan.tfplan"))
	if err != nil || file.Mode().Perm() != 0o600 {
		t.Fatalf("plan file mode %v %v", file, err)
	}
}

func TestPlanSucceeds(t *testing.T) {
	fake := &engine.Fake{Lines: lines(fixtures.PlanLog), PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPlan(nil, nil); err != nil && !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded || done.PlanID == "" || *done.ExitCode != 0 || done.Diagnostic != nil {
		t.Fatalf("%+v", done)
	}
	p, ok := m.Plan(done.PlanID)
	if !ok {
		t.Fatal("plan not stored")
	}
	digest := sha256.Sum256(fixtures.PlanAllActions)
	if p.ArtifactSHA256 != hex.EncodeToString(digest[:]) || p.Kind != "plan" || !p.Destructive || p.Workspace != "demo" || p.Fingerprint == "" || len(p.Changes) != 13 {
		t.Fatalf("plan metadata: %+v", p)
	}
	// An applicable plan keeps exactly one private saved artifact.
	if status := m.ApplyStatus(p, m.currentFingerprint(p)); !status.Available || !status.ArtifactRetained {
		t.Fatalf("apply status %+v", status)
	}
	assertArtifactPrivate(t, m)
	_, log, _ := m.Operation(op.ID)
	events, _, finished, _ := log.Since(0)
	if !finished {
		t.Fatal("log not finished")
	}
	text := ""
	for _, e := range events {
		text += e.Text + "\n"
	}
	for _, want := range []string{"aws_s3_bucket.logs: plan to create", "module.compute.aws_instance.main[0]: plan to replace (cannot_update)", "changed outside Terraform (update)", "Plan: 2 to add, 3 to change, 4 to destroy, 1 to import.", "Warning: Deprecated attribute", "[line withheld: looks like a secret]", "Plan ready for review"} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hclaro-demo-artifacts") {
		t.Fatal("refresh id values must not be echoed")
	}
	if len(done.Diagnostics) != 1 || done.Diagnostics[0].Severity != "warning" {
		t.Fatalf("diagnostics: %+v", done.Diagnostics)
	}
	assertNoCanary(t, events, done, p, p.Details, m.Operations(), m.Plans())
	if calls := strings.Join(fake.Calls(), ","); calls != "version,workspace,show,workspace,plan,show-plan" {
		t.Fatalf("unexpected engine calls %s", calls)
	}
}

func TestCancelRemovesArtifactsAndLeavesNoPlan(t *testing.T) {
	fake := &engine.Fake{Lines: lines(fixtures.PlanLog), Block: true}
	m, _ := setup(t, fake, workspace.Info{})
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, m, op.ID, "Planning")
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second plan must be rejected while one runs: %v", err)
	}
	if _, err := m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Cancelled || done.Diagnostic.Code != "cancelled" || done.PlanID != "" || !done.CancelRequested {
		t.Fatalf("%+v", done)
	}
	if len(m.Plans()) != 0 {
		t.Fatal("cancelled plan must not be reviewable")
	}
	assertTempEmpty(t, m)
	if _, err := m.Cancel(op.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("finished operation cancelled again")
	}
	if _, err := m.Cancel("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown operation")
	}
	// The manager accepts a new plan after cancellation.
	fake.Block = false
	fake.PlanJSON = fixtures.PlanNoChanges
	next, err := m.StartPlan(nil, nil)
	if err != nil || wait(t, m, next.ID).Status != Succeeded {
		t.Fatalf("plan after cancel: %v", err)
	}
}

func TestFailureClassification(t *testing.T) {
	for name, tc := range map[string]struct {
		fake *engine.Fake
		code string
		exit int
	}{
		"lock from diagnostics": {&engine.Fake{Lines: lines(fixtures.PlanLogLocked), PlanErr: &engine.ExitError{Code: 1}}, "state_locked", 1},
		"credentials from hint": {&engine.Fake{PlanErr: &engine.ExitError{Code: 1, Hint: "credentials"}}, "credentials", 1},
		"generic":               {&engine.Fake{PlanErr: &engine.ExitError{Code: 1}}, "plan_failed", 1},
		"unsafe args":           {&engine.Fake{PlanErr: engine.ErrUnsafeArgs}, "unsafe_cli_args", -1},
		"unreadable plan":       {&engine.Fake{PlanJSON: []byte(`{"format_version":"9.0"}`)}, "plan_format", 0},
		"show fails":            {&engine.Fake{PlanJSON: fixtures.PlanNoChanges, ShowErr: errors.New("x")}, "plan_read", 0},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := setup(t, tc.fake, workspace.Info{})
			op, err := m.StartPlan(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := wait(t, m, op.ID)
			if done.Status != Failed || done.Diagnostic.Code != tc.code || done.PlanID != "" {
				t.Fatalf("%+v", done)
			}
			if tc.exit >= 0 && (done.ExitCode == nil || *done.ExitCode != tc.exit) {
				t.Fatalf("exit code %v", done.ExitCode)
			}
			if len(m.Plans()) != 0 {
				t.Fatal("failed plan must not be reviewable")
			}
			assertTempEmpty(t, m)
			_, log, _ := m.Operation(op.ID)
			events, _, _, _ := log.Since(0)
			assertNoCanary(t, done, events)
		})
	}
}

func TestTimeoutAndShutdown(t *testing.T) {
	m, _ := setup(t, &engine.Fake{Block: true}, workspace.Info{})
	m.Timeout = 50 * time.Millisecond
	op, _ := m.StartPlan(nil, nil)
	if done := wait(t, m, op.ID); done.Status != Failed || done.Diagnostic.Code != "timeout" {
		t.Fatalf("%+v", done)
	}
	m.Timeout = time.Hour
	op, _ = m.StartPlan(nil, nil)
	waitStage(t, m, op.ID, "Planning")
	m.Close(5 * time.Second)
	done, _, _ := m.Operation(op.ID)
	if done.Status != Cancelled || done.Diagnostic.Code != "shutdown" {
		t.Fatalf("%+v", done)
	}
	assertTempEmpty(t, m)
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal("plan accepted after shutdown")
	}
}

func TestWorkspaceChangeAndAvailability(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanNoChanges}
	m, _ := setup(t, fake, workspace.Info{})
	fake.WorkspaceValue = "prod"
	op, _ := m.StartPlan(nil, nil)
	done := wait(t, m, op.ID)
	if done.Diagnostic.Code != "workspace_changed" || !strings.Contains(done.Diagnostic.Message, `"demo"`) {
		t.Fatalf("%+v", done.Diagnostic)
	}
	for _, info := range []workspace.Info{
		{Project: "x", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"},
		{Project: "x", Initialized: true, Backend: "unknown", ExecutionMode: "unsupported"},
		{Project: "x", Initialized: false, Backend: "local", ExecutionMode: "local-cli"},
	} {
		m, _ := setup(t, &engine.Fake{}, info)
		if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%+v: %v", info, err)
		}
	}
	loading := workspace.New(workspace.Info{Project: "x", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	if _, err := NewManager(loading, &engine.Fake{}, t.TempDir()).StartPlan(nil, nil); !errors.Is(err, ErrLoading) {
		t.Fatalf("plan accepted while the workspace was loading: %v", err)
	}
	// A project with no recorded state can still be planned.
	m, _ = setup(t, &engine.Fake{StateJSON: fixtures.Empty, PlanJSON: fixtures.PlanNoChanges}, workspace.Info{})
	if op, err := m.StartPlan(nil, nil); err != nil || wait(t, m, op.ID).Status != Succeeded {
		t.Fatalf("absent state should allow planning: %v", err)
	}
}

func TestVarFileSelection(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanNoChanges}
	m, root := setup(t, fake, workspace.Info{})
	for _, name := range []string{"prod.tfvars", "base.tfvars.json", "terraform.tfvars", "x.auto.tfvars", "-flag.tfvars", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("a = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "prod.tfvars"), filepath.Join(root, "link.tfvars")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(workspace.VarFiles(root), ","); got != "base.tfvars.json,prod.tfvars" {
		t.Fatalf("candidates %s", got)
	}
	for _, bad := range [][]string{{"../prod.tfvars"}, {"terraform.tfvars"}, {"link.tfvars"}, {"-flag.tfvars"}, {"prod.tfvars", "prod.tfvars"}, {"a", "b", "c", "d", "e", "f", "g", "h", "i"}} {
		if _, err := m.StartPlan(bad, nil); !errors.Is(err, ErrVarFiles) {
			t.Fatalf("%v accepted", bad)
		}
	}
	op, err := m.StartPlan([]string{"prod.tfvars", "base.tfvars.json"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if strings.Join(done.Command, " ") != "terraform plan -input=false -json -out=<private temporary file> -var-file=prod.tfvars -var-file=base.tfvars.json" {
		t.Fatalf("command display %v", done.Command)
	}
	if got := fake.Requests()[0].VarFiles; strings.Join(got, ",") != "prod.tfvars,base.tfvars.json" {
		t.Fatalf("engine received %v", got)
	}
	p, _ := m.Plan(done.PlanID)
	snap := m.store.Get()
	if p.Fingerprint != m.Fingerprint(snap, p.VarFiles) {
		t.Fatal("fingerprint not reproducible")
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint == m.Fingerprint(snap, p.VarFiles) {
		t.Fatal("configuration change not reflected in fingerprint")
	}
}

func TestLogIsBounded(t *testing.T) {
	l := NewLog(100, 10_000)
	line := strings.Repeat("x", 1000)
	// 10 MB of output must not grow memory beyond the configured bounds.
	for i := 0; i < 10_000; i++ {
		l.Append("info", line)
	}
	events, dropped, _, _ := l.Since(0)
	if len(events) > 100 || len(events)*1000 > 10_000 || !dropped || l.Dropped() != 10_000-int64(len(events)) {
		t.Fatalf("events %d dropped %v %d", len(events), dropped, l.Dropped())
	}
	if events[len(events)-1].Seq != 10_000 {
		t.Fatal("sequence numbers must keep increasing")
	}
	recent, dropped, _, _ := l.Since(9_995)
	if len(recent) != 5 || dropped {
		t.Fatal("bounded replay from a recent sequence")
	}
	l.Finish()
	l.Append("info", "late")
	if _, _, done, _ := l.Since(0); !done {
		t.Fatal("finish")
	}
}

func TestTranslateSanitizes(t *testing.T) {
	for input, want := range map[string]string{
		`{"type":"log","@message":"\u001b[31mtoken=abc\u001b[0m"}`:  "[line withheld: looks like a secret]",
		`{"type":"log","@level":"warn","@message":"plain message"}`: "plain message",
		// Terraform 1.5's interrupt message starts with a newline.
		`{"type":"log","@message":"\nInterrupt received.\nPlease wait for Terraform to exit"}`:                               "Interrupt received.\n…",
		`{"type":"planned_change","change":{"resource":{"addr":"a.b[\"DO_NOT_LEAK_x\"]"},"action":"create"}}`:                "[redacted address]: plan to create",
		`{"type":"planned_change","change":{"resource":{"addr":"a.b"},"action":"<script>"}}`:                                 "a.b: plan to unknown",
		`{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Bad","detail":"password = \"hunter2\"\nok line"}}`: "Error: Bad",
	} {
		_, text, diag, ok := translate([]byte(input), "plan", "Terraform", nil)
		if !ok || text != want {
			t.Errorf("%s: got %q", input, text)
		}
		if diag != nil && (strings.Contains(diag.Detail, "hunter2") || !strings.Contains(diag.Detail, "ok line")) {
			t.Errorf("detail %q", diag.Detail)
		}
	}
	for input, want := range map[string]string{
		`{"type":"version"}`: "Apply started",
		`{"type":"apply_start","hook":{"resource":{"addr":"a.b"},"action":"delete"}}`:                                "a.b: delete started",
		`{"type":"apply_errored","hook":{"resource":{"addr":"a.b"},"action":"create"}}`:                              "a.b: create failed",
		`{"type":"provision_progress","hook":{"resource":{"addr":"a.b"}},"output":"DO_NOT_LEAK_HCLARO_TEST_SECRET"}`: "",
		`{"type":"change_summary","changes":{"add":2,"change":0,"remove":2,"operation":"apply"}}`:                    "Apply complete: 2 added, 0 changed, 2 destroyed.",
	} {
		if _, text, _, ok := translate([]byte(input), "apply", "Terraform", nil); !ok || text != want {
			t.Errorf("apply %s: got %q", input, text)
		}
	}
	for _, input := range []string{`not json`, `{"type":"apply_unknown"}`, `{}`} {
		if _, _, _, ok := translate([]byte(input), "plan", "Terraform", nil); ok {
			t.Errorf("%s rendered", input)
		}
	}
}
