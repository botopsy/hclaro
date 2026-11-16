//go:build realengine

package qualify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"hclaro/internal/graph"
)

// M27.04 scenarios (tasks/M27/tasks/M27-04-DRIFT-GRAPH-FAILURES.md), once per configured
// engine: real drift (D1-D4), graphs (G1), locks (L1, L2) and failure classification (F1-F5).

// Scan is the part of GET /api/v1/drift-scans/{id} the harness reads.
type Scan struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Outcome    string `json:"outcome"`
	Exhaustive bool   `json:"exhaustive"`
	Summary    struct {
		Changed, Missing, CannotVerify, Total int
	} `json:"summary"`
	Resources []struct {
		ID             string   `json:"id"`
		Address        string   `json:"address"`
		Classification string   `json:"classification"`
		Actions        []string `json:"actions"`
	} `json:"resources"`
	Current     bool   `json:"current"`
	StaleReason string `json:"staleReason"`
	Withheld    bool   `json:"withheld"`
	Diagnostic  *struct {
		Code string `json:"code"`
	} `json:"diagnostic"`
}

// drifted maps each scan record to "<classification> <actions>".
func (sc Scan) drifted() map[string]string {
	out := map[string]string{}
	for _, r := range sc.Resources {
		out[r.Address] = r.Classification + " " + strings.Join(r.Actions, ",")
	}
	return out
}

func (s *Scenario) getScan(h *Hclaro, id string) Scan {
	var sc Scan
	if r, err := h.client.Do("GET", "drift-scans/"+id, nil, &sc); err != nil || r.Status != 200 {
		s.Fatalf("drift scan: %v %d", err, r.Status)
	}
	return sc
}

// driftScan asks Hclaro for a drift scan as the drift dialog does and follows it to its end.
// Every scan asserts the engine argv is a refresh-only plan and that the state file's serial
// and bytes did not change (M27.04: any change is FAIL).
func (s *Scenario) driftScan(h *Hclaro, project string, varFiles []string) (Operation, Scan) {
	sum, serial := stateFile(s, project)
	s.Guard(project)
	var started struct {
		Operation Operation `json:"operation"`
		Scan      Scan      `json:"scan"`
	}
	body := map[string]any{"acknowledgeProviderAccess": true, "varFiles": varFiles}
	if h.view != nil {
		body["contextFingerprint"] = s.fingerprint(h, "drift-scans/options")
	}
	r, err := h.client.Do("POST", "drift-scans", body, &started)
	if err != nil || r.Status != 202 {
		s.Fatalf("POST drift-scans: %v %d %s", err, r.Status, r.Code)
	}
	op := s.FollowOp(h, started.Operation.ID, 15*time.Minute)
	if !slices.Contains(op.Command, "-refresh-only") || !slices.Contains(op.Command, "plan") {
		s.Fatalf("drift scan argv %v is not a refresh-only plan", op.Command)
	}
	assertStateUnchanged(s, project, sum, serial, "by the drift scan")
	return op, s.getScan(h, started.Scan.ID)
}

// failedPlan starts a plan expected to fail and returns its final record (with the engine
// diagnostics). A plan refused before any operation started returns a zero operation.
func (s *Scenario) failedPlan(h *Hclaro, project string, varFiles []string) (Operation, Response) {
	s.Guard(project)
	body := map[string]any{"acknowledgeBackendAccess": true, "varFiles": varFiles, "replace": []string{}}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var op Operation
		r, err := h.client.Do("POST", "plans", body, &op)
		if err != nil {
			s.Fatalf("POST plans: %v", err)
		}
		// A harness file edit makes Hclaro read the project again; wait for that read.
		if r.Status == 409 && r.Code == "workspace_loading" && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if r.Status != 202 {
			return Operation{}, r
		}
		return s.FollowOp(h, op.ID, 15*time.Minute), r
	}
}

func code(op Operation) string {
	if op.Diagnostic == nil {
		return ""
	}
	return op.Diagnostic.Code
}

// engineRan fails unless op carries the engine's argv and exit code: the classification
// under test came from real engine output, not from a refusal before the engine.
func engineRan(s *Scenario, what string, op Operation) {
	if len(op.Command) == 0 || op.ExitCode == nil {
		s.Fatalf("%s: no engine run recorded (argv %v, exit %v)", what, op.Command, op.ExitCode)
	}
}

func diagnostics(op Operation) string {
	var b strings.Builder
	for _, d := range op.Diagnostics {
		fmt.Fprintf(&b, "%s: %s\n%s\n", d.Severity, d.Summary, d.Detail)
	}
	return b.String()
}

// launchApplied launches template, plans varFiles and applies the plan through Hclaro.
func launchApplied(s *Scenario, engine, template string, varFiles []string, allowed ...string) (string, *Hclaro) {
	return appliedWith(s, launchStart, engine, template, varFiles, allowed...)
}

// appliedWith is launchApplied with the Hclaro handle start returns (launch or Hub view).
func appliedWith(s *Scenario, start starter, engine, template string, varFiles []string, allowed ...string) (string, *Hclaro) {
	project, _, _, h := openWith(s, start, engine, template, allowed...)
	detail := s.LaunchPlan(h, project, engine, varFiles)
	if _, op := s.ApplyReviewed(h, detail.ID); op.Status != "succeeded" || op.Outcome != "applied" {
		s.Fatalf("setup apply ended %s, outcome %q", op.Status, op.Outcome)
	}
	return project, h
}

