//go:build realengine

package qualify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/internal/engine"
)

// M27.03 A6, A7 and A7b with the OD-8 gated fixture (approved with restrictions 2026-11-15,
// for these scenarios only). The gate is the fixed script fixtures/qualify/launch-gate/gate.sh,
// run by the engine's local-exec as "/bin/sh ./gate.sh <normal|trap>". It touches only
// <project>/.qual/. Synchronisation is on observable state only: the operation status (SSE /
// GET), the gate's marker files and the engine PID, each with a hard timeout.

var stage1 = []string{"terraform_data.stage1[0]", "terraform_data.stage1[1]", "terraform_data.stage1[2]"}

type gateRun struct {
	project, planID string
	dirs            ProcessDirs
	env             []string
	h               *Hclaro
	op              Operation
	pid             int
	started         time.Time
	mode            string
}

func (g *gateRun) qual(name string) string { return filepath.Join(g.project, ".qual", name) }

// gateStart plans the gated fixture, approves and starts the apply, and waits for the
// operation to run and the gate to report it was reached.
func gateStart(s *Scenario, name string, varFiles []string, mode string) *gateRun {
	if runtime.GOOS == "windows" {
		s.NotTested("the Windows gate variant is not implemented (M27.07, F-5)")
	}
	g := &gateRun{mode: mode}
	g.project, g.dirs, g.env, g.h = launch(s, name, "launch-gate", ".qual/reached", ".qual/interrupted", ".qual/release")
	s.Observe("gate command (OD-8): /bin/sh ./gate.sh %s, working directory the project, files under .qual/ only", mode)
	detail := s.LaunchPlan(g.h, g.project, name, varFiles)
	if detail.Summary["create"] != 6 {
		s.Fatalf("gated plan: %s, want 6 creates", detail)
	}
	g.planID = detail.ID
	a, r := s.Approve(g.h, detail.ID)
	if r.Status != 200 {
		s.Fatalf("approval: %d %s", r.Status, r.Code)
	}
	g.started = time.Now()
	op, r := s.StartApply(g.h, detail.ID, a.Token, a.Phrase)
	if r.Status != 202 {
		s.Fatalf("apply: %d %s", r.Status, r.Code)
	}
	g.op = op
	waitRunning(s, g.h, op.ID)
	if !waitFile(g.qual("reached"), 60*time.Second) {
		release(s, g)
		s.NotTested("gate not reached")
	}
	pids, err := enginePIDs(g.dirs.Bin, name)
	if err != nil || len(pids) != 1 {
		release(s, g)
		s.NotTested(fmt.Sprintf("engine process not identified (%v, %v)", pids, err))
	}
	g.pid = pids[0]
	s.Observe("gate reached %s after the apply started; engine pid %d", time.Since(g.started).Round(time.Millisecond), g.pid)
	return g
}

