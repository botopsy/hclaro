package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

// TestReplaceAndApplyThroughSyntheticCLI drives the real CLI adapter against the compiled
// synthetic engine (scripts/fake-engine) and checks the exact argv it received.
func TestReplaceAndApplyThroughSyntheticCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the synthetic engine")
	}
	bin := filepath.Join(t.TempDir(), "terraform")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "hclaro/scripts/fake-engine")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	spy := filepath.Join(t.TempDir(), "argv")
	t.Setenv("HCLARO_FAKE_SPY", spy)
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_plan", "TF_CLI_ARGS_apply", "TF_CLI_ARGS_show", "TF_CLI_ARGS_workspace", "HCLARO_FAKE_PLAN", "HCLARO_FAKE_APPLY"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	cli := &engine.CLI{Name: "terraform", Executable: bin, Root: root}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), cli, "terraform")
	m := NewManager(store, cli, root)
	m.TempDir = t.TempDir()
	m.Reload = func(ctx context.Context) { store.Load(ctx, cli, "terraform") }
	t.Cleanup(func() { m.Close(5 * time.Second) })

	var ids, addresses []string
	for _, r := range store.Get().Index.Resources {
		if r.Address == "aws_iam_role.deployer" || strings.Contains(r.Address, "workers") {
			ids = append(ids, r.ID)
			addresses = append(addresses, r.Address)
		}
	}
	op, err := m.StartPlan(nil, ids)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	if done.Status != Succeeded {
		t.Fatalf("%+v %+v", done, done.Diagnostic)
	}
	applied := wait(t, m, approveAndApply(t, m, done.PlanID).ID)
	if applied.Status != Succeeded || applied.Outcome != "applied" || !applied.StateReloaded {
		t.Fatalf("%+v %+v", applied, applied.Diagnostic)
	}
	b, err := os.ReadFile(spy)
	if err != nil {
		t.Fatal(err)
	}
	var plans, applies [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		args := strings.Split(line, "\x00")
		switch args[0] {
		case "plan":
			plans = append(plans, args)
		case "apply":
			applies = append(applies, args)
		}
	}
	if len(plans) != 1 || len(applies) != 1 {
		t.Fatalf("plans %q applies %q", plans, applies)
	}
	var replace []string
	for _, a := range plans[0] {
		if strings.HasPrefix(a, "-target") || strings.HasPrefix(a, "-destroy") || strings.HasPrefix(a, "-auto-approve") {
			t.Fatalf("forbidden flag %q", a)
		}
		if strings.HasPrefix(a, "-replace=") {
			replace = append(replace, strings.TrimPrefix(a, "-replace="))
		}
	}
	if strings.Join(replace, "|") != strings.Join(addresses, "|") || len(replace) != 2 {
		t.Fatalf("replace flags %q want %q", replace, addresses)
	}
	if len(applies[0]) != 4 || strings.Join(applies[0][:3], " ") != "apply -input=false -json" || !filepath.IsAbs(applies[0][3]) || !strings.HasPrefix(applies[0][3], m.TempDir) {
		t.Fatalf("apply argv %q", applies[0])
	}
	assertTempEmpty(t, m)
}

// TestDriftScanThroughSyntheticCLI runs drift scans with the real CLI adapter against the
// compiled synthetic engine, as Terraform and as OpenTofu, and checks every argv it received:
// one refresh-only plan and read-backs, never refresh, apply, import or a state command.
func TestDriftScanThroughSyntheticCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the synthetic engine")
	}
	dir := t.TempDir()
	build := func(name string) string {
		bin := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "hclaro/scripts/fake-engine")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return bin
	}
	bins := map[string]string{"terraform": build("terraform"), "tofu": build("tofu")}
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_plan", "TF_CLI_ARGS_show", "TF_CLI_ARGS_workspace", "HCLARO_FAKE_PLAN", "HCLARO_FAKE_APPLY"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		engine, mode, status, outcome string
		changed, missing              int
	}{
		{"terraform", "", ScanCompleted, OutcomeDrift, 2, 1},
		{"tofu", "", ScanCompleted, OutcomeDrift, 1, 1},
		{"terraform", "none", ScanCompleted, OutcomeNoDrift, 0, 0},
		{"tofu", "none", ScanCompleted, OutcomeNoDrift, 0, 0},
		{"terraform", "denied", ScanPartial, "", 1, 0},
		{"tofu", "sso", ScanFailed, "", 0, 0},
		{"terraform", "lock", ScanFailed, "", 0, 0},
		{"terraform", "not-refresh-only", ScanFailed, "", 0, 0},
	} {
		t.Run(tc.engine+"/"+tc.mode, func(t *testing.T) {
			spy := filepath.Join(t.TempDir(), "argv")
			t.Setenv("HCLARO_FAKE_SPY", spy)
			t.Setenv("HCLARO_FAKE_DRIFT", tc.mode)
			root := t.TempDir()
			cli := &engine.CLI{Name: tc.engine, Executable: bins[tc.engine], Root: root}
			store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
			store.Load(context.Background(), cli, tc.engine)
			m := NewManager(store, cli, root)
			m.TempDir = t.TempDir()
			t.Cleanup(func() { m.Close(5 * time.Second) })
			op, scan, err := m.StartDriftScan(DriftStart{})
			if err != nil {
				t.Fatal(err)
			}
			wait(t, m, op.ID)
			view, _ := m.DriftScan(scan.ID)
			if view.Status != tc.status || view.Outcome != tc.outcome || view.Summary.Changed != tc.changed || view.Summary.Missing != tc.missing {
				t.Fatalf("%+v %+v", view, view.Diagnostic)
			}
			assertNoCanary(t, view, m.Operations())
			assertTempEmpty(t, m)
			b, err := os.ReadFile(spy)
			if err != nil {
				t.Fatal(err)
			}
			plans := 0
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				args := strings.Split(line, "\x00")
				switch args[0] {
				case "version", "workspace", "show":
				case "plan":
					plans++
					if len(args) != 5 || args[4] != "-refresh-only" || !strings.HasPrefix(args[3], "-out=") {
						t.Fatalf("plan argv %q", args)
					}
				default:
					t.Fatalf("drift scan ran %q", args)
				}
			}
			if plans != 1 {
				t.Fatalf("%d plans", plans)
			}
		})
	}
}
