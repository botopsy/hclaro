package projects

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/profiles"
)

// Tests for execution scopes (M24.04, ADR M24.03). All engine commands run the synthetic
// CLI (scripts/fake-engine) in temporary roots; nothing contacts a real backend.

// scopedHub is hubFixture with scoped execution enabled. The spy also logs each child's
// working directory and AWS_PROFILE.
func scopedHub(t *testing.T) (*Hub, map[string]string, string) {
	t.Helper()
	hub, ids, spy := hubFixture(t)
	t.Setenv("HCLARO_FAKE_SPY_ENV", "CWD,AWS_PROFILE")
	for _, key := range []string{"HCLARO_FAKE_PLAN", "HCLARO_FAKE_APPLY", "HCLARO_FAKE_WORKSPACE"} {
		t.Setenv(key, "")
	}
	engines := hub.Engines
	hub.ScopedExecution = true
	hub.TempDir = t.TempDir()
	hub.Executors = func(root, preference, selected string, environ []string) (string, string, engine.Planner, error) {
		name, executable, adapter, err := engines(root, preference, selected, environ)
		if err != nil {
			return "", "", nil, err
		}
		planner, ok := adapter.(engine.Planner)
		if !ok {
			return "", "", nil, errors.New("not a planner")
		}
		return name, executable, planner, nil
	}
	return hub, ids, spy
}

func rootOf(t *testing.T, hub *Hub, id string) string {
	t.Helper()
	s, err := hub.Summary(id)
	if err != nil {
		t.Fatal(err)
	}
	return s.RootPath
}

// openScope opens a read context, waits for its read and creates its execution scope.
func openScope(t *testing.T, hub *Hub, project, ws, profileID string) (*Context, *Scope) {
	t.Helper()
	c, _, err := hub.OpenWith(project, ws, profileID, false)
	if err != nil {
		t.Fatal(err)
	}
	snap := waitLoaded(t, c)
	if !snap.Info.Capabilities.CanPlan {
		t.Fatalf("scoped context cannot plan: %s %+v", snap.Status, snap.Info.Capabilities)
	}
	s, err := hub.Execution(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func waitDone(t *testing.T, s *Scope, id string) operations.Operation {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if op, _, ok := s.Operation(id); ok && op.Status != operations.Running {
			return op
		}
	}
	t.Fatal("operation did not finish")
	return operations.Operation{}
}

func waitStage(t *testing.T, s *Scope, id, stage string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if op, _, _ := s.Operation(id); op.Stage == stage {
			return
		}
	}
	t.Fatalf("operation did not reach stage %q", stage)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}

// planDirs lists the private saved-plan directories left in dir.
func planDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hclaro-plan-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// spyCalls returns the spy lines of one engine subcommand as their NUL-separated fields.
func spyCalls(t *testing.T, spy, subcommand string) [][]string {
	t.Helper()
	out := [][]string{}
	for _, line := range spyLines(t, spy) {
		fields := strings.Split(line, "\x00")
		for _, f := range fields {
			if strings.Contains(f, "=") {
				continue
			}
			if f == subcommand {
				out = append(out, fields)
			}
			break
		}
	}
	return out
}

func field(fields []string, prefix string) string {
	for _, f := range fields {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix)
		}
	}
	return ""
}

func scopeErr(t *testing.T, err error, code, detail string) {
	t.Helper()
	var se *ScopeError
	if !errors.As(err, &se) || se.Code != code || se.Detail != detail {
		t.Fatalf("got %v, want %s/%s", err, code, detail)
	}
	// Fixtures live under the temp directory: no path of theirs may appear in a message.
	temp, _ := filepath.EvalSymlinks(os.TempDir())
	for _, prefix := range []string{os.TempDir(), temp} {
		if prefix != "" && strings.Contains(se.Message, prefix) {
			t.Fatalf("scope message names a path: %q", se.Message)
		}
	}
}

