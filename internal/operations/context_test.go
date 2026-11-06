package operations

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/profiles"
	"hclaro/internal/workspace"
)

const testProject = "0123456789abcdef0123456789abcdef"

// binding records the environments the manager bound planners to.
type binding struct {
	mu         sync.Mutex
	environs   [][]string
	workspaces []string
}

func (b *binding) last() ([]string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.environs) == 0 {
		return nil, ""
	}
	return b.environs[len(b.environs)-1], b.workspaces[len(b.workspaces)-1]
}

// profileSetup is setup plus a real profile store and a binder equivalent to the Hub's: the
// profile is resolved against the root and the given environment and composed.
func profileSetup(t *testing.T, fake *engine.Fake, environ []string) (*Manager, string, *profiles.Store, *binding) {
	t.Helper()
	m, root := setup(t, fake, workspace.Info{})
	store := profiles.Open(t.TempDir(), time.Now())
	m.Binder = func(id string) (Binding, error) {
		p, err := store.Get(testProject, id)
		if err != nil {
			return Binding{}, &RejectError{"profile_not_found", "gone"}
		}
		r := profiles.Resolve(p, root, environ)
		if issue := r.Blocking(); issue != nil {
			return Binding{}, &RejectError{issue.Code, issue.Message}
		}
		env, err := profiles.Compose(environ, r)
		if err != nil {
			return Binding{}, &RejectError{"unsafe_cli_args", "unsafe"}
		}
		return Binding{Context: FromResolved(r), Environ: env}, nil
	}
	rec := &binding{}
	m.Bind = func(env []string, ws string) engine.Planner {
		rec.mu.Lock()
		rec.environs = append(rec.environs, slices.Clone(env))
		rec.workspaces = append(rec.workspaces, ws)
		rec.mu.Unlock()
		return fake
	}
	return m, root, store, rec
}

