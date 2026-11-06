package operations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/profiles"
	"hclaro/internal/workspace"
)

const unflaggedKey = "AKIAABCDEFGHIJKLMNOP"

func scanAndWait(t *testing.T, m *Manager, req DriftStart) (Operation, DriftScanView) {
	t.Helper()
	op, scan, err := m.StartDriftScan(req)
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != "drift" || op.DriftScanID != scan.ID || scan.Status != ScanRunning {
		t.Fatalf("%+v %+v", op, scan)
	}
	done := wait(t, m, op.ID)
	view, ok := m.DriftScan(scan.ID)
	if !ok {
		t.Fatal("scan not stored")
	}
	return done, view
}

// assertNoSecrets checks every serialized result, including details and the log.
func assertNoSecrets(t *testing.T, m *Manager, op Operation, view DriftScanView) {
	t.Helper()
	values := []any{op, view, m.DriftScans()}
	for _, r := range view.Resources {
		if d, err := m.DriftDetail(view.ID, r.ID); err == nil {
			values = append(values, d)
		}
	}
	if _, log, ok := m.Operation(op.ID); ok {
		events, _, _, _ := log.Since(0)
		values = append(values, events)
	}
	assertNoCanary(t, values...)
	b, _ := json.Marshal(values)
	if strings.Contains(string(b), unflaggedKey) {
		t.Fatalf("unflagged credential leaked: %s", b)
	}
}

// Calls never include apply, and the only plan is the refresh-only one.
func assertOnlyRefreshOnly(t *testing.T, fake *engine.Fake) {
	t.Helper()
	for _, call := range fake.Calls() {
		if call == "apply" {
			t.Fatal("a drift scan ran apply")
		}
	}
	requests := fake.Requests()
	if len(requests) != 1 || !requests[0].RefreshOnly || len(requests[0].Replace) != 0 || requests[0].GenerateConfigOut != "" {
		t.Fatalf("plan requests %+v", requests)
	}
}

func TestDriftScanFixtures(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		doc                         []byte
		outcome                     string
		changed, missing, uncertain int
	}{
		{"no drift", fixtures.DriftNone, OutcomeNoDrift, 0, 0, 0},
		{"changed tag", fixtures.DriftChangedTag, OutcomeDrift, 1, 0, 0},
		{"security group", fixtures.DriftSecurityGroup, OutcomeDrift, 1, 0, 0},
		{"deleted remote resource", fixtures.DriftDeleted, OutcomeDrift, 0, 1, 0},
		{"unknown value", fixtures.DriftUnknown, OutcomeCannotVerify, 0, 0, 1},
		{"mixed", fixtures.DriftMixed, OutcomeDrift, 2, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &engine.Fake{Lines: lines(fixtures.DriftLog), PlanJSON: tc.doc}
			m, _ := setup(t, fake, workspace.Info{})
			op, view := scanAndWait(t, m, DriftStart{})
			if op.Status != Succeeded || view.Status != ScanCompleted || !view.Exhaustive || view.Outcome != tc.outcome || !view.Current {
				t.Fatalf("%+v\n%+v", op, view)
			}
			if s := view.Summary; s.Changed != tc.changed || s.Missing != tc.missing || s.CannotVerify != tc.uncertain {
				t.Fatalf("summary %+v", s)
			}
			if view.Workspace != "demo" || view.Engine != "terraform" || view.EngineVersion != "1.9.8" || view.CompletedAt == "" {
				t.Fatalf("metadata %+v", view)
			}
			assertNoSecrets(t, m, op, view)
			assertOnlyRefreshOnly(t, fake)
			assertTempEmpty(t, m)
			// A scan is never a reviewable or applicable plan, and completion starts nothing.
			if len(m.Plans()) != 0 || m.artifact != nil {
				t.Fatal("a drift scan left a plan or retained artifact")
			}
			if ops := m.Operations(); len(ops) != 1 {
				t.Fatalf("scan completion started another operation: %+v", ops)
			}
			if !slices.Equal(op.Command, []string{"terraform", "plan", "-input=false", "-json", "-out=<private temporary file>", "-refresh-only"}) {
				t.Fatalf("command %v", op.Command)
			}
		})
	}
}

