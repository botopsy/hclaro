//go:build realengine

package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// M27.03 launch scenarios (tasks/M27/tasks/M27-03-LAUNCH-APPLY-IMPORT-INIT.md), once per
// configured engine. Every apply goes through plan → preflight → approval → apply.

// stateWrites are the project files an apply or import may write (local backend).
var stateWrites = []string{"terraform.tfstate", "terraform.tfstate.backup"}

// engines lists the configured engines in a fixed order.
func engines() []string {
	var out []string
	for _, name := range []string{"terraform", "tofu"} {
		if _, ok := run.Engine(name); ok {
			out = append(out, name)
		}
	}
	return out
}

// launch prepares a launch project from template, initializes it directly and starts
// Hclaro on it.
func launch(s *Scenario, engine, template string, allowed ...string) (string, ProcessDirs, []string, *Hclaro) {
	return openWith(s, launchStart, engine, template, allowed...)
}

// starter initializes a prepared project and returns the Hclaro handle the scenario drives:
// the launch process (M27.03, M27.04) or a Hub workspace view (M27.05 H2).
type starter func(s *Scenario, engine, project string) (ProcessDirs, []string, *Hclaro)

func launchStart(s *Scenario, engine, project string) (ProcessDirs, []string, *Hclaro) {
	dirs, env, err := s.Process(engine)
	if err != nil {
		s.Fatalf("%v", err)
	}
	s.SetupInit(dirs, env, engine, project)
	return dirs, env, s.StartLaunch(dirs, env, engine, project)
}

// openWith prepares a project from template (state files allowed) and starts it with start.
func openWith(s *Scenario, start starter, engine, template string, allowed ...string) (string, ProcessDirs, []string, *Hclaro) {
	project := s.Project(template, append(slices.Clone(stateWrites), allowed...)...)
	dirs, env, h := start(s, engine, project)
	return project, dirs, env, h
}

// creates returns the addresses a plan creates.
func planned(d PlanDetail, action string) []string {
	var out []string
	for _, c := range d.Changes {
		if c.Action == action {
			out = append(out, c.Address)
		}
	}
	sort.Strings(out)
	return out
}

func addresses(m map[string]StateObject) []string {
	return slices.Sorted(maps.Keys(m))
}

// applyBase plans the launch fixture's base variant (plus extra var files) and applies it,
// asserting A1: applied, state reread holds exactly the planned objects, saved plan removed,
// history entry apply/applied for that plan.
func applyBase(s *Scenario, h *Hclaro, dirs ProcessDirs, env []string, engine, project string, extra ...string) map[string]StateObject {
	detail := s.LaunchPlan(h, project, engine, append([]string{"launch.tfvars"}, extra...))
	want := planned(detail, "create")
	if len(want) < 3 || len(detail.Changes) != len(want) {
		s.Fatalf("base plan: %s, want creates only", detail)
	}
	_, op := s.ApplyReviewed(h, detail.ID)
	if op.Status != "succeeded" || op.Outcome != "applied" {
		s.Fatalf("apply ended %s, outcome %q", op.Status, op.Outcome)
	}
	got := s.ShowState(dirs, env, engine, project, "")
	if a := addresses(got); !slices.Equal(a, want) {
		s.Fatalf("state holds %v, plan created %v", a, want)
	}
	if n := s.CheckPlanArtifacts(h); n != 0 {
		s.Fatalf("%d saved-plan directories left after the apply", n)
	}
	e := s.LastHistory(h, "apply")
	if e.Outcome.Result != "applied" || e.Outcome.PlanID != detail.ID {
		s.Fatalf("history apply entry: outcome %q plan %q", e.Outcome.Result, e.Outcome.PlanID)
	}
	return got
}

