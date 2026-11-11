package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/internal/operations"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
)

// Tests for Hub-scoped preflight, approval and apply (M24.06, ADR M24.03). The synthetic CLI
// (scripts/fake-engine) "applies" only a saved plan file it wrote itself, in temporary roots.
// Nothing contacts a real backend, provider or account.

type scopedPreflight struct {
	operations.PreflightReport
	Scope projects.ScopeView `json:"scope"`
}

type scopedApproval struct {
	operations.Approval
	Scope projects.ScopeView `json:"scope"`
}

// calls returns the spied invocations whose command is cmd ("apply", "plan", "show", ...).
func (s *scopedServer) calls(cmd string) [][]string {
	s.t.Helper()
	b, err := os.ReadFile(s.spy)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	out := [][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Split(line, "\x00")
		for _, f := range fields {
			if strings.Contains(f, "=") {
				continue
			}
			if f == cmd {
				out = append(out, fields)
			}
			break
		}
	}
	return out
}

// stateReads counts `show -json` state reads (not saved plan reads) in root and workspace.
func (s *scopedServer) stateReads(root, ws string) int {
	n := 0
	for _, c := range s.calls("show") {
		if c[len(c)-1] != "-json" {
			continue
		}
		cwd, _ := filepath.EvalSymlinks(spyField(c, "CWD="))
		if cwd == root && spyField(c, "TF_WORKSPACE=") == ws {
			n++
		}
	}
	return n
}

// planned runs a plan in cid and returns its ID.
func (s *scopedServer) planned(cid string) string {
	s.t.Helper()
	done := s.waitScoped(cid, s.plan(cid).ID)
	if done.Status != operations.Succeeded || done.PlanID == "" {
		s.t.Fatalf("plan: %+v", done)
	}
	return done.PlanID
}

// approve runs the preflight and acknowledges each warning, as the UI would.
func (s *scopedServer) approve(cid, pid string) scopedApproval {
	s.t.Helper()
	w := s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/preflight", `{}`)
	if w.Code != 200 {
		s.t.Fatalf("preflight: %d %s", w.Code, w.Body)
	}
	report := decode[scopedPreflight](s.t, w)
	if report.Blocked || report.Binding == "" || report.Scope.ContextID != cid {
		s.t.Fatalf("preflight: %+v", report)
	}
	ack, _ := jsonString(report.Acknowledge)
	w = s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/approval", fmt.Sprintf(`{"preflightBinding":%q,"acknowledged":%s}`, report.Binding, ack))
	if w.Code != 200 {
		s.t.Fatalf("approval: %d %s", w.Code, w.Body)
	}
	a := decode[scopedApproval](s.t, w)
	if a.Token == "" || a.PlanID != pid || a.Scope.ContextID != cid || len(a.ArtifactSHA256) != 64 {
		s.t.Fatalf("approval: %+v", a)
	}
	return a
}

func applyBody(a scopedApproval) string {
	return fmt.Sprintf(`{"confirmationToken":%q,"confirmation":%q}`, a.Token, a.Phrase)
}

func jsonString(v []string) (string, error) {
	quoted := make([]string, len(v))
	for i, s := range v {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ",") + "]", nil
}