func TestDriftDeepLinksOnlyRecordedObjects(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftMixed}
	m, _ := setup(t, fake, workspace.Info{})
	_, view := scanAndWait(t, m, DriftStart{})
	links := map[string]string{}
	for _, r := range view.Resources {
		links[r.Address] = r.ResourceID
	}
	// The bucket and the instance are in the state fixture; the security group is not.
	if links["aws_s3_bucket.artifacts"] == "" || links["module.compute.aws_instance.main[3]"] == "" || links["aws_security_group.web"] != "" {
		t.Fatalf("%v", links)
	}
	if _, ok := m.store.Get().Index.ByID[links["aws_s3_bucket.artifacts"]]; !ok {
		t.Fatal("deep link does not resolve")
	}
	for _, r := range view.Resources {
		if r.Address == "aws_security_group.web" {
			d, err := m.DriftDetail(view.ID, r.ID)
			if err != nil || len(d.Attributes) == 0 {
				t.Fatal(err)
			}
		}
	}
	if _, err := m.DriftDetail(view.ID, strings.Repeat("0", 32)); !errors.Is(err, ErrScanNotFound) {
		t.Fatal(err)
	}
}

// Truthful non-success: no outcome, no zero-drift claim.
func TestDriftScanFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fake   *engine.Fake
		status string
		code   string
	}{
		{"backend lock conflict", &engine.Fake{Lines: lines(fixtures.DriftLogLocked), PlanErr: &engine.ExitError{Code: 1}}, ScanFailed, "state_locked"},
		{"expired SSO", &engine.Fake{Lines: lines(fixtures.DriftLogSSO), PlanErr: &engine.ExitError{Code: 1}}, ScanFailed, "credentials"},
		{"partial provider failure", &engine.Fake{Lines: lines(fixtures.DriftLogDenied), PlanErr: &engine.ExitError{Code: 1}, PlanJSON: fixtures.DriftInaccessible, WriteOnError: true}, ScanPartial, "credentials"},
		{"partial without plan file", &engine.Fake{Lines: lines(fixtures.DriftLogDenied), PlanErr: &engine.ExitError{Code: 1}}, ScanFailed, "credentials"},
		{"not a refresh-only result", &engine.Fake{PlanJSON: fixtures.DriftNotRefreshOnly}, ScanFailed, "not_refresh_only"},
		{"undecodable result", &engine.Fake{PlanJSON: fixtures.State}, ScanFailed, "plan_format"},
		{"show fails", &engine.Fake{PlanJSON: fixtures.DriftNone, ShowErr: errors.New("x")}, ScanFailed, "plan_read"},
		{"errored but exit 0", &engine.Fake{PlanJSON: fixtures.DriftInaccessible}, ScanPartial, "plan_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := setup(t, tc.fake, workspace.Info{})
			op, view := scanAndWait(t, m, DriftStart{})
			if view.Status != tc.status || view.Outcome != "" || view.Exhaustive || op.Status == Succeeded {
				t.Fatalf("%+v", view)
			}
			if view.Diagnostic == nil || view.Diagnostic.Code != tc.code || op.Diagnostic == nil || op.Diagnostic.Code != tc.code {
				t.Fatalf("diagnostic %+v", view.Diagnostic)
			}
			if tc.status == ScanPartial && (view.Summary.Changed != 1 || len(view.Resources) != 1) {
				t.Fatalf("partial result lost its records: %+v", view)
			}
			if tc.status == ScanFailed && len(view.Resources) != 0 {
				t.Fatalf("failed scan has resources: %+v", view.Resources)
			}
			assertNoSecrets(t, m, op, view)
			assertOnlyRefreshOnly(t, tc.fake)
			assertTempEmpty(t, m)
		})
	}
}

