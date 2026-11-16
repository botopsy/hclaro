//go:build realengine

package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/internal/engine"
)

// M27.05 Hub execution scenarios (tasks/M27/tasks/M27-05-HUB-EXECUTION.md). Every Hclaro
// process runs `--hub --no-browser --experimental-hub-execution` (the flag is passed
// explicitly and recorded per scenario) with both configured engines in its PATH directory
// and the combined mirror config. Each project is registered with an explicit engine
// preference; every scoped options, plan and operation response's engine and version are
// asserted (assertScope). Operations that must overlap are held by the hub-hold fixture's
// named pipe, never by timing.

// hubProcess prepares one Hub process's directories with every configured engine.
func hubProcess(s *Scenario) (ProcessDirs, []string) {
	dirs, env, err := s.Process(engines()...)
	if err != nil {
		s.Fatalf("%v", err)
	}
	if len(engines()) > 1 {
		s.Observe("combined CLI config hub.tfrc sha256 %s", s.run.hubConfigSha)
	}
	return dirs, env
}

// view opens a read context on workspace (with profile, "" for none) and returns the
// handle that drives its scoped routes.
func (s *Scenario) view(h *Hclaro, pid, project, engine, workspace, profile string) *Hclaro {
	cid, _ := s.OpenView(h, pid, workspace, profile)
	return h.In(HubView{ContextID: cid, ProjectID: pid, Project: project, Engine: engine, Workspace: workspace})
}

// contextRead is a view's read result: status, diagnostic code and the backend Hclaro found.
func (s *Scenario) contextRead(h *Hclaro, cid string) (status, code, backend string, initialized bool) {
	var c struct {
		Status     string `json:"status"`
		Diagnostic *struct {
			Code string `json:"code"`
		} `json:"diagnostic"`
		Info struct {
			Backend     string `json:"backend"`
			Initialized bool   `json:"initialized"`
		} `json:"info"`
	}
	if r, err := h.client.Do("GET", "contexts/"+cid, nil, &c); err != nil || r.Status != 200 {
		s.Fatalf("context: %v %d", err, r.Status)
	}
	if c.Diagnostic != nil {
		code = c.Diagnostic.Code
	}
	return c.Status, code, c.Info.Backend, c.Info.Initialized
}

// hubStart is the H2 starter: a Hub process with both engines, the project registered with
// engine as its preference and its default workspace open.
func hubStart(s *Scenario, engine, project string) (ProcessDirs, []string, *Hclaro) {
	dirs, env := hubProcess(s)
	s.SetupInit(dirs, env, engine, project)
	h := s.StartHub(dirs, env, true)
	pid := s.HubRegister(h, project, engine, engine)
	return dirs, env, s.view(h, pid, project, engine, "default", "")
}

// hubProject prepares template as projects/<name>, starts one Hub process and registers the
// project with engine. It returns the project, the process handle and the project ID.
func hubProject(s *Scenario, engine, template string, allowed ...string) (string, ProcessDirs, []string, *Hclaro, string) {
	project := s.Project(template, append(slices.Clone(stateWrites), allowed...)...)
	dirs, env := hubProcess(s)
	s.SetupInit(dirs, env, engine, project)
	h := s.StartHub(dirs, env, true)
	return project, dirs, env, h, s.HubRegister(h, project, engine, engine)
}

func hubScenario(t *testing.T, id, engine string, fn func(s *Scenario)) {
	run.Scenario(t, id, engine, "hub", true, fn)
}

func other(engine string) string {
	if engine == "terraform" {
		return "tofu"
	}
	return "terraform"
}

// pipe holds an operation that reads the hub-hold fixture's data.local_file.gate (var file
// hold.tfvars) through a gate (gate.go): a FIFO on Unix, a harness-served named pipe on
// Windows (FU-WIN-HOLD).
type pipe struct{ gate }

func (s *Scenario) holdPipe(project string) *pipe {
	g, err := newGate(project)
	if err != nil {
		s.NotTested("no gate to hold an operation on this platform: " + err.Error())
	}
	return &pipe{g}
}

// release lets the held engine read finish.
func (p *pipe) release() { p.Release() }

// close releases the gate and frees it.
func (p *pipe) close() { p.Close() }

// startHeld starts a plan with hold.tfvars and waits until its SSE stream reports it
// running. The plan stays running until the pipe is released.
func (s *Scenario) startHeld(h *Hclaro, project string) (Operation, *pipe) {
	p := s.holdPipe(project)
	body := map[string]any{"acknowledgeBackendAccess": true, "varFiles": []string{"hold.tfvars"}, "replace": []string{}}
	if h.view != nil {
		body["contextFingerprint"] = s.fingerprint(h, "plans/options")
	}
	s.Guard(project)
	var op Operation
	r, err := h.client.Do("POST", "plans", body, &op)
	if err != nil || r.Status != 202 {
		p.close()
		s.Fatalf("held plan: %v %d %s", err, r.Status, r.Code)
	}
	s.waitRunningSSE(h, op.ID, p)
	return op, p
}

// waitRunningSSE reads the operation's SSE stream until a status event says running.
func (s *Scenario) waitRunningSSE(h *Hclaro, id string, p *pipe) {
	if ok, err := h.client.WaitLog("operations/"+id, `"status":"running"`, 2*time.Minute); err != nil || !ok {
		p.close()
		s.Fatalf("operation %s never reported running on its SSE stream: %v", id, err)
	}
}

// opStatus reads an operation's current status.
func (s *Scenario) opStatus(h *Hclaro, id string) string {
	var op Operation
	if r, err := h.client.Do("GET", "operations/"+id, nil, &op); err != nil || r.Status != 200 {
		s.Fatalf("operation: %v %d", err, r.Status)
	}
	s.assertScope(h, op.Scope)
	return op.Status
}

func (s *Scenario) planBody(h *Hclaro, varFiles []string) map[string]any {
	return map[string]any{"acknowledgeBackendAccess": true, "varFiles": varFiles, "replace": []string{}, "contextFingerprint": s.fingerprint(h, "plans/options")}
}