// An apply in a Hub workspace runs exactly the reviewed saved plan, once, in its own root
// and workspace, and reads only that workspace again afterwards.
func TestScopedApplyRunsOnlyInItsScope(t *testing.T) {
	s := newScopedServer(t)
	dev := s.openLoaded("network", "dev", "")
	// Two other open workspaces: another project, and another workspace of the same root.
	s.openLoaded("platform", "staging", "")
	s.openLoaded("network", "prod", "")
	pid := s.planned(dev)
	devReads, otherReads, prodReads := s.stateReads(s.roots["network"], "dev"), s.stateReads(s.roots["platform"], "staging"), s.stateReads(s.roots["network"], "prod")

	a := s.approve(dev, pid)
	if a.Phrase != "apply dev" || a.Workspace != "dev" || a.Scope.Workspace != "dev" || a.Scope.ProjectID != s.ids["network"] {
		t.Fatalf("%+v", a)
	}
	w := s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[scopedOp](t, w)
	if op.Kind != "apply" || op.PlanID != pid || op.Scope.ContextID != dev {
		t.Fatalf("%+v", op)
	}
	done := s.waitScoped(dev, op.ID)
	if done.Status != operations.Succeeded || done.Outcome != "applied" || !done.StateReloaded || done.StateReloadFailed {
		t.Fatalf("%+v", done)
	}
	applies := s.calls("apply")
	if len(applies) != 1 {
		t.Fatalf("%d apply calls", len(applies))
	}
	cwd, _ := filepath.EvalSymlinks(spyField(applies[0], "CWD="))
	if cwd != s.roots["network"] || spyField(applies[0], "TF_WORKSPACE=") != "dev" {
		t.Fatalf("apply ran in %q workspace %q", cwd, spyField(applies[0], "TF_WORKSPACE="))
	}
	for _, f := range applies[0] {
		if strings.HasPrefix(f, "-target") || strings.HasPrefix(f, "-replace") || f == "-auto-approve" || f == "-refresh-only" || f == "-destroy" {
			t.Fatalf("apply argv has %s: %q", f, applies[0])
		}
	}
	// Only the applied workspace was read again.
	if s.stateReads(s.roots["network"], "dev") != devReads+1 || s.stateReads(s.roots["platform"], "staging") != otherReads || s.stateReads(s.roots["network"], "prod") != prodReads {
		t.Fatal("state was read again outside the applied workspace")
	}
	// The saved plan is gone: the same token, a new approval and the launch route all fail.
	wantError(t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a)), 409, "approval_required")
	if report := decode[scopedPreflight](t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/preflight", `{}`)); !report.Blocked || report.Binding != "" {
		t.Fatalf("%+v", report)
	}
	detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+dev+"/plans/"+pid, ""))
	if detail.Apply.Available || detail.Apply.ArtifactRetained {
		t.Fatalf("%+v", detail.Apply)
	}
	if n := len(s.calls("apply")); n != 1 {
		t.Fatalf("%d apply calls", n)
	}
}

// A plan ID and approval token from A/dev never apply in another project, another workspace
// of the same project, the same workspace with another profile, or the launch project.
func TestScopedApprovalReplayIsRefused(t *testing.T) {
	s := newScopedServer(t)
	vars := filepath.Join(s.roots["network"], "dev.tfvars")
	if err := os.WriteFile(vars, []byte("# synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "dev-alt", VarFiles: []string{"dev.tfvars"}})
	if err != nil {
		t.Fatal(err)
	}
	dev := s.openLoaded("network", "dev", "")
	pid := s.planned(dev)
	a := s.approve(dev, pid)

	staging := s.openLoaded("platform", "staging", "")
	stagingPlan := s.planned(staging)
	prod := s.openLoaded("network", "prod", "")
	s.planned(prod)
	devProfile := s.openLoaded("network", "dev", profile.ID)
	s.options(devProfile)
	for _, cid := range []string{staging, prod, devProfile} {
		for _, step := range []string{"preflight", "approval", "apply"} {
			body := `{}`
			if step == "apply" {
				body = applyBody(a)
			}
			wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/"+step, body), 404, "plan_not_found")
		}
	}
	// The launch routes do not know the scoped plan.
	wantError(t, s.do("POST", "/api/v1/plans/"+pid+"/apply", applyBody(a)), 404, "plan_not_found")
	wantError(t, s.do("POST", "/api/v1/plans/"+pid+"/preflight", `{}`), 404, "plan_not_found")

	// A's token on B's own plan is refused, and consumes only B's approval.
	b := s.approve(staging, stagingPlan)
	wantError(t, s.do("POST", "/api/v1/contexts/"+staging+"/plans/"+stagingPlan+"/apply", fmt.Sprintf(`{"confirmationToken":%q,"confirmation":%q}`, a.Token, b.Phrase)), 409, "approval_invalid")
	wantError(t, s.do("POST", "/api/v1/contexts/"+staging+"/plans/"+stagingPlan+"/apply", applyBody(b)), 409, "approval_required")
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}

	// A's approval is still A's: a wrong phrase consumes it.
	wantError(t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", fmt.Sprintf(`{"confirmationToken":%q,"confirmation":"apply prod"}`, a.Token)), 409, "confirmation_mismatch")
	wantError(t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a)), 409, "approval_required")
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
}

// Reading the workspace again opens a new context: the old context's plan, preflight and
// approval are gone.
func TestScopedApprovalDoesNotSurviveAReread(t *testing.T) {
	s := newScopedServer(t)
	dev := s.openLoaded("network", "dev", "")
	pid := s.planned(dev)
	a := s.approve(dev, pid)
	fresh := s.openLoaded("network", "dev", "")
	if fresh == dev {
		t.Fatal("reread kept the context")
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+dev+"/plans/"+pid+"/apply", applyBody(a)), 404, "context_closed")
	wantError(t, s.do("POST", "/api/v1/contexts/"+fresh+"/plans/"+pid+"/apply", applyBody(a)), 404, "plan_not_found")
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
}