// d2Want is what every CORE engine's refresh-only plan reports for an edited local_file:
// pinned from fixtures/engines/<engine>/local-drift.json (TestRealEngineLocalFileDrift).
// hashicorp/local 2.9.1 drops an object whose file checksum no longer matches.
var d2Want = map[string]string{"local_file.edited": "missing delete"}

// TestRealDrift covers D1 to D4.
func TestRealDrift(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	vars, files := driftVars, driftFiles
	for _, name := range engines() {
		run.Scenario(t, "d1-drift-deleted-"+name, name, "launch", false, func(s *Scenario) {
			d1DriftDeleted(s, name, launchStart)
		})
		run.Scenario(t, "d2-drift-edited-"+name, name, "launch", false, func(s *Scenario) {
			project, h := launchApplied(s, name, "drift", vars, files...)
			if err := os.WriteFile(filepath.Join(project, "out", "edited.txt"), []byte("edited outside\n"), 0o600); err != nil {
				s.Fatalf("%v", err)
			}
			_, sc := s.driftScan(h, project, vars)
			got := sc.drifted()
			s.Observe("D2 scan %s %s: %v (pinned recording: %v)", sc.Status, sc.Outcome, got, d2Want)
			if sc.Status != "completed" || sc.Outcome != "drift_detected" || len(got) != len(d2Want) || got["local_file.edited"] != d2Want["local_file.edited"] {
				s.Fatalf("D2: Hclaro reports %v, the engine's recorded refresh-only plan reports %v", got, d2Want)
			}
		})
		run.Scenario(t, "d3-no-drift-"+name, name, "launch", false, func(s *Scenario) {
			project, h := launchApplied(s, name, "drift", vars, files...)
			_, sc := s.driftScan(h, project, vars)
			s.Observe("D3 scan %s %s exhaustive %v total %d", sc.Status, sc.Outcome, sc.Exhaustive, sc.Summary.Total)
			if sc.Status != "completed" || sc.Outcome != "no_drift" || !sc.Exhaustive || sc.Summary.Total != 0 || len(sc.Resources) != 0 {
				s.Fatalf("D3: scan %s %s exhaustive %v %v", sc.Status, sc.Outcome, sc.Exhaustive, sc.drifted())
			}
		})
		run.Scenario(t, "d4-drift-stale-"+name, name, "launch", false, func(s *Scenario) {
			project, h := launchApplied(s, name, "drift", vars, append(slices.Clone(files), "main.tf")...)
			if err := os.Remove(filepath.Join(project, "out", "gone.txt")); err != nil {
				s.Fatalf("%v", err)
			}
			_, sc := s.driftScan(h, project, vars)
			if !sc.Current || len(sc.Resources) != 1 {
				s.Fatalf("D4 setup scan: current %v %v", sc.Current, sc.drifted())
			}
			rid := sc.Resources[0].ID
			main := filepath.Join(project, "main.tf")
			b, _ := os.ReadFile(main)
			edited := strings.Replace(string(b), `input = "kept"`, `input = "kept, edited"`, 1)
			if edited == string(b) || os.WriteFile(main, []byte(edited), 0o600) != nil {
				s.Fatalf("could not edit main.tf")
			}
			deadline := time.Now().Add(60 * time.Second)
			for sc = s.getScan(h, sc.ID); sc.Current; sc = s.getScan(h, sc.ID) {
				if time.Now().After(deadline) {
					s.Fatalf("D4: the kept scan is still current 60 s after main.tf changed")
				}
				time.Sleep(200 * time.Millisecond)
			}
			s.Observe("D4 kept scan: current %v withheld %v reason %q", sc.Current, sc.Withheld, sc.StaleReason)
			if sc.StaleReason == "" || !sc.Withheld || len(sc.Resources) != 0 {
				s.Fatalf("D4: stale scan reason %q withheld %v resources %v", sc.StaleReason, sc.Withheld, sc.drifted())
			}
			if r, err := h.client.Do("GET", "drift-scans/"+sc.ID+"/resources/"+rid, nil, nil); err != nil || r.Status != 409 || r.Code != "scan_stale" {
				s.Fatalf("D4: stale detail %v %d %s, want 409 scan_stale", err, r.Status, r.Code)
			}
		})
	}
}

// graphView is the part of GET /api/v1/graphs/{id}?planId= the harness reads.
type graphView struct {
	Status  string `json:"status"`
	Format  string `json:"format"`
	Current bool   `json:"current"`
	Nodes   []struct {
		ID              string `json:"id"`
		ResourceAddress string `json:"resourceAddress"`
		Kind            string `json:"nodeKind"`
		Confidence      string `json:"confidence"`
		InstanceCount   int    `json:"instanceCount"`
		Planned         []struct {
			ChangeID string `json:"changeId"`
			Address  string `json:"address"`
			Action   string `json:"action"`
		} `json:"planned"`
	} `json:"nodes"`
	Plan *struct {
		Changes  int `json:"changes"`
		Unmapped []struct {
			ChangeID string `json:"changeId"`
			Address  string `json:"address"`
		} `json:"unmapped"`
	} `json:"plan"`
}