func waitFile(p string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(p); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// interrupts counts the gate's INT trap lines.
func interrupts(g *gateRun) int {
	b, _ := os.ReadFile(g.qual("interrupted"))
	return bytes.Count(b, []byte("\n"))
}

// waitInterrupts waits (d) for at least one trap line and returns the count.
func waitInterrupts(g *gateRun, d time.Duration) int {
	waitFile(g.qual("interrupted"), d)
	time.Sleep(200 * time.Millisecond) // shortcut: lets a second signal land before counting; a later one is caught by the final count
	return interrupts(g)
}

func release(s *Scenario, g *gateRun) {
	if err := os.WriteFile(g.qual("release"), nil, 0o600); err != nil {
		s.Errorf("release: %v", err)
	}
}

func alive(g *gateRun, engineName string) bool {
	pids, err := enginePIDs(g.dirs.Bin, engineName)
	return err == nil && slices.Contains(pids, g.pid)
}

// waitGone waits up to d for the engine PID to disappear and returns when it did. outlived is
// set when a sample saw the engine alive after Hclaro had exited (P3 needs the engine to end
// first; sampling cannot order two exits inside one interval, so only this direction is judged).
func waitGone(g *gateRun, engineName string, d time.Duration) (gone time.Time, ok, outlived bool) {
	deadline := time.Now().Add(d)
	for {
		exited := g.h.Exited()
		if !alive(g, engineName) {
			return time.Now(), true, outlived
		}
		outlived = outlived || exited
		if time.Now().After(deadline) {
			return time.Time{}, false, outlived
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// historyUncertain: history stores an apply's operation outcome "uncertain" (and a cancelled
// or unknown apply) as result "partial" (internal/history/convert.go).
func historyUncertain(result string) bool { return result == "partial" }

// gateState checks stage 1 is fully in state and returns the stage-2 objects present.
func gateState(s *Scenario, g *gateRun, engineName string) []string {
	got := s.ShowState(g.dirs, g.env, engineName, g.project, "")
	for _, a := range stage1 {
		if _, ok := got[a]; !ok {
			s.Fatalf("stage-1 object %s is not in state: %v", a, addresses(got))
		}
	}
	var stage2 []string
	for a := range got {
		if strings.HasPrefix(a, "terraform_data.stage2") {
			stage2 = append(stage2, a)
		}
	}
	return stage2
}

// historyFile reads the final apply entry for planID from history.json in the config dir
// (read-only, after Hclaro exited).
func historyFile(s *Scenario, g *gateRun) HistoryEntry {
	var found *HistoryEntry
	_ = filepath.WalkDir(g.dirs.Config, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "history.json" {
			return nil
		}
		b, err := os.ReadFile(p)
		var doc struct {
			Entries []HistoryEntry `json:"entries"`
		}
		if err != nil || json.Unmarshal(b, &doc) != nil {
			return nil
		}
		for i := range doc.Entries {
			if e := doc.Entries[i]; e.Kind == "apply" && e.Outcome.PlanID == g.planID {
				found = &e
			}
		}
		return nil
	})
	if found == nil {
		s.Fatalf("history.json has no apply entry for the plan")
	}
	return *found
}

func exitCode(c *int) string {
	if c == nil {
		return "absent"
	}
	return fmt.Sprint(*c)
}

func stdoutHas(h *Hclaro, text string) bool {
	b, _ := os.ReadFile(h.Stdout)
	return bytes.Contains(b, []byte(text))
}

const shutdownNotice = "An apply is running. Hclaro interrupts it once"

// engineAck is the engine's interrupt acknowledgement in its log (-json "log" message).
const engineAck = "Interrupt received"

// excerptAcks counts the acknowledgement in a history entry's log excerpt.
func excerptAcks(e HistoryEntry) int {
	n := 0
	for _, l := range e.Excerpt {
		n += strings.Count(l.Text, engineAck)
	}
	return n
}

// sseAcks counts the acknowledgement in the operation's recorded SSE log lines.
func sseAcks(s *Scenario, id string) int {
	b, _ := os.ReadFile(filepath.Join(s.Dir, "responses.raw"))
	seen := map[string]bool{}
	for _, blk := range strings.Split(string(b), "### SSE operations/"+id+"\n")[1:] {
		line, _, _ := strings.Cut(blk, "\n")
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, engineAck) {
			seen[line] = true // replays of the same event count once
		}
	}
	return len(seen)
}

func savedPlanDirs(h *Hclaro) []string {
	entries, _ := os.ReadDir(h.Dirs.Tmp)
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "hclaro-plan-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestRealLaunchGate covers A6, A7 and A7b.
func TestRealLaunchGate(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	for _, name := range engines() {
		run.Scenario(t, "a6-interrupt-apply-"+name, name, "launch", false, func(s *Scenario) {
			g := gateStart(s, name, []string{"launch.tfvars"}, "normal")
			defer release(s, g)
			if r, err := g.h.client.Do("POST", "operations/"+g.op.ID+"/cancel", map[string]any{}, nil); err != nil || r.Status != 202 {
				s.Fatalf("cancel: %v %d %s", err, r.Status, r.Code)
			}
			n := waitInterrupts(g, 60*time.Second)
			release(s, g)
			done := s.FollowOp(g.h, g.op.ID, 5*time.Minute)
			gone, ok, _ := waitGone(g, name, 60*time.Second)
			if !ok {
				s.Fatalf("engine pid %d still running 60 s after the apply ended", g.pid)
			}
			s.Observe("cancel: gate interrupts %d; apply %s outcome %q exit %s diagnostic %v; engine gone %s after start",
				n, done.Status, done.Outcome, exitCode(done.ExitCode), done.Diagnostic, gone.Sub(g.started).Round(time.Millisecond))
			if stage2 := gateState(s, g, name); len(stage2) > 0 {
				s.NotTested("interrupt arrived after the gate (stage-2 objects in state: " + strings.Join(stage2, ", ") + ")")
			}
			acks := sseAcks(s, g.op.ID)
			s.Observe("engine interrupt acknowledgements in the log: %d", acks)
			if n == 0 && acks == 0 {
				s.NotTested("interrupt delivery not observed (no gate marker, no engine acknowledgement)")
			}
			if interrupts(g) > 1 || acks > 1 {
				s.Fatalf("more than one interrupt: gate %d, engine acknowledgements %d", interrupts(g), acks)
			}
			if done.Outcome == "not_applied" || done.Outcome != "applied" && done.Outcome != "uncertain" {
				s.Fatalf("outcome %q with stage-1 objects in state; want applied or uncertain", done.Outcome)
			}
			if _, err := os.Stat(filepath.Join(g.project, ".terraform.tfstate.lock.info")); err == nil {
				s.Fatalf("lock-info file left after the interrupted apply")
			}
		})
		run.Scenario(t, "a7-shutdown-apply-"+name, name, "launch", false, func(s *Scenario) {
			g := gateStart(s, name, []string{"launch.tfvars"}, "normal")
			defer release(s, g)
			if !alive(g, name) {
				s.NotTested("shutdown timing not established (engine gone before the interrupt)")
			}
			t0 := time.Now()
			if err := g.h.Interrupt(); err != nil {
				s.Fatalf("interrupt hclaro: %v", err)
			}
			// P1: the notice, with Hclaro still running.
			deadline := time.Now().Add(60 * time.Second)
			for !stdoutHas(g.h, shutdownNotice) {
				if g.h.Exited() || time.Now().After(deadline) {
					s.Fatalf("P1: no shutdown notice while Hclaro ran (exited %v)", g.h.Exited())
				}
				time.Sleep(50 * time.Millisecond)
			}
			p1 := time.Since(t0)
			// P2: one interrupt delivered to the engine's process group (the gate's trap).
			n := waitInterrupts(g, 10*time.Second)
			p2 := time.Since(t0)
			release(s, g)
			// P3: the engine exits unaided, before Hclaro.
			gone, ok, outlived := waitGone(g, name, 5*time.Minute)
			if !ok {
				s.Fatalf("P3: engine pid %d still running 5 min after the interrupt", g.pid)
			}
			hclaroDuring := !outlived
			p3 := gone.Sub(t0)
			code, exited := g.h.Wait(engine.ApplyGracePeriod + 2*time.Minute)
			if !exited {
				s.Fatalf("Hclaro still running %s after the interrupt", engine.ApplyGracePeriod+2*time.Minute)
			}
			pExit := time.Since(t0)
			// P4: final history entry.
			e := historyFile(s, g)
			// P5: state and artifacts.
			stage2 := gateState(s, g, name)
			_, lockErr := os.Stat(filepath.Join(g.project, ".terraform.tfstate.lock.info"))
			plans := savedPlanDirs(g.h)
			s.Observe("phases after the interrupt: P1 notice %s; P2 gate interrupts %d by %s; P3 engine gone %s (engine never seen alive after Hclaro exited: %v); Hclaro exit %d at %s",
				p1.Round(time.Millisecond), n, p2.Round(time.Millisecond), p3.Round(time.Millisecond), hclaroDuring, code, pExit.Round(time.Millisecond))
			diag := ""
			if e.Diagnostic != nil {
				diag = e.Diagnostic.Code
			}
			s.Observe("P4 history: status %s outcome %q exit %s diagnostic %q; P5 stage-2 %v, lock-info present %v, saved plans %v", e.Status, e.Outcome.Result, exitCode(e.ExitCode), diag, stage2, lockErr == nil, plans)
			acks := excerptAcks(e)
			s.Observe("P2: gate interrupts %d, engine acknowledgements in the history excerpt %d", interrupts(g), acks)
			if interrupts(g) == 0 && acks == 0 {
				s.NotTested("shutdown timing not established (no gate marker, no engine acknowledgement)")
			}
			if interrupts(g) > 1 || acks > 1 {
				s.Fatalf("P2: more than one interrupt: gate %d, engine acknowledgements %d", interrupts(g), acks)
			}
			if !hclaroDuring {
				s.Fatalf("P3: the engine was still running after Hclaro exited")
			}
			if p3 > engine.ApplyGracePeriod-time.Minute {
				s.Fatalf("P3: the engine took %s, not well before the grace period", p3)
			}
			if e.Outcome.Result == "not_applied" {
				s.Fatalf("P4: outcome not_applied with stage-1 objects in state")
			}
			allPlanned := len(stage2) == 2
			if e.Outcome.Result == "applied" && !(e.ExitCode != nil && *e.ExitCode == 0 && allPlanned) {
				s.Fatalf("P4: outcome applied, but exit %s and stage-2 %v", exitCode(e.ExitCode), stage2)
			}
			if e.Outcome.Result != "applied" && !historyUncertain(e.Outcome.Result) {
				s.Fatalf("P4: outcome %q, want applied or uncertain (history: partial)", e.Outcome.Result)
			}
			if lockErr == nil {
				s.Fatalf("P5: lock-info file left")
			}
			if len(plans) > 0 || stdoutHas(g.h, "saved plan was not deleted") {
				s.Fatalf("P5: saved plan left %v or the not-deleted notice printed", plans)
			}
		})
		run.Scenario(t, "a7b-shutdown-grace-"+name, name, "launch", false, func(s *Scenario) {
			g := gateStart(s, name, []string{"trap.tfvars"}, "trap")
			s.keepLock, s.keepPlanDir = true, true
			defer release(s, g) // cleanup only, after the engine is gone
			if !alive(g, name) {
				s.NotTested("shutdown timing not established (engine gone before the interrupt)")
			}
			t0 := time.Now()
			if err := g.h.Interrupt(); err != nil {
				s.Fatalf("interrupt hclaro: %v", err)
			}
			deadline := time.Now().Add(60 * time.Second)
			for !stdoutHas(g.h, shutdownNotice) {
				if g.h.Exited() || time.Now().After(deadline) {
					s.Fatalf("P1: no shutdown notice while Hclaro ran (exited %v)", g.h.Exited())
				}
				time.Sleep(50 * time.Millisecond)
			}
			n := waitInterrupts(g, 10*time.Second)
			grace := engine.ApplyGracePeriod
			gone, ok, _ := waitGone(g, name, grace+3*time.Minute)
			if !ok {
				s.Fatalf("engine pid %d still running %s after the interrupt", g.pid, grace+3*time.Minute)
			}
			at := gone.Sub(t0)
			s.Observe("A7b: gate interrupts %d; engine gone %s after the interrupt (grace %s)", n, at.Round(time.Second), grace)
			if at < grace-2*time.Minute {
				_, _ = g.h.Wait(grace + time.Minute)
				s.NotTested(fmt.Sprintf("engine did not outlive the grace period (exited %s after the interrupt)", at.Round(time.Second)))
			}
			code, exited := g.h.Wait(2 * time.Minute)
			if !exited {
				s.Fatalf("Hclaro still running after the engine was killed")
			}
			release(s, g)
			e := historyFile(s, g)
			stage2 := gateState(s, g, name)
			_, lockErr := os.Stat(filepath.Join(g.project, ".terraform.tfstate.lock.info"))
			plans := savedPlanDirs(g.h)
			notice := stdoutHas(g.h, "saved plan was not deleted")
			diag := ""
			if e.Diagnostic != nil {
				diag = e.Diagnostic.Code
			}
			s.Observe("A7b: Hclaro exit %d; history status %s outcome %q exit %s diagnostic %q; stage-2 %v; lock-info present %v; saved plans %v, not-deleted notice %v",
				code, e.Status, e.Outcome.Result, exitCode(e.ExitCode), diag, stage2, lockErr == nil, plans, notice)
			acks := excerptAcks(e)
			s.Observe("A7b: gate interrupts %d, engine acknowledgements in the history excerpt %d", interrupts(g), acks)
			if interrupts(g) == 0 && acks == 0 {
				s.NotTested("shutdown timing not established (no gate marker, no engine acknowledgement)")
			}
			if interrupts(g) > 1 || acks > 1 {
				s.Fatalf("more than one interrupt before the kill: gate %d, engine acknowledgements %d", interrupts(g), acks)
			}
			if at > grace+2*time.Minute {
				s.Fatalf("engine killed %s after the interrupt, outside grace ±2 min", at.Round(time.Second))
			}
			if diag != "shutdown" || !historyUncertain(e.Outcome.Result) || e.ExitCode != nil && *e.ExitCode == 0 {
				s.Fatalf("history: diagnostic %q outcome %q exit %s, want shutdown, uncertain (history: partial), non-zero or absent", diag, e.Outcome.Result, exitCode(e.ExitCode))
			}
			if len(plans) > 0 && !notice {
				s.Fatalf("saved plan %v left without the not-deleted notice", plans)
			}
		})
	}
}