// Anything that changes what was reviewed, between approval and apply, refuses the apply
// before the engine runs.
func TestScopedApplyRefusesChangesAfterReview(t *testing.T) {
	s := newScopedServer(t)
	root := s.roots["network"]
	write := func(name, text string) func() {
		path := filepath.Join(root, name)
		before, err := os.ReadFile(path)
		existed := err == nil
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return func() {
			if existed {
				_ = os.WriteFile(path, before, 0o644)
			} else {
				_ = os.Remove(path)
			}
		}
	}
	vars := filepath.Join(root, "dev.tfvars")
	if err := os.WriteFile(vars, []byte("# synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := s.hub.Profiles.Create(s.ids["network"], profiles.Input{Name: "dev-vars", VarFiles: []string{"dev.tfvars"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		profile string
		change  func() func()
		status  int
		code    string
		detail  string
	}{
		{"source", "", func() func() { return write("main.tf", "# Synthetic Hclaro fixture. Not real infrastructure.\nresource \"aws_s3_bucket\" \"other\" {}\n") }, 409, "context_changed", ""},
		{"lock file", "", func() func() { return write(".terraform.lock.hcl", "# synthetic\n") }, 409, "context_changed", ""},
		{"variable file", profile.ID, func() func() { return write("dev.tfvars", "# synthetic, changed\n") }, 409, "context_changed", ""},
		{"backend metadata", "", func() func() {
			return write(filepath.Join(".terraform", "terraform.tfstate"), `{"version":3,"backend":{"type":"s3","config":{}}}`)
		}, 409, projects.CodeScopeChanged, projects.DetailBackendChanged},
		{"environment", "", func() func() {
			t.Setenv("AWS_PROFILE", "synthetic-other")
			return func() { t.Setenv("AWS_PROFILE", "") }
		}, 409, projects.CodeScopeChanged, projects.DetailEnvironmentChanged},
		{"profile", profile.ID, func() func() {
			p, err := s.hub.Profiles.Get(s.ids["network"], profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.hub.Profiles.Update(s.ids["network"], profile.ID, p.Revision, profiles.Input{Name: "dev-vars", VarFiles: []string{"dev.tfvars"}, AWSRegion: "eu-west-1"}); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}, 409, projects.CodeScopeChanged, projects.DetailProfileChanged},
		{"engine binary", "", func() func() {
			exe := filepath.Join(s.bin, "terraform")
			if _, err := os.Stat(exe); err != nil {
				exe += ".exe"
			}
			before, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exe, append(before, 0), 0o755); err != nil {
				t.Fatal(err)
			}
			return func() { _ = os.WriteFile(exe, before, 0o755) }
		}, 409, projects.CodeScopeChanged, projects.DetailEngineChanged},
		{"root folder", "", func() func() {
			moved := root + ".moved"
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			return func() { _ = os.Remove(root); _ = os.Rename(moved, root) }
		}, 409, projects.CodeScopeChanged, projects.DetailRootChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cid := s.openLoaded("network", "dev", tc.profile)
			pid := s.planned(cid)
			a := s.approve(cid, pid)
			restore := tc.change()
			defer restore()
			body := wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)), tc.status, tc.code)
			if body["detail"] != tc.detail {
				t.Fatalf("%v", body)
			}
			if n := len(s.calls("apply")); n != 0 {
				t.Fatalf("%d apply calls", n)
			}
		})
	}
}

