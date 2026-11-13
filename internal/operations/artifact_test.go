package operations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

// readOnly makes dir undeletable until the test ends: its files cannot be unlinked.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// lockingPlanner makes the saved plan's folder read-only when the plan is read back, so the
// cleanup that follows fails.
type lockingPlanner struct {
	*engine.Fake
	t *testing.T
}

func (p lockingPlanner) ShowPlan(ctx context.Context, path string) ([]byte, error) {
	readOnly(p.t, filepath.Dir(path))
	return p.Fake.ShowPlan(ctx, path)
}

func assertReportedLeft(t *testing.T, terminal *bytes.Buffer, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if strings.Contains(text, "was deleted") || strings.Contains(text, "deleted the saved plan") || !strings.Contains(text, leftNote) {
			t.Errorf("a failed deletion is not reported as such: %q", text)
		}
	}
	if !strings.Contains(terminal.String(), "could not delete a saved plan folder") {
		t.Errorf("terminal: %q", terminal.String())
	}
}

func TestFailedApplyCleanupIsNotReportedAsDeleted(t *testing.T) {
	m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	id := planned(t, m)
	m.mu.Lock()
	dir := m.artifact.dir
	m.mu.Unlock()
	readOnly(t, dir)
	done := wait(t, m, approveAndApply(t, m, id).ID)
	if done.Status != Succeeded || !done.PlanFileLeft {
		t.Fatalf("%+v", done)
	}
	p, _ := m.Plan(id)
	_, status := m.ContextStatus(p)
	if status.Available {
		t.Fatal("an applied plan whose file was left is applicable again")
	}
	assertReportedLeft(t, &terminal, status.Reason, logText(t, m, done.ID))
	if !strings.Contains(terminal.String(), dir) {
		t.Errorf("terminal does not name the folder: %q", terminal.String())
	}
}

func TestFailedApplyRefusalCleanupIsNotReportedAsDeleted(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	id := planned(t, m)
	m.mu.Lock()
	readOnly(t, m.artifact.dir)
	m.mu.Unlock()
	// The workspace changes after approval: the run refuses before the engine applies.
	fake.SetWorkspace("prod")
	done := wait(t, m, approveAndApply(t, m, id).ID)
	if done.Diagnostic == nil || done.Diagnostic.Code != "workspace_changed" || !done.PlanFileLeft {
		t.Fatalf("%+v %+v", done, done.Diagnostic)
	}
	assertReportedLeft(t, &terminal, done.Diagnostic.Message)
}

func TestFailedPlanCleanupIsNotReportedAsDeleted(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanNoChanges}
	m, _ := setup(t, fake, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	m.planner = lockingPlanner{fake, t}
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
	assertReportedLeft(t, &terminal, logText(t, m, done.ID))
}

func TestFailedDriftCleanupIsNotReportedAsDeleted(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftMixed}
	m, _ := setup(t, fake, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	m.planner = lockingPlanner{fake, t}
	op, _ := scanAndWait(t, m, DriftStart{})
	assertReportedLeft(t, &terminal, logText(t, m, op.ID))
}

func TestCloseKeepsTheArtifactAnApplyIsReading(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, BlockApply: true, ApplyStopDelay: 300 * time.Millisecond}
	m, _ := setup(t, fake, workspace.Info{})
	var terminal bytes.Buffer
	m.Terminal = &terminal
	op := approveAndApply(t, m, planned(t, m))
	waitStage(t, m, op.ID, "Applying")
	m.mu.Lock()
	path := m.artifact.path
	m.mu.Unlock()
	// Close(0), as a Hub scope closes, returns while the engine is still stopping.
	m.Close(0)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Close deleted the plan file an apply is reading: %v", err)
	}
	if !strings.Contains(terminal.String(), filepath.Dir(path)) {
		t.Errorf("terminal: %q", terminal.String())
	}
	wait(t, m, op.ID)
	assertTempEmpty(t, m)
}
