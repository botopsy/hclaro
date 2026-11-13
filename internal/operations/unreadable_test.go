package operations

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

// lockDir makes a configuration subdirectory unreadable until the test ends.
func lockDir(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	dir := filepath.Join(root, "locked")
	writeFile(t, root, "locked/main.tf", "# module\n")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func logText(t *testing.T, m *Manager, id string) string {
	t.Helper()
	_, log, _ := m.Operation(id)
	events, _, _, _ := log.Since(0)
	text := ""
	for _, e := range events {
		text += e.Text + "\n"
	}
	return text
}

func TestUnreadableConfigurationAfterPlanIsItsOwnReason(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	lockDir(t, root)
	_, err := approve(m, id)
	if rejected(err) != "configuration_unreadable" || !strings.Contains(err.Error(), `"locked"`) || strings.Contains(err.Error(), root) {
		t.Fatalf("approve: %v", err)
	}
	p, _ := m.Plan(id)
	if _, status := m.ContextStatus(p); status.Available || status.Code != "configuration_unreadable" {
		t.Fatalf("%+v", status)
	}
	checks := preflight.Evaluate(m.facts(context.Background(), p, notRequested()))
	i := slices.IndexFunc(checks, func(c preflight.Check) bool { return c.ID == preflight.CheckFingerprint })
	if checks[i].Status != preflight.Fail || !strings.Contains(checks[i].Evidence, "could not read") {
		t.Fatalf("%+v", checks[i])
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestUnreadableConfigurationAtPlanTimeIsReviewableNotApplicable(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	lockDir(t, root)
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v", done)
	}
	if text := logText(t, m, op.ID); !strings.Contains(text, `"locked" could not be read`) || strings.Contains(text, root) {
		t.Fatalf("log: %s", text)
	}
	// Restoring access does not make the plan applicable: it was never tied to a configuration.
	_ = os.Chmod(filepath.Join(root, "locked"), 0o755)
	p, _ := m.Plan(done.PlanID)
	if _, status := m.ContextStatus(p); status.Available || status.Code != "configuration_unreadable" {
		t.Fatalf("%+v", status)
	}
}

func TestUnreadableConfigurationFailsADriftScanBeforeTheEngine(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftMixed}
	m, root := setup(t, fake, workspace.Info{})
	_, view := scanAndWait(t, m, DriftStart{})
	lockDir(t, root)
	if stale, _ := m.DriftScan(view.ID); stale.Current || !strings.Contains(stale.StaleReason, "could not read") {
		t.Fatalf("%+v", stale)
	}
	calls := len(fake.Calls())
	op, _, err := m.StartDriftScan(DriftStart{})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Failed || done.Diagnostic == nil || done.Diagnostic.Code != "configuration_unreadable" {
		t.Fatalf("%+v", done)
	}
	if slices.Contains(fake.Calls()[calls:], "plan") {
		t.Fatal("engine ran")
	}
	if text := logText(t, m, op.ID); !strings.Contains(text, `"locked"`) {
		t.Fatalf("log: %s", text)
	}
}

func TestUnreadableConfigurationRefusesAtTheApplyTimeRecheck(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	writeFile(t, root, "locked/main.tf", "# module\n")
	dir := filepath.Join(root, "locked")
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	id := planned(t, m)
	// The Verify hook runs with the claim held, after StartApply checked the fingerprint.
	m.Verify = func(ctx context.Context, kind string) *Diagnostic {
		if kind == "apply" {
			if err := os.Chmod(dir, 0); err != nil {
				t.Error(err)
			}
		}
		return nil
	}
	done := wait(t, m, approveAndApply(t, m, id).ID)
	if done.Status != Failed || done.Diagnostic == nil || done.Diagnostic.Code != "configuration_unreadable" || done.Outcome != "not_applied" {
		t.Fatalf("%+v %+v", done, done.Diagnostic)
	}
	if text := logText(t, m, done.ID); !strings.Contains(text, `"locked"`) {
		t.Fatalf("log: %s", text)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
	assertTempEmpty(t, m)
}

func TestUnreadableReasonSurvivesAnEarlierRefusal(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	planned(t, m) // supersedes id: its saved file is no longer kept
	lockDir(t, root)
	p, _ := m.Plan(id)
	if _, status := m.ContextStatus(p); status.Code != "not_applicable" || !strings.Contains(status.ConfigurationReason, `"locked"`) {
		t.Fatalf("%+v", status)
	}
}

func TestUnreadableConfigurationRefusesAGraph(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform}
	m, root := setup(t, fake, workspace.Info{})
	lockDir(t, root)
	done, _ := graphAndWait(t, m, GraphStart{})
	if done.Diagnostic == nil || done.Diagnostic.Code != "configuration_unreadable" || slices.Contains(fake.Calls(), "graph") {
		t.Fatalf("%+v %+v", done, fake.Calls())
	}
}