// Workspace deletion and saved plan tampering are found by the checks immediately before
// the engine would run.
func TestScopedApplyRechecksBeforeTheEngine(t *testing.T) {
	s := newScopedServer(t)
	root := s.roots["network"]

	// The saved plan file changes after approval: the preflight that runs again at apply
	// time sees the digest mismatch.
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	a := s.approve(cid, pid)
	saved, _ := filepath.Glob(filepath.Join(s.hub.TempDir, "hclaro-plan-*", "*"))
	if len(saved) != 1 {
		t.Fatalf("saved plans: %v", saved)
	}
	if err := os.WriteFile(saved[0], []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)), 409, "preflight_blocked")

	// The workspace is deleted outside Hclaro after approval: the apply starts, the scope's
	// engine checks with the root claim held find it missing, and nothing is applied.
	cid = s.openLoaded("network", "dev", "")
	pid = s.planned(cid)
	a = s.approve(cid, pid)
	scope, err := s.hub.ExistingScope(cid)
	if err != nil || scope == nil {
		t.Fatal(err)
	}
	list := filepath.Join(root, ".fake-workspaces")
	before, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, []byte("  default\n* prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(list, before, 0o644)
	w := s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	op := decode[scopedOp](t, w)
	// The scope change closes the context, so the operation is read from the scope itself.
	var done operations.Operation
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if o, _, ok := scope.Operation(op.ID); ok && o.Status != operations.Running {
			done = o
			break
		}
	}
	if done.Status != operations.Failed || done.Outcome != "not_applied" || done.Diagnostic == nil || done.Diagnostic.Code != projects.CodeScopeChanged {
		t.Fatalf("%+v", done)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, _, err := s.hub.Context(cid); err != nil {
			break
		}
	}
	wantError(t, s.do("GET", "/api/v1/contexts/"+cid+"/plans", ""), 404, "context_closed")
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
	if left, _ := filepath.Glob(filepath.Join(s.hub.TempDir, "hclaro-plan-*")); len(left) != 0 {
		// The closed scope deletes its saved plan in the background.
		time.Sleep(500 * time.Millisecond)
		if left, _ = filepath.Glob(filepath.Join(s.hub.TempDir, "hclaro-plan-*")); len(left) != 0 {
			t.Fatalf("saved plans left: %v", left)
		}
	}
}

// Unsupported action combinations are refused in a Hub workspace exactly as in the launch
// project (scoped TestUnknownActionsRefusedOverHTTP).
func TestScopedUnknownActionsRefused(t *testing.T) {
	s := newScopedServer(t)
	t.Setenv("HCLARO_FAKE_PLAN", "unsupported")
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	detail := decode[scopedPlanDetail](t, s.do("GET", "/api/v1/contexts/"+cid+"/plans/"+pid, ""))
	if detail.Apply.Available || detail.Apply.Code != "unknown_actions" || detail.Apply.ArtifactRetained {
		t.Fatalf("%+v", detail.Apply)
	}
	w := s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/preflight", `{}`)
	if report := decode[scopedPreflight](t, w); w.Code != 200 || !report.Blocked || report.Binding != "" {
		t.Fatalf("%d %+v", w.Code, report)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/approval", `{"preflightBinding":"x","acknowledged":[]}`), 409, "unknown_actions")
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", `{"confirmationToken":"x","confirmation":"apply dev"}`), 409, "approval_required")
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
}

// Two clicks with one approval start one apply; the second is refused. A second apply
// request while one runs is refused too.
func TestScopedApplyDuplicateClick(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	a := s.approve(cid, pid)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = s.hubServer.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)).Code
		}()
	}
	wg.Wait()
	started := 0
	var oid string
	for _, code := range codes {
		if code == 202 {
			started++
		} else if code != 409 {
			t.Fatalf("codes %v", codes)
		}
	}
	if started != 1 {
		t.Fatalf("codes %v", codes)
	}
	ops := decode[struct {
		Operations []operations.Operation `json:"operations"`
	}](t, s.do("GET", "/api/v1/contexts/"+cid+"/operations", ""))
	for _, o := range ops.Operations {
		if o.Kind == "apply" {
			oid = o.ID
		}
	}
	if done := s.waitScoped(cid, oid); done.Outcome != "applied" {
		t.Fatalf("%+v", done)
	}
	if n := len(s.calls("apply")); n != 1 {
		t.Fatalf("%d apply calls", n)
	}
}