// checkGraph generates a graph through Hclaro, overlays the reviewed plan and asserts G1:
// no node unknown, every resource node mapped to configuration or state, and every confirmed
// change of the plan on exactly one node or, for orphans only, in the unmapped list.
func checkGraph(s *Scenario, h *Hclaro, plan PlanDetail, orphans []string) {
	var opts struct {
		CanGenerate        bool     `json:"canGenerate"`
		Reason             string   `json:"reason"`
		ContextFingerprint string   `json:"contextFingerprint"`
		Scope              *OpScope `json:"scope"`
	}
	if r, err := h.client.Do("GET", "graphs/options", nil, &opts); err != nil || r.Status != 200 || !opts.CanGenerate {
		s.Fatalf("graphs/options: %v %d can %v %s", err, r.Status, opts.CanGenerate, opts.Reason)
	}
	s.assertScope(h, opts.Scope)
	var started struct {
		Operation Operation `json:"operation"`
		Graph     struct {
			ID string `json:"id"`
		} `json:"graph"`
	}
	gbody := map[string]any{"acknowledgeEngineRun": true}
	if h.view != nil {
		gbody["contextFingerprint"] = opts.ContextFingerprint
	}
	if r, err := h.client.Do("POST", "graphs", gbody, &started); err != nil || r.Status != 202 {
		s.Fatalf("POST graphs: %v %d %s", err, r.Status, r.Code)
	}
	if op := s.FollowOp(h, started.Operation.ID, 15*time.Minute); op.Status != "succeeded" {
		s.Fatalf("graph operation %s (%s)", op.Status, code(op))
	}
	var v graphView
	r, err := h.client.Do("GET", "graphs/"+started.Graph.ID+"?planId="+plan.ID, nil, &v)
	if err != nil || r.Status != 200 || v.Plan == nil {
		s.Fatalf("graph with plan overlay: %v %d %s", err, r.Status, r.Code)
	}
	s.Artifact("graph-view.json", r.Body)
	confidence := map[string]int{}
	seen := map[string]int{}
	for _, n := range v.Nodes {
		confidence[n.Confidence]++
		if n.Confidence == "unknown" {
			s.Errorf("node %s (%s) is unknown", n.ID, n.ResourceAddress)
		}
		if (n.Kind == "resource" || n.Kind == "data") && n.Confidence != "exact" && n.Confidence != "configuration" {
			s.Errorf("resource node %s has confidence %q", n.ResourceAddress, n.Confidence)
		}
		for _, c := range n.Planned {
			seen[c.ChangeID]++
			// A change sits on its own instanced node or on its configuration resource.
			if key, _ := graph.ConfigAddress(c.Address); c.Address != n.ResourceAddress && key != n.ResourceAddress {
				s.Errorf("change %s is on node %s", c.Address, n.ResourceAddress)
			}
			if a := actionOf(plan, c.ChangeID); c.Action != a {
				s.Errorf("change %s shows %q on the graph, the plan says %q", c.Address, c.Action, a)
			}
		}
	}
	var unmapped []string
	for _, c := range v.Plan.Unmapped {
		seen[c.ChangeID]++
		unmapped = append(unmapped, c.Address)
	}
	confirmed := 0
	for _, c := range plan.Changes {
		if c.Action == "no-op" {
			continue
		}
		confirmed++
		if seen[c.ID] != 1 {
			s.Errorf("change %s %s is on %d nodes or lists", c.Address, c.Action, seen[c.ID])
		}
	}
	sort.Strings(unmapped)
	// An orphan is a graph node mapped to state where the engine's graph lists it (Terraform
	// 1.5.x plan graphs) and unmapped where it does not; nothing else may be unmapped.
	for _, a := range unmapped {
		if !slices.Contains(orphans, a) {
			s.Errorf("change %s matches no graph node and is not an orphan", a)
		}
	}
	s.Observe("G1 graph %s format %s: %d nodes %v; overlay %d changes, unmapped %v (orphans %v)", v.Status, v.Format, len(v.Nodes), confidence, v.Plan.Changes, unmapped, orphans)
	if v.Plan.Changes != confirmed {
		s.Fatalf("overlay: %d changes, the reviewed plan has %d confirmed", v.Plan.Changes, confirmed)
	}
}

func actionOf(plan PlanDetail, id string) string {
	for _, c := range plan.Changes {
		if c.ID == id {
			return c.Action
		}
	}
	return ""
}

// TestRealGraph covers G1 on the M26.16 recording project and the module-heavy project.
func TestRealGraph(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "g1-graph-m2616-"+name, name, "launch", false, func(s *Scenario) {
			g1GraphM2616(s, name, launchStart)
		})
		run.Scenario(t, "g1-graph-modules-"+name, name, "launch", false, func(s *Scenario) {
			g1GraphModules(s, name, launchStart)
		})
	}
}