func reviewedFingerprint(t *testing.T, m *Manager, id string) string {
	t.Helper()
	b, err := m.Binder(id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Context.Fingerprint
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePlanAndApplyUseTheBoundContext(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, ApplyLines: lines(fixtures.ApplyLog)}
	environ := []string{"PATH=/bin", "AWS_SECRET_ACCESS_KEY=" + canary, "TF_WORKSPACE=other", "TF_VAR_password=" + canary}
	m, root, store, rec := profileSetup(t, fake, environ)
	writeFile(t, root, "env/prod.tfvars", `password = "`+canary+`"`)
	p, err := store.Create(testProject, profiles.Input{Name: "Production", VarFiles: []string{"env/prod.tfvars"}, Options: profiles.Options{Parallelism: 4, LockTimeoutSeconds: 30}, AWSProfile: "prod-sso", AWSRegion: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartPlanWith(PlanStart{VarFiles: []string{"env/prod.tfvars"}, ProfileID: p.ID, ContextFingerprint: reviewedFingerprint(t, m, p.ID)})
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join(op.Command, " ")
	if !strings.Contains(command, "-var-file=env/prod.tfvars -parallelism=4 -lock-timeout=30s") {
		t.Fatalf("command %q", command)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded || done.Context == nil || done.Context.ProfileName != "Production" || done.Context.Revision != 1 || done.Context.Workspace != "demo" || done.Context.Identity != profiles.IdentityNotVerified {
		t.Fatalf("operation %+v context %+v", done, done.Context)
	}
	requests := fake.Requests()
	if len(requests) != 1 || requests[0].Parallelism != 4 || requests[0].LockTimeoutSeconds != 30 || !slices.Equal(requests[0].VarFiles, []string{"env/prod.tfvars"}) {
		t.Fatalf("requests %+v", requests)
	}
	env, ws := rec.last()
	if ws != "demo" || !slices.Contains(env, "AWS_PROFILE=prod-sso") || !slices.Contains(env, "AWS_REGION=eu-west-1") {
		t.Fatalf("plan env %v workspace %q", env, ws)
	}
	for _, item := range env {
		if strings.HasPrefix(item, "TF_WORKSPACE=") || strings.HasPrefix(item, "AWS_SECRET_ACCESS_KEY=") {
			t.Fatalf("inherited %s reached the plan", item)
		}
	}
	plan, _ := m.Plan(done.PlanID)
	if plan.Context == nil || plan.Context.Fingerprint != done.Context.Fingerprint {
		t.Fatalf("plan context %+v", plan.Context)
	}
	if changed, status := m.ContextStatus(plan); changed || !status.Available {
		t.Fatalf("fresh plan: changed %v status %+v", changed, status)
	}
	approval, err := m.Approve(done.PlanID)
	if err != nil || approval.Context == nil || approval.Context.ProfileID != p.ID {
		t.Fatalf("approval %+v %v", approval, err)
	}
	applied, err := m.StartApply(done.PlanID, approval.Token, approval.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if finished := wait(t, m, applied.ID); finished.Status != Succeeded || finished.Context == nil || finished.Context.ProfileID != p.ID {
		t.Fatalf("apply %+v", finished)
	}
	applyEnv, applyWS := rec.last()
	if applyWS != "demo" || !slices.Equal(applyEnv, env) {
		t.Fatalf("apply env %v differs from plan env %v", applyEnv, env)
	}
	// Operation metadata, plans and approvals carry names, never values.
	assertNoCanary(t, m.Operations(), m.Plans(), approval)
}

func TestProfilePlanRefusesUnreviewedOrMismatchedContext(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root, store, _ := profileSetup(t, fake, []string{"PATH=/bin"})
	p, _ := store.Create(testProject, profiles.Input{Name: "a"})
	fp := reviewedFingerprint(t, m, p.ID)
	for name, req := range map[string]PlanStart{
		"no fingerprint":    {ProfileID: p.ID},
		"other fingerprint": {ProfileID: p.ID, ContextFingerprint: strings.Repeat("0", 64)},
	} {
		if _, err := m.StartPlanWith(req); rejected(err) != "context_changed" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := store.Update(testProject, p.ID, 1, profiles.Input{Name: "a", AWSProfile: "changed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPlanWith(PlanStart{ProfileID: p.ID, ContextFingerprint: fp}); rejected(err) != "context_changed" {
		t.Errorf("edited after review: %v", err)
	}
	ws, _ := store.Create(testProject, profiles.Input{Name: "prod only", Workspace: "prod"})
	if _, err := m.StartPlanWith(PlanStart{ProfileID: ws.ID, ContextFingerprint: reviewedFingerprint(t, m, ws.ID)}); rejected(err) != "profile_workspace" {
		t.Errorf("workspace mismatch: %v", err)
	}
	tofu, _ := store.Create(testProject, profiles.Input{Name: "tofu", Engine: "tofu"})
	if _, err := m.StartPlanWith(PlanStart{ProfileID: tofu.ID, ContextFingerprint: reviewedFingerprint(t, m, tofu.ID)}); rejected(err) != "profile_engine" {
		t.Errorf("engine mismatch: %v", err)
	}
	writeFile(t, root, "env/prod.tfvars", "x = 1")
	vars, _ := store.Create(testProject, profiles.Input{Name: "vars", VarFiles: []string{"env/prod.tfvars"}})
	fp = reviewedFingerprint(t, m, vars.ID)
	writeFile(t, root, "env/other.tfvars", "x = 2")
	if _, err := m.StartPlanWith(PlanStart{VarFiles: []string{"env/other.tfvars"}, ProfileID: vars.ID, ContextFingerprint: fp}); err != ErrVarFiles {
		t.Errorf("unapproved nested file: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "env", "prod.tfvars")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPlanWith(PlanStart{ProfileID: vars.ID, ContextFingerprint: fp}); rejected(err) != "var_file_missing" {
		t.Errorf("deleted variable file: %v", err)
	}
	if _, err := m.StartPlanWith(PlanStart{ProfileID: "ffffffffffffffffffffffffffffffff", ContextFingerprint: fp}); rejected(err) != "profile_not_found" {
		t.Errorf("unknown profile: %v", err)
	}
	if calls := fake.Calls(); slices.Contains(calls, "plan") {
		t.Fatalf("engine planned: %v", calls)
	}
}

func TestProfileInjectedCLIArgsRefuseThePlan(t *testing.T) {
	for _, item := range []string{"TF_CLI_ARGS_plan=-lock=false", "TF_CLI_ARGS=-auto-approve", "TF_CLI_ARGS_plan=-destroy", "TF_CLI_ARGS_apply=-auto-approve", "tf_cli_args_plan=-refresh=false"} {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		m, _, store, _ := profileSetup(t, fake, []string{"PATH=/bin", item})
		p, _ := store.Create(testProject, profiles.Input{Name: "a"})
		_, bindErr := m.Binder(p.ID)
		if _, err := m.StartPlanWith(PlanStart{ProfileID: p.ID, ContextFingerprint: strings.Repeat("0", 64)}); rejected(err) != "unsafe_cli_args" || rejected(bindErr) != "unsafe_cli_args" {
			t.Errorf("%s: %v / %v", item, err, bindErr)
		}
		if slices.Contains(fake.Calls(), "plan") {
			t.Errorf("%s: engine planned", item)
		}
	}
}

// Editing or deleting the profile after review makes the stored plan and any approval
// token unusable, and nothing is applied.
func TestProfileChangeInvalidatesStoredPlanAndToken(t *testing.T) {
	for _, change := range []string{"edit", "delete"} {
		fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
		m, _, store, _ := profileSetup(t, fake, []string{"PATH=/bin"})
		p, _ := store.Create(testProject, profiles.Input{Name: "staging", AWSProfile: "staging"})
		op, err := m.StartPlanWith(PlanStart{ProfileID: p.ID, ContextFingerprint: reviewedFingerprint(t, m, p.ID)})
		if err != nil {
			t.Fatal(err)
		}
		done := wait(t, m, op.ID)
		approval, err := m.Approve(done.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if change == "edit" {
			_, err = store.Update(testProject, p.ID, 1, profiles.Input{Name: "staging", AWSProfile: "production"})
		} else {
			err = store.Delete(testProject, p.ID, 1)
		}
		if err != nil {
			t.Fatal(err)
		}
		plan, _ := m.Plan(done.PlanID)
		changed, status := m.ContextStatus(plan)
		if !changed || status.Available || !strings.Contains(status.Reason, "staging") {
			t.Fatalf("%s: changed %v status %+v", change, changed, status)
		}
		if _, err := m.StartApply(done.PlanID, approval.Token, approval.Phrase); rejected(err) != "context_changed" {
			t.Fatalf("%s: apply with old token: %v", change, err)
		}
		if _, err := m.Approve(done.PlanID); rejected(err) != "context_changed" {
			t.Fatalf("%s: new approval: %v", change, err)
		}
		if applyCalls(fake) != 0 {
			t.Fatalf("%s: engine applied", change)
		}
	}
}

// A running operation keeps the context and environment it started with, whatever happens
// to the profile meanwhile.
func TestProfileDeletedWhileRunningKeepsSnapshot(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, Lines: lines(fixtures.PlanLog), Delay: 20 * time.Millisecond}
	m, _, store, rec := profileSetup(t, fake, []string{"PATH=/bin"})
	p, _ := store.Create(testProject, profiles.Input{Name: "prod", AWSProfile: "prod-sso", Options: profiles.Options{Parallelism: 2}})
	op, err := m.StartPlanWith(PlanStart{ProfileID: p.ID, ContextFingerprint: reviewedFingerprint(t, m, p.ID)})
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, m, op.ID, "Planning")
	if err := store.Delete(testProject, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(testProject, profiles.Input{Name: "prod", AWSProfile: "attacker"}); err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded || done.Context.ProfileID != p.ID || done.Context.AWSProfile != "prod-sso" || done.Context.Parallelism != 2 || done.Context.Revision != 1 {
		t.Fatalf("snapshot changed: %+v %+v", done, done.Context)
	}
	if env, _ := rec.last(); !slices.Contains(env, "AWS_PROFILE=prod-sso") || slices.Contains(env, "AWS_PROFILE=attacker") {
		t.Fatalf("environment %v", env)
	}
	if requests := fake.Requests(); requests[0].Parallelism != 2 {
		t.Fatalf("request %+v", requests[0])
	}
	plan, _ := m.Plan(done.PlanID)
	if changed, _ := m.ContextStatus(plan); !changed {
		t.Fatal("plan of a deleted profile is still applicable")
	}
}

// Without a profile nothing changes: no binding, no TF_WORKSPACE override, old command.
func TestPlanWithoutProfileIsUnchanged(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, _, _, rec := profileSetup(t, fake, []string{"PATH=/bin"})
	id := planned(t, m)
	plan, _ := m.Plan(id)
	if plan.Context != nil {
		t.Fatalf("context %+v", plan.Context)
	}
	if env, _ := rec.last(); env != nil {
		t.Fatalf("bound without a profile: %v", env)
	}
	if changed, status := m.ContextStatus(plan); changed || !status.Available {
		t.Fatalf("status %v %+v", changed, status)
	}
}