// TestRealHubEngines covers H1 and H4: both engines in one Hub process.
func TestRealHubEngines(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	both := strings.Join(engines(), "+")
	hubScenario(t, "h1-register-open", both, func(s *Scenario) {
		if len(engines()) < 2 {
			s.NotTested("H1 needs Terraform and OpenTofu in one Hub process; this run configures " + both)
		}
		dirs, env := hubProcess(s)
		projects := map[string]string{}
		for _, name := range engines() {
			projects[name] = s.ProjectAs("hub-hold", "p-"+name, stateWrites...)
			s.SetupInit(dirs, env, name, projects[name])
			// The combined config installed each family's providers from its own mirror.
			var cli string
			for _, kv := range env {
				if v, ok := strings.CutPrefix(kv, "TF_CLI_CONFIG_FILE="); ok {
					cli = v
				}
			}
			checkLockfile(s, name, projects[name], cli)
		}
		h := s.StartHub(dirs, env, true)
		for _, name := range engines() {
			pid := s.HubRegister(h, projects[name], name, name)
			v := s.view(h, pid, projects[name], name, "default", "")
			// A new project has no state: the read ends state_absent, which can be planned.
			if st, c, backend, _ := s.contextRead(h, v.view.ContextID); st != "error" || c != "state_absent" || backend != "local" {
				s.Fatalf("H1 %s context read %s %s backend %q, want error state_absent on the local backend", name, st, c, backend)
			}
			var plan struct {
				CanPlan bool     `json:"canPlan"`
				Reason  string   `json:"reason"`
				Scope   *OpScope `json:"scope"`
			}
			if r, err := v.client.Do("GET", "plans/options", nil, &plan); err != nil || r.Status != 200 || !plan.CanPlan {
				s.Fatalf("%s plans/options: %v %d can %v %s", name, err, r.Status, plan.CanPlan, plan.Reason)
			}
			s.assertScope(v, plan.Scope)
			var imp struct {
				CanApply    bool     `json:"canApply"`
				ApplyReason string   `json:"applyReason"`
				Scope       *OpScope `json:"scope"`
			}
			if r, err := v.client.Do("GET", "imports/options", nil, &imp); err != nil || r.Status != 200 || !imp.CanApply {
				s.Fatalf("%s imports/options: %v %d canApply %v %s", name, err, r.Status, imp.CanApply, imp.ApplyReason)
			}
			s.assertScope(v, imp.Scope)
			s.Observe("H1 %s: %s %s, workspace %s, canPlan and canApply", filepath.Base(projects[name]), plan.Scope.Engine, plan.Scope.Version, plan.Scope.Workspace)
		}
	})
	hubScenario(t, "h4-parallel-engines", both, func(s *Scenario) {
		if len(engines()) < 2 {
			s.NotTested("H4 needs Terraform and OpenTofu in one Hub process; this run configures " + both)
		}
		dirs, env := hubProcess(s)
		projects, views := map[string]string{}, map[string]*Hclaro{}
		for _, name := range engines() {
			projects[name] = s.ProjectAs("hub-hold", "p-"+name, stateWrites...)
			s.SetupInit(dirs, env, name, projects[name])
		}
		h := s.StartHub(dirs, env, true)
		for _, name := range engines() {
			pid := s.HubRegister(h, projects[name], name, name)
			views[name] = s.view(h, pid, projects[name], name, "default", "")
		}
		ops, pipes := map[string]Operation{}, map[string]*pipe{}
		for _, name := range engines() {
			op, p := s.startHeld(views[name], projects[name])
			defer p.close()
			ops[name], pipes[name] = op, p
		}
		// Each plan is blocked on its own pipe, which only the harness writes, so both were
		// running at once when the second was observed running.
		for _, name := range engines() {
			if st := s.opStatus(views[name], ops[name].ID); st != "running" {
				s.Fatalf("H4 %s plan is %s while both are held", name, st)
			}
		}
		s.Observe("H4 both plans observed running at the same time (SSE, then GET while both held)")
		for _, name := range engines() {
			pipes[name].release()
		}
		for _, name := range engines() {
			v := views[name]
			text, err := v.client.Text("operations/"+ops[name].ID, 5*time.Minute)
			if err != nil {
				s.Fatalf("H4 %s events: %v", name, err)
			}
			s.Artifact("events-"+name+".txt", []byte(text))
			op := s.FollowOp(v, ops[name].ID, 5*time.Minute)
			if op.Status != "succeeded" {
				s.Fatalf("H4 %s plan ended %s (%s)", name, op.Status, code(op))
			}
			e, _ := run.Engine(name)
			if err := ownEvents(text, ops[name].ID, name); err != nil {
				s.Fatalf("H4 %s events: %v", name, err)
			}
			s.Observe("H4 %s %s: plan %s, argv %v; every event is this operation's, with this engine's argv", name, e.Version, op.Status, op.Command)
		}
	})
}

// ownEvents checks an operation's SSE data lines: every status event is operation id with
// engine's argv, and no line names the other engine's argv. Hclaro's log events carry its own
// summaries of the engine's -json output, so the argv is where the engine shows.
func ownEvents(text, id, engine string) error {
	statuses := 0
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.Contains(line, `"command":["`+other(engine)+`"`) {
			return fmt.Errorf("an event names %s's argv: %.200s", other(engine), line)
		}
		if strings.HasPrefix(line, `{"id":`) {
			statuses++
			if !strings.HasPrefix(line, `{"id":"`+id+`"`) || !strings.Contains(line, `"command":["`+engine+`",`) {
				return fmt.Errorf("a status event is not this operation's %s run: %.200s", engine, line)
			}
		}
	}
	if statuses == 0 {
		return fmt.Errorf("no status events")
	}
	return nil
}

// TestRealHubFlows covers H2 (A1, A2, A3, R1, I1, D1, G1 in a Hub workspace view) and H3.
func TestRealHubFlows(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	flows := []struct {
		id string
		fn func(*Scenario, string, starter)
	}{
		{"a1", a1Apply}, {"a2", a2Destructive}, {"a3", a3StalePlan}, {"r1", r1Replace}, {"i1", i1ImportScenario},
		{"d1", d1DriftDeleted}, {"g1-m2616", g1GraphM2616}, {"g1-modules", g1GraphModules},
	}
	for _, name := range engines() {
		for _, f := range flows {
			hubScenario(t, "h2-"+f.id+"-"+name, name, func(s *Scenario) { f.fn(s, name, hubStart) })
		}
		hubScenario(t, "h3-workspaces-"+name, name, func(s *Scenario) { h3Workspaces(s, name) })
	}
}

