package operations

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/history"
	"hclaro/internal/workspace"
)

func TestDiscardDeletesTheSavedPlan(t *testing.T) {
	m, _ := setup(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	rec := &records{}
	m.Recorder = rec
	id := planned(t, m)
	if err := m.Discard(id); err != nil {
		t.Fatal(err)
	}
	assertTempEmpty(t, m)
	p, _ := m.Plan(id)
	st := m.ApplyStatus(p, "")
	if st.Available || st.ArtifactRetained || st.Code != "not_applicable" || !strings.Contains(st.Reason, "You discarded this plan.") || !strings.Contains(st.Reason, "was deleted") {
		t.Fatalf("%+v", st)
	}
	// The plan stays reviewable, but nothing can approve or discard it again.
	if _, err := approve(m, id); rejected(err) != "not_applicable" {
		t.Fatalf("approve: %v", err)
	}
	if err := m.Discard(id); rejected(err) != "not_applicable" {
		t.Fatalf("second discard: %v", err)
	}
	if err := m.Discard("0123456789abcdef0123456789abcdef"); err != ErrNotFound {
		t.Fatalf("unknown plan: %v", err)
	}
	var discards []history.Event
	for _, e := range rec.all() {
		if e.Kind == "discard" {
			discards = append(discards, e)
		}
	}
	if len(discards) != 1 || discards[0].PlanID != id || !discards[0].Final || len(discards[0].Command) != 0 || discards[0].Excerpt != nil {
		t.Fatalf("history %+v", discards)
	}
}

func TestApprovalIssuedBeforeDiscardIsRefusedAtApply(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	id := planned(t, m)
	approval, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Discard(id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartApply(id, approval.Token, approval.Phrase); err == nil || rejected(err) == "" {
		t.Fatalf("apply after discard: %v", err)
	}
	if applyCalls(fake) != 0 {
		t.Fatalf("the engine applied a discarded plan: %v", fake.Calls())
	}
}

func TestDiscardIsRefusedDuringAnApply(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions, BlockApply: true}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	id := planned(t, m)
	op := approveAndApply(t, m, id)
	waitStage(t, m, op.ID, "Applying")
	if err := m.Discard(id); rejected(err) != "operation_running" {
		t.Fatalf("discard during apply: %v", err)
	}
	if _, err := m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
}

func TestDiscardReportsAFileThatCouldNotBeDeleted(t *testing.T) {
	skipUnenforcedPermissions(t)
	m, _ := setup(t, &enginetest.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	restoreTemp(t, m)
	id := planned(t, m)
	m.mu.Lock()
	dir := m.artifact.dir
	m.mu.Unlock()
	readOnly(t, dir)
	if err := m.Discard(id); err != nil {
		t.Fatal(err)
	}
	p, _ := m.Plan(id)
	st := m.ApplyStatus(p, "")
	assertReportedLeft(t, &terminal, st.Reason)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the folder should still be there")
	}
}
