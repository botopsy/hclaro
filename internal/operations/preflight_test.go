package operations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/preflight"
	"hclaro/internal/workspace"
)

func find(r PreflightReport, id string) (preflight.Check, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return preflight.Check{}, false
}

func failed(r PreflightReport, id string) bool {
	c, ok := find(r, id)
	return ok && c.Status != preflight.Pass
}

func runPreflight(t *testing.T, m *Manager, id string, req PreflightRequest) PreflightReport {
	t.Helper()
	r, err := m.Preflight(context.Background(), id, req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writePolicy(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".hclaro"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hclaro", "policy.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitState is a fake Git probe whose result can change during a test.
type gitState struct {
	mu sync.Mutex
	r  preflight.GitResult
}

func (g *gitState) set(r preflight.GitResult) { g.mu.Lock(); g.r = r; g.mu.Unlock() }
func (g *gitState) probe(context.Context, string) preflight.GitResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.r
}

var (
	clean = preflight.GitResult{State: preflight.GitClean, Paths: []string{}}
	dirty = preflight.GitResult{State: preflight.GitDirty, Modified: 2, Untracked: 1, Paths: []string{"main.tf", "variables.tf", "notes.txt"}}
)

func TestPreflightPassesAndBindsApproval(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	m.GitProbe = (&gitState{r: clean}).probe
	id := planned(t, m)
	r := runPreflight(t, m, id, PreflightRequest{})
	if r.Blocked || r.Binding == "" || len(r.Binding) != 64 || r.Blockers != 0 {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Checks {
		if c.Severity == preflight.Block && c.Status != preflight.Pass {
			t.Fatalf("blocking check %s %s", c.ID, c.Evidence)
		}
	}
	// The impact covers the whole plan with exact counts.
	if r.Impact.DestructiveCount == 0 || len(r.Impact.HighRisk) != r.Impact.DestructiveCount || r.Impact.Total != len(r.Impact.Changes) {
		t.Fatalf("%+v", r.Impact)
	}
	if r.Phrase != "apply demo" || r.Context.Workspace != "demo" || r.Context.ArtifactSHA256 == "" || len(r.Command) == 0 {
		t.Fatalf("%+v", r)
	}
	// An approval needs the binding: no binding, a forged one, or one from an earlier
	// preflight is refused.
	for _, b := range []string{"", strings.Repeat("0", 64)} {
		if _, err := m.Approve(id, ApprovalRequest{Binding: b, Acknowledged: r.Acknowledge}); rejected(err) != "preflight_required" {
			t.Fatalf("%q: %v", b, err)
		}
	}
	again := runPreflight(t, m, id, PreflightRequest{})
	if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge}); rejected(err) != "preflight_required" {
		t.Fatalf("superseded binding: %v", err)
	}
	a, err := m.Approve(id, ApprovalRequest{Binding: again.Binding, Acknowledged: again.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	// A newer preflight also invalidates an unused approval.
	runPreflight(t, m, id, PreflightRequest{})
	if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "approval_required" {
		t.Fatalf("%v", err)
	}
	if applyCalls(fake) != 0 {
		t.Fatal("engine apply ran")
	}
}

func TestPreflightDirtyGitFollowsPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		policy   string
		severity preflight.Severity
		present  bool
	}{
		"default warns": {"", preflight.Warn, true},
		"policy blocks": {`{"version":1,"rules":{"gitDirty":"block"}}`, preflight.Block, true},
		"policy info":   {`{"version":1,"rules":{"gitDirty":"info"}}`, preflight.Info, true},
		"policy off":    {`{"version":1,"rules":{"gitDirty":"off"}}`, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
			m, root := setup(t, fake, workspace.Info{})
			m.GitProbe = (&gitState{r: dirty}).probe
			id := planned(t, m)
			if tc.policy != "" {
				writePolicy(t, root, tc.policy)
			}
			r := runPreflight(t, m, id, PreflightRequest{})
			c, ok := find(r, preflight.CheckGit)
			if ok != tc.present {
				t.Fatalf("present %v", ok)
			}
			if !ok {
				if r.Blocked || slices.Contains(r.Acknowledge, preflight.CheckGit) {
					t.Fatalf("%+v", r)
				}
				return
			}
			if c.Severity != tc.severity || c.Status != preflight.Fail || c.Source != "policy" || !strings.Contains(c.Evidence, "2 modified and 1 untracked") {
				t.Fatalf("%+v", c)
			}
			switch tc.severity {
			case preflight.Warn:
				// A warning is not a blocker, but it must be acknowledged individually.
				if r.Blocked || r.Binding == "" || !slices.Contains(r.Acknowledge, preflight.CheckGit) {
					t.Fatalf("%+v", r)
				}
				if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding}); rejected(err) != "acknowledgement_required" {
					t.Fatalf("%v", err)
				}
				// An acknowledgement must name exactly the warnings; extra IDs are refused too.
				if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: append(slices.Clone(r.Acknowledge), "all")}); rejected(err) != "acknowledgement_required" {
					t.Fatalf("%v", err)
				}
				if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge}); err != nil {
					t.Fatal(err)
				}
			case preflight.Block:
				if !r.Blocked || r.Binding != "" {
					t.Fatalf("%+v", r)
				}
			case preflight.Info:
				if r.Blocked || slices.Contains(r.Acknowledge, preflight.CheckGit) {
					t.Fatalf("%+v", r)
				}
			}
		})
	}
}