func TestDriftScanCancel(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftNone, Block: true}
	m, _ := setup(t, fake, workspace.Info{})
	op, scan, err := m.StartDriftScan(DriftStart{})
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, m, op.ID, "Scanning")
	// One operation at a time: a plan or a second scan is refused while scanning.
	if _, _, err := m.StartDriftScan(DriftStart{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if running, _ := m.DriftScan(scan.ID); running.Status != ScanRunning || !running.Current {
		t.Fatalf("%+v", running)
	}
	if _, err := m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	view, _ := m.DriftScan(scan.ID)
	if done.Status != Cancelled || view.Status != ScanCancelled || view.Outcome != "" || view.Diagnostic.Code != "cancelled" {
		t.Fatalf("%+v %+v", done, view)
	}
	assertTempEmpty(t, m)
}

func TestDriftScanShutdown(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftNone, Block: true}
	m, _ := setup(t, fake, workspace.Info{})
	op, scan, _ := m.StartDriftScan(DriftStart{})
	waitStage(t, m, op.ID, "Scanning")
	m.Close(5 * time.Second)
	view, _ := m.DriftScan(scan.ID)
	if view.Status != ScanCancelled || view.Diagnostic.Code != "shutdown" {
		t.Fatalf("%+v", view)
	}
	if _, _, err := m.StartDriftScan(DriftStart{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestDriftUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		info workspace.Info
		fake *engine.Fake
		want string
	}{
		{"remote execution", workspace.Info{Project: "x", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"}, &engine.Fake{}, "remote"},
		{"unknown backend", workspace.Info{Project: "x", Initialized: true, Backend: "unknown", ExecutionMode: "unsupported"}, &engine.Fake{}, "refresh-only plan"},
		{"no state", workspace.Info{}, &engine.Fake{StateJSON: fixtures.Empty}, "No state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := setup(t, tc.fake, tc.info)
			ok, reason := m.DriftCapability()
			if ok || !strings.Contains(reason, tc.want) {
				t.Fatalf("%v %q", ok, reason)
			}
			_, _, err := m.StartDriftScan(DriftStart{})
			var rejected *RejectError
			if !errors.As(err, &rejected) || rejected.Code != "drift_unsupported" {
				t.Fatalf("%v", err)
			}
			if slices.Contains(tc.fake.Calls(), "plan") || len(m.DriftScans()) != 0 {
				t.Fatal("unsupported scan ran or was recorded")
			}
		})
	}
	loading := workspace.New(workspace.Info{Project: "x", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	if _, _, err := NewManager(loading, &engine.Fake{}, t.TempDir()).StartDriftScan(DriftStart{}); !errors.Is(err, ErrLoading) {
		t.Fatal(err)
	}
	if _, _, err := NewManager(loading, nil, "").StartDriftScan(DriftStart{}); err == nil {
		t.Fatal("scan accepted without a planner")
	}
}

// A scan of workspace A never returns its resources once the project reads workspace B, and
// a context change withholds the result.
func TestDriftWorkspaceIsolationAndInvalidation(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftMixed}
	m, root := setup(t, fake, workspace.Info{})
	_, view := scanAndWait(t, m, DriftStart{})
	if !view.Current || len(view.Resources) != 4 {
		t.Fatalf("%+v", view)
	}
	changeID := view.Resources[0].ID

	// Configuration edited after the scan.
	writeFile(t, root, "main.tf", "# edited\n")
	stale, _ := m.DriftScan(view.ID)
	if stale.Current || !stale.Withheld || len(stale.Resources) != 0 || !strings.Contains(stale.StaleReason, "Configuration") {
		t.Fatalf("%+v", stale)
	}
	if _, err := m.DriftDetail(view.ID, changeID); !errors.Is(err, ErrScanStale) {
		t.Fatal(err)
	}
	if err := os.Remove(root + "/main.tf"); err != nil {
		t.Fatal(err)
	}
	if again, _ := m.DriftScan(view.ID); !again.Current {
		t.Fatalf("restored context still stale: %+v", again)
	}

	// The project is read again in another workspace.
	fake.SetWorkspace("other")
	m.store.Load(context.Background(), fake, "terraform")
	other, _ := m.DriftScan(view.ID)
	if other.Current || len(other.Resources) != 0 || !strings.Contains(other.StaleReason, "workspace demo") {
		t.Fatalf("%+v", other)
	}
	if _, err := m.DriftDetail(view.ID, changeID); !errors.Is(err, ErrScanStale) {
		t.Fatal(err)
	}
	for _, s := range m.DriftScans() {
		if len(s.Resources) != 0 {
			t.Fatal("list returned another workspace's resources")
		}
	}
	// A scan in the new workspace is its own record.
	_, second := scanAndWaitN(t, m)
	if second.Workspace != "other" || !second.Current {
		t.Fatalf("%+v", second)
	}
	if first, _ := m.DriftScan(view.ID); first.Current {
		t.Fatal("first scan became current")
	}
}

func scanAndWaitN(t *testing.T, m *Manager) (Operation, DriftScanView) {
	t.Helper()
	op, scan, err := m.StartDriftScan(DriftStart{})
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	view, _ := m.DriftScan(scan.ID)
	return done, view
}

func TestDriftScanWithProfile(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftChangedTag}
	m, root, store, rec := profileSetup(t, fake, []string{"PATH=/bin", "AWS_PROFILE=inherited"})
	writeFile(t, root, "env/prod.tfvars", "region = \"eu-central-1\"\n")
	p, err := store.Create(testProject, profiles.Input{Name: "prod", VarFiles: []string{"env/prod.tfvars"}, AWSProfile: "demo-profile"})
	if err != nil {
		t.Fatal(err)
	}
	// Unreviewed or mismatched contexts are refused.
	if _, _, err := m.StartDriftScan(DriftStart{ProfileID: p.ID, ContextFingerprint: "x"}); err == nil {
		t.Fatal("unreviewed context accepted")
	}
	fp := reviewedFingerprint(t, m, p.ID)
	op, scan, err := m.StartDriftScan(DriftStart{ProfileID: p.ID, ContextFingerprint: fp, VarFiles: []string{"env/prod.tfvars"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
	view, _ := m.DriftScan(scan.ID)
	if view.Context == nil || view.Context.ProfileID != p.ID || !view.Current || view.Status != ScanCompleted {
		t.Fatalf("%+v", view)
	}
	if env, ws := rec.last(); ws != "demo" || !slices.Contains(env, "AWS_PROFILE=demo-profile") {
		t.Fatalf("bound env %v %s", env, ws)
	}
	if r := fake.Requests(); !r[0].RefreshOnly || !slices.Equal(r[0].VarFiles, []string{"env/prod.tfvars"}) {
		t.Fatalf("%+v", r)
	}
	// Editing the profile invalidates the scan.
	if _, err := store.Update(testProject, p.ID, p.Revision, profiles.Input{Name: "prod", AWSProfile: "other-profile"}); err != nil {
		t.Fatal(err)
	}
	if stale, _ := m.DriftScan(scan.ID); stale.Current || !strings.Contains(stale.StaleReason, "execution profile") {
		t.Fatalf("%+v", stale)
	}
}

func TestDriftRetentionIsBounded(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.DriftNone}
	m, _ := setup(t, fake, workspace.Info{})
	for i := 0; i < maxScans+3; i++ {
		scanAndWaitN(t, m)
	}
	if n := len(m.DriftScans()); n != maxScans {
		t.Fatalf("%d scans kept", n)
	}
	m.mu.Lock()
	m.scans[0].started = time.Now().Add(-DriftMaxAge - time.Minute)
	m.mu.Unlock()
	if n := len(m.DriftScans()); n != maxScans-1 {
		t.Fatalf("expired scan kept: %d", n)
	}
}

func TestSweepIncludesDriftDirectories(t *testing.T) {
	m, _ := setup(t, &engine.Fake{}, workspace.Info{})
	old := time.Now().Add(-(m.PlanMaxAge + m.ApplyTimeout + time.Hour))
	dir, _ := os.MkdirTemp(m.TempDir, "hclaro-drift-")
	_ = os.Chtimes(dir, old, old)
	if m.SweepStale(time.Now()) != 1 {
		t.Fatal("stale drift directory kept")
	}
}
