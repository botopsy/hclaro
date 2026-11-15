package qualify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// realEngineBuild is set by the realengine-tagged files; the smoke test needs the Go
// toolchain, which does not run inside the isolation.
var realEngineBuild bool

// buildBinaries compiles hclaro and the fake engine (as terraform and tofu) into a temp dir.
func buildBinaries(t *testing.T) (hclaro, terraform, tofu string) {
	t.Helper()
	dir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	hclaro, terraform, tofu = filepath.Join(dir, "hclaro"+ext), filepath.Join(dir, "fake-terraform"+ext), filepath.Join(dir, "fake-tofu"+ext)
	for out, pkg := range map[string]string{hclaro: "hclaro/cmd/hclaro", terraform: "hclaro/scripts/fake-engine"} {
		cmd := exec.Command("go", "build", "-trimpath", "-o", out, pkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", pkg, err, b)
		}
	}
	// The fake engine reports an OpenTofu version when its executable is named tofu; Process
	// copies it under that name.
	b, err := os.ReadFile(terraform)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tofu, b, 0o700); err != nil {
		t.Fatal(err)
	}
	return
}

// smokeRun returns a Run over the fake engine with empty CLI configs.
func smokeRun(t *testing.T) *Run {
	t.Helper()
	if testing.Short() || realEngineBuild {
		t.Skip("builds hclaro and the fake engine")
	}
	hclaro, terraform, tofu := buildBinaries(t)
	root := t.TempDir()
	cli := filepath.Join(root, "empty.tfrc")
	if err := os.WriteFile(cli, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	repo, _ := filepath.Abs("../..")
	r, err := NewRun(Config{Hclaro: hclaro, Engines: map[string]string{"terraform": terraform, "tofu": tofu},
		CLIConfig: map[string]string{"terraform": cli, "tofu": cli}, Out: filepath.Join(root, "out"), Root: filepath.Join(root, "run"), Repo: repo, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestSmokeFakeEngine runs the harness end to end against the fake engine: a launch-mode plan,
// a reviewed apply and a Hub registration. It is a self-test of the harness, not qualification evidence.
func TestSmokeFakeEngine(t *testing.T) {
	r := smokeRun(t)
	r.Scenario(t, "smoke-launch-plan", "terraform", "launch", false, func(s *Scenario) {
		project := s.Project("smoke")
		dirs, env, err := s.Process("terraform")
		if err != nil {
			s.Fatalf("%v", err)
		}
		s.SetupInit(dirs, env, "terraform", project)
		h := s.StartLaunch(dirs, env, "terraform", project)
		detail := s.LaunchPlan(h, project, "terraform", []string{"smoke.tfvars"})
		if len(detail.Changes) == 0 {
			s.Fatalf("fake plan has no changes")
		}
		s.Observe("fake plan: %s", detail)
	})
	r.Scenario(t, "smoke-launch-apply", "terraform", "launch", false, func(s *Scenario) {
		project := s.Project("smoke")
		dirs, env, err := s.Process("terraform")
		if err != nil {
			s.Fatalf("%v", err)
		}
		s.SetupInit(dirs, env, "terraform", project)
		h := s.StartLaunch(dirs, env, "terraform", project)
		detail := s.LaunchPlan(h, project, "terraform", []string{"smoke.tfvars"})
		approval, op := s.ApplyReviewed(h, detail.ID)
		if op.Outcome != "applied" || len(approval.Command) == 0 {
			s.Fatalf("fake apply: %s %q command %v", op.Status, op.Outcome, approval.Command)
		}
		if e := s.LastHistory(h, "apply"); e.Outcome.PlanID != detail.ID {
			s.Fatalf("history apply entry for plan %q, want %q", e.Outcome.PlanID, detail.ID)
		}
	})
	r.Scenario(t, "smoke-hub-register", "tofu", "hub", false, func(s *Scenario) {
		project := s.Project("smoke")
		dirs, env, err := s.Process("tofu")
		if err != nil {
			s.Fatalf("%v", err)
		}
		s.SetupInit(dirs, env, "tofu", project)
		h := s.StartHub(dirs, env, false)
		s.HubRegister(h, project, "tofu", "tofu")
	})
	exit := 0
	if t.Failed() {
		exit = 1
	}
	if err := r.Finish(exit); err != nil {
		t.Fatal(err)
	}
	var got RunResult
	b, err := os.ReadFile(filepath.Join(r.Cfg.Out, "run.json"))
	if err != nil || json.Unmarshal(b, &got) != nil {
		t.Fatalf("run.json: %v", err)
	}
	if got.GoTestExit != exit || got.Partial || len(got.Scenarios) != 3 {
		t.Fatalf("run.json: exit %d partial %v scenarios %d", got.GoTestExit, got.Partial, len(got.Scenarios))
	}
	for _, sc := range got.Scenarios {
		if sc.Status != Pass || sc.Canary != "absent" || sc.Cleanup.Processes != "none left" {
			t.Errorf("%s: %s (%s) canary %s cleanup %+v", sc.ID, sc.Status, sc.Reason, sc.Canary, sc.Cleanup)
		}
	}
	if testing.Verbose() {
		t.Logf("%s", b)
	}
}