func TestPreflightBlocksContextAndPlanChanges(t *testing.T) {
	t.Run("configuration changed", func(t *testing.T) {
		m, root := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
		id := planned(t, m)
		if err := os.WriteFile(filepath.Join(root, "extra.tf"), []byte("# changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runPreflight(t, m, id, PreflightRequest{})
		if !r.Blocked || r.Binding != "" || !failed(r, preflight.CheckFingerprint) {
			t.Fatalf("%+v", r.Checks)
		}
	})
	t.Run("plan expired", func(t *testing.T) {
		m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
		m.PlanMaxAge = 150 * time.Millisecond
		id := planned(t, m)
		time.Sleep(200 * time.Millisecond)
		r := runPreflight(t, m, id, PreflightRequest{})
		if !r.Blocked || !failed(r, preflight.CheckArtifact) {
			t.Fatalf("%+v", r.Checks)
		}
	})
	t.Run("preflight binding expired", func(t *testing.T) {
		m, _ := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
		m.PreflightTTL = 50 * time.Millisecond
		id := planned(t, m)
		r := runPreflight(t, m, id, PreflightRequest{})
		time.Sleep(80 * time.Millisecond)
		if _, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge}); rejected(err) != "preflight_expired" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("invalid policy fails closed", func(t *testing.T) {
		m, root := setup(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions}, workspace.Info{})
		id := planned(t, m)
		writePolicy(t, root, `{"version":1,"rules":{"gitDirty":"warn","script":"rm -rf /"}}`)
		r := runPreflight(t, m, id, PreflightRequest{})
		if !r.Blocked || !failed(r, preflight.CheckPolicy) || r.Policy.Valid || strings.Contains(r.Policy.Error, "rm -rf") {
			t.Fatalf("%+v %+v", r.Checks, r.Policy)
		}
	})
	t.Run("operation running", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		m, _ := setup(t, fake, workspace.Info{})
		id := planned(t, m)
		fake.Block = true
		op, err := m.StartPlan(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		waitStage(t, m, op.ID, "Planning")
		r := runPreflight(t, m, id, PreflightRequest{})
		if !r.Blocked || !failed(r, preflight.CheckConflict) {
			t.Fatalf("%+v", r.Checks)
		}
		_, _ = m.Cancel(op.ID)
		wait(t, m, op.ID)
	})
}

// A backend or engine that changed to something unsupported after planning blocks apply.
func TestPreflightBlocksUnsupportedBackendAndEngine(t *testing.T) {
	for name, tc := range map[string]struct {
		info    workspace.Info
		version string
		check   string
	}{
		"remote backend":       {workspace.Info{Project: "demo", Initialized: true, Backend: "cloud", ExecutionMode: "remote-exec"}, "1.9.8", preflight.CheckBackend},
		"unverified backend":   {workspace.Info{Project: "demo", Initialized: true, Backend: "unknown", ExecutionMode: "unsupported"}, "1.9.8", preflight.CheckBackend},
		"unrecognized version": {workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"}, "2.0.0", preflight.CheckEngine},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
			m, _ := setup(t, fake, workspace.Info{})
			id := planned(t, m)
			fake.VersionValue = tc.version
			m.Reinspect = func() workspace.Info { return tc.info }
			m.Reload = func(ctx context.Context) { m.store.Load(ctx, fake, "terraform") }
			if err := m.Reread(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for m.store.Get().Status == "loading" && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			r := runPreflight(t, m, id, PreflightRequest{})
			if !r.Blocked || r.Binding != "" || !failed(r, tc.check) {
				t.Fatalf("%+v", r.Checks)
			}
			if _, err := approve(m, id); err == nil {
				t.Fatal("approved")
			}
			if applyCalls(fake) != 0 {
				t.Fatal("engine apply ran")
			}
		})
	}
}

// Context changes between preflight and apply are refused; nothing is re-planned.
func TestContextChangeBetweenPreflightAndApplyIsRejected(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	id := planned(t, m)
	plans := countCalls(fake, "plan")
	a, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "late.tf"), []byte("# edited after review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "context_changed" {
		t.Fatalf("%v", err)
	}
	if applyCalls(fake) != 0 || countCalls(fake, "plan") != plans {
		t.Fatalf("calls %v", fake.Calls())
	}
}