func TestScopedExecutionIsOffByDefault(t *testing.T) {
	hub, ids, _ := hubFixture(t)
	if hub.ScopedExecution {
		t.Fatal("scoped execution must be off unless enabled at startup")
	}
	c, _, err := hub.Open(ids["network"], "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	snap := waitLoaded(t, c)
	if caps := snap.Info.Capabilities; caps.CanPlan || caps.CanApply || caps.CanImport || caps.PlanReason != ReadOnlyReason {
		t.Fatalf("Hub contexts must stay read-only: %+v", caps)
	}
	_, err = hub.Execution(c.ID)
	scopeErr(t, err, CodeScopedDisabled, "")
}

func TestScopesIsolateRootsWithTheSameWorkspaceName(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	network, platform := rootOf(t, hub, ids["network"]), rootOf(t, hub, ids["platform"])
	_, a := openScope(t, hub, ids["network"], "default", "")
	_, b := openScope(t, hub, ids["platform"], "default", "")
	if v := a.View(); v.RootPath != network || v.Workspace != "default" || v.Engine != "terraform" || v.ExecutionMode != "local-cli" || v.Profile != nil {
		t.Fatalf("%+v", v)
	}
	opA, err := a.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, a, opA.ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	opB, err := b.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, b, opB.ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
	planA, planB := a.Plans(), b.Plans()
	if len(planA) != 1 || len(planB) != 1 || planA[0].Engine != "terraform" || planB[0].Engine != "tofu" {
		t.Fatalf("%d %d", len(planA), len(planB))
	}
	// IDs of another scope are indistinguishable from missing ones.
	if _, ok := a.Plan(planB[0].ID); ok {
		t.Fatal("plan of another scope found")
	}
	if _, _, ok := b.Operation(opA.ID); ok {
		t.Fatal("operation of another scope found")
	}
	if _, err := b.Cancel(opA.ID); !errors.Is(err, operations.ErrNotFound) {
		t.Fatalf("cancel across scopes: %v", err)
	}
	if len(a.Operations()) != 1 || len(b.Operations()) != 1 {
		t.Fatal("operations shared between scopes")
	}
	plans := spyCalls(t, spy, "plan")
	if len(plans) != 2 {
		t.Fatalf("%q", plans)
	}
	for i, root := range []string{network, platform} {
		cwd, _ := filepath.EvalSymlinks(field(plans[i], "CWD="))
		if cwd != root || field(plans[i], "TF_WORKSPACE=") != "default" {
			t.Fatalf("plan %d ran in %q workspace %q", i, cwd, field(plans[i], "TF_WORKSPACE="))
		}
	}
	// Each scope keeps its own private saved plan.
	if dirs := planDirs(t, hub.TempDir); len(dirs) != 2 {
		t.Fatalf("%v", dirs)
	}
}

func TestScopeRefusesClientTargets(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	if _, err := hub.Execution("../../etc"); !errors.Is(err, ErrContextNotFound) {
		t.Fatalf("%v", err)
	}
	_, s := openScope(t, hub, ids["network"], "dev", "")
	before := len(spyLines(t, spy))
	for _, files := range [][]string{{"../outside.tfvars"}, {"/etc/passwd"}, {"-target=aws_s3_bucket.logs"}, {"a.tfvars; rm -rf /"}} {
		if _, err := s.startPlan(files, nil); !errors.Is(err, operations.ErrVarFiles) {
			t.Fatalf("%q: %v", files, err)
		}
	}
	for _, replace := range [][]string{{"aws_s3_bucket.logs"}, {"$(id)"}, {"-destroy"}} {
		if _, err := s.startPlan(nil, replace); !errors.Is(err, operations.ErrReplace) {
			t.Fatalf("%q: %v", replace, err)
		}
	}
	// A request cannot pick another execution profile than the scope's.
	if _, err := s.manager.StartPlanWith(operations.PlanStart{ProfileID: strings.Repeat("a", 32), ContextFingerprint: "x"}); err == nil || !strings.Contains(err.Error(), "another execution profile") {
		t.Fatalf("%v", err)
	}
	if after := len(spyLines(t, spy)); after != before {
		t.Fatal("a refused request ran the engine")
	}
}

func TestSameRootSerializesAcrossWorkspacesAndLaunch(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	root := rootOf(t, hub, ids["network"])
	_, dev := openScope(t, hub, ids["network"], "dev", "")
	_, prod := openScope(t, hub, ids["network"], "prod", "")
	_, other := openScope(t, hub, ids["platform"], "default", "")
	// The launch manager shares the same claims: while it plans, no scope of that root can.
	release, ok := hub.Locks.Acquire(root, "plan")
	if !ok {
		t.Fatal("root unexpectedly claimed")
	}
	if _, err := dev.startPlan(nil, nil); !errors.Is(err, operations.ErrBusy) {
		t.Fatalf("%v", err)
	}
	release()
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	op, err := dev.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, dev, op.ID, "Planning")
	if holder, _ := hub.Locks.Holder(root); holder != "plan" {
		t.Fatalf("holder %q", holder)
	}
	if _, err := prod.startPlan(nil, nil); !errors.Is(err, operations.ErrBusy) {
		t.Fatalf("second workspace of the same root: %v", err)
	}
	if _, _, err := hub.Open(ids["network"], "default", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("read during a plan: %v", err)
	}
	if _, err := hub.Inspect(context.Background(), ids["network"]); !errors.Is(err, ErrBusy) {
		t.Fatalf("inspection during a plan: %v", err)
	}
	// Another root is not blocked.
	if err := other.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hub.Open(ids["platform"], "staging", false); err != nil {
		t.Fatalf("other root: %v", err)
	}
	if _, err := dev.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, dev, op.ID); done.Status != operations.Cancelled {
		t.Fatalf("%+v", done)
	}
	if _, busy := hub.Locks.Holder(root); busy {
		t.Fatal("claim not released after cancel")
	}
	t.Setenv("HCLARO_FAKE_PLAN", "")
	op, err = prod.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, prod, op.ID); done.Status != operations.Succeeded {
		t.Fatalf("%+v", done)
	}
}

