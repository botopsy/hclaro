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
	approval, err := m.Approve(id)
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
	if status := m.ApplyStatus(p, m.currentFingerprint(p)); status.Available || status.ArtifactRetained {
		t.Fatalf("applied plan still applicable: %+v", status)
	}
	if _, err := m.Approve(id); rejected(err) != "not_applicable" {
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
	approval, _ := m.Approve(id)
	if approval.Phrase != "apply demo" {
		t.Fatalf("destructive plan needs a typed phrase: %+v", approval)
	}
	// Declining is just not confirming: the token expires unused and nothing runs.
	m.ApprovalTTL = time.Millisecond
	approval, _ = m.Approve(id)
	time.Sleep(5 * time.Millisecond)
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "approval_invalid" {
		t.Fatalf("expired token: %v", err)
	}
	m.ApprovalTTL = time.Minute
	approval, _ = m.Approve(id)
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
	approval, _ := m.Approve(id)
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(`resource "x" "y" {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); rejected(err) != "context_changed" {
		t.Fatalf("edited configuration: %v", err)
	}
	if _, err := m.Approve(id); rejected(err) != "context_changed" {
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

	id = planned(t, m)
	entries, _ := os.ReadDir(m.TempDir)
	if err := os.WriteFile(filepath.Join(m.TempDir, entries[0].Name(), "plan.tfplan"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	done = wait(t, m, approveAndApply(t, m, id).ID)
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
	approval, _ := m.Approve(first)
	second := planned(t, m)
	assertArtifactPrivate(t, m)
	if _, err := m.StartApply(first, approval.Token, approval.Phrase); rejected(err) != "approval_required" {
		t.Fatalf("superseded plan: %v", err)
	}
	p, _ := m.Plan(first)
	if status := m.ApplyStatus(p, m.currentFingerprint(p)); status.Available || !strings.Contains(status.Reason, "newer plan") {
		t.Fatalf("%+v", status)
	}
	// Expiry deletes the saved plan.
	m.mu.Lock()
	m.artifact.created = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()
	if _, err := m.Approve(second); rejected(err) != "plan_expired" {
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
			if _, err := m.Approve(id); rejected(err) != "not_applicable" {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyBlockedWhileBusy(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	approval, _ := m.Approve(id)
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