// l1Snapshot keeps Hclaro's workspace view (status, diagnostic, capabilities) and its
// operations (each engine invocation's argv, status and code) at one point of L1, as
// artifacts, with the UTC time and a one-line summary. Read-only GETs; no assertion.
func l1Snapshot(s *Scenario, h *Hclaro, when string) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	var ws struct {
		Status     string `json:"status"`
		Diagnostic *struct {
			Code string `json:"code"`
		} `json:"diagnostic"`
		Info struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"info"`
	}
	r, err := h.client.Do("GET", "workspace", nil, &ws)
	if err != nil {
		s.Observe("L1 %s (%s): workspace not readable: %v", when, at, err)
		return
	}
	s.Artifact("workspace-"+when+".json", r.Body)
	diag := ""
	if ws.Diagnostic != nil {
		diag = ws.Diagnostic.Code
	}
	n := 0
	if ro, err := h.client.Do("GET", "operations", nil, nil); err == nil {
		s.Artifact("operations-"+when+".json", ro.Body)
		n = len(s.operations(h))
	}
	s.Observe("L1 %s (%s): workspace %d status %q diagnostic %q canPlan %v; %d operations recorded", when, at, r.Status, ws.Status, diag, ws.Info.Capabilities["canPlan"], n)
}

// windowsLockRead reports whether engine diagnostics show a lock acquisition that failed
// because another process's lock blocked reading the state (Terraform on Windows): both the
// lock error and the OS lock-violation text, whatever the line wrapping.
func windowsLockRead(d string) bool {
	flat := strings.Join(strings.Fields(strings.ToLower(d)), " ")
	return strings.Contains(flat, "error acquiring the state lock") && strings.Contains(flat, "another process has locked a portion of the file")
}

// l1RereadBound is Hclaro's automatic re-read bound after a lock-blocked read (operations
// lockRetryDelays, about 2 min) plus slack for one engine read.
const l1RereadBound = 150 * time.Second

// l1Workspace returns the launch workspace's status, diagnostic code and canPlan.
func l1Workspace(s *Scenario, h *Hclaro) (string, string, bool) {
	var ws struct {
		Status     string `json:"status"`
		Diagnostic *struct {
			Code string `json:"code"`
		} `json:"diagnostic"`
		Info struct {
			Capabilities struct {
				CanPlan bool `json:"canPlan"`
			} `json:"capabilities"`
		} `json:"info"`
	}
	if r, err := h.client.Do("GET", "workspace", nil, &ws); err != nil || r.Status != 200 {
		s.Fatalf("workspace: %v", err)
	}
	code := ""
	if ws.Diagnostic != nil {
		code = ws.Diagnostic.Code
	}
	return ws.Status, code, ws.Info.Capabilities.CanPlan
}

// l1WindowsLocked: while the holder holds the lock on Windows, the post-apply read is
// state_locked, and a plan and a drift scan are refused with 409 state_locked before any
// engine starts (FU-WIN-STATE-READ). The lock-info file stays as the holder wrote it.
func l1WindowsLocked(s *Scenario, h *Hclaro, project string, holder *Holder, lock, info string) {
	status, code, canPlan := l1Workspace(s, h)
	s.Observe("L1 Windows after the refused apply: workspace %q diagnostic %q canPlan %v", status, code, canPlan)
	if code != "state_locked" || canPlan {
		s.Errorf("L1 Windows: workspace %q %q canPlan %v after the refused apply, want state_locked without planning", status, code, canPlan)
	}
	n := len(s.operations(h))
	op, r := s.failedPlan(h, project, []string{"gen3.tfvars"})
	s.Artifact("plan-refusal.json", r.Body)
	if op.ID != "" || r.Status != 409 || r.Code != "state_locked" {
		s.Errorf("L1 Windows plan while locked: operation %q, %d %s; want 409 state_locked before the engine", op.ID, r.Status, r.Code)
	}
	dr, err := h.client.Do("POST", "drift-scans", map[string]any{"acknowledgeProviderAccess": true, "varFiles": []string{"launch.tfvars"}}, nil)
	if err != nil || dr.Status != 409 || dr.Code != "state_locked" {
		s.Errorf("L1 Windows drift scan while locked: %v %d %s; want 409 state_locked before the engine", err, dr.Status, dr.Code)
	}
	if got := len(s.operations(h)); got != n {
		s.Errorf("L1 Windows: %d operations started on unread state", got-n)
	}
	now, err := fileSHA256(lock)
	s.Observe("L1 Windows plan and drift scan refused %d %s / %d %s; holder alive %v, lock info unchanged %v", r.Status, r.Code, dr.Status, dr.Code, holder.Alive(), err == nil && now == info)
	if err != nil || now != info || !holder.Alive() {
		s.Fatalf("L1 Windows: the lock-info file changed or went away while the holder held it")
	}
}

// showUnderHolder records what the engine's own `show -json` (the read Hclaro reloads state
// with) prints while the F-2 holder holds the lock: FU-WIN-STATE-READ step 0 needs the exact
// Windows text. Diagnostic only, never an assertion. A successful read is recorded by size,
// never its content.
func showUnderHolder(s *Scenario, dirs ProcessDirs, engine, project string) {
	env := Environment(dirs, s.run.Cfg.CLIConfig[engine], nil)
	out, err := s.Direct(filepath.Join(dirs.Bin, exeName(engine)), project, env, "show -json")
	if err != nil {
		s.Artifact("show-under-holder.txt", []byte(err.Error()+"\n"))
		s.Observe("L1 direct show -json under the holder failed (see show-under-holder.txt)")
		return
	}
	s.Artifact("show-under-holder.txt", []byte(fmt.Sprintf("succeeded, %d bytes of state JSON (not recorded)\n", len(out))))
	s.Observe("L1 direct show -json under the holder succeeded (%d bytes)", len(out))
}

// TestRealLocks covers L1 (F-2 holder) and L2 (F-3).
func TestRealLocks(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "l1-state-locked-"+name, name, "launch", false, func(s *Scenario) {
			project, h := launchApplied(s, name, "launch-lock", []string{"launch.tfvars"})
			// The apply's plan is reviewed and approved before the lock is taken (as in A5).
			detail := s.LaunchPlan(h, project, name, []string{"gen2.tfvars"})
			ap, r := s.Approve(h, detail.ID)
			if r.Status != 200 {
				s.Fatalf("approval: %d %s", r.Status, r.Code)
			}
			fifo := filepath.Join(project, ".qual", "gate.fifo")
			defer os.Remove(fifo) // snapshotting a FIFO would block
			sum, serial := stateFile(s, project)
			dirs := s.procs[0].Dirs
			holder := hold(s, dirs, name, project)
			// Every exit path releases gracefully, so the engine removes its own lock-info file;
			// a kill (dispatch 3, run 43760321517) left it behind.
			defer func() {
				if err := holder.Close(); err != nil {
					s.Errorf("L1 holder cleanup: %v", err)
				}
				// Diagnostics (dispatch 4, run 43767550235: Windows plan refused with
				// planning_unavailable): the final workspace view once the holder is gone, and
				// Hclaro's own output, on every exit path.
				l1Snapshot(s, h, "after-release")
				for name, f := range map[string]string{"hclaro.stdout": h.Stdout, "hclaro.stderr": h.Stderr} {
					if b, err := os.ReadFile(f); err == nil {
						s.Artifact(name, b)
					}
				}
			}()
			lock := filepath.Join(project, ".terraform.tfstate.lock.info")
			info, err := fileSHA256(lock)
			if err != nil {
				s.Fatalf("lock info: %v", err)
			}
			check := func(what string, op Operation) {
				engineRan(s, what, op)
				now, err := fileSHA256(lock)
				s.Observe("L1 %s: %s (%s), holder alive %v, lock info unchanged %v", what, op.Status, code(op), holder.Alive(), err == nil && now == info)
				if code(op) != "state_locked" {
					s.Errorf("L1 %s ended %s with %q, want state_locked", what, op.Status, code(op))
				}
				if err != nil || now != info || !holder.Alive() {
					s.Fatalf("L1 %s: the lock-info file changed or went away while the holder held it", what)
				}
				// On Windows Terraform cannot read the held lock's info and reports the blocked
				// read instead (run 43871517546); owner decision 2026-11-16 accepts that there only.
				switch d := diagnostics(op); {
				case strings.Contains(d, "Lock Info"):
				case runtime.GOOS == "windows" && windowsLockRead(d):
					s.Observe("L1 %s: no Lock Info; the engine reports the state lock blocked by another process's lock (Windows)", what)
				default:
					s.Errorf("L1 %s: no lock info in the engine diagnostics: %q", what, d)
				}
				// On Windows the holder's lock blocks every read of terraform.tfstate (run
				// 43760321517: "another process has locked a portion of the file"), so the state is
				// compared once the holder has released it, below.
				if runtime.GOOS != "windows" {
					assertStateUnchanged(s, project, sum, serial, "during "+what)
				}
			}
			started, r := s.StartApply(h, detail.ID, ap.Token, ap.Phrase)
			if r.Status != 202 {
				s.Fatalf("apply: %d %s", r.Status, r.Code)
			}
			op := s.FollowOp(h, started.ID, 5*time.Minute)
			check("apply", op)
			if op.Outcome != "not_applied" {
				s.Errorf("L1 apply outcome %q, want not_applied", op.Outcome)
			}
			l1Snapshot(s, h, "after-refused-apply")
			showUnderHolder(s, dirs, name, project)
			if runtime.GOOS == "windows" {
				// FU-WIN-STATE-READ: the holder's lock blocks reading terraform.tfstate, so Hclaro
				// could not read the state after the apply and must not start anything on it.
				l1WindowsLocked(s, h, project, holder, lock, info)
			} else {
				op, r = s.failedPlan(h, project, []string{"gen3.tfvars"})
				if op.ID == "" {
					s.Artifact("plan-refusal.json", r.Body)
					l1Snapshot(s, h, "at-plan-refusal")
					s.Fatalf("plan refused before the engine: %d %s", r.Status, r.Code)
				}
				check("plan", op)
				op, sc := s.driftScan(h, project, []string{"launch.tfvars"})
				check("drift", op)
				if sc.Status != "failed" || sc.Diagnostic == nil || sc.Diagnostic.Code != "state_locked" {
					s.Errorf("L1 drift scan %s %v, want failed state_locked", sc.Status, sc.Diagnostic)
				}
			}
			if err := holder.Release(); err != nil {
				s.Fatalf("release: %v", err)
			}
			// Hclaro never unlocks: the lock goes away only because its holder exited.
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(lock); os.IsNotExist(err) {
					break
				}
				if time.Now().After(deadline) {
					s.Fatalf("lock-info file still present 30 s after the holder exited")
				}
				time.Sleep(100 * time.Millisecond)
			}
			s.Observe("L1 holder %q released; lock-info file removed by the holder", od9(name))
			if runtime.GOOS == "windows" {
				assertStateUnchanged(s, project, sum, serial, "by the refused apply, plan and drift scan (read after the release)")
				s.Observe("L1 state unchanged by the refused apply, plan and drift scan (Windows: read after the release)")
				// No Read again: Hclaro reads the state again on its own within its bound
				// (about 2 min), then planning works.
				start := time.Now()
				for status := ""; status != "ready" && status != "empty"; time.Sleep(time.Second) {
					if time.Since(start) > l1RereadBound {
						l1Snapshot(s, h, "not-read-again")
						s.Fatalf("L1 workspace not read again %v after the release (last status %q)", l1RereadBound, status)
					}
					status, _, _ = l1Workspace(s, h)
				}
				s.Observe("L1 workspace ready %v after the release, without Read again", time.Since(start).Round(time.Second))
				l1Snapshot(s, h, "read-again")
				s.LaunchPlan(h, project, name, []string{"gen3.tfvars"})
				s.Observe("L1 plan after the release succeeded")
			}
		})
		run.Scenario(t, "l2-unlock-failed-"+name, name, "launch", false, func(s *Scenario) {
			e, _ := run.Engine(name)
			s.NotTested(fmt.Sprintf("unlock failure not reproducible on %s %s (F-3 source analysis 2026-11-14, NOT_REPRODUCIBLE for the local backend); synthetic state_unlock_failed coverage stays in internal/engine/planner_test.go and internal/operations/apply_test.go", map[string]string{"terraform": "Terraform", "tofu": "OpenTofu"}[e.Name], e.Version))
		})
	}
}

// failurePlans runs F1 (plan_failed) and F2's providers-removed case (init_required) on an
// initialized failures project; F5 repeats them under another locale.
func failurePlans(s *Scenario, h *Hclaro, project, label string) {
	op, r := s.failedPlan(h, project, []string{"missing.tfvars"})
	if op.ID == "" {
		s.Fatalf("%s F1 plan refused before the engine: %d %s", label, r.Status, r.Code)
	}
	engineRan(s, label+" F1", op)
	s.Observe("%s F1: %s (%s): %s", label, op.Status, code(op), diagnostics(op))
	// The var-file value is a path holding the canary: the diagnostic must reach Hclaro's
	// output with that value replaced, or with its line withheld when the random canary looks
	// like a secret; never merely absent (the canary scan covers the rest).
	d := diagnostics(op)
	redacted := strings.Contains(d, `no file exists at "[redacted]"`) || strings.Contains(d, "Invalid function argument\n[line withheld: looks like a secret]")
	if op.Status != "failed" || code(op) != "plan_failed" || !redacted {
		s.Fatalf("%s F1: plan %s (%s), want plan_failed with the file() diagnostic and its path redacted or withheld", label, op.Status, code(op))
	}
	if err := os.RemoveAll(filepath.Join(project, ".terraform", "providers")); err != nil {
		s.Fatalf("%v", err)
	}
	op, r = s.failedPlan(h, project, []string{"launch.tfvars"})
	if op.ID == "" {
		s.Fatalf("%s F2 plan refused before the engine: %d %s", label, r.Status, r.Code)
	}
	engineRan(s, label+" F2", op)
	s.Observe("%s F2 (providers removed): %s (%s): %s", label, op.Status, code(op), diagnostics(op))
	if code(op) != "init_required" {
		s.Fatalf("%s F2 (providers removed): plan %s (%s), want init_required", label, op.Status, code(op))
	}
}

// TestRealFailures covers F1 to F5.
func TestRealFailures(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "f1-f2-plan-failed-init-required-"+name, name, "launch", false, func(s *Scenario) {
			project, _, _, h := launch(s, name, "failures")
			failurePlans(s, h, project, "C.UTF-8")
		})
		run.Scenario(t, "f2-lockfile-changed-"+name, name, "launch", false, func(s *Scenario) {
			project, _, _, h := launch(s, name, "failures", ".terraform.lock.hcl")
			lock := filepath.Join(project, ".terraform.lock.hcl")
			b, _ := os.ReadFile(lock)
			edited := strings.Replace(string(b), `"2.9.1"`, `"2.9.0"`, 1)
			if edited == string(b) || os.WriteFile(lock, []byte(edited), 0o600) != nil {
				s.Fatalf("could not edit the lock file")
			}
			op, r := s.failedPlan(h, project, []string{"launch.tfvars"})
			if op.ID == "" {
				s.Fatalf("plan refused before the engine: %d %s", r.Status, r.Code)
			}
			engineRan(s, "F2 lock file", op)
			s.Observe("F2 (lock file changed): %s (%s): %s", op.Status, code(op), diagnostics(op))
			if code(op) != "init_required" {
				s.Fatalf("F2 (lock file changed): plan %s (%s), want init_required", op.Status, code(op))
			}
		})
		run.Scenario(t, "f2-never-initialized-"+name, name, "launch", false, func(s *Scenario) {
			project := s.Project("failures")
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.startCodes = map[string]bool{"uninitialized": true}
			h := s.StartLaunch(dirs, env, name, project)
			var opts struct {
				CanPlan bool   `json:"canPlan"`
				Reason  string `json:"reason"`
			}
			if r, err := h.client.Do("GET", "plans/options", nil, &opts); err != nil || r.Status != 200 {
				s.Fatalf("plans/options: %v %d", err, r.Status)
			}
			if !opts.CanPlan {
				// Refused before any engine run: the project read must say it needs init.
				var ws struct {
					Diagnostic *struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"diagnostic"`
				}
				if r, err := h.client.Do("GET", "workspace", nil, &ws); err != nil || r.Status != 200 || ws.Diagnostic == nil {
					s.Fatalf("workspace: %v %d", err, r.Status)
				}
				s.Observe("F2 (never initialized): planning unavailable before the engine (%q); project read %s: %q", opts.Reason, ws.Diagnostic.Code, ws.Diagnostic.Message)
				if ws.Diagnostic.Code != "uninitialized" || !strings.Contains(strings.ToLower(ws.Diagnostic.Message), "init") {
					s.Fatalf("F2 (never initialized): project read %s %q, want uninitialized pointing at init", ws.Diagnostic.Code, ws.Diagnostic.Message)
				}
				// The engine never ran, so init_required classification is not exercised here
				// (the engine's missing-init text is pinned by local-noinit.jsonl).
				s.NotTested("engine not run: Hclaro refuses before the engine with " + ws.Diagnostic.Code + ", not init_required; classifier not exercised on this path")
			}
			op, r := s.failedPlan(h, project, []string{"launch.tfvars"})
			s.Observe("F2 (never initialized): POST %d %s, plan %s (%s): %s", r.Status, r.Code, op.Status, code(op), diagnostics(op))
			if code(op) != "init_required" {
				s.Fatalf("F2 (never initialized): plan %s (%s), want init_required", op.Status, code(op))
			}
		})
		run.Scenario(t, "f3-plan-too-large-"+name, name, "launch", false, func(s *Scenario) {
			project, _, _, h := launch(s, name, "failures")
			op, r := s.failedPlan(h, project, []string{"big.tfvars"})
			if op.ID == "" {
				s.Fatalf("plan refused before the engine: %d %s", r.Status, r.Code)
			}
			engineRan(s, "F3", op)
			s.Observe("F3: plan %s (%s), exit %d", op.Status, code(op), *op.ExitCode)
			if code(op) != "plan_too_large" {
				s.Fatalf("F3: plan %s (%s), want plan_too_large", op.Status, code(op))
			}
			if r, err := h.client.Do("GET", "workspace", nil, nil); err != nil || r.Status != 200 {
				s.Fatalf("F3: Hclaro not answering after the large plan: %v %d", err, r.Status)
			}
			rss, method := peakRSS(h.Pid())
			s.Observe("F3: Hclaro peak memory %d bytes (%.1f MiB; %s; bound %d MiB)", rss, float64(rss)/(1<<20), method, f3RSSBoundMiB)
			switch {
			case rss < 0:
				s.NotTested("F3: peak RSS is not measured on " + runtime.GOOS)
			case rss > f3RSSBoundMiB<<20:
				s.Fatalf("F3: peak RSS %d bytes exceeds the %d MiB bound", rss, f3RSSBoundMiB)
			}
		})
		run.Scenario(t, "f4-unknown-actions-"+name, name, "launch", false, func(s *Scenario) {
			project := s.Project("forget", stateWrites...)
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			e, _ := run.Engine(name)
			// An engine that rejects the only trigger found (lifecycle destroy) cannot emit it.
			if _, err := s.Direct(filepath.Join(dirs.Bin, exeName(name)), project, env, "init -input=false -no-color"); err != nil {
				s.NotTested(fmt.Sprintf("no engine emits an unsupported combination: %s %s rejects the fixture at init: %.700s", e.Name, e.Version, err))
			}
			s.result.SetupInit = "direct"
			s.Guard(project)
			s.RefreshBaseline(project)
			h := s.StartLaunch(dirs, env, name, project)
			if op, r := s.failedPlan(h, project, []string{"launch.tfvars"}); op.Status != "succeeded" {
				// An engine that rejects the only trigger found (lifecycle destroy) cannot emit it.
				if op.ID == "" {
					s.Fatalf("F4 plan refused before the engine: %d %s", r.Status, r.Code)
				}
				engineRan(s, "F4", op)
				s.NotTested(fmt.Sprintf("no engine emits an unsupported combination: %s %s rejects the fixture (%s): %s", e.Name, e.Version, code(op), strings.TrimSpace(diagnostics(op))))
			} else {
				detail := PlanDetail{}
				if r, err := h.client.Do("GET", "plans/"+op.PlanID, nil, &detail); err != nil || r.Status != 200 {
					s.Fatalf("F4 setup plan: %v %d", err, r.Status)
				}
				if _, applied := s.ApplyReviewed(h, detail.ID); applied.Outcome != "applied" {
					s.Fatalf("F4 setup apply ended %s, outcome %q", applied.Status, applied.Outcome)
				}
			}
			sum, serial := stateFile(s, project)
			detail := s.LaunchPlan(h, project, name, []string{"gen2.tfvars"})
			var actions []string
			for _, c := range detail.Changes {
				if c.Address == "terraform_data.kept" {
					actions = c.Actions
				}
			}
			s.Observe("F4: terraform_data.kept actions %v; plan %s", actions, detail)
			if slices.Equal(actions, []string{"delete", "create"}) || slices.Equal(actions, []string{"create", "delete"}) || slices.Equal(actions, []string{"forget"}) {
				s.NotTested(fmt.Sprintf("no engine emits an unsupported combination: %s planned %v", name, actions))
			}
			var raw struct {
				Apply struct {
					Available bool   `json:"available"`
					Code      string `json:"code"`
				} `json:"apply"`
			}
			if err := json.Unmarshal(detail.Raw, &raw); err != nil || raw.Apply.Available || raw.Apply.Code != "unknown_actions" {
				s.Fatalf("F4: plan apply status %+v (%v), want unavailable unknown_actions", raw.Apply, err)
			}
			ops := len(s.operations(h))
			// The preflight blocks it (plan.eligible), so no approval token exists to apply with.
			if _, r := s.Approve(h, detail.ID); r.Status == 200 {
				s.Fatalf("F4: a plan with unsupported actions was approved")
			} else {
				s.Observe("F4: approval refused: %d %s", r.Status, r.Code)
			}
			if len(s.operations(h)) != ops {
				s.Fatalf("F4: an operation started for a plan with unsupported actions")
			}
			assertStateUnchanged(s, project, sum, serial, "after the refused approval")
		})
		run.Scenario(t, "f5-locale-de-"+name, name, "launch", false, func(s *Scenario) {
			if runtime.GOOS == "windows" {
				s.Skip("LANG is not the locale mechanism on Windows; a display-language test is out of scope")
			}
			// Declared in the scenario's allowlist (extraEnvironment) for Hclaro and its engines.
			extra := map[string]string{"LANG": "de_DE.UTF-8", "LC_ALL": "de_DE.UTF-8"}
			project := s.Project("failures", stateWrites...)
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.SetupInit(dirs, env, name, project)
			h, err := s.StartHclaro(dirs, Environment(dirs, s.run.Cfg.CLIConfig[name], extra), extra, "--path", project, "--no-browser", "--engine", name)
			if err != nil {
				s.Fatalf("start hclaro: %v", err)
			}
			s.waitReady(h)
			failurePlans(s, h, project, "de_DE.UTF-8")
		})
	}
}

