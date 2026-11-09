package operations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

// planned runs a plan to completion and returns its ID.
func planned(t *testing.T, m *Manager) string {
	t.Helper()
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
	return done.PlanID
}

func rejected(err error) string {
	var r *RejectError
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func approveAndApply(t *testing.T, m *Manager, id string) Operation {
	t.Helper()
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartApply(id, approval.Token, approval.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func applyCalls(f *engine.Fake) int { return strings.Count(strings.Join(f.Calls(), ","), "apply") }

func TestApplyAppliesReviewedArtifactOnceAndReloads(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
	m, _ := setup(t, fake, workspace.Info{})
	var reloads atomic.Int32
	m.Reload = func(context.Context) { reloads.Add(1) }
	id := planned(t, m)
	op := approveAndApply(t, m, id)
	if strings.Join(op.Command[:4], " ") != "terraform apply -input=false -json" || strings.Contains(strings.Join(op.Command, " "), "auto-approve") {
		t.Fatalf("command %q", op.Command)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded || done.Outcome != "applied" || !done.StateReloaded || reloads.Load() != 1 || done.PlanID != id {
		t.Fatalf("%+v", done)
	}
	if got := fake.Applied(); len(got) != 1 || !bytes.Equal(got[0], fixtures.PlanAllActions) {
		t.Fatal("the reviewed artifact was not the one applied")
	}
	assertTempEmpty(t, m)
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	text := ""
	for _, e := range events {
		text += e.Text + "\n"
	}
	for _, want := range []string{"Apply started", "module.compute.aws_instance.main[0]: create complete", "module.compute.aws_instance.main[0]: delete started", "Apply complete: 1 added, 0 changed, 1 destroyed.", "Reloading state"} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q:\n%s", want, text)
		}
	}
	assertNoCanary(t, events, done, m.Operations())
	p, _ := m.Plan(id)
	if _, status := m.ContextStatus(p); status.Available || status.ArtifactRetained {
		t.Fatalf("applied plan still applicable: %+v", status)
	}
	if _, err := approve(m, id); rejected(err) != "not_applicable" {
		t.Fatalf("second approval: %v", err)
	}
}

func TestApplyRefusedWithoutValidApproval(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	if _, err := m.StartApply(id, "", "apply demo"); rejected(err) != "approval_required" {
		t.Fatalf("no approval: %v", err)
	}
	approval, _ := approve(m, id)
	if approval.Phrase != "apply demo" {
		t.Fatalf("destructive plan needs a typed phrase: %+v", approval)
	}
	// Declining is just not confirming: the token expires unused and nothing runs.
	m.ApprovalTTL = time.Millisecond
	approval, _ = approve(m, id)
	time.Sleep(5 * time.Millisecond)
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "approval_invalid" {
		t.Fatalf("expired token: %v", err)
	}
	m.ApprovalTTL = time.Minute
	approval, _ = approve(m, id)
	if _, err := m.StartApply(id, approval.Token, "APPLY DEMO"); rejected(err) != "confirmation_mismatch" {
		t.Fatalf("wrong phrase: %v", err)
	}
	// The token was consumed by the failed attempt.
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "approval_required" {
		t.Fatalf("replayed token: %v", err)
	}
	if _, err := m.StartApply(strings.Repeat("0", 32), "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestApplyRejectsChangedContext(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	approval, _ := approve(m, id)
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(`resource "x" "y" {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "context_changed" {
		t.Fatalf("edited configuration: %v", err)
	}
	if _, err := approve(m, id); rejected(err) != "context_changed" {
		t.Fatalf("approval after edit: %v", err)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestApplyRechecksWorkspaceAndArtifact(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	fake.SetWorkspace("prod")
	done := wait(t, m, approveAndApply(t, m, id).ID)
	if done.Status != Failed || done.Diagnostic.Code != "workspace_changed" || done.Outcome != "not_applied" || done.StateReloaded {
		t.Fatalf("%+v", done)
	}
	fake.SetWorkspace("demo")
	assertTempEmpty(t, m)

	// A file changed before the preflight is caught by the preflight: no binding is issued.
	id = planned(t, m)
	entries, _ := os.ReadDir(m.TempDir)
	tamper := func() {
		if err := os.WriteFile(filepath.Join(m.TempDir, entries[0].Name(), "plan.tfplan"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tamper()
	if _, err := approve(m, id); rejected(err) != "preflight_required" {
		t.Fatalf("%v", err)
	}
	report, _ := m.Preflight(context.Background(), id, PreflightRequest{})
	if !report.Blocked || report.Binding != "" || !failed(report, "plan.artifact") {
		t.Fatalf("%+v", report.Checks)
	}
	m.Close(5 * time.Second)

	// A file changed after the last preflight evaluation is still caught by the run itself,
	// which hashes the file again just before the engine. The Git probe runs after the
	// evaluation hashed the file, so it is used to tamper at exactly that point.
	m, _ = setup(t, fake, workspace.Info{})
	id = planned(t, m)
	entries, _ = os.ReadDir(m.TempDir)
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	m.GitProbe = func(context.Context, string) preflight.GitResult {
		tamper()
		return preflight.GitResult{State: preflight.GitUnavailable}
	}
	op, err := m.StartApply(id, approval.Token, approval.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	done = wait(t, m, op.ID)
	if done.Status != Failed || done.Diagnostic.Code != "artifact_changed" || done.Outcome != "not_applied" {
		t.Fatalf("%+v", done)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
	assertTempEmpty(t, m)
}

func TestApplyFailureOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		err            error
		code, outcome  string
		status         string
		expectReloaded bool
		lines          [][]byte
		blockAndCancel bool
	}{
		"stale":     {err: &engine.ExitError{Code: 1, Hint: "stale_plan"}, code: "stale_plan", outcome: "not_applied", status: Failed, expectReloaded: true},
		"failed":    {err: &engine.ExitError{Code: 1}, code: "apply_failed", outcome: "uncertain", status: Failed, expectReloaded: true},
		"cancelled": {blockAndCancel: true, code: "apply_cancelled", outcome: "uncertain", status: Cancelled, expectReloaded: true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyErr: tc.err, BlockApply: tc.blockAndCancel}
			m, _ := setup(t, fake, workspace.Info{})
			m.Reload = func(context.Context) {}
			id := planned(t, m)
			op := approveAndApply(t, m, id)
			if tc.blockAndCancel {
				waitStage(t, m, op.ID, "Applying")
				if _, err := m.Cancel(op.ID); err != nil {
					t.Fatal(err)
				}
			}
			done := wait(t, m, op.ID)
			if done.Status != tc.status || done.Diagnostic == nil || done.Diagnostic.Code != tc.code || done.Outcome != tc.outcome || done.StateReloaded != tc.expectReloaded {
				t.Fatalf("%+v %+v", done, done.Diagnostic)
			}
			// Never retried, never re-planned: the artifact is gone.
			assertTempEmpty(t, m)
			if applyCalls(fake) != 1 || strings.Count(strings.Join(fake.Calls(), ","), ",plan") != 1 {
				t.Fatalf("calls %v", fake.Calls())
			}
		})
	}
}

func TestOnlyLatestApplicablePlanIsKept(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	first := planned(t, m)
	approval, _ := approve(m, first)
	second := planned(t, m)
	assertArtifactPrivate(t, m)
	if _, err := m.StartApply(first, approval.Token, approval.Phrase); rejected(err) != "approval_required" {
		t.Fatalf("superseded plan: %v", err)
	}
	p, _ := m.Plan(first)
	if _, status := m.ContextStatus(p); status.Available || !strings.Contains(status.Reason, "newer plan") {
		t.Fatalf("%+v", status)
	}
	// Expiry deletes the saved plan.
	m.mu.Lock()
	m.artifact.created = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()
	if _, err := approve(m, second); rejected(err) != "plan_expired" {
		t.Fatalf("expired: %v", err)
	}
	m.Close(time.Second)
	assertTempEmpty(t, m)
}

func TestExpiryTimerDeletesArtifact(t *testing.T) {
	m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	m.PlanMaxAge = 50 * time.Millisecond
	planned(t, m)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if entries, _ := os.ReadDir(m.TempDir); len(entries) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expired artifact not deleted")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIneligiblePlansKeepNoArtifact(t *testing.T) {
	for name, document := range map[string][]byte{"errored": fixtures.PlanErrored, "no changes": fixtures.PlanNoChanges} {
		t.Run(name, func(t *testing.T) {
			m, _ := setup(t, &engine.Fake{PlanJSON: document}, workspace.Info{})
			id := planned(t, m)
			assertTempEmpty(t, m)
			if _, err := approve(m, id); rejected(err) != "not_applicable" {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyBlockedWhileBusy(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	approval, _ := approve(m, id)
	fake.Block = true
	running, _ := m.StartPlan(nil, nil)
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "operation_running" {
		t.Fatalf("concurrent apply: %v", err)
	}
	_, _ = m.Cancel(running.ID)
	wait(t, m, running.ID)
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestReplacePlanRequest(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanCollateral}
	m, _ := setup(t, fake, workspace.Info{})
	snap := m.store.Get()
	var managed, data []string
	for _, r := range snap.Index.Resources {
		if r.Mode == "managed" && strings.Contains(r.Address, "workers") || r.Address == "aws_iam_role.deployer" {
			managed = append(managed, r.ID)
		}
		if r.Mode == "data" {
			data = append(data, r.ID)
		}
	}
	for _, bad := range [][]string{data, {managed[0], managed[0]}, {"aws_iam_role.deployer"}, make([]string, engine.MaxReplace+1)} {
		if _, err := m.StartPlan(nil, bad); !errors.Is(err, ErrReplace) {
			t.Fatalf("%v accepted", bad)
		}
	}
	op, err := m.StartPlan(nil, managed)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
	got := fake.Requests()[0].Replace
	if len(got) != 2 || !strings.Contains(strings.Join(got, " "), `module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]`) {
		t.Fatalf("replace %q", got)
	}
}

// Remote execution keeps plans review-only: no artifact is retained.
func TestRemoteExecutionNeverRetainsArtifact(t *testing.T) {
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "cloud", ExecutionMode: "remote-exec"})
	fake := &engine.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: fixtures.State}
	store.Load(context.Background(), fake, "terraform")
	caps := store.Get().Info.Capabilities
	if caps.CanApply || !strings.Contains(caps.ApplyReason, "Remote execution") {
		t.Fatalf("%+v", caps)
	}
}

func TestSweepStaleRemovesOnlyOldPlanDirectories(t *testing.T) {
	m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	now := time.Now()
	old := now.Add(-(m.PlanMaxAge + m.ApplyTimeout + time.Minute))
	mk := func(name string, mod time.Time) string {
		dir := filepath.Join(m.TempDir, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.tfplan"), []byte(canary), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stale := mk("hclaro-plan-1", old)
	// A live instance may still apply a plan this recent.
	recent := mk("hclaro-plan-2", now.Add(-m.PlanMaxAge))
	other := mk("unrelated-1", old)
	// A symlink named like a plan directory is never followed or removed.
	target := mk("link-target", old)
	link := filepath.Join(m.TempDir, "hclaro-plan-link")
	linked := os.Symlink(target, link) == nil
	if n := m.SweepStale(now); n != 1 {
		t.Fatalf("removed %d", n)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale plan directory kept")
	}
	for _, keep := range []string{recent, other, target} {
		if _, err := os.Stat(filepath.Join(keep, "plan.tfplan")); err != nil {
			t.Fatalf("%s removed: %v", keep, err)
		}
	}
	if linked {
		if _, err := os.Lstat(link); err != nil {
			t.Fatal("symlink removed")
		}
	}
}

func TestSweepStaleKeepsARetainedPlan(t *testing.T) {
	m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if n := m.SweepStale(time.Now()); n != 0 {
		t.Fatalf("removed %d", n)
	}
	p, _ := m.Plan(done.PlanID)
	if _, err := approve(m, p.ID); err != nil {
		t.Fatalf("retained plan no longer applicable: %v", err)
	}
}

// M24.01: a plan with an unsupported action combination is never applied. Destructive
// confirmation cannot bypass the refusal, no saved plan file is kept, and the engine's apply
// never runs.
func TestUnknownActionsFailClosed(t *testing.T) {
	outputOnly := []byte(`{"format_version":"1.2","terraform_version":"1.9.8","planned_values":{"root_module":{}},"applyable":true,"complete":true,"resource_changes":[{"address":"aws_vpc.main","mode":"managed","type":"aws_vpc","name":"main","change":{"actions":["update"],"before":{"id":"vpc-1"},"after":{"id":"vpc-2"}}}],"output_changes":{"vpc":{"actions":["update","delete"],"before":"vpc-1","after":"vpc-2"}}}`)
	for name, document := range map[string][]byte{"resource and output": fixtures.PlanUnknownActions, "output only": outputOnly} {
		t.Run(name, func(t *testing.T) {
			fake := &engine.Fake{PlanJSON: document}
			m, _ := setup(t, fake, workspace.Info{})
			id := planned(t, m)
			assertTempEmpty(t, m)
			p, _ := m.Plan(id)
			if p.Unsupported == 0 || !p.Destructive {
				t.Fatalf("plan %+v", p.Review)
			}
			current, _ := m.currentFingerprint(p)
			if s := m.ApplyStatus(p, current); s.Available || s.Code != "unknown_actions" || s.ArtifactRetained || !strings.Contains(s.Reason, "does not support") {
				t.Fatalf("status %+v", s)
			}
			// Preflight explains the refusal and issues no binding.
			report, err := m.Preflight(context.Background(), id, PreflightRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Blocked || report.Binding != "" {
				t.Fatalf("preflight not blocked: %+v", report)
			}
			found := false
			for _, c := range report.Checks {
				if c.ID == preflight.CheckEligible {
					found = c.Status == preflight.Fail && strings.Contains(c.Evidence, "does not support") && strings.Contains(c.Remediation, "docs/plan-actions.md")
				}
			}
			if !found {
				t.Fatalf("eligibility check: %+v", report.Checks)
			}
			if _, err := m.Approve(id, ApprovalRequest{Binding: "forged", Acknowledged: []string{preflight.CheckDestructive}}); rejected(err) != "unknown_actions" {
				t.Fatalf("approve: %v", err)
			}
			// The destructive typed phrase does not help.
			if _, err := m.StartApply(id, "forged", "apply demo"); rejected(err) != "approval_required" {
				t.Fatalf("apply: %v", err)
			}
			if applyCalls(fake) != 0 {
				t.Fatal("engine apply ran")
			}
		})
	}
}

// A plan whose recorded actions change after approval (for example, a corrupted in-memory
// record) is refused at apply time, and again immediately before the engine runs.
func TestUnknownActionsRecheckedBeforeEngine(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := m.Plan(id)
	m.mu.Lock()
	original := p.Changes[0].Actions
	p.Changes[0].Actions = []string{"update", "delete"}
	m.mu.Unlock()
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "unknown_actions" {
		t.Fatalf("apply: %v", err)
	}
	// The last gate inside the apply runner, reached directly.
	m.mu.Lock()
	a := m.artifact
	m.mu.Unlock()
	if a == nil {
		t.Fatal("artifact not retained")
	}
	e := &entry{op: Operation{ID: "x", PlanID: id}, log: NewLog(logEvents, logBytes)}
	if code, _, started := m.executeApply(context.Background(), e, a); code != "unknown_actions" || started {
		t.Fatalf("executeApply = %s %v", code, started)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
	m.mu.Lock()
	p.Changes[0].Actions = original
	m.mu.Unlock()
	if code, _, started := m.executeApply(context.Background(), e, a); code == "unknown_actions" || !started {
		t.Fatalf("supported plan refused: %s %v", code, started)
	}
}