// Interrupting an apply reports an uncertain outcome, deletes the saved plan and releases
// the project root.
func TestScopedApplyCancelIsUncertain(t *testing.T) {
	s := newScopedServer(t)
	t.Setenv("HCLARO_FAKE_APPLY", "slow")
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	a := s.approve(cid, pid)
	op := decode[scopedOp](t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)))
	for deadline := time.Now().Add(20 * time.Second); len(s.calls("apply")) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("apply did not start")
		}
	}
	// A scope change seen by a request while the apply runs refuses the request but does not
	// interrupt the apply, which already passed its checks.
	original, set := os.LookupEnv("AWS_PROFILE")
	t.Setenv("AWS_PROFILE", "synthetic-other")
	body := wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/preflight", `{}`), 409, projects.CodeScopeChanged)
	if body["detail"] != projects.DetailEnvironmentChanged {
		t.Fatalf("%v", body)
	}
	if set {
		os.Setenv("AWS_PROFILE", original)
	} else {
		os.Unsetenv("AWS_PROFILE")
	}
	if running := decode[scopedOp](t, s.do("GET", "/api/v1/contexts/"+cid+"/operations/"+op.ID, "")); running.Status != operations.Running || running.CancelRequested {
		t.Fatalf("%+v", running)
	}
	// Another workspace of the same root cannot even be read during the apply.
	other := s.hubServer.do("POST", "/api/v1/contexts", fmt.Sprintf(`{"projectId":%q,"workspace":"prod","reload":true}`, s.ids["network"]))
	if other.Code != 409 {
		t.Fatalf("open during apply: %d %s", other.Code, other.Body)
	}
	if w := s.do("POST", "/api/v1/contexts/"+cid+"/operations/"+op.ID+"/cancel", `{}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	done := s.waitScoped(cid, op.ID)
	if done.Status != operations.Cancelled || done.Outcome != "uncertain" || done.Diagnostic == nil || done.Diagnostic.Code != "apply_cancelled" {
		t.Fatalf("%+v", done)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)), 409, "approval_required")
	if left, _ := filepath.Glob(filepath.Join(s.hub.TempDir, "hclaro-plan-*")); len(left) != 0 {
		t.Fatalf("saved plans left: %v", left)
	}
	// The root is free again.
	t.Setenv("HCLARO_FAKE_APPLY", "")
	s.openLoaded("network", "prod", "")
	if n := len(s.calls("apply")); n != 1 {
		t.Fatalf("%d apply calls", n)
	}
}

// Shutting Hclaro down during an apply reports an uncertain outcome and deletes the plan.
func TestScopedApplyShutdownIsUncertain(t *testing.T) {
	s := newScopedServer(t)
	t.Setenv("HCLARO_FAKE_APPLY", "slow")
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	a := s.approve(cid, pid)
	scope, err := s.hub.ExistingScope(cid)
	if err != nil || scope == nil {
		t.Fatal(err)
	}
	op := decode[scopedOp](t, s.do("POST", "/api/v1/contexts/"+cid+"/plans/"+pid+"/apply", applyBody(a)))
	for deadline := time.Now().Add(20 * time.Second); len(s.calls("apply")) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("apply did not start")
		}
	}
	s.hub.Close()
	done, _, ok := scope.Operation(op.ID)
	if !ok || done.Status == operations.Running || done.Outcome != "uncertain" || done.Diagnostic == nil || done.Diagnostic.Code != "shutdown" {
		t.Fatalf("%+v", done)
	}
	if left, _ := filepath.Glob(filepath.Join(s.hub.TempDir, "hclaro-plan-*")); len(left) != 0 {
		t.Fatalf("saved plans left: %v", left)
	}
	if _, held := s.hub.Locks.Holder(s.roots["network"]); held {
		t.Fatal("root claim still held")
	}
}

// Scoped apply routes answer like the other scoped routes while the gate is off, and
// refuse fields a request must not carry.
func TestScopedApplyRequestValidation(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	pid := s.planned(cid)
	base := "/api/v1/contexts/" + cid + "/plans/" + pid + "/"
	for _, body := range []string{
		`{"confirmationToken":"x","confirmation":"apply dev","target":["aws_s3_bucket.logs"]}`,
		`{"confirmationToken":"x","confirmation":"apply dev","planFile":"/tmp/plan"}`,
		`{"confirmationToken":"x","confirmation":"apply dev","workspace":"prod"}`,
		`{"confirmationToken":"x","confirmation":"apply dev","contextId":"` + strings.Repeat("0", 32) + `"}`,
		`{"confirmationToken":"` + strings.Repeat("a", 129) + `","confirmation":"apply dev"}`,
	} {
		wantError(t, s.do("POST", base+"apply", body), 400, "invalid_request")
	}
	wantError(t, s.do("POST", base+"preflight", `{"verifyIdentity":false,"profileId":"x"}`), 400, "invalid_request")
	wantError(t, s.do("POST", base+"approval", `{"preflightBinding":"x","acknowledged":[],"all":true}`), 400, "invalid_request")
	// A malformed plan ID is no route (the catch-all answers 404).
	if w := s.do("POST", "/api/v1/contexts/"+cid+"/plans/not-an-id/apply", `{}`); w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+strings.Repeat("e", 32)+"/plans/"+pid+"/apply", `{}`), 404, "context_closed")

	s.hub.ScopedExecution = false
	for _, step := range []string{"preflight", "approval", "apply"} {
		wantError(t, s.do("POST", base+step, `{}`), 404, projects.CodeScopedDisabled)
		wantError(t, s.do("POST", "/api/v1/contexts/"+strings.Repeat("e", 32)+"/plans/"+pid+"/"+step, `{}`), 404, projects.CodeScopedDisabled)
	}
	if n := len(s.calls("apply")); n != 0 {
		t.Fatalf("%d apply calls", n)
	}
}