// f3RSSBoundMiB is the F3 peak RSS bound, set in docs/qualification/README.md before the run.
const f3RSSBoundMiB = 512

// driftVars and driftFiles are the drift fixture's var files and the files it manages.
var driftVars, driftFiles = []string{"launch.tfvars"}, []string{"out/gone.txt", "out/edited.txt"}

func d1DriftDeleted(s *Scenario, name string, start starter) {
	vars, files := driftVars, driftFiles
	project, h := appliedWith(s, start, name, "drift", vars, files...)
	if err := os.Remove(filepath.Join(project, "out", "gone.txt")); err != nil {
		s.Fatalf("%v", err)
	}
	_, sc := s.driftScan(h, project, vars)
	got := sc.drifted()
	s.Observe("D1 scan %s %s: %v", sc.Status, sc.Outcome, got)
	if sc.Status != "completed" || sc.Outcome != "drift_detected" || len(got) != 1 || got["local_file.gone"] != "missing delete" || sc.Summary.Missing != 1 {
		s.Fatalf("D1: scan %s %s %v summary %+v, want local_file.gone missing", sc.Status, sc.Outcome, got, sc.Summary)
	}
	for _, rc := range sc.Resources {
		if r, err := h.client.Do("GET", "drift-scans/"+sc.ID+"/resources/"+rc.ID, nil, nil); err != nil || r.Status != 200 {
			s.Fatalf("D1 detail %s: %v %d", rc.Address, err, r.Status)
		}
	}
}