// stateFile reads the local state's bytes hash and serial (a file read, not an engine call).
func stateFile(s *Scenario, project string) (string, int) {
	p := filepath.Join(project, "terraform.tfstate")
	sum, err := fileSHA256(p)
	if err != nil {
		s.Fatalf("state: %v", err)
	}
	b, _ := os.ReadFile(p)
	var st struct {
		Serial int `json:"serial"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		s.Fatalf("state: %v", err)
	}
	return sum, st.Serial
}

func assertStateUnchanged(s *Scenario, project, sum string, serial int, when string) {
	if got, n := stateFile(s, project); got != sum || n != serial {
		s.Fatalf("state changed %s (serial %d → %d)", when, serial, n)
	}
}

// planCommand returns the engine argv of the plan's operation.
func planCommand(s *Scenario, h *Hclaro, d PlanDetail) []string {
	var op Operation
	if r, err := h.client.Do("GET", "operations/"+d.OperationID, nil, &op); err != nil || r.Status != 200 {
		s.Fatalf("plan operation: %v %d", err, r.Status)
	}
	return op.Command
}

func savedPlans(h *Hclaro) int {
	entries, _ := os.ReadDir(h.Dirs.Tmp)
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "hclaro-plan-") {
			n++
		}
	}
	return n
}

// TestRealLaunchApply covers A1 to A4 and R1.
func TestRealLaunchApply(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "a1-apply-"+name, name, "launch", false, func(s *Scenario) {
			a1Apply(s, name, launchStart)
		})
		run.Scenario(t, "a2-destructive-"+name, name, "launch", false, func(s *Scenario) {
			a2Destructive(s, name, launchStart)
		})
		run.Scenario(t, "a3-stale-plan-"+name, name, "launch", false, func(s *Scenario) {
			a3StalePlan(s, name, launchStart)
		})
		run.Scenario(t, "a4-superseded-"+name, name, "launch", false, func(s *Scenario) {
			project, dirs, env, h := launch(s, name, "launch")
			applyBase(s, h, dirs, env, name, project)
			first := s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
			if n := savedPlans(h); n != 1 {
				s.Fatalf("%d saved plans after plan 1", n)
			}
			s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
			if n := savedPlans(h); n != 1 {
				s.Fatalf("%d saved plans after plan 2, want plan 1's removed", n)
			}
			sum, serial := stateFile(s, project)
			a, r := s.Approve(h, first.ID)
			code := r.Code
			if r.Status == 200 {
				var op Operation
				if op, r = s.StartApply(h, first.ID, a.Token, a.Phrase); r.Status == 202 {
					op = s.FollowOp(h, op.ID, 15*time.Minute)
					s.Fatalf("superseded plan was applied: %s %s", op.Status, op.Outcome)
				}
				code = r.Code
			}
			if code == "" {
				s.Fatalf("superseded plan: no refusal code")
			}
			s.Observe("plan 1 refused with %s", code)
			assertStateUnchanged(s, project, sum, serial, "after the superseded apply")
		})
		run.Scenario(t, "r1-replace-"+name, name, "launch", false, func(s *Scenario) {
			r1Replace(s, name, launchStart)
		})
	}
}

func (s *Scenario) operations(h *Hclaro) []Operation {
	var out struct {
		Operations []Operation `json:"operations"`
	}
	if r, err := h.client.Do("GET", "operations", nil, &out); err != nil || r.Status != 200 {
		s.Fatalf("operations: %v %d", err, r.Status)
	}
	return out.Operations
}

// i1Import imports random_integer.imported through Hclaro and applies it (I1).
func i1Import(s *Scenario, h *Hclaro, dirs ProcessDirs, env []string, engine, project string) {
	req := ImportRequest("random_integer.imported", "15390,1,50000", false)
	p := s.PreviewImport(h, req)
	if p.Blocked || p.File != "imports.tf" {
		s.Fatalf("I1 preview: blocked %v file %q issues %+v", p.Blocked, p.File, p.Issues)
	}
	if _, err := os.Stat(filepath.Join(project, "imports.tf")); err == nil {
		s.Fatalf("the preview wrote imports.tf")
	}
	detail := s.ImportPlan(h, project, engine, req, p, []string{"launch.tfvars"})
	s.Observe("I1 import plan: %s", detail)
	if detail.Imports != 1 || len(detail.Changes) != 1 || !detail.Changes[0].Importing || detail.Changes[0].Address != "random_integer.imported" {
		s.Fatalf("I1 import plan: %s, %d imports, want exactly 1 import of random_integer.imported", detail, detail.Imports)
	}
	_, op := s.ApplyReviewed(h, detail.ID)
	if op.Status != "succeeded" || op.Outcome != "applied" {
		s.Fatalf("I1 apply ended %s, outcome %q", op.Status, op.Outcome)
	}
	got := s.ShowState(dirs, env, engine, project, "")
	if o, ok := got["random_integer.imported"]; !ok || o.ID != "15390" {
		s.Fatalf("I1 state: %v", got)
	}
	var kinds []string
	for _, e := range s.History(h) {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Contains(kinds, "import") {
		s.Fatalf("I1 history kinds %v, want an import entry", kinds)
	}
	s.Observe("I1 history kinds: %v", kinds)
}

// TestRealLaunchImport covers I1 to I3.
func TestRealLaunchImport(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "i1-import-"+name, name, "launch", false, func(s *Scenario) {
			i1ImportScenario(s, name, launchStart)
		})
		run.Scenario(t, "i2-import-collision-"+name, name, "launch", false, func(s *Scenario) {
			project, dirs, env, h := launch(s, name, "launch-import", "imports.tf")
			vars := []string{"launch.tfvars", "string.tfvars"}
			req := ImportRequest("random_string.imported[0]", "qualcollide1", false)
			p := s.PreviewImport(h, req)
			if p.Blocked {
				s.Fatalf("I2 setup preview blocked: %+v", p.Issues)
			}
			detail := s.ImportPlan(h, project, name, req, p, vars)
			if _, op := s.ApplyReviewed(h, detail.ID); op.Outcome != "applied" {
				s.Fatalf("I2 setup apply: %s %s", op.Status, op.Outcome)
			}
			got := s.ShowState(dirs, env, name, project, "")
			if got["random_string.imported[0]"].ID != "qualcollide1" {
				s.Fatalf("I2 setup state: %v", got)
			}
			s.RefreshBaseline(project)
			sum, serial := stateFile(s, project)
			ops := len(s.operations(h))
			preview := func(req map[string]any) ImportPreview {
				var p ImportPreview
				if r, err := h.client.Do("POST", "imports/preview", req, &p); err != nil || r.Status != 200 {
					s.Fatalf("preview: %v %d", err, r.Status)
				}
				return p
			}
			codes := func(p ImportPreview) []string {
				var out []string
				for _, i := range p.Issues {
					out = append(out, i.Severity+":"+i.Code)
				}
				return out
			}
			// (a) The ID equals the state id: M26.10 refuses it before writing. A new file name,
			// so the only conflict is the ID.
			dup := ImportRequest("random_string.duplicate", "qualcollide1", true)
			dup["file"] = "collision.tf"
			p = preview(dup)
			s.Observe("I2 exact-ID preview blocked %v issues %v", p.Blocked, codes(p))
			if !p.Blocked || !slices.Contains(codes(p), "error:id_in_state") {
				s.Fatalf("I2 import of an ID already in state is not refused with id_in_state: %v", codes(p))
			}
			if _, r := s.StartImport(h, dup, p, vars); r.Status == 202 {
				s.Fatalf("I2 import of an ID already in state started")
			} else {
				s.Observe("I2 import refused: %d %s", r.Status, r.Code)
			}
			// (b) random_integer's import ID ("<result>,<min>,<max>") differs from its state id
			// ("<result>"); recorded, not asserted (finding for the owner, M27-PROGRESS.md).
			result := got["random_integer.imported"].ID
			comp := ImportRequest("random_integer.duplicate", result+",1,50000", true)
			comp["file"] = "collision-integer.tf"
			p = preview(comp)
			s.Observe("I2 composite-ID (random_integer) preview blocked %v issues %v", p.Blocked, codes(p))
			if n := len(s.operations(h)); n != ops {
				s.Fatalf("I2 started an operation")
			}
			assertStateUnchanged(s, project, sum, serial, "after the refused import")
		})
		run.Scenario(t, "i3-generated-config-"+name, name, "launch", false, func(s *Scenario) {
			i3Generated(s, name, launchStart)
		})
	}
}

// wsWrites are the workspace state files W1 and W2 may create (local backend).
func wsWrites(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, "terraform.tfstate.d/"+n+"/terraform.tfstate", "terraform.tfstate.d/"+n+"/terraform.tfstate.backup")
	}
	return out
}

// cliWorkspaces reads the CLI default and the workspace list with the direct, read-only
// `workspace show` and `workspace list`.
func cliWorkspaces(s *Scenario, dirs ProcessDirs, env []string, engine, project string) (string, []string) {
	bin := filepath.Join(dirs.Bin, exeName(engine))
	show, err := s.Direct(bin, project, env, "workspace show")
	if err != nil {
		s.Fatalf("workspace show: %v", err)
	}
	list, err := s.Direct(bin, project, env, "workspace list")
	if err != nil {
		s.Fatalf("workspace list: %v", err)
	}
	var names []string
	for _, l := range strings.Split(string(list), "\n") {
		if n := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "*")); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return strings.TrimSpace(string(show)), names
}

// wsRun runs a workspace command through the Hub and asserts it succeeded and that Hclaro's
// reported CLI default matches the engine's.
func wsRun(s *Scenario, h *Hclaro, dirs ProcessDirs, env []string, engine, project, pid, action, name string) Operation {
	rv, op, r := s.HubWorkspace(h, pid, action, name)
	if op.ID == "" {
		s.Fatalf("workspace %s %s not run: %d %s %s %s", action, name, r.Status, r.Code, rv.Code, rv.Reason)
	}
	if op.Status != "succeeded" || op.Workspace == nil || !op.Workspace.Ran {
		s.Fatalf("workspace %s %s ended %s", action, name, op.Status)
	}
	show, list := cliWorkspaces(s, dirs, env, engine, project)
	s.Observe("workspace %s %s: Hclaro CLI default %q → %q, engine %q, list %v", action, name, op.Workspace.CLIDefaultBefore, op.Workspace.CLIDefaultAfter, show, list)
	if op.Workspace.CLIDefaultAfter != "" && op.Workspace.CLIDefaultAfter != show {
		s.Fatalf("Hclaro reports CLI default %q, engine shows %q", op.Workspace.CLIDefaultAfter, show)
	}
	return op
}

// TestRealLaunchHub covers N1, W1 and W2 (Hub project pages; no execution flag).
func TestRealLaunchHub(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "n1-init-"+name, name, "hub", false, func(s *Scenario) {
			if !beforeEnforced() {
				s.NotTested("offline not verified (isolation " + run.Result.Isolation.Level + ": " + run.Result.Isolation.Reason + ")")
			}
			needsEnforced[s.ID] = true
			project := s.Project("providers", ".terraform.lock.hcl")
			s.Observe("no canary path: init reads no variables (the providers fixture has none)")
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			// Started from the terminal on the uninitialized project, as a user would; the
			// launch project's own page runs the init, and the launch view is read again after.
			s.startCodes = map[string]bool{"uninitialized": true}
			h := s.StartLaunch(dirs, env, name, project)
			pid := s.HubRegister(h, project, name, name)
			st, op := s.HubInit(h, pid)
			s.Observe("init command %v, before: initialized %v lockfile %v", st.Command, st.Initialized, st.Lockfile)
			if op.Status != "succeeded" {
				s.Fatalf("init ended %s", op.Status)
			}
			// Launch mode, one engine per process: its family CLI config (Scenario.Process).
			checkLockfile(s, name, project, s.run.Cfg.CLIConfig[name])
			if fi, err := os.Stat(filepath.Join(project, ".terraform", "providers")); err != nil || !fi.IsDir() {
				s.Fatalf(".terraform/providers missing after init")
			}
			if e := s.LastHistory(h, "init"); e.Status != "succeeded" {
				s.Fatalf("history init entry: %s", e.Status)
			}
			// The launch view is read again after the init: no longer uninitialized.
			s.startCodes = nil
			deadline := time.Now().Add(2 * time.Minute)
			for {
				var ws struct {
					Info struct {
						Initialized bool `json:"initialized"`
					} `json:"info"`
					Status string `json:"status"`
				}
				if _, err := h.client.Do("GET", "workspace", nil, &ws); err != nil {
					s.Fatalf("workspace: %v", err)
				}
				if ws.Info.Initialized && ws.Status != "loading" {
					s.Observe("launch view after init: %s, initialized", ws.Status)
					break
				}
				if time.Now().After(deadline) {
					s.Fatalf("launch view still not initialized 2 minutes after the init")
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
		run.Scenario(t, "w1-workspaces-"+name, name, "hub", false, func(s *Scenario) {
			project := s.Project("launch", append(slices.Clone(stateWrites), wsWrites("full", "dev")...)...)
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.SetupInit(dirs, env, name, project)
			h := s.StartHub(dirs, env, false)
			pid := s.HubRegister(h, project, name, name)
			wsRun(s, h, dirs, env, name, project, pid, "new", "full")
			if show, _ := cliWorkspaces(s, dirs, env, name, project); show != "full" {
				wsRun(s, h, dirs, env, name, project, pid, "select", "full")
			}
			// Fill "full" through Hclaro: a launch process plans and applies in the CLI default.
			ldirs, lenv, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			l := s.StartLaunch(ldirs, lenv, name, project)
			var opts struct {
				Workspace string `json:"workspace"`
			}
			if r, err := l.client.Do("GET", "plans/options", nil, &opts); err != nil || r.Status != 200 || opts.Workspace != "full" {
				s.Fatalf("launch workspace %q, want full: %v", opts.Workspace, err)
			}
			full := applyBase(s, l, ldirs, lenv, name, project)
			if code, err := l.Stop(60 * time.Second); err != nil || code != 0 {
				s.Fatalf("launch process stop: %v %d", err, code)
			}
			wsRun(s, h, dirs, env, name, project, pid, "select", "default")
			// Deleting a non-empty workspace is refused; it still exists.
			rv, op, r := s.HubWorkspace(h, pid, "delete", "full")
			if op.ID != "" && op.Status == "succeeded" {
				s.Fatalf("non-empty workspace deleted")
			}
			s.Observe("delete full (non-empty): review canRun %v code %q, start %d %s, op %s", rv.CanRun, rv.Code, r.Status, r.Code, op.Status)
			if _, list := cliWorkspaces(s, dirs, env, name, project); !slices.Contains(list, "full") {
				s.Fatalf("workspace full is gone after the refused delete")
			}
			wsRun(s, h, dirs, env, name, project, pid, "new", "dev")
			// Listing: the review (not run) parses the engine's real output.
			rv, _ = s.ReviewWorkspace(h, pid, "select", "default")
			_, list := cliWorkspaces(s, dirs, env, name, project)
			got := slices.Sorted(slices.Values(rv.Workspaces))
			if !slices.Equal(got, list) {
				s.Fatalf("Hclaro lists %v, engine %v", got, list)
			}
			show, _ := cliWorkspaces(s, dirs, env, name, project)
			// Per-context TF_WORKSPACE: each context reads its own workspace, whatever the CLI default.
			want := addresses(full)
			for ws, n := range map[string][]string{"full": want, "dev": nil, "default": nil} {
				addrs := s.OpenContext(h, pid, ws)
				sort.Strings(addrs)
				if !slices.Equal(addrs, n) {
					s.Fatalf("context %s shows %v, want %v (CLI default %q)", ws, addrs, n, show)
				}
			}
			if after, _ := cliWorkspaces(s, dirs, env, name, project); after != show {
				s.Fatalf("opening contexts changed the CLI default %q → %q", show, after)
			}
			if show != "default" {
				wsRun(s, h, dirs, env, name, project, pid, "select", "default")
			}
			wsRun(s, h, dirs, env, name, project, pid, "delete", "dev")
			if _, list := cliWorkspaces(s, dirs, env, name, project); slices.Contains(list, "dev") {
				s.Fatalf("workspace dev still listed after the delete")
			}
		})
		run.Scenario(t, "w3-launch-cli-default-"+name, name, "launch", false, func(s *Scenario) {
			// The launch project's workspace review promises that its plans and applies refuse to
			// run after the CLI default changes, until Hclaro is restarted.
			project, dirs, env, h := launch(s, name, "launch", wsWrites("dev")...)
			pid := s.HubRegister(h, project, name, name)
			rv, _ := s.ReviewWorkspace(h, pid, "new", "dev")
			if !rv.Launch || !slices.ContainsFunc(rv.Notices, func(n string) bool { return strings.Contains(n, "until Hclaro is restarted") }) {
				s.Fatalf("launch review: launch %v notices %v", rv.Launch, rv.Notices)
			}
			wsRun(s, h, dirs, env, name, project, pid, "new", "dev")
			if show, _ := cliWorkspaces(s, dirs, env, name, project); show == "default" {
				s.Fatalf("workspace new did not change the CLI default; the promise is not exercised")
			}
			ops := len(s.operations(h))
			r, err := h.client.Do("POST", "plans", map[string]any{"acknowledgeBackendAccess": true, "varFiles": []string{"launch.tfvars"}, "replace": []string{}}, nil)
			if err != nil {
				s.Fatalf("plan: %v", err)
			}
			s.Observe("plan after the CLI default changed: %d %s", r.Status, r.Code)
			if r.Status == 202 {
				last := s.operations(h)
				op := s.FollowOp(h, last[len(last)-1].ID, 15*time.Minute)
				if op.Status == "succeeded" {
					s.Fatalf("the launch session planned after the CLI default changed (status %s)", op.Status)
				}
				code := ""
				if op.Diagnostic != nil {
					code = op.Diagnostic.Code
				}
				s.Observe("plan operation ended %s (%s)", op.Status, code)
			} else if n := len(s.operations(h)); n != ops {
				s.Fatalf("a refused plan started an operation")
			}
			wsRun(s, h, dirs, env, name, project, pid, "select", "default")
			wsRun(s, h, dirs, env, name, project, pid, "delete", "dev")
		})
		run.Scenario(t, "w2-inherited-force-"+name, name, "hub", false, func(s *Scenario) {
			project := s.Project("launch", wsWrites("w2")...)
			s.Observe("no canary path: no plan runs; workspace commands read no variables")
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.SetupInit(dirs, env, name, project)
			h := s.StartHub(dirs, env, false)
			pid := s.HubRegister(h, project, name, name)
			wsRun(s, h, dirs, env, name, project, pid, "new", "w2")
			if show, _ := cliWorkspaces(s, dirs, env, name, project); show != "default" {
				wsRun(s, h, dirs, env, name, project, pid, "select", "default")
			}
			if code, err := h.Stop(60 * time.Second); err != nil || code != 0 {
				s.Fatalf("hub stop: %v %d", err, code)
			}
			// A second Hclaro started with the variable added for this scenario only.
			fdirs, _, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			extra := map[string]string{"TF_CLI_ARGS_workspace_delete": "-force"}
			fenv := Environment(fdirs, s.run.Cfg.CLIConfig[name], extra)
			f, err := s.StartHclaro(fdirs, fenv, extra, "--hub", "--no-browser")
			if err != nil {
				s.Fatalf("start hclaro: %v", err)
			}
			fpid := s.HubRegister(f, project, name, name)
			rv, op, r := s.HubWorkspace(f, fpid, "delete", "w2")
			s.Observe("delete w2 with TF_CLI_ARGS_workspace_delete=-force: review canRun %v code %q reason %q, start %d %s, op %s", rv.CanRun, rv.Code, rv.Reason, r.Status, r.Code, op.Status)
			if op.ID != "" && op.Status == "succeeded" {
				s.Fatalf("workspace deleted with an inherited -force")
			}
			if _, list := cliWorkspaces(s, dirs, env, name, project); !slices.Contains(list, "w2") {
				s.Fatalf("workspace w2 is gone")
			}
			// Plan and apply refusals name the variable (engine.UnsafeArgsHelp); so must this one.
			if !strings.Contains(rv.Reason, "TF_CLI_ARGS_workspace_delete") && !strings.Contains(string(r.Body), "TF_CLI_ARGS_workspace_delete") {
				s.Fatalf("refused, but the refusal does not name TF_CLI_ARGS_workspace_delete (M27.03 defect D-2): %q", rv.Reason)
			}
		})
	}
}

// lockfileEvidence keeps, before any assertion, what is needed to explain a lock-file hash
// mismatch (dispatch 3, run 43760321517: OpenTofu on Windows): the lock file and mirror files
// exactly as read, the engine, platform and init inputs, the observed and manifest hashes per
// provider, and "sha256  path" for every file of each installed package (h1: is a hash of
// exactly these lines, so it can be recomputed offline). Nothing is normalised or rewritten.
func lockfileEvidence(s *Scenario, engine, project, cfg string, lock, manifest []byte, blocks [][]string) {
	s.Artifact("lockfile-"+engine+".hcl", lock)
	s.Artifact("mirror-manifest.json", manifest)
	e, _ := s.run.Engine(engine)
	cfgSum, _ := fileSHA256(cfg)
	s.Observe("lock evidence: %s %s on %s_%s; setup command `%s init -input=false -no-color` in the project, TF_CLI_CONFIG_FILE %s sha256 %s",
		engine, e.Version, runtime.GOOS, runtime.GOARCH, engine, filepath.Base(cfg), cfgSum)
	mirror := os.Getenv("HCLARO_QUAL_MIRROR")
	for _, blk := range blocks {
		addr := blk[1] // host/namespace/name
		version := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(blk[2])
		observed := regexp.MustCompile(`"((?:h1|zh):[^"]+)"`).FindAllStringSubmatch(blk[2], -1)
		var obs []string
		for _, h := range observed {
			obs = append(obs, h[1])
		}
		s.Observe("lock evidence %s %v: observed hashes %v", addr, version, obs)
		if version == nil {
			continue
		}
		family := "terraform"
		if engine == "tofu" {
			family = "tofu"
		}
		if j, err := os.ReadFile(filepath.Join(mirror, family, filepath.FromSlash(addr), version[1]+".json")); err == nil {
			s.Artifact("mirror-"+strings.ReplaceAll(addr, "/", "_")+"-"+version[1]+".json", j)
		} else {
			s.Observe("lock evidence %s: mirror JSON not readable: %v", addr, err)
		}
		pkg := filepath.Join(project, ".terraform", "providers", filepath.FromSlash(addr), version[1], runtime.GOOS+"_"+runtime.GOARCH)
		var lines []string
		err := filepath.WalkDir(pkg, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			sum, err := fileSHA256(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(pkg, p)
			lines = append(lines, sum+"  "+filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			s.Observe("lock evidence %s: installed package not listed: %v", addr, err)
		}
		sort.Strings(lines)
		s.Artifact("installed-"+strings.ReplaceAll(addr, "/", "_")+".txt", []byte(strings.Join(lines, "\n")+"\n"))
	}
}

