//go:build windows

package qualify

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestF5WindowsInterrupt is FEASIBILITY-CHECKLIST F-5: does CTRL_BREAK to Hclaro's process
// group (proc_windows.go) reach cmd/hclaro's signal handler, and does Hclaro then interrupt
// a running apply instead of killing it? Fake engine only, no network. It runs only in the
// real-engine workflow's Windows job, which sets HCLARO_QUAL_F5_OUT; its run.json is the evidence.
func TestF5WindowsInterrupt(t *testing.T) {
	out := os.Getenv("HCLARO_QUAL_F5_OUT")
	if out == "" {
		t.Skip("F-5 runs in the real-engine workflow only (HCLARO_QUAL_F5_OUT)")
	}
	hclaro, terraform, tofu := buildBinaries(t)
	root := t.TempDir()
	cli := filepath.Join(root, "empty.tfrc")
	if err := os.WriteFile(cli, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	repo, _ := filepath.Abs("../..")
	if img := os.Getenv("ImageOS"); img != "" {
		buildRunnerImage, buildRunnerImageVersion = os.Getenv("RUNNER_LABEL")+"/"+img, os.Getenv("ImageVersion")
	}
	r, err := NewRun(Config{Hclaro: hclaro, Engines: map[string]string{"terraform": terraform, "tofu": tofu},
		CLIConfig: map[string]string{"terraform": cli, "tofu": cli}, Out: out, Root: filepath.Join(root, "run"), Repo: repo, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	// F-5 asks for a second arrangement when the first gives no interrupt path: a step without
	// a console runs again after the test process allocates one.
	// GetConsoleWindow is 0 for a windowless (pseudo) console too; AllocConsole then fails with
	// access denied and the first arrangement is the only one (run 43753465542).
	arrangements := []string{"console window"}
	if c, _, _ := kernel32.NewProc("GetConsoleWindow").Call(); c == 0 {
		arrangements = []string{"no console window", "AllocConsole"}
	}
	for _, arrangement := range arrangements {
		suffix := ""
		if arrangement == "AllocConsole" {
			if ok, _, err := kernel32.NewProc("AllocConsole").Call(); ok == 0 {
				t.Logf("AllocConsole: %v", err)
				break
			}
			suffix = "-allocconsole"
		}
		r.Scenario(t, "f5-hub-stop"+suffix, "terraform", "hub", false, func(s *Scenario) {
			s.Observe("mechanism: CREATE_NEW_PROCESS_GROUP + GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT); console arrangement %q (Hclaro inherits the test process console)", arrangement)
			dirs, env, err := s.Process("terraform")
			if err != nil {
				s.Fatalf("%v", err)
			}
			h := s.StartHub(dirs, env, false)
			code, err := h.Stop(30 * time.Second)
			if err != nil {
				s.Fatalf("no shutdown path: %v", err)
			}
			s.Observe("Hclaro exited on its own with code %d", code)
		})
		r.Scenario(t, "f5-launch-apply-shutdown"+suffix, "terraform", "launch", false, func(s *Scenario) {
			project := s.Project("smoke")
			dirs, env, err := s.Process("terraform")
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.SetupInit(dirs, env, "terraform", project)
			// Declared in the scenario's allowlist: the fake engine's apply then waits 3 s per line.
			extra := map[string]string{"HCLARO_FAKE_APPLY": "slow"}
			h, err := s.StartHclaro(dirs, Environment(dirs, s.run.Cfg.CLIConfig["terraform"], extra), extra, "--path", project, "--no-browser", "--engine", "terraform")
			if err != nil {
				s.Fatalf("start hclaro: %v", err)
			}
			s.waitReady(h)
			detail := s.LaunchPlan(h, project, "terraform", []string{"smoke.tfvars"})
			a, resp := s.Approve(h, detail.ID)
			if resp.Status != 200 {
				s.Fatalf("approval: %d %s", resp.Status, resp.Code)
			}
			op, resp := s.StartApply(h, detail.ID, a.Token, a.Phrase)
			if resp.Status != 202 {
				s.Fatalf("apply: %d %s", resp.Status, resp.Code)
			}
			// The engine itself must be running: the operation is "running" from its preflight on.
			if ok, err := h.client.WaitLog("operations/"+op.ID, "Apply started", 60*time.Second); err != nil || !ok {
				s.Fatalf("fake engine never logged its start: %v", err)
			}
			if err := h.Interrupt(); err != nil {
				s.Fatalf("CTRL_BREAK to Hclaro failed: %v", err)
			}
			code, exited := h.Wait(2 * time.Minute)
			notice := stdoutHas(h, shutdownNotice)
			b, _ := os.ReadFile(h.Stdout)
			s.Artifact("hclaro.stdout", b)
			e := historyFile(s, dirs.Config, detail.ID)
			acks := excerptAcks(e)
			s.Observe("Hclaro exited %v code %d; shutdown notice %v; apply %s outcome %q exit %s; fake engine interrupt acknowledgements %d",
				exited, code, notice, e.Status, e.Outcome.Result, exitCode(e.ExitCode), acks)
			if !exited || !notice {
				s.Fatalf("no shutdown path (exited %v, notice %v)", exited, notice)
			}
			if acks != 1 {
				s.Fatalf("fake engine acknowledged %d interrupts, want 1 (0 means it was killed)", acks)
			}
		})
	}
	exit := 0
	if t.Failed() {
		exit = 1
	}
	if err := r.Finish(exit); err != nil {
		t.Fatal(err)
	}
}