// Warnings and blockers that appear after the approval refuse the apply.
func TestApplyReevaluatesPreflight(t *testing.T) {
	t.Run("new warning", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		m, _ := setup(t, fake, workspace.Info{})
		git := &gitState{r: clean}
		m.GitProbe = git.probe
		id := planned(t, m)
		a, err := approve(m, id)
		if err != nil || slices.Contains(a.Acknowledged, preflight.CheckGit) {
			t.Fatal(err, a.Acknowledged)
		}
		git.set(dirty)
		if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "preflight_changed" {
			t.Fatalf("%v", err)
		}
		if applyCalls(fake) != 0 {
			t.Fatal("engine apply ran")
		}
	})
	t.Run("new blocker", func(t *testing.T) {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		m, root := setup(t, fake, workspace.Info{})
		m.GitProbe = (&gitState{r: dirty}).probe
		id := planned(t, m)
		a, err := approve(m, id)
		if err != nil {
			t.Fatal(err)
		}
		writePolicy(t, root, `{"version":1,"rules":{"gitDirty":"block"}}`)
		if _, err := m.StartApply(id, a.Token, a.Phrase); rejected(err) != "preflight_blocked" {
			t.Fatalf("%v", err)
		}
		if applyCalls(fake) != 0 {
			t.Fatal("engine apply ran")
		}
		// The binding was dropped: a new approval needs a new preflight.
		if _, err := m.Approve(id, ApprovalRequest{}); rejected(err) != "preflight_required" {
			t.Fatalf("%v", err)
		}
	})
}

// A two-instance replacement lists every other change in the plan, and that scope warning
// must be acknowledged.
func TestReplacePreflightIncludesCollateralChanges(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanCollateral}
	m, _ := setup(t, fake, workspace.Info{})
	var managed []string
	for _, r := range m.store.Get().Index.Resources {
		if r.Mode == "managed" && strings.Contains(r.Address, "workers") || r.Address == "aws_iam_role.deployer" {
			managed = append(managed, r.ID)
		}
	}
	op, err := m.StartPlan(nil, managed[:2])
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	r := runPreflight(t, m, done.PlanID, PreflightRequest{})
	if len(r.Impact.Requested) != 2 || r.Impact.Extra == 0 || !slices.Contains(r.Acknowledge, preflight.CheckScope) {
		t.Fatalf("%+v %+v", r.Impact, r.Acknowledge)
	}
	requested, extra := 0, 0
	for _, c := range r.Impact.Changes {
		if c.Requested {
			requested++
		} else {
			extra++
		}
	}
	// The synthetic plan replaces other instances than the two requested, so every change
	// is collateral and listed; none is hidden behind the selection.
	if extra != r.Impact.Extra || r.Impact.Total != requested+extra || r.Impact.Total != 3 || len(r.Impact.HighRisk) != 3 {
		t.Fatalf("%+v", r.Impact)
	}
	if _, err := m.Approve(done.PlanID, ApprovalRequest{Binding: r.Binding, Acknowledged: slices.DeleteFunc(slices.Clone(r.Acknowledge), func(id string) bool { return id == preflight.CheckScope })}); rejected(err) != "acknowledgement_required" {
		t.Fatalf("%v", err)
	}
}