// checkLockfile asserts the lock file pins the mirrored versions with hashes from the mirror
// manifest (h1 hashes, or zh hashes of the mirrored archives).
func checkLockfile(s *Scenario, engine, project, cli string) {
	b, err := os.ReadFile(filepath.Join(project, ".terraform.lock.hcl"))
	if err != nil {
		s.Fatalf("lock file: %v", err)
	}
	mb, err := os.ReadFile(filepath.Join(os.Getenv("HCLARO_QUAL_MIRROR"), "mirror-manifest.json"))
	if err != nil {
		s.Fatalf("mirror manifest: %v", err)
	}
	var m struct {
		Entries []struct {
			Family, Host, Provider, Version, Sha256 string
			Hashes                                  []string
		} `json:"entries"`
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		s.Fatalf("mirror manifest: %v", err)
	}
	blocks := regexp.MustCompile(`(?s)provider "([^"]+)" \{(.*?)\n\}`).FindAllStringSubmatch(string(b), -1)
	lockfileEvidence(s, engine, project, cli, b, mb, blocks)
	if len(blocks) != 2 {
		s.Fatalf("lock file has %d providers, want random and local", len(blocks))
	}
	for _, blk := range blocks {
		version := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(blk[2])
		hashes := regexp.MustCompile(`"((?:h1|zh):[^"]+)"`).FindAllStringSubmatch(blk[2], -1)
		allowed, wantVersion, h1 := map[string]bool{}, "", ""
		for _, e := range m.Entries {
			if e.Family == engine && e.Host+"/"+e.Provider == blk[1] {
				wantVersion = e.Version
				allowed["zh:"+e.Sha256] = true
				for _, h := range e.Hashes {
					allowed[h] = true
					if strings.HasPrefix(h, "h1:") {
						h1 = h
					}
				}
			}
		}
		if wantVersion == "" || version == nil || version[1] != wantVersion {
			s.Fatalf("lock file %s: version %v, mirror %q", blk[1], version, wantVersion)
		}
		seen := false
		for _, h := range hashes {
			if !allowed[h[1]] {
				s.Fatalf("lock file %s: hash %s is not in the mirror manifest", blk[1], h[1])
			}
			seen = seen || h[1] == h1
		}
		if !seen {
			s.Fatalf("lock file %s: the mirror's h1 hash is missing", blk[1])
		}
		s.Observe("lock file %s %s: %d hashes, all from the mirror manifest", blk[1], version[1], len(hashes))
	}
}