func h3Workspaces(s *Scenario, name string) {
	project, dirs, env, h, pid := hubProject(s, name, "launch", wsWrites("qa")...)
	// `workspace new` selects the new workspace (engine behaviour, as in M27.03 W1); select
	// default again through Hclaro, then the plans and applies in qa must not change it.
	wsRun(s, h, dirs, env, name, project, pid, "new", "qa")
	wsRun(s, h, dirs, env, name, project, pid, "select", "default")
	before, _ := cliWorkspaces(s, dirs, env, name, project)
	def := s.view(h, pid, project, name, "default", "")
	base := applyBase(s, def, dirs, env, name, project)
	sum, serial := stateFile(s, project)
	qa := s.view(h, pid, project, name, "qa", "")
	// qa has no state: its plan creates everything default already holds.
	qplan := s.LaunchPlan(qa, project, name, []string{"launch.tfvars"})
	if creates := planned(qplan, "create"); len(creates) != len(base) || len(qplan.Changes) != len(base) {
		s.Fatalf("H3 qa plan %s: does not plan against qa's empty state", qplan)
	}
	if _, op := s.ApplyReviewed(qa, qplan.ID); op.Outcome != "applied" {
		s.Fatalf("H3 qa apply ended %s %s", op.Status, op.Outcome)
	}
	qstate := s.ShowState(dirs, env, name, project, "terraform.tfstate.d/qa/terraform.tfstate")
	for a, o := range qstate {
		if base[a].ID == o.ID {
			s.Fatalf("H3 qa object %s has default's ID", a)
		}
	}
	if len(qstate) != len(base) {
		s.Fatalf("H3 qa state holds %v", addresses(qstate))
	}
	assertStateUnchanged(s, project, sum, serial, "in default by the qa apply")
	// An approval of default's plan is refused in qa.
	dplan := s.LaunchPlan(def, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	a, r := s.Approve(def, dplan.ID)
	if r.Status != 200 {
		s.Fatalf("H3 default approval: %d %s", r.Status, r.Code)
	}
	qplan2 := s.LaunchPlan(qa, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	qfile := filepath.Join(project, "terraform.tfstate.d", "qa", "terraform.tfstate")
	qsum, _ := fileSHA256(qfile)
	if _, r := s.StartApply(qa, dplan.ID, a.Token, a.Phrase); r.Status != 404 || r.Code != "plan_not_found" {
		s.Fatalf("H3 default's plan through qa: %d %s, want 404 plan_not_found", r.Status, r.Code)
	}
	if _, r := s.StartApply(qa, qplan2.ID, a.Token, a.Phrase); r.Status != 409 {
		s.Fatalf("H3 default's approval on qa's plan: %d %s, want a 409 refusal", r.Status, r.Code)
	} else {
		s.Observe("H3 default's approval token on qa's plan: %d %s", r.Status, r.Code)
	}
	assertStateUnchanged(s, project, sum, serial, "in default by the refused applies")
	if now, _ := fileSHA256(qfile); now != qsum {
		s.Fatalf("H3 qa state changed by the refused applies")
	}
	after, _ := cliWorkspaces(s, dirs, env, name, project)
	s.Observe("H3 CLI default workspace %q before, %q after", before, after)
	if before != "default" || after != before {
		s.Fatalf("H3 CLI default workspace %q → %q", before, after)
	}
}

// TestRealHubPaths covers H5 (same folder through two paths), with the first engine.
func TestRealHubPaths(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	name := engines()[0]
	type variant struct{ id, why string }
	sensitive := run.Result.FilesystemCaseSensitive
	fsNote := fmt.Sprintf("run root filesystem case-sensitive %v (probed by the harness; F-4 found APFS case-insensitive on darwin/amd64)", sensitive)
	paths := func(s *Scenario, project string) map[string][]string {
		parent, base := filepath.Dir(project), filepath.Base(project)
		alias := filepath.Join(s.Dir, "alias")
		return map[string][]string{
			"h5a": {project + "/", project + "/./", project + "/../" + base},
			"h5b": {alias},
			"h5c": {filepath.Join(parent, strings.ToLower(base))},
		}
	}
	for _, id := range []string{"h5a", "h5b", "h5c"} {
		hubScenario(t, id+"-path-identity-"+name, name, func(s *Scenario) {
			s.Observe("H5 %s", fsNote)
			if id == "h5c" && sensitive {
				s.Skip("case-only variant on a case-sensitive filesystem (" + fsNote + ")")
			}
			project := s.ProjectAs("hub-hold", "Proj", stateWrites...)
			forms := paths(s, project)[id]
			if id == "h5b" {
				if err := os.Symlink(project, forms[0]); err != nil {
					s.Skip("symlink not creatable without elevation: " + err.Error())
				}
			}
			dirs, env := hubProcess(s)
			s.SetupInit(dirs, env, name, project)
			for _, form := range forms {
				h5Claim(s, dirs, env, name, project, form, forms)
			}
		})
	}
	hubScenario(t, "h5c-prime-distinct-case-"+name, name, func(s *Scenario) {
		if !sensitive {
			s.Skip("two folders differing only by case cannot exist on a case-insensitive filesystem (" + fsNote + ")")
		}
		s.Observe("H5c′ requires a case-sensitive volume: %s", fsNote)
		h5cPrime(s, name)
	})
	// H5d is WIN2 (M27.07): drive-letter case, the 8.3 short name and a junction alias.
	hubScenario(t, "h5d-windows-forms-"+name, name, func(s *Scenario) {
		if runtime.GOOS != "windows" {
			s.Skip("drive-letter case, 8.3 short names and junctions apply to windows/amd64 only (WIN2)")
		}
		project := s.ProjectAs("hub-hold", "Proj", stateWrites...)
		vol := filepath.VolumeName(project)
		swapped := strings.ToLower(vol)
		if swapped == vol {
			swapped = strings.ToUpper(vol)
		}
		forms := []string{swapped + project[len(vol):]}
		if short := shortPath(project); short != "" && !strings.EqualFold(short, project) {
			forms = append(forms, short)
		} else {
			s.Observe("WIN2: no 8.3 short name differs from the path (%q); that form is not exercised", s.run.San.String(short))
		}
		junction := filepath.Join(s.Dir, "junction")
		if out, err := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", junction, project).CombinedOutput(); err != nil {
			s.Fatalf("WIN2 junction: %v %s", err, out)
		}
		forms = append(forms, junction)
		s.Observe("WIN2 forms: %s", s.run.San.String(strings.Join(forms, " | ")))
		dirs, env := hubProcess(s)
		s.SetupInit(dirs, env, name, project)
		for _, form := range forms {
			h5Claim(s, dirs, env, name, project, form, forms)
		}
	})
}

// h5cPrime (H5c′, case-sensitive volumes): "Proj" and "proj" are two distinct folders, so they
// are two projects with independent claims. A plan held on one must not block a plan on the
// other; both proceed.
func h5cPrime(s *Scenario, name string) {
	upper := s.ProjectAs("hub-hold", "Proj", stateWrites...)
	lower := s.ProjectAs("hub-hold", "proj", stateWrites...)
	if filepath.Base(upper) == filepath.Base(lower) || filepath.Dir(upper) != filepath.Dir(lower) {
		s.Fatalf("H5c′ folders are not siblings differing only by case: %s, %s", filepath.Base(upper), filepath.Base(lower))
	}
	dirs, env := hubProcess(s)
	s.SetupInit(dirs, env, name, upper)
	s.SetupInit(dirs, env, name, lower)
	h := s.StartHub(dirs, env, true)
	up, low := s.HubRegister(h, upper, name, name), s.HubRegister(h, lower, name, name)
	if up == low {
		s.Fatalf("H5c′ Proj and proj registered as one project %q", up)
	}
	vu, vl := s.view(h, up, upper, name, "default", ""), s.view(h, low, lower, name, "default", "")
	held, p := s.startHeld(vu, upper)
	defer p.close()
	var op Operation
	r, err := vl.client.Do("POST", "plans", s.planBody(vl, []string{"launch.tfvars"}), &op)
	if err != nil || r.Status != 202 {
		s.Fatalf("H5c′ plan on proj while Proj is held: %v %d %s, want 202", err, r.Status, r.Code)
	}
	if done := s.FollowOp(vl, op.ID, 5*time.Minute); done.Status != "succeeded" {
		s.Fatalf("H5c′ plan on proj ended %s (%s)", done.Status, code(done))
	}
	if st := s.opStatus(vu, held.ID); st != "running" {
		s.Fatalf("H5c′ held plan on Proj is %s after the proj plan finished, want running", st)
	}
	p.release()
	if done := s.FollowOp(vu, held.ID, 5*time.Minute); done.Status != "succeeded" {
		s.Fatalf("H5c′ held plan on Proj ended %s (%s)", done.Status, code(done))
	}
	s.Observe("H5c′ Proj and proj are projects %s and %s; the proj plan succeeded while the Proj plan was held, then the Proj plan succeeded", up, low)
}

// h5Claim starts Hclaro on the launch path form (with the Hub flag), registers the
// canonical folder in its Hub, checks every form is refused as the same folder, then holds
// a launch plan and asserts the Hub plan on the folder is refused for the root claim.
func h5Claim(s *Scenario, dirs ProcessDirs, env []string, name, project, form string, forms []string) {
	h, err := s.StartHclaro(dirs, env, nil, "--path", form, "--no-browser", "--engine", name, "--experimental-hub-execution")
	if err != nil {
		s.Fatalf("start hclaro on %s: %v", form, err)
	}
	s.waitReady(h)
	pid := s.HubRegister(h, project, name, name)
	for _, f := range forms {
		var dup struct {
			ExistingID string `json:"existingId"`
		}
		r, err := h.client.Do("POST", "projects", map[string]string{"path": f, "alias": "dup", "engine": name}, nil)
		if err == nil && r.Status == 409 {
			_ = json.Unmarshal(r.Body, &dup)
		}
		if err != nil || r.Status != 409 || r.Code != "duplicate_project" || dup.ExistingID != pid {
			s.Fatalf("H5 register %s: %v %d %s existing %q, want 409 duplicate_project of the same project", s.run.San.String(f), err, r.Status, r.Code, dup.ExistingID)
		}
	}
	v := s.view(h, pid, project, name, "default", "")
	op, p := s.startHeld(h, project)
	defer p.close()
	var hop Operation
	r, err := v.client.Do("POST", "plans", s.planBody(v, []string{"launch.tfvars"}), &hop)
	if err != nil || r.Status != 409 || r.Code != "project_busy" {
		if r.Status == 202 {
			s.FollowOp(v, hop.ID, 5*time.Minute)
		}
		s.Fatalf("H5 Hub plan while the launch plan (path %s) holds the folder: %v %d %s, want 409 project_busy", s.run.San.String(form), err, r.Status, r.Code)
	}
	if st := s.opStatus(h, op.ID); st != "running" {
		s.Fatalf("H5 launch plan %s at the refusal", st)
	}
	p.release()
	if done := s.FollowOp(h, op.ID, 5*time.Minute); done.Status != "succeeded" {
		s.Fatalf("H5 held launch plan ended %s (%s)", done.Status, code(done))
	}
	// With the claim released the same Hub plan runs: the refusal was the shared claim.
	s.LaunchPlan(v, project, name, []string{"launch.tfvars"})
	s.Observe("H5 launch path %s: %d forms refused as duplicates; Hub plan refused project_busy while the launch plan ran, then ran", s.run.San.String(form), len(forms))
	// The next form's process shares this configuration directory (registry).
	if r, err := h.client.Do("DELETE", "projects/"+pid, map[string]any{}, nil); err != nil || r.Status != 200 {
		s.Fatalf("unregister: %v %d %s", err, r.Status, r.Code)
	}
	if code, err := h.Stop(60 * time.Second); err != nil || code != 0 {
		s.Fatalf("stop hclaro on %s: exit %d %v", s.run.San.String(form), code, err)
	}
	h.shutdown = true
}

// TestRealHubChanges covers H6 to H9, H11 and H12, once per engine.
func TestRealHubChanges(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		for _, change := range []string{"config", "profile", "engine-preference", "binary"} {
			hubScenario(t, "h6-"+change+"-"+name, name, func(s *Scenario) { h6Change(s, name, change) })
		}
		hubScenario(t, "h7-busy-edits-"+name, name, func(s *Scenario) { h7BusyEdits(s, name) })
		hubScenario(t, "h8-capacity-"+name, name, func(s *Scenario) { h8Capacity(s, name) })
		hubScenario(t, "h9-close-context-"+name, name, func(s *Scenario) { h9Close(s, name) })
		hubScenario(t, "h11-import-no-stall-"+name, name, func(s *Scenario) { h11Import(s, name) })
		hubScenario(t, "h12-remote-backend-"+name, name, func(s *Scenario) { h12Remote(s, name) })
	}
}