func g1GraphM2616(s *Scenario, name string, start starter) {
	project := s.Project(m2616Project, "terraform.tfstate")
	if err := os.Rename(filepath.Join(project, "state.json"), filepath.Join(project, "terraform.tfstate")); err != nil {
		s.Fatalf("%v", err)
	}
	// The project's test secret becomes the run canary, so the standard scan covers it.
	for _, f := range []string{"prod.tfvars", "terraform.tfstate", "remote.json"} {
		p := filepath.Join(project, f)
		b, err := os.ReadFile(p)
		if err == nil {
			err = os.WriteFile(p, []byte(strings.ReplaceAll(string(b), "DO_NOT_LEAK_HCLARO_TEST_SECRET", s.run.Canary)), 0o600)
		}
		if err != nil {
			s.Fatalf("%v", err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(project, "prod.tfvars")); !strings.Contains(string(b), s.run.Canary) {
		s.Fatalf("the canary is not in prod.tfvars")
	}
	s.RefreshBaseline(project)
	_, _, h := start(s, name, project)
	plan := s.LaunchPlan(h, project, name, []string{"prod.tfvars"})
	checkGraph(s, h, plan, []string{"terraform_data.gone", "terraform_data.counted_gone[0]", "terraform_data.counted_gone[1]", `module.old["x"].terraform_data.this`})
}

func g1GraphModules(s *Scenario, name string, start starter) {
	project, h := appliedWith(s, start, name, "graph", []string{"launch.tfvars"})
	if err := os.Remove(filepath.Join(project, "retired.tf")); err != nil {
		s.Fatalf("%v", err)
	}
	plan := s.LaunchPlan(h, project, name, []string{"grow.tfvars"})
	s.Observe("G1 plan: %s", plan)
	checkGraph(s, h, plan, []string{"module.old.terraform_data.leaf", "terraform_data.orphan[0]", "terraform_data.orphan[1]"})
}