// od9 is the F-2 holder per engine (OD-9, 2026-11-14, darwin/amd64): console everywhere
// except Terraform 1.17.2, whose console takes no lock.
func od9(engine string) string {
	if e, _ := run.Engine(engine); e.Name == "terraform" && e.Version == "1.17.2" {
		return "plan -input=false -no-color"
	}
	return "console -no-color"
}

// createProfile saves an execution profile on the registered launch project.
func createProfile(s *Scenario, h *Hclaro, pid, name, engine string, varFiles []string, parallelism, lockTimeout int) string {
	var p struct {
		ID string `json:"id"`
	}
	body := map[string]any{"name": name, "engine": engine, "workspace": "", "varFiles": varFiles, "parallelism": parallelism,
		"lockTimeoutSeconds": lockTimeout, "awsProfile": "", "awsRegion": "", "envNames": []string{}}
	if r, err := h.client.Do("POST", "projects/"+pid+"/execution-profiles", body, &p); err != nil || r.Status != 201 {
		s.Fatalf("create profile %s: %v %d %s %s", name, err, r.Status, r.Code, r.Body)
	}
	return p.ID
}

// hold starts the F-2 holder in project and waits (60 s) for the lock-info file.
func hold(s *Scenario, dirs ProcessDirs, engine, project string) *Holder {
	sub := od9(engine)
	var g gate
	extra := map[string]string{"TF_VAR_hold": "false"}
	if strings.HasPrefix(sub, "plan") {
		var err error
		if g, err = newGate(project); err != nil {
			s.NotTested("no F-2 lock holder on this platform: " + err.Error())
		}
		extra["TF_VAR_hold"] = "true"
		// Windows: the gated data source reads the harness's named pipe (FU-WIN-HOLD).
		if name := gateName(project); name != "" {
			extra["TF_VAR_gate"] = name
		}
	}
	env := Environment(dirs, s.run.Cfg.CLIConfig[engine], extra)
	s.mu.Lock()
	s.result.DirectEngineCall = append(s.result.DirectEngineCall, "lock holder: "+sub)
	s.mu.Unlock()
	h, err := startHolder(filepath.Join(dirs.Bin, exeName(engine)), project, env, extra, sub, g)
	if err != nil {
		s.Fatalf("lock holder: %v", err)
	}
	lock := filepath.Join(project, ".terraform.tfstate.lock.info")
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(lock); err == nil {
			return h
		}
		if !h.Alive() || time.Now().After(deadline) {
			_ = h.Release()
			s.NotTested(fmt.Sprintf("F-2 holder %q took no lock (alive %v): %.500s", sub, h.Alive(), h.Log.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

const lockWait = "Acquiring state lock. This may take a few moments..."

// TestRealLaunchOptions covers A5 (apply options and lock timeout).
func TestRealLaunchOptions(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "a5-apply-options-"+name, name, "launch", false, func(s *Scenario) {
			project, dirs, env, h := launch(s, name, "launch-lock")
			pid := s.HubRegister(h, project, name, name)
			// (a) parallelism and lock timeout reach the approved command and the engine argv.
			a := createProfile(s, h, pid, "a5-options", name, []string{"launch.tfvars"}, 2, 120)
			detail := s.LaunchPlanProfile(h, project, name, a, []string{"launch.tfvars"})
			approval, op := s.ApplyReviewed(h, detail.ID)
			for _, flag := range []string{"-parallelism=2", "-lock-timeout=120s"} {
				if !slices.Contains(approval.Command, flag) || !slices.Contains(op.Command, flag) {
					s.Fatalf("(a) %s: approved %v, engine argv %v", flag, approval.Command, op.Command)
				}
			}
			if op.Outcome != "applied" {
				s.Fatalf("(a) apply ended %s %s", op.Status, op.Outcome)
			}
			s.Observe("(a) approved command %v; engine argv %v", approval.Command, op.Command)
			fifo := filepath.Join(project, ".qual", "gate.fifo")
			defer os.Remove(fifo) // snapshotting a FIFO would block
			// (b) Long timeout: the apply waits for the holder and finishes after its release.
			b := createProfile(s, h, pid, "a5-wait", name, []string{"gen2.tfvars"}, 0, 120)
			detail = s.LaunchPlanProfile(h, project, name, b, []string{"gen2.tfvars"})
			ap, r := s.Approve(h, detail.ID)
			if r.Status != 200 {
				s.Fatalf("(b) approval: %d %s", r.Status, r.Code)
			}
			holder := hold(s, dirs, name, project)
			defer holder.Kill()
			started, r := s.StartApply(h, detail.ID, ap.Token, ap.Phrase)
			if r.Status != 202 {
				_ = holder.Release()
				s.Fatalf("(b) apply: %d %s", r.Status, r.Code)
			}
			waitRunning(s, h, started.ID)
			// The engine prints the F-2 lock-wait message once it has waited ~400 ms for the lock.
			waiting, err := h.client.WaitLog("operations/"+started.ID, lockWait, 60*time.Second)
			if _, statErr := os.Stat(filepath.Join(project, ".terraform.tfstate.lock.info")); statErr != nil || !holder.Alive() {
				_ = holder.Release()
				s.NotTested("(b) the holder released the lock before the apply ran")
			}
			// Terraform on Windows (run 43871517546): its lock acquisition cannot read the state
			// another process has locked, fails at once and does not retry, so -lock-timeout does
			// not wait. Owner decision 2026-11-16: (b) is NOT_TESTED there, after (c) runs.
			bEngineLimit := ""
			if runtime.GOOS == "windows" && name == "terraform" && (err != nil || !waiting) {
				done := s.FollowOp(h, started.ID, 5*time.Minute)
				code := ""
				if done.Diagnostic != nil {
					code = done.Diagnostic.Code
				}
				s.Observe("(b) Windows Terraform: apply %s outcome %q diagnostic %q without waiting (holder alive %v)", done.Status, done.Outcome, code, holder.Alive())
				if done.Status == "running" || code != "state_locked" || done.Outcome != "not_applied" {
					_ = holder.Release()
					s.Fatalf("(b) Windows Terraform: apply %s %q %q, want a refused apply (state_locked, not_applied)", done.Status, done.Outcome, code)
				}
				if err := holder.Release(); err != nil {
					s.Fatalf("(b) %v", err)
				}
				bEngineLimit = "(b) Terraform's -lock-timeout does not wait for a local-backend lock held by another process on Windows: the apply fails at once with state_locked (engine behaviour; Hclaro passed -lock-timeout=120s, (a))"
			} else {
				if err != nil || !waiting {
					// Without the message, the apply still running after 60 s while the holder holds
					// the lock shows it waited (lock timeout 120 s).
					var now Operation
					if r, err := h.client.Do("GET", "operations/"+started.ID, nil, &now); err != nil || r.Status != 200 || now.Status != "running" {
						_ = holder.Release()
						s.Fatalf("(b) no lock-wait message and the apply is %q, not waiting: %v %d %.300s", now.Status, err, r.Status, r.Body)
					}
					s.Errorf("(b) the apply waited 60 s for the lock, but Hclaro's operation log shows no lock-wait message (M27.03 defect D-3: -json type state_lock_acquire is dropped)")
				}
				release := time.Now().UTC()
				if err := holder.Release(); err != nil {
					s.Fatalf("(b) %v", err)
				}
				done := s.FollowOp(h, started.ID, 5*time.Minute)
				finished, _ := time.Parse(time.RFC3339Nano, done.CompletedAt)
				if done.Outcome != "applied" || finished.Before(release.Truncate(time.Second)) {
					s.Fatalf("(b) apply %s %s, finished %s, released %s", done.Status, done.Outcome, done.CompletedAt, release.Format(time.RFC3339Nano))
				}
				s.Observe("(b) holder %q released %s, apply finished %s", od9(name), release.Format(time.RFC3339Nano), done.CompletedAt)
			}
			// (c) Short timeout with the holder kept: state_locked, state unchanged.
			c := createProfile(s, h, pid, "a5-short", name, []string{"gen3.tfvars"}, 0, 2)
			detail = s.LaunchPlanProfile(h, project, name, c, []string{"gen3.tfvars"})
			ap, r = s.Approve(h, detail.ID)
			if r.Status != 200 {
				s.Fatalf("(c) approval: %d %s", r.Status, r.Code)
			}
			sum, serial := stateFile(s, project)
			holder = hold(s, dirs, name, project)
			defer holder.Kill()
			started, r = s.StartApply(h, detail.ID, ap.Token, ap.Phrase)
			if r.Status != 202 {
				_ = holder.Release()
				s.Fatalf("(c) apply: %d %s", r.Status, r.Code)
			}
			done := s.FollowOp(h, started.ID, 5*time.Minute)
			alive := holder.Alive()
			if err := holder.Release(); err != nil {
				s.Fatalf("(c) %v", err)
			}
			code := ""
			if done.Diagnostic != nil {
				code = done.Diagnostic.Code
			}
			s.Observe("(c) apply %s outcome %q diagnostic %q (holder alive at the end: %v)", done.Status, done.Outcome, code, alive)
			if code != "state_locked" || !alive {
				s.Fatalf("(c) apply ended %s with %q, want state_locked while the holder held the lock", done.Status, code)
			}
			assertStateUnchanged(s, project, sum, serial, "after the locked apply")
			_ = env
			if bEngineLimit != "" {
				s.NotTested(bEngineLimit)
			}
		})
	}
}

func a1Apply(s *Scenario, name string, start starter) {
	project, dirs, env, h := openWith(s, start, name, "launch")
	got := applyBase(s, h, dirs, env, name, project)
	s.Observe("applied %d objects: %s", len(got), strings.Join(addresses(got), ", "))
}

func a2Destructive(s *Scenario, name string, start starter) {
	project, dirs, env, h := openWith(s, start, name, "launch")
	before := applyBase(s, h, dirs, env, name, project)
	detail := s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	if detail.Summary["delete"] != 1 || detail.Summary["replace"] != 1 || len(detail.Changes)-detail.Summary["no-op"] != 2 {
		s.Fatalf("destructive plan: %s, want 1 delete and 1 replace", detail)
	}
	a, r := s.Approve(h, detail.ID)
	if r.Status != 200 || !a.Destructive || a.Phrase == "" {
		s.Fatalf("approval: %d %s destructive %v phrase %q", r.Status, r.Code, a.Destructive, a.Phrase)
	}
	sum, serial := stateFile(s, project)
	if _, r := s.StartApply(h, detail.ID, a.Token, ""); r.Status == 202 {
		s.Fatalf("destructive apply without the typed phrase was accepted")
	} else {
		s.Observe("without the phrase: %d %s", r.Status, r.Code)
	}
	assertStateUnchanged(s, project, sum, serial, "after the refused apply")
	// The refusal may consume the token; the dialog then asks for a new approval.
	op, r := s.StartApply(h, detail.ID, a.Token, a.Phrase)
	if r.Status != 202 {
		s.Observe("same token after the refusal: %d %s; approving again", r.Status, r.Code)
		a, r = s.Approve(h, detail.ID)
		if r.Status != 200 {
			s.Fatalf("second approval: %d %s", r.Status, r.Code)
		}
		if op, r = s.StartApply(h, detail.ID, a.Token, a.Phrase); r.Status != 202 {
			s.Fatalf("apply with the phrase: %d %s", r.Status, r.Code)
		}
	}
	op = s.FollowOp(h, op.ID, 15*time.Minute)
	if op.Status != "succeeded" || op.Outcome != "applied" {
		s.Fatalf("apply ended %s, outcome %q", op.Status, op.Outcome)
	}
	after := s.ShowState(dirs, env, name, project, "")
	if _, ok := after["terraform_data.retired[0]"]; ok {
		s.Fatalf("deleted object still in state")
	}
	if after["terraform_data.rotated"].ID == before["terraform_data.rotated"].ID {
		s.Fatalf("replaced object kept its ID")
	}
	if after["terraform_data.kept"].ID != before["terraform_data.kept"].ID {
		s.Fatalf("unrelated object changed ID")
	}
}

func a3StalePlan(s *Scenario, name string, start starter) {
	project, dirs, env, h := openWith(s, start, name, "launch")
	applyBase(s, h, dirs, env, name, project)
	sum, serial := stateFile(s, project)
	main := filepath.Join(project, "main.tf")
	edit := func(from, to string) {
		b, _ := os.ReadFile(main)
		edited := strings.Replace(string(b), from, to, 1)
		if edited == string(b) || os.WriteFile(main, []byte(edited), 0o600) != nil {
			s.Fatalf("could not edit main.tf")
		}
		s.RefreshBaseline(project)
	}
	// (i) Edited before the preflight: the preflight blocks; no approval exists.
	detail := s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	edit(`input = "kept"`, `input = "edited"`)
	if _, r := s.Approve(h, detail.ID); r.Status == 200 || !strings.Contains(r.Code, "context.fingerprint") {
		s.Fatalf("edited before the preflight: %d %q, want a context.fingerprint block", r.Status, r.Code)
	}
	assertStateUnchanged(s, project, sum, serial, "after the blocked preflight")
	// (ii) Edited after the approval: the apply is refused with context_changed.
	detail = s.LaunchPlan(h, project, name, []string{"launch.tfvars", "destroy.tfvars"})
	a, r := s.Approve(h, detail.ID)
	if r.Status != 200 {
		s.Fatalf("approval: %d %s", r.Status, r.Code)
	}
	edit(`input = "edited"`, `input = "edited again"`)
	op, r := s.StartApply(h, detail.ID, a.Token, a.Phrase)
	if r.Status == 202 {
		op = s.FollowOp(h, op.ID, 15*time.Minute)
		s.Fatalf("stale plan was applied: %s %s", op.Status, op.Outcome)
	}
	if r.Code != "context_changed" {
		s.Fatalf("edited after the approval: %d %q, want context_changed", r.Status, r.Code)
	}
	assertStateUnchanged(s, project, sum, serial, "after the refused apply")
	if got := s.ShowState(dirs, env, name, project, ""); len(got) != 3 {
		s.Fatalf("state holds %v after the refusals", addresses(got))
	}
}

func r1Replace(s *Scenario, name string, start starter) {
	project, dirs, env, h := openWith(s, start, name, "launch")
	before := applyBase(s, h, dirs, env, name, project, "fleet.tfvars")
	ids := s.Resources(h)
	fleet := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("terraform_data.fleet[%d]", i))
		}
		return out
	}
	for _, n := range []int{1, 50} {
		var sel []string
		for _, a := range fleet(n) {
			if ids[a] == "" {
				s.Fatalf("%s missing from the resource list", a)
			}
			sel = append(sel, ids[a])
		}
		detail := s.LaunchPlanWith(h, project, name, []string{"launch.tfvars", "fleet.tfvars"}, sel)
		argv := planCommand(s, h, detail)
		var replaces []string
		for _, arg := range argv {
			if strings.HasPrefix(arg, "-target") {
				s.Fatalf("plan argv carries %s", arg)
			}
			if a, ok := strings.CutPrefix(arg, "-replace="); ok {
				replaces = append(replaces, a)
			}
		}
		sort.Strings(replaces)
		want := fleet(n)
		sort.Strings(want)
		if !slices.Equal(replaces, want) {
			s.Fatalf("select %d: argv -replace %v, want %v", n, replaces, want)
		}
		if detail.Summary["replace"] != n || len(detail.Changes)-detail.Summary["no-op"] != n {
			s.Fatalf("select %d: plan %s", n, detail)
		}
		_, op := s.ApplyReviewed(h, detail.ID)
		if op.Status != "succeeded" || op.Outcome != "applied" {
			s.Fatalf("select %d: apply ended %s, outcome %q", n, op.Status, op.Outcome)
		}
		after := s.ShowState(dirs, env, name, project, "")
		chosen := map[string]bool{}
		for _, a := range want {
			chosen[a] = true
		}
		for a, o := range after {
			if chosen[a] == (o.ID == before[a].ID) {
				s.Fatalf("select %d: %s ID changed=%v, selected=%v", n, a, o.ID != before[a].ID, chosen[a])
			}
		}
		before = after
		ids = s.Resources(h)
	}
	var sel []string
	for _, a := range fleet(51) {
		sel = append(sel, ids[a])
	}
	ops := len(s.operations(h))
	body := map[string]any{"acknowledgeBackendAccess": true, "varFiles": []string{"launch.tfvars", "fleet.tfvars"}, "replace": sel}
	if h.view != nil {
		body["contextFingerprint"] = s.fingerprint(h, "plans/options")
	}
	r, err := h.client.Do("POST", "plans", body, nil)
	if err != nil || r.Status < 400 {
		s.Fatalf("51 selections: %v %d", err, r.Status)
	}
	if n := len(s.operations(h)); n != ops {
		s.Fatalf("51 selections started an operation")
	}
	s.Observe("51 selections refused: %d %s", r.Status, r.Code)
}