func TestHubReadsHoldASharedClaim(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	root := rootOf(t, hub, ids["network"])
	// The read blocks in `workspace list` until the context is closed, so it is still
	// loading below however fast the machine is.
	t.Setenv("HCLARO_FAKE_WORKSPACE", "slow-list")
	c, _, err := hub.Open(ids["network"], "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	if hub.Locks.Readers(root) == 0 {
		t.Fatal("read started without a shared claim")
	}
	// Two reads of one root may run together; an exclusive operation may not.
	if _, _, err := hub.Open(ids["network"], "prod", false); err != nil {
		t.Fatalf("parallel read: %v", err)
	}
	if _, ok := hub.Locks.Acquire(root, "init"); ok {
		t.Fatal("init could start while the root was being read")
	}
	if snap := c.Snapshot(); snap.Status != "loading" {
		t.Fatalf("%s", snap.Status)
	}
	hub.mu.Lock()
	for cid := range hub.contexts {
		hub.closeLocked(cid, "test")
	}
	hub.mu.Unlock()
	eventually(t, "shared claims not released after the reads ended", func() bool { return hub.Locks.Readers(root) == 0 })
	release, ok := hub.Locks.Acquire(root, "init")
	if !ok {
		t.Fatal("root still claimed")
	}
	release()
}

func TestScopeIdentityChangesFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		change func(t *testing.T, hub *Hub, root, exe string)
	}{
		{"folder moved", DetailRootChanged, func(t *testing.T, _ *Hub, root, _ string) {
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal(err)
			}
		}},
		{"folder replaced at the same path", DetailRootChanged, func(t *testing.T, _ *Hub, root, _ string) {
			copyTree(t, root, root+".copy")
			if err := os.Rename(root, root+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(root+".copy", root); err != nil {
				t.Fatal(err)
			}
		}},
		{"folder swapped for a symlink", DetailRootChanged, func(t *testing.T, _ *Hub, root, _ string) {
			if err := os.Rename(root, root+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(root+".real", root); err != nil {
				if runtime.GOOS == "windows" {
					t.Skip("symbolic links need privileges on Windows")
				}
				t.Fatal(err)
			}
		}},
		{"engine binary replaced", DetailEngineChanged, func(t *testing.T, _ *Hub, _, exe string) {
			f, err := os.OpenFile(exe, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte{0})
			_ = f.Close()
		}},
		{"environment changed", DetailEnvironmentChanged, func(t *testing.T, _ *Hub, _, _ string) {
			t.Setenv("AWS_REGION", "synthetic-region-2")
		}},
		{"absolute data directory", DetailDataDirChanged, func(t *testing.T, _ *Hub, _, _ string) {
			t.Setenv("TF_DATA_DIR", t.TempDir())
		}},
		{"initialization removed", DetailBackendChanged, func(t *testing.T, _ *Hub, root, _ string) {
			if err := os.RemoveAll(filepath.Join(root, ".terraform")); err != nil {
				t.Fatal(err)
			}
		}},
		{"backend switched to remote execution", DetailBackendChanged, func(t *testing.T, _ *Hub, root, _ string) {
			if err := os.WriteFile(filepath.Join(root, ".terraform", "terraform.tfstate"), []byte(`{"backend":{"type":"remote"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub, ids, spy := scopedHub(t)
			c, s := openScope(t, hub, ids["network"], "dev", "")
			root := rootOf(t, hub, ids["network"])
			tc.change(t, hub, root, c.Executable)
			scopeErr(t, s.Revalidate(context.Background()), CodeScopeChanged, tc.detail)
			before := len(spyCalls(t, spy, "plan"))
			op, err := s.startPlan(nil, nil)
			if err == nil {
				done := waitDone(t, s, op.ID)
				if done.Status != operations.Failed || done.Diagnostic == nil || done.Diagnostic.Code != CodeScopeChanged {
					t.Fatalf("%+v", done)
				}
				// The context is closed so it cannot be used again.
				eventually(t, "context not closed", func() bool {
					_, reason, err := hub.Context(c.ID)
					return errors.Is(err, ErrContextNotFound) && reason == CodeScopeChanged
				})
			}
			if after := len(spyCalls(t, spy, "plan")); after != before {
				t.Fatal("the engine planned after the scope changed")
			}
		})
	}
}

func TestScopeProfileIsPinned(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	hub.Profiles = profiles.Open(t.TempDir(), time.Now())
	network := ids["network"]
	p, err := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso"})
	if err != nil {
		t.Fatal(err)
	}
	c, s := openScope(t, hub, network, "dev", p.ID)
	if v := s.View(); v.Profile == nil || v.Profile.Name != "dev" || v.Profile.Revision != 1 {
		t.Fatalf("%+v", v.Profile)
	}
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, s, op.ID); done.Status != operations.Succeeded || done.Context == nil || done.Context.ProfileName != "dev" {
		t.Fatalf("%+v", done)
	}
	plans := spyCalls(t, spy, "plan")
	if len(plans) != 1 || field(plans[0], "AWS_PROFILE=") != "dev-sso" || field(plans[0], "TF_WORKSPACE=") != "dev" {
		t.Fatalf("%q", plans)
	}
	// An edit outside this tab (here: directly in the store) is caught by revalidation.
	if _, err := hub.Profiles.Update(network, p.ID, 1, profiles.Input{Name: "dev", AWSProfile: "prod-admin"}); err != nil {
		t.Fatal(err)
	}
	scopeErr(t, s.Revalidate(context.Background()), CodeScopeChanged, DetailProfileChanged)
	// An edit through the Hub closes the context and its scope at once.
	hub.ProfileChanged(network, p.ID, "profile_changed")
	if _, err := hub.Execution(c.ID); !errors.Is(err, ErrContextNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDeletedWorkspaceStopsBeforePlanning(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	c, s := openScope(t, hub, ids["network"], "dev", "")
	root := rootOf(t, hub, ids["network"])
	if err := os.WriteFile(filepath.Join(root, ".fake-workspaces"), []byte("  default\n* prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, s, op.ID)
	if done.Diagnostic == nil || done.Diagnostic.Code != CodeScopeChanged {
		t.Fatalf("%+v", done)
	}
	if len(spyCalls(t, spy, "plan")) != 0 {
		t.Fatal("planned a deleted workspace")
	}
	eventually(t, "context not closed", func() bool {
		_, reason, _ := hub.Context(c.ID)
		return reason == CodeScopeChanged
	})
}

func TestClosingAScopeCancelsReleasesAndDeletes(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	root := rootOf(t, hub, ids["network"])
	// A retained saved plan is deleted when its context is reloaded.
	_, s := openScope(t, hub, ids["platform"], "default", "")
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, s, op.ID)
	if len(planDirs(t, hub.TempDir)) != 1 {
		t.Fatal("saved plan not retained")
	}
	if _, _, err := hub.Open(ids["platform"], "default", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "saved plan not deleted on reload", func() bool { return len(planDirs(t, hub.TempDir)) == 0 })
	// Removing a project cancels its running operation and releases the root.
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	_, s = openScope(t, hub, ids["network"], "dev", "")
	op, err = s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, s, op.ID, "Planning")
	if err := hub.Remove(ids["network"]); err != nil {
		t.Fatal(err)
	}
	if done := waitDone(t, s, op.ID); done.Status != operations.Cancelled {
		t.Fatalf("%+v", done)
	}
	eventually(t, "claim not released", func() bool { _, busy := hub.Locks.Holder(root); return !busy })
	if _, err := s.startPlan(nil, nil); !errors.Is(err, operations.ErrClosed) {
		t.Fatalf("closed scope still plans: %v", err)
	}
	if len(planDirs(t, hub.TempDir)) != 0 {
		t.Fatal("private plan directory left behind")
	}
}

func TestShutdownClosesScopes(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	c, s := openScope(t, hub, ids["platform"], "default", "")
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, s, op.ID)
	hub.Close()
	if _, err := hub.Execution(c.ID); !errors.Is(err, operations.ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := s.startPlan(nil, nil); !errors.Is(err, operations.ErrClosed) {
		t.Fatalf("%v", err)
	}
	if dirs := planDirs(t, hub.TempDir); len(dirs) != 0 {
		t.Fatalf("%v", dirs)
	}
}

func TestBusyContextsAndScopesAreNotEvicted(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	hub.MaxContexts, hub.MaxScopes = 2, 1
	t.Setenv("HCLARO_FAKE_PLAN", "slow")
	busy, s := openScope(t, hub, ids["network"], "dev", "")
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, s, op.ID, "Planning")
	idle, _, err := hub.Open(ids["platform"], "default", false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, idle)
	// The scope cap is reached by a busy scope: nothing is closed.
	_, err = hub.Execution(idle.ID)
	scopeErr(t, err, CodeCapacityExceeded, "")
	// Opening a third context evicts the idle one, never the busy one.
	third, _, err := hub.Open(ids["platform"], "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, reason, err := hub.Context(idle.ID); err == nil || reason != "evicted" {
		t.Fatalf("idle context: %v %q", err, reason)
	}
	if _, _, err := hub.Context(busy.ID); err != nil {
		t.Fatal("busy context evicted")
	}
	waitLoaded(t, third)
	// When the only context slot holds a running operation, opening another refuses.
	hub.MaxContexts = 1
	hub.mu.Lock()
	hub.closeLocked(third.ID, "test")
	hub.mu.Unlock()
	_, _, err = hub.Open(ids["platform"], "staging", false)
	scopeErr(t, err, CodeCapacityExceeded, "")
	if _, err := s.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	waitDone(t, s, op.ID)
	// Once idle, the least recently used scope makes room.
	t.Setenv("HCLARO_FAKE_PLAN", "")
	hub.MaxContexts = 2
	again, _, err := hub.Open(ids["platform"], "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, again)
	if _, err := hub.Execution(again.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hub.Context(busy.ID); err != nil {
		t.Fatal("a scope eviction must not close its read context")
	}
}

func TestRemoteBackendCannotGetAScope(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	root := rootOf(t, hub, ids["platform"])
	if err := os.WriteFile(filepath.Join(root, ".terraform", "terraform.tfstate"), []byte(`{"backend":{"type":"cloud"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _, err := hub.Open(ids["platform"], "default", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap := waitLoaded(t, c); snap.Info.Capabilities.CanPlan || snap.Info.Capabilities.CanApply {
		t.Fatalf("%+v", snap.Info.Capabilities)
	}
	_, err = hub.Execution(c.ID)
	scopeErr(t, err, CodeExecutionUnsupported, "")
	if len(spyCalls(t, spy, "plan")) != 0 {
		t.Fatal("planned a remote execution backend")
	}
}

func TestApplyRevalidatesTheScopeLast(t *testing.T) {
	hub, ids, spy := scopedHub(t)
	approveAndApply := func(s *Scope) operations.Operation {
		t.Helper()
		op, err := s.startPlan(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		done := waitDone(t, s, op.ID)
		if done.PlanID == "" {
			t.Fatalf("%+v", done)
		}
		r, err := s.manager.Preflight(context.Background(), done.PlanID, operations.PreflightRequest{})
		if err != nil {
			t.Fatal(err)
		}
		approval, err := s.manager.Approve(done.PlanID, operations.ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
		if err != nil {
			t.Fatal(err)
		}
		return func() operations.Operation {
			if strings.HasSuffix(s.id.root, "network") {
				// Replace the engine after approval, before apply.
				f, err := os.OpenFile(s.id.executable, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.Write([]byte{0})
				_ = f.Close()
			}
			applied, err := s.manager.StartApply(done.PlanID, approval.Token, approval.Phrase)
			if err != nil {
				t.Fatal(err)
			}
			return waitDone(t, s, applied.ID)
		}()
	}
	// Control: an unchanged scope applies, in its own root and workspace.
	_, ok := openScope(t, hub, ids["platform"], "default", "")
	if applied := approveAndApply(ok); applied.Status != operations.Succeeded || applied.Outcome != "applied" {
		t.Fatalf("%+v", applied)
	}
	applies := spyCalls(t, spy, "apply")
	if cwd, _ := filepath.EvalSymlinks(field(applies[0], "CWD=")); len(applies) != 1 || cwd != rootOf(t, hub, ids["platform"]) {
		t.Fatalf("%q", applies)
	}
	// A replaced engine binary refuses the apply before the engine runs.
	_, changedScope := openScope(t, hub, ids["network"], "default", "")
	refused := approveAndApply(changedScope)
	if refused.Status != operations.Failed || refused.Outcome != "not_applied" || refused.Diagnostic == nil || refused.Diagnostic.Code != CodeScopeChanged {
		t.Fatalf("%+v", refused)
	}
	if len(spyCalls(t, spy, "apply")) != 1 {
		t.Fatal("applied after the engine changed")
	}
	eventually(t, "saved plan kept after a refused apply", func() bool { return len(planDirs(t, hub.TempDir)) == 0 })
}

// copyTree copies a directory of regular files and directories.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A FileInfo captured with statIdentity keeps describing the original folder after another
// folder takes its path. Plain os.Stat does not on Windows, where the identity is loaded
// lazily from the path when os.SameFile first runs.
func TestStatIdentityDetectsAFolderReplacedAtTheSamePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := statIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, now) {
		t.Fatal("replaced folder not detected")
	}
	old, _ := os.Stat(dir + ".old")
	if !os.SameFile(before, old) {
		t.Fatal("identity does not follow the original folder")
	}
}

// M26.08: a read never creates or evicts a scope, and a scope or context that keeps a
// reviewed saved plan is not evicted to make room for another.
func TestPreviewAndEvictionKeepReviewedPlans(t *testing.T) {
	hub, ids, _ := scopedHub(t)
	hub.MaxContexts, hub.MaxScopes = 2, 1
	kept, s := openScope(t, hub, ids["network"], "default", "")
	op, err := s.startPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID := waitDone(t, s, op.ID).PlanID
	if planID == "" {
		t.Fatal("no plan")
	}
	other, _, err := hub.Open(ids["platform"], "default", false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, other)
	// Options of another workspace are shown without creating its scope.
	preview, err := hub.Preview(other.ID)
	if err != nil || preview.View().ContextID != other.ID || preview.ProfileFingerprint() != "" {
		t.Fatalf("%v %+v", err, preview)
	}
	if existing, _ := hub.ExistingScope(other.ID); existing != nil {
		t.Fatal("a preview created a scope")
	}
	if again, _ := hub.Preview(kept.ID); again != s {
		t.Fatal("preview of an open scope is not that scope")
	}
	// Creating a scope does not evict the one that keeps a reviewed plan.
	_, err = hub.Execution(other.ID)
	scopeErr(t, err, CodeCapacityExceeded, "")
	// Opening a third context evicts the context without a plan, not the one with it.
	third, _, err := hub.Open(ids["platform"], "staging", false)
	if err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, third)
	if _, reason, err := hub.Context(other.ID); err == nil || reason != "evicted" {
		t.Fatalf("context without a plan: %v %q", err, reason)
	}
	if _, _, err := hub.Context(kept.ID); err != nil {
		t.Fatal("context with a reviewed plan evicted")
	}
	if _, ok := s.Plan(planID); !ok || !s.manager.KeepsPlan() || len(planDirs(t, hub.TempDir)) != 1 {
		t.Fatalf("reviewed plan lost: %v", planDirs(t, hub.TempDir))
	}
	// Once the plan is no longer kept, the scope makes room again.
	s.manager.Close(0)
	if _, err := hub.Execution(third.ID); err != nil {
		t.Fatal(err)
	}
}
