package qualify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The M27.02 deliberate faults, kept as permanent tests: each runs the harness in a child test
// process and asserts that the child exits non-zero and its run JSON records FAIL with the
// expected reason. (The leaking-build fault is simulated by a request whose response echoes
// the canary; a real leaking Hclaro build is a scratch-branch check, see the README.)
func TestHarnessReportsFaults(t *testing.T) {
	if testing.Short() || realEngineBuild {
		t.Skip("builds hclaro and the fake engine")
	}
	hclaro, terraform, tofu := buildBinaries(t)
	cases := map[string]string{
		"panic":        "panic: deliberate fault",
		"wrong-engine": "engine mismatch",
		"s3-backend":   `fixture_scope: main.tf: backend "s3"`,
		"canary-leak":  "canary found in responses",
		"credential":   "environment: credential-like variables [AWS_SECRET_ACCESS_KEY]",
		// A failed assertion stays FAIL when a later precondition gives NOT_TESTED.
		"fail-then-not-tested": "deliberate assertion; then not tested: late precondition",
	}
	for fault, reason := range cases {
		t.Run(fault, func(t *testing.T) {
			out := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestFaultChild$", "-test.v", "-test.count=1")
			cmd.Env = []string{"QUAL_FAULT=" + fault, "QUAL_FAULT_OUT=" + out, "QUAL_FAULT_BIN=" + strings.Join([]string{hclaro, terraform, tofu}, string(os.PathListSeparator)),
				"PATH=" + os.Getenv("PATH")}
			for _, k := range []string{"SystemRoot", "TMP", "TEMP", "TMPDIR", "HOME", "USERPROFILE", "ComSpec", "PATHEXT"} {
				if v, ok := os.LookupEnv(k); ok {
					cmd.Env = append(cmd.Env, k+"="+v)
				}
			}
			if fault == "credential" {
				cmd.Env = append(cmd.Env, "AWS_SECRET_ACCESS_KEY=synthetic-not-a-secret")
			}
			b, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("child exited 0; want non-zero\n%s", b)
			}
			var got RunResult
			raw, rerr := os.ReadFile(filepath.Join(out, "run.json"))
			if rerr != nil || json.Unmarshal(raw, &got) != nil {
				t.Fatalf("run.json: %v\n%s", rerr, b)
			}
			if got.GoTestExit == 0 {
				t.Errorf("goTestExit 0 with a failed child")
			}
			found := false
			for _, s := range got.Scenarios {
				if s.Status == Fail && strings.Contains(s.Reason, reason) {
					found = true
				}
			}
			if !found {
				t.Errorf("no FAIL with %q in %s", reason, raw)
			}
		})
	}
}

// TestFaultChild is the child side of TestHarnessReportsFaults; it does nothing unless
// QUAL_FAULT is set.
func TestFaultChild(t *testing.T) {
	fault := os.Getenv("QUAL_FAULT")
	if fault == "" {
		t.Skip("child of TestHarnessReportsFaults")
	}
	out := os.Getenv("QUAL_FAULT_OUT")
	if err := Preflight(os.Environ()); err != nil {
		if err := WriteAborted(out, err.Error(), ""); err != nil {
			t.Fatal(err)
		}
		t.Fatal(err)
	}
	bins := filepath.SplitList(os.Getenv("QUAL_FAULT_BIN"))
	root := t.TempDir()
	cli := filepath.Join(root, "empty.tfrc")
	if err := os.WriteFile(cli, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewRun(Config{Hclaro: bins[0], Engines: map[string]string{"terraform": bins[1], "tofu": bins[2]},
		CLIConfig: map[string]string{"terraform": cli, "tofu": cli}, Out: out, Root: filepath.Join(root, "run")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		exit := 0
		if t.Failed() {
			exit = 1
		}
		if err := r.Finish(exit); err != nil {
			t.Error(err)
		}
	}()
	r.Scenario(t, fault, "tofu", "hub", false, func(s *Scenario) {
		project := s.Project("smoke")
		switch fault {
		case "panic":
			panic("deliberate fault")
		case "fail-then-not-tested":
			s.Errorf("deliberate assertion")
			s.NotTested("late precondition")
		case "s3-backend":
			f, err := os.OpenFile(filepath.Join(project, "main.tf"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				s.Fatalf("%v", err)
			}
			_, _ = f.WriteString("\nterraform {\n  backend \"s3\" {}\n}\n")
			f.Close()
			s.Guard(project)
			return
		}
		dirs, env, err := s.Process("terraform", "tofu")
		if err != nil {
			s.Fatalf("%v", err)
		}
		h := s.StartHub(dirs, env, false)
		switch fault {
		case "wrong-engine":
			// Registered as terraform, but the scenario expects tofu.
			s.HubRegister(h, project, "terraform", "tofu")
		case "canary-leak":
			// The alias is echoed in the response body, standing in for a leaking build.
			if _, err := h.Client().Do("POST", "projects", map[string]string{"path": project, "alias": s.run.Canary, "engine": "tofu"}, nil); err != nil {
				s.Fatalf("%v", err)
			}
		}
	})
}