// The identity probe runs only on request; its account never reaches logs or records.
func TestIdentityProbeIsExplicitAndNotLogged(t *testing.T) {
	const account = "123456789012"
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
	m, root := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	var probes atomic.Int32
	m.IdentityProbe = func(context.Context, []string) preflight.Identity {
		probes.Add(1)
		return preflight.Identity{State: "verified", Account: account, ARN: "arn:aws:sts::" + account + ":assumed-role/deployer/session"}
	}
	rec := &records{}
	m.Recorder = rec
	id := planned(t, m)
	r := runPreflight(t, m, id, PreflightRequest{})
	c, ok := find(r, preflight.CheckAWS)
	if probes.Load() != 0 || !ok || c.Status != preflight.Unknown || r.Blocked {
		t.Fatalf("probe ran or check missing: %d %+v", probes.Load(), c)
	}
	// With a policy that allows another account only, a verified identity blocks.
	writePolicy(t, root, `{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["210987654321"]}}}`)
	if r = runPreflight(t, m, id, PreflightRequest{}); !r.Blocked || !failed(r, preflight.CheckAWS) {
		t.Fatalf("unverified identity must block under an account policy: %+v", r.Checks)
	}
	if r = runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true}); !r.Blocked || probes.Load() != 1 || !strings.Contains(mustCheck(t, r, preflight.CheckAWS).Evidence, "not in the policy") {
		t.Fatalf("%+v", r.Checks)
	}
	writePolicy(t, root, `{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["`+account+`"]}}}`)
	// The verified identity is kept with the binding, so a re-check does not call AWS again.
	r = runPreflight(t, m, id, PreflightRequest{VerifyIdentity: true})
	if r.Blocked || mustCheck(t, r, preflight.CheckAWS).Status != preflight.Pass {
		t.Fatalf("%+v", r.Checks)
	}
	a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartApply(id, a.Token, a.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	// Apply under an account policy asks STS again (the third probe) instead of trusting the
	// preflight's answer.
	if done.Outcome != "applied" || done.Preflight == nil || !done.Preflight.IdentityVerified || probes.Load() != 3 {
		t.Fatalf("%+v %d", done.Preflight, probes.Load())
	}
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	blob, _ := json.Marshal([]any{events, m.Operations(), rec.all()})
	for _, secret := range []string{account, "assumed-role", canary} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("%q leaked into logs or records", secret)
		}
	}
}

// identityAt is a fake STS answer that a test can change between preflight and apply.
type identityAt struct {
	mu     sync.Mutex
	id     preflight.Identity
	probes int
}

func (f *identityAt) set(id preflight.Identity) { f.mu.Lock(); f.id = id; f.mu.Unlock() }
func (f *identityAt) count() int                { f.mu.Lock(); defer f.mu.Unlock(); return f.probes }
func (f *identityAt) probe(context.Context, []string) preflight.Identity {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.id
}

func verifiedAs(account string) preflight.Identity {
	return preflight.Identity{State: "verified", Account: account, ARN: "arn:aws:sts::" + account + ":assumed-role/deployer/session"}
}