// i3Generated imports random_password with generated configuration (I3).
func i3Generated(s *Scenario, name string, start starter) {
	project, _, _, h := openWith(s, start, name, "launch-import", "imports.tf")
	// random_password's import ID is the password, so the canary is the ID. The preview's
	// import block must repeat the user's input; only that exact echo is masked, and the
	// generated configuration, plan, history and logs stay under the canary check.
	req := ImportRequest("random_password.generated", s.run.Canary, true)
	// The block (id = "…") and the manual recipe (terraform import '…' '…').
	h.client.echo = []string{`id = \"` + s.run.Canary + `\"`, `'` + s.run.Canary + `'`}
	p := s.PreviewImport(h, req)
	h.client.echo = nil
	s.Observe("I3 preview repeated the typed import ID in its import block %d time(s) (masked as user input)", h.client.echoed)
	if p.Blocked || p.GeneratedFile == "" {
		s.Fatalf("I3 preview: blocked %v generated file %q issues %+v", p.Blocked, p.GeneratedFile, p.Issues)
	}
	s.AllowWrite(project, p.GeneratedFile)
	detail := s.ImportPlan(h, project, name, req, p, []string{"launch.tfvars"})
	s.Observe("I3 import plan: %s", detail)
	if detail.Imports != 1 {
		s.Fatalf("I3 plan: %s, %d imports, want 1", detail, detail.Imports)
	}
	if detail.GeneratedConfig == "" {
		s.Fatalf("I3 plan shows no generated configuration")
	}
	if strings.Contains(detail.GeneratedConfig, s.run.Canary) {
		s.Fatalf("I3 generated configuration shows the canary")
	}
	s.Artifact("generated-config.txt", []byte(detail.GeneratedConfig))
	b, err := os.ReadFile(filepath.Join(project, p.GeneratedFile))
	if err != nil {
		s.Fatalf("I3 generated file: %v", err)
	}
	// The engine's file is not Hclaro output; whether it holds the canary is recorded.
	s.Observe("I3 engine-written %s contains the canary: %v; CRLF line endings: %v", p.GeneratedFile, strings.Contains(string(b), s.run.Canary), bytes.Contains(b, []byte("\r\n")))
}

func i1ImportScenario(s *Scenario, name string, start starter) {
	project, dirs, env, h := openWith(s, start, name, "launch-import", "imports.tf")
	i1Import(s, h, dirs, env, name, project)
}