type profile struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

func profileBody(name, engine string, parallelism int) map[string]any {
	return map[string]any{"name": name, "engine": engine, "workspace": "", "varFiles": []string{}, "parallelism": parallelism,
		"lockTimeoutSeconds": 0, "awsProfile": "", "awsRegion": "", "envNames": []string{}}
}

func (s *Scenario) createProfile(h *Hclaro, pid, name, engine string) profile {
	var p profile
	if r, err := h.client.Do("POST", "projects/"+pid+"/execution-profiles", profileBody(name, engine, 0), &p); err != nil || r.Status != 201 {
		s.Fatalf("create profile %s: %v %d %s", name, err, r.Status, r.Code)
	}
	return p
}

// h6Change approves a destructive plan in a Hub view, makes one change and asserts the apply
// is refused with the documented code (docs/architecture.md, scoped execution) and the state
// bytes are unchanged.
func h6Change(s *Scenario, name, change string) {
	var alt Alternate
	if change == "binary" {
		var ok bool
		if alt, ok = s.run.alt[name]; !ok {
			s.NotTested("no second approved " + name + " binary configured (TF_ALT / TOFU_ALT)")
		}
	}
	project, dirs, env, h, pid := hubProject(s, name, "launch", "main.tf")
	prof := ""
	if change == "profile" {
		prof = s.createProfile(h, pid, "first", name).ID
	}
	v := s.view(h, pid, project, name, "default", prof)
	applyBase(s, v, dirs, env, name, project)
	plan := s.LaunchPlan(v, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	a, r := s.Approve(v, plan.ID)
	if r.Status != 200 {
		s.Fatalf("H6 approval: %d %s", r.Status, r.Code)
	}
	sum, serial := stateFile(s, project)
	apply := func(via *Hclaro, what string, status int, code string) Response {
		op, r := s.StartApply(via, plan.ID, a.Token, a.Phrase)
		if r.Status == 202 {
			op = s.FollowOp(via, op.ID, 15*time.Minute)
			s.Fatalf("H6 %s: the approved plan was applied after the change: %s %s", what, op.Status, op.Outcome)
		}
		s.Observe("H6 %s: apply refused %d %s %.300s", what, r.Status, r.Code, r.Body)
		if r.Status != status || r.Code != code {
			s.Errorf("H6 %s: apply refused %d %s, want %d %s", what, r.Status, r.Code, status, code)
		}
		assertStateUnchanged(s, project, sum, serial, "after the refused apply ("+what+")")
		return r
	}
	switch change {
	case "config":
		main := filepath.Join(project, "main.tf")
		b, _ := os.ReadFile(main)
		edited := strings.Replace(string(b), `input = "kept"`, `input = "edited"`, 1)
		if edited == string(b) || os.WriteFile(main, []byte(edited), 0o600) != nil {
			s.Fatalf("could not edit main.tf")
		}
		apply(v, "configuration edited", 409, "context_changed")
	case "profile":
		// Switching to another profile is another context: the plan is not found there.
		second := s.createProfile(h, pid, "second", name)
		apply(s.view(h, pid, project, name, "default", second.ID), "applied through another profile's view", 404, "plan_not_found")
		// Editing the view's own profile closes the view with its plan and approval.
		var list struct {
			Profiles []profile `json:"profiles"`
		}
		if r, err := h.client.Do("GET", "projects/"+pid+"/execution-profiles", nil, &list); err != nil || r.Status != 200 {
			s.Fatalf("profiles: %v %d", err, r.Status)
		}
		rev := 0
		for _, p := range list.Profiles {
			if p.ID == prof {
				rev = p.Revision
			}
		}
		body := profileBody("first", name, 3)
		body["expectedRevision"] = rev
		if r, err := h.client.Do("PATCH", "projects/"+pid+"/execution-profiles/"+prof, body, nil); err != nil || r.Status != 200 {
			s.Fatalf("edit profile: %v %d %s", err, r.Status, r.Code)
		}
		apply(v, "view's profile edited", 404, "context_closed")
	case "engine-preference":
		if r, err := h.client.Do("PATCH", "projects/"+pid, map[string]string{"engine": other(name)}, nil); err != nil || r.Status != 200 {
			s.Fatalf("change engine preference: %v %d %s", err, r.Status, r.Code)
		}
		r := apply(v, "engine preference changed to "+other(name), 404, "context_closed")
		if !strings.Contains(string(r.Body), "engine") {
			s.Errorf("H6 engine preference: the refusal does not name the engine: %s", r.Body)
		}
	case "binary":
		bin := filepath.Join(dirs.Bin, exeName(name))
		if err := os.Remove(bin); err != nil {
			s.Fatalf("%v", err)
		}
		if err := copyExecutable(alt.Path, bin); err != nil {
			s.Fatalf("%v", err)
		}
		s.Observe("H6 replaced %s at the same path with approved %s %s (sha256 %s)", name, alt.Record.Name, alt.Record.Version, alt.Record.BinarySha256)
		r := apply(v, "engine binary replaced", 409, "scope_changed")
		var d struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(r.Body, &d) != nil || d.Detail != "engine_changed" {
			s.Errorf("H6 binary: detail %q, want engine_changed", d.Detail)
		}
	}
}

// h7BusyEdits holds a plan in a view opened with a profile and asserts every edit that
// would close the view is refused with 409 project_busy (M26.08), and nothing changed.
func h7BusyEdits(s *Scenario, name string) {
	project, _, _, h, pid := hubProject(s, name, "hub-hold")
	prof := s.createProfile(h, pid, "held", name)
	v := s.view(h, pid, project, name, "default", prof.ID)
	op, p := s.startHeld(v, project)
	defer p.close()
	pbody := profileBody("held", name, 3)
	pbody["expectedRevision"] = prof.Revision
	edits := []struct {
		what, method, path string
		body               any
	}{
		{"engine preference", "PATCH", "projects/" + pid, map[string]string{"engine": other(name)}},
		{"root path", "PATCH", "projects/" + pid, map[string]string{"rootPath": project}},
		{"profile edit", "PATCH", "projects/" + pid + "/execution-profiles/" + prof.ID, pbody},
		{"profile delete", "DELETE", "projects/" + pid + "/execution-profiles/" + prof.ID, map[string]int{"expectedRevision": prof.Revision}},
		{"unregister", "DELETE", "projects/" + pid, map[string]any{}},
	}
	for _, e := range edits {
		r, err := h.client.Do(e.method, e.path, e.body, nil)
		if err != nil || r.Status != 409 || r.Code != "project_busy" {
			s.Errorf("H7 %s while the plan runs: %v %d %s, want 409 project_busy", e.what, err, r.Status, r.Code)
		}
	}
	if st := s.opStatus(v, op.ID); st != "running" {
		s.Fatalf("H7 the held plan is %s after the edits", st)
	}
	p.release()
	if done := s.FollowOp(v, op.ID, 5*time.Minute); done.Status != "succeeded" {
		s.Fatalf("H7 held plan ended %s (%s)", done.Status, code(done))
	}
	var list struct {
		Projects []struct {
			ID               string `json:"id"`
			EnginePreference string `json:"enginePreference"`
		} `json:"projects"`
	}
	if r, err := h.client.Do("GET", "projects", nil, &list); err != nil || r.Status != 200 || len(list.Projects) != 1 || list.Projects[0].EnginePreference != name {
		s.Fatalf("H7 registry after the refused edits: %v %d %+v", err, r.Status, list.Projects)
	}
	var profs struct {
		Profiles []profile `json:"profiles"`
	}
	if r, err := h.client.Do("GET", "projects/"+pid+"/execution-profiles", nil, &profs); err != nil || r.Status != 200 || len(profs.Profiles) != 1 || profs.Profiles[0] != prof {
		s.Fatalf("H7 profile after the refused edits: %v %d %+v", err, r.Status, profs.Profiles)
	}
	s.Observe("H7 %d edits refused with project_busy while the plan ran; project and profile unchanged", len(edits))
}

// h8Capacity keeps reviewed plans in four workspaces and asserts the fifth start is refused
// with capacity_exceeded, and reads never delete a reviewed plan.
func h8Capacity(s *Scenario, name string) {
	ws := []string{"default", "w1", "w2", "w3", "w4"}
	project, dirs, env, h, pid := hubProject(s, name, "launch", wsWrites(ws[1:]...)...)
	for _, w := range ws[1:] {
		wsRun(s, h, dirs, env, name, project, pid, "new", w)
	}
	var views []*Hclaro
	var plans []PlanDetail
	for _, w := range ws {
		views = append(views, s.view(h, pid, project, name, w, ""))
	}
	for _, v := range views[:4] {
		plans = append(plans, s.LaunchPlan(v, project, name, []string{"launch.tfvars"}))
	}
	if n := savedPlans(h); n != 4 {
		s.Fatalf("H8 %d saved plans after four plans", n)
	}
	r, err := views[4].client.Do("POST", "plans", s.planBody(views[4], []string{"launch.tfvars"}), nil)
	if err != nil || r.Status != 409 || r.Code != "capacity_exceeded" {
		s.Fatalf("H8 fifth plan: %v %d %s, want 409 capacity_exceeded", err, r.Status, r.Code)
	}
	if ops := s.operations(views[4]); len(ops) != 0 {
		s.Fatalf("H8 the refused fifth plan started %d operations", len(ops))
	}
	for i, v := range views[:4] {
		for _, path := range []string{"plans", "plans/" + plans[i].ID, "plans/options", "resources?mode=managed&limit=200", "operations"} {
			// A workspace with no state has no resource snapshot: 503 snapshot_unavailable.
			r, err := v.client.Do("GET", path, nil, nil)
			if err != nil || r.Status != 200 && !(r.Status == 503 && r.Code == "snapshot_unavailable" && strings.HasPrefix(path, "resources")) {
				s.Fatalf("H8 read %s in %s: %v %d %s", path, ws[i], err, r.Status, r.Code)
			}
		}
		if r, err := h.client.Do("GET", "contexts/"+v.view.ContextID, nil, nil); err != nil || r.Status != 200 {
			s.Fatalf("H8 read context %s: %v %d", ws[i], err, r.Status)
		}
	}
	s.fingerprint(views[4], "plans/options")
	if n := savedPlans(h); n != 4 {
		s.Fatalf("H8 %d saved-plan files after the reads, want 4", n)
	}
	for i, v := range views[:4] {
		if r, err := v.client.Do("GET", "plans/"+plans[i].ID, nil, nil); err != nil || r.Status != 200 {
			s.Fatalf("H8 plan in %s gone after the reads: %v %d %s", ws[i], err, r.Status, r.Code)
		}
	}
	s.Observe("H8 fifth start refused capacity_exceeded; four saved-plan files present after the reads")
}

// h9Close asserts DELETE /contexts/{cid} removes the saved plan and is refused while an
// operation runs in the view.
func h9Close(s *Scenario, name string) {
	project, _, _, h, pid := hubProject(s, name, "hub-hold")
	v := s.view(h, pid, project, name, "default", "")
	plan := s.LaunchPlan(v, project, name, []string{"launch.tfvars"})
	if n := savedPlans(h); n != 1 {
		s.Fatalf("H9 %d saved plans after the plan", n)
	}
	if r, err := h.client.Do("DELETE", "contexts/"+v.view.ContextID, map[string]any{}, nil); err != nil || r.Status != 200 {
		s.Fatalf("H9 close: %v %d %s", err, r.Status, r.Code)
	}
	if n := waitSavedPlans(h, 0); n != 0 {
		s.Fatalf("H9 %d saved plans left 30 s after the close", n)
	}
	if r, err := v.client.Do("GET", "plans/"+plan.ID, nil, nil); err != nil || r.Status != 404 || r.Code != "context_closed" {
		s.Fatalf("H9 plan after the close: %v %d %s, want 404 context_closed", err, r.Status, r.Code)
	}
	v = s.view(h, pid, project, name, "default", "")
	op, p := s.startHeld(v, project)
	defer p.close()
	if r, err := h.client.Do("DELETE", "contexts/"+v.view.ContextID, map[string]any{}, nil); err != nil || r.Status != 409 || r.Code != "project_busy" {
		s.Fatalf("H9 close while the plan runs: %v %d %s, want 409 project_busy", err, r.Status, r.Code)
	}
	if st := s.opStatus(v, op.ID); st != "running" {
		s.Fatalf("H9 the plan is %s after the refused close", st)
	}
	p.release()
	if done := s.FollowOp(v, op.ID, 5*time.Minute); done.Status != "succeeded" {
		s.Fatalf("H9 held plan ended %s (%s)", done.Status, code(done))
	}
	if r, err := h.client.Do("DELETE", "contexts/"+v.view.ContextID, map[string]any{}, nil); err != nil || r.Status != 200 || waitSavedPlans(h, 0) != 0 {
		s.Fatalf("H9 close after the plan: %v %d %s, %d saved plans", err, r.Status, r.Code, savedPlans(h))
	}
	s.Observe("H9 close removed the saved plan; close refused project_busy while a plan ran")
}

// waitSavedPlans waits up to 30 s for the saved-plan directory count to reach n: closing a
// view deletes its scope's plan in the background (projects.closeScopeLocked).
func waitSavedPlans(h *Hclaro, n int) int {
	deadline := time.Now().Add(30 * time.Second)
	for savedPlans(h) != n && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	return savedPlans(h)
}

// h11Import holds an import plan and asserts other Hub requests, including a plan in another
// project, complete while it runs (M26.08: the scope check runs outside the manager lock).
func h11Import(s *Scenario, name string) {
	dirs, env := hubProcess(s)
	a := s.ProjectAs("hub-hold", "import", append(slices.Clone(stateWrites), "imports.tf")...)
	b := s.ProjectAs("hub-hold", "other", stateWrites...)
	bEngine := name
	if _, ok := run.Engine(other(name)); ok {
		bEngine = other(name)
	}
	s.SetupInit(dirs, env, name, a)
	s.SetupInit(dirs, env, bEngine, b)
	h := s.StartHub(dirs, env, true)
	va := s.view(h, s.HubRegister(h, a, name, name), a, name, "default", "")
	pidB := s.HubRegister(h, b, bEngine, bEngine)
	vb := s.view(h, pidB, b, bEngine, "default", "")
	req := ImportRequest("random_integer.imported", "15390,1,50000", false)
	prev := s.PreviewImport(va, req)
	if prev.Blocked {
		s.Fatalf("H11 preview blocked: %+v", prev.Issues)
	}
	p := s.holdPipe(a)
	defer p.close()
	s.Guard(a)
	op, r := s.StartImport(va, req, prev, []string{"hold.tfvars"})
	if r.Status != 202 {
		s.Fatalf("H11 import: %d %s", r.Status, r.Code)
	}
	s.waitRunningSSE(va, op.ID, p)
	if r, err := h.client.Do("GET", "projects", nil, nil); err != nil || r.Status != 200 {
		s.Fatalf("H11 project list while the import runs: %v %d", err, r.Status)
	}
	if r, err := h.client.Do("POST", "projects/"+pidB+"/inspect", map[string]any{}, nil); err != nil || r.Status != 200 {
		s.Fatalf("H11 inspect while the import runs: %v %d %s", err, r.Status, r.Code)
	}
	bplan := s.LaunchPlan(vb, b, bEngine, []string{"launch.tfvars"})
	if st := s.opStatus(va, op.ID); st != "running" {
		s.Fatalf("H11 the import is %s after the other requests; it was not held", st)
	}
	s.Observe("H11 while the import ran: project list, inspect and a %s plan (%s) completed", bEngine, bplan)
	p.release()
	done := s.FollowOp(va, op.ID, 5*time.Minute)
	if done.Status != "succeeded" || done.PlanID == "" {
		s.Fatalf("H11 import plan ended %s (%s)", done.Status, code(done))
	}
	var detail PlanDetail
	if r, err := va.client.Do("GET", "plans/"+done.PlanID, nil, &detail); err != nil || r.Status != 200 || detail.Imports != 1 {
		s.Fatalf("H11 import plan: %v %d, %d imports", err, r.Status, detail.Imports)
	}
}

// h12Remote registers a project whose backend block is s3 (never initialized) and asserts
// every execution route refuses before any engine call. Never init, never plan.
func h12Remote(s *Scenario, name string) {
	if !beforeEnforced() {
		s.NotTested("a remote-backend fixture runs only under enforced isolation (isolation " + run.Result.Isolation.Level + ": " + run.Result.Isolation.Reason + ")")
	}
	needsEnforced[s.ID] = true
	project := s.ProjectAs("hub-remote", "remote")
	dirs, env := hubProcess(s)
	h := s.StartHub(dirs, env, true)
	// Registered without the inspection (which would run `version` and `workspace list`).
	var p struct {
		ID string `json:"id"`
	}
	if r, err := h.client.Do("POST", "projects", map[string]string{"path": project, "alias": "remote", "engine": name}, &p); err != nil || r.Status != 201 {
		s.Fatalf("register: %v %d %s", err, r.Status, r.Code)
	}
	v := s.view(h, p.ID, project, name, "default", "")
	// The read stops at the missing initialization. Hclaro takes the backend type from the
	// init metadata, so a never-initialized s3 project reports "unknown" (recorded): the
	// refusals below are the uninitialized path, not a remote-backend-specific one.
	rs, c, backend, initialized := s.contextRead(h, v.view.ContextID)
	s.Observe("H12 context read: %s %s, backend %q, initialized %v", rs, c, backend, initialized)
	if c != "uninitialized" || initialized {
		s.Fatalf("H12 context read %s %s backend %q initialized %v, want uninitialized", rs, c, backend, initialized)
	}
	var opts struct {
		CanPlan bool   `json:"canPlan"`
		Code    string `json:"code"`
		Reason  string `json:"reason"`
	}
	if r, err := v.client.Do("GET", "plans/options", nil, &opts); err != nil || r.Status != 200 || opts.CanPlan {
		s.Fatalf("H12 plans/options: %v %d can %v", err, r.Status, opts.CanPlan)
	}
	s.Observe("H12 plans/options: %s (%s)", opts.Code, opts.Reason)
	starts := []struct {
		what, path string
		body       map[string]any
	}{
		{"plan", "plans", map[string]any{"acknowledgeBackendAccess": true, "varFiles": []string{}, "replace": []string{}, "contextFingerprint": ""}},
		{"drift scan", "drift-scans", map[string]any{"acknowledgeProviderAccess": true, "varFiles": []string{}, "contextFingerprint": ""}},
		{"graph", "graphs", map[string]any{"acknowledgeEngineRun": true, "contextFingerprint": ""}},
		{"import preview", "imports/preview", ImportRequest("terraform_data.kept", "x", false)},
	}
	imp := ImportRequest("terraform_data.kept", "x", false)
	for k, val := range map[string]any{"varFiles": []string{}, "previewSha256": "", "acknowledgeWrite": true, "acknowledgeBackendAccess": true, "contextFingerprint": ""} {
		imp[k] = val
	}
	starts = append(starts, struct {
		what, path string
		body       map[string]any
	}{"import", "imports", imp})
	for _, st := range starts {
		r, err := v.client.Do("POST", st.path, st.body, nil)
		if err != nil || r.Status < 400 {
			s.Fatalf("H12 %s: %v %d accepted", st.what, err, r.Status)
		}
		s.Observe("H12 %s refused: %d %s", st.what, r.Status, r.Code)
	}
	var st InitStatus
	if r, err := h.client.Do("GET", "projects/"+p.ID+"/init-status", nil, &st); err == nil && r.Status == 200 {
		s.Observe("H12 init status (read only, never started): initialized %v canInit %v", st.Initialized, st.CanInit)
	}
	// The operation logs: nothing ran in the view or the project.
	if ops := s.operations(v); len(ops) != 0 {
		s.Fatalf("H12 %d scoped operations ran", len(ops))
	}
	var pops struct {
		Operations []Operation `json:"operations"`
	}
	if r, err := h.client.Do("GET", "projects/"+p.ID+"/operations", nil, &pops); err != nil || r.Status != 200 || len(pops.Operations) != 0 {
		s.Fatalf("H12 project operations: %v %d %d", err, r.Status, len(pops.Operations))
	}
	if _, err := os.Stat(filepath.Join(project, ".terraform")); err == nil {
		s.Fatalf("H12 .terraform exists: something initialized the project")
	}
	if e := s.History(h); len(e) != 0 {
		s.Fatalf("H12 %d history entries", len(e))
	}
}

// TestRealHubShutdown covers H10 with the OD-8 gated fixture (extended to M27.05 H10 by the
// owner on 2026-11-15, same restrictions): applies in two Hub scopes, one per engine, are
// held at the gate; Hclaro is interrupted once. M27.03 A7's phases P1-P5 are checked per
// scope, each against its own engine PID, history entry, state and files.
func TestRealHubShutdown(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	both := strings.Join(engines(), "+")
	hubScenario(t, "h10-shutdown-applies", both, func(s *Scenario) {
		if runtime.GOOS == "windows" {
			s.NotTested("the PowerShell gate is approved for WIN1 (A6, A7, A7b) only; H10 on Windows needs its own approval")
		}
		if len(engines()) < 2 {
			s.NotTested("H10 tells the two scopes' engines apart by name; this run configures " + both)
		}
		dirs, env := hubProcess(s)
		gates := map[string]*gateRun{}
		for _, name := range engines() {
			project := s.ProjectAs("launch-gate", "g-"+name, append(slices.Clone(stateWrites), ".qual/reached", ".qual/interrupted", ".qual/release")...)
			s.SetupInit(dirs, env, name, project)
			gates[name] = &gateRun{project: project, dirs: dirs, env: env, mode: "normal"}
		}
		h := s.StartHub(dirs, env, true)
		s.Observe("gate command (OD-8, H10): /bin/sh ./gate.sh normal, working directory each project, files under .qual/ only")
		for _, name := range engines() {
			g := gates[name]
			defer release(s, g)
			pid := s.HubRegister(h, g.project, name, name)
			v := s.view(h, pid, g.project, name, "default", "")
			detail := s.LaunchPlan(v, g.project, name, []string{"launch.tfvars"})
			if detail.Summary["create"] != 6 {
				s.Fatalf("%s gated plan: %s, want 6 creates", name, detail)
			}
			g.planID, g.h = detail.ID, h
			a, r := s.Approve(v, detail.ID)
			if r.Status != 200 {
				s.Fatalf("%s approval: %d %s", name, r.Status, r.Code)
			}
			g.started = time.Now()
			op, r := s.StartApply(v, detail.ID, a.Token, a.Phrase)
			if r.Status != 202 {
				s.Fatalf("%s apply: %d %s", name, r.Status, r.Code)
			}
			g.op = op
			waitRunning(s, v, op.ID)
			if !waitFile(g.qual("reached"), 60*time.Second) {
				s.NotTested(name + " gate not reached")
			}
			pids, err := enginePIDs(dirs.Bin, name)
			if err != nil || len(pids) != 1 {
				s.NotTested(fmt.Sprintf("%s engine process not identified (%v, %v)", name, pids, err))
			}
			g.pid = pids[0]
			s.Observe("%s scope: gate reached, engine pid %d", name, g.pid)
		}
		for _, name := range engines() {
			if !alive(gates[name], name) {
				s.NotTested("shutdown timing not established (" + name + " engine gone before the interrupt)")
			}
		}
		t0 := time.Now()
		if err := h.Interrupt(); err != nil {
			s.Fatalf("interrupt hclaro: %v", err)
		}
		// P1: one notice while Hclaro runs.
		deadline := time.Now().Add(60 * time.Second)
		for !stdoutHas(h, shutdownNotice) {
			if h.Exited() || time.Now().After(deadline) {
				s.Fatalf("P1: no shutdown notice while Hclaro ran (exited %v)", h.Exited())
			}
			time.Sleep(50 * time.Millisecond)
		}
		// P2: one interrupt per scope engine.
		for _, name := range engines() {
			s.Observe("P2 %s: gate interrupts %d by %s", name, waitInterrupts(gates[name], 10*time.Second), time.Since(t0).Round(time.Millisecond))
		}
		for _, name := range engines() {
			release(s, gates[name])
		}
		// P3: each engine exits unaided, before Hclaro.
		for _, name := range engines() {
			g := gates[name]
			gone, ok, outlived := waitGone(g, name, 5*time.Minute)
			if !ok {
				s.Fatalf("P3 %s: engine pid %d still running 5 min after the interrupt", name, g.pid)
			}
			s.Observe("P3 %s: engine gone %s after the interrupt; seen alive after Hclaro exited: %v", name, gone.Sub(t0).Round(time.Millisecond), outlived)
			if outlived {
				s.Errorf("P3 %s: the engine was still running after Hclaro exited", name)
			}
			if gone.Sub(t0) > engine.ApplyGracePeriod-time.Minute {
				s.Errorf("P3 %s: the engine took %s, not well before the grace period", name, gone.Sub(t0))
			}
		}
		code, exited := h.Wait(engine.ApplyGracePeriod + 2*time.Minute)
		if !exited {
			s.Fatalf("Hclaro still running %s after the interrupt", engine.ApplyGracePeriod+2*time.Minute)
		}
		b, _ := os.ReadFile(h.Stdout)
		notices := bytes.Count(b, []byte(shutdownNotice))
		s.Observe("P1: %d shutdown notice(s); Hclaro exit %d at %s", notices, code, time.Since(t0).Round(time.Millisecond))
		if notices != 1 {
			s.Errorf("P1: %d shutdown notices, want one", notices)
		}
		// P4 and P5 per scope.
		for _, name := range engines() {
			g := gates[name]
			e := historyFile(s, g.dirs.Config, g.planID)
			stage2 := gateState(s, g, name)
			_, lockErr := os.Stat(filepath.Join(g.project, ".terraform.tfstate.lock.info"))
			diag := ""
			if e.Diagnostic != nil {
				diag = e.Diagnostic.Code
			}
			acks := excerptAcks(e)
			s.Observe("P4 %s history: status %s outcome %q exit %s diagnostic %q, acknowledgements %d; P5 stage-2 %v, lock-info present %v",
				name, e.Status, e.Outcome.Result, exitCode(e.ExitCode), diag, acks, stage2, lockErr == nil)
			if interrupts(g) == 0 && acks == 0 {
				s.NotTested("shutdown timing not established for " + name + " (no gate marker, no engine acknowledgement)")
			}
			if interrupts(g) > 1 || acks > 1 {
				s.Errorf("P2 %s: more than one interrupt: gate %d, engine acknowledgements %d", name, interrupts(g), acks)
			}
			if e.Outcome.Result == "not_applied" {
				s.Errorf("P4 %s: outcome not_applied with stage-1 objects in state", name)
			}
			if e.Outcome.Result == "applied" && !(e.ExitCode != nil && *e.ExitCode == 0 && len(stage2) == 2) {
				s.Errorf("P4 %s: outcome applied, but exit %s and stage-2 %v", name, exitCode(e.ExitCode), stage2)
			}
			if e.Outcome.Result != "applied" && !historyUncertain(e.Outcome.Result) {
				s.Errorf("P4 %s: outcome %q, want applied or uncertain (history: partial)", name, e.Outcome.Result)
			}
			if lockErr == nil {
				s.Errorf("P5 %s: lock-info file left", name)
			}
		}
		if plans := savedPlanDirs(h); len(plans) > 0 || stdoutHas(h, "saved plan was not deleted") {
			s.Errorf("P5: saved plans left %v or the not-deleted notice printed", plans)
		}
	})
}