// An account policy is enforced against the credentials at apply time, not the account the
// preflight verified up to PreflightTTL earlier.
func TestApplyRechecksTheAccountPolicy(t *testing.T) {
	const allowed, other = "123456789012", "210987654321"
	for name, tc := range map[string]struct {
		policy  string
		verify  bool
		atApply preflight.Identity
		code    string
		probes  int
	}{
		"account switched":         {`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["` + allowed + `"]}}}`, true, verifiedAs(other), "preflight_blocked", 2},
		"probe fails at apply":     {`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["` + allowed + `"]}}}`, true, preflight.Identity{State: "failed", Message: "expired"}, "preflight_blocked", 2},
		"warn rule, switched":      {`{"version":1,"rules":{"awsAccounts":{"severity":"warn","allowed":["` + allowed + `"]}}}`, true, verifiedAs(other), "preflight_changed", 2},
		"same account":             {`{"version":1,"rules":{"awsAccounts":{"severity":"block","allowed":["` + allowed + `"]}}}`, true, verifiedAs(allowed), "", 2},
		"no account rule":          {`{"version":1}`, true, verifiedAs(other), "", 1},
		"warn rule, never checked": {`{"version":1,"rules":{"awsAccounts":{"severity":"warn","allowed":["` + allowed + `"]}}}`, false, verifiedAs(other), "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
			m, root := setup(t, fake, workspace.Info{})
			m.Reload = func(context.Context) {}
			sts := &identityAt{id: verifiedAs(allowed)}
			m.IdentityProbe = sts.probe
			writePolicy(t, root, tc.policy)
			id := planned(t, m)
			r := runPreflight(t, m, id, PreflightRequest{VerifyIdentity: tc.verify})
			if r.Blocked {
				t.Fatalf("%+v", r.Checks)
			}
			a, err := m.Approve(id, ApprovalRequest{Binding: r.Binding, Acknowledged: r.Acknowledge})
			if err != nil {
				t.Fatal(err)
			}
			sts.set(tc.atApply)
			op, err := m.StartApply(id, a.Token, a.Phrase)
			if sts.count() != tc.probes {
				t.Fatalf("%d identity probes, want %d", sts.count(), tc.probes)
			}
			if tc.code != "" {
				if rejected(err) != tc.code || slices.Contains(fake.Calls(), "apply") {
					t.Fatalf("%v, engine calls %v", err, fake.Calls())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if done := wait(t, m, op.ID); done.Outcome != "applied" {
				t.Fatalf("%+v", done)
			}
			// Without a profile, apply gets no engine options: the engine defaults hold.
			if r := fake.ApplyRequests(); len(r) != 1 || r[0].Parallelism != 0 || r[0].LockTimeoutSeconds != 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func mustCheck(t *testing.T, r PreflightReport, id string) preflight.Check {
	t.Helper()
	c, ok := find(r, id)
	if !ok {
		t.Fatalf("check %s missing", id)
	}
	return c
}

type records struct {
	mu   sync.Mutex
	list []Record
}

func (r *records) Record(rec Record) { r.mu.Lock(); r.list = append(r.list, rec); r.mu.Unlock() }
func (r *records) all() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.list)
}

// Only the reviewed saved plan reaches the engine's apply, after preflight.
func TestApplyUsesOnlyTheReviewedArtifact(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	id := planned(t, m)
	p, _ := m.Plan(id)
	op := approveAndApply(t, m, id)
	done := wait(t, m, op.ID)
	if done.Outcome != "applied" || slices.Contains(done.Command, "-auto-approve") || !strings.HasPrefix(done.Command[len(done.Command)-1], "<reviewed saved plan "+p.ArtifactSHA256[:12]) {
		t.Fatalf("%+v", done)
	}
	if got := fake.Applied(); len(got) != 1 || string(got[0]) != string(fixtures.PlanAllActions) {
		t.Fatal("the applied file is not the reviewed artifact")
	}
	if n := countCalls(fake, "plan"); n != 1 {
		t.Fatalf("apply re-planned: %v", fake.Calls())
	}
}

func countCalls(f *engine.Fake, name string) int {
	n := 0
	for _, c := range f.Calls() {
		if c == name {
			n++
		}
	}
	return n
}
