package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// planHelper is the fake engine for plan tests, entered from TestHelperProcess. It verifies
// the exact argv it receives; anything unexpected exits 2.
func planHelper() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "graph" {
		graphHelper(args)
	}
	if len(args) == 3 && args[0] == "show" && args[1] == "-json" {
		b, err := os.ReadFile(args[2])
		if err != nil {
			os.Exit(4)
		}
		os.Stdout.Write(b)
		os.Exit(0)
	}
	if len(args) > 0 && args[0] == "apply" {
		if len(args) != 4 || strings.Join(args[:3], " ") != "apply -input=false -json" || !filepath.IsAbs(args[3]) {
			os.Exit(2)
		}
		b, err := os.ReadFile(args[3])
		if err != nil {
			os.Exit(4)
		}
		fmt.Printf("{\"type\":\"apply_start\",\"plan\":%q}\n", b)
		if os.Getenv("HCLARO_PLAN_MODE") == "stale" {
			fmt.Fprintln(os.Stderr, "Error: Saved plan is stale")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) < 4 || strings.Join(args[:3], " ") != "plan -input=false -json" || !strings.HasPrefix(args[3], "-out=") || !filepath.IsAbs(strings.TrimPrefix(args[3], "-out=")) {
		os.Exit(2)
	}
	for _, a := range args[4:] {
		if a != "-refresh-only" && !strings.HasPrefix(a, "-var-file=") && !strings.HasPrefix(a, "-replace=") && !strings.HasPrefix(a, "-generate-config-out=") && !strings.HasPrefix(a, "-parallelism=") && !strings.HasPrefix(a, "-lock-timeout=") {
			os.Exit(2)
		}
	}
	out := strings.TrimPrefix(args[3], "-out=")
	switch os.Getenv("HCLARO_PLAN_MODE") {
	case "ok":
		fmt.Println(`{"type":"version","@message":"Terraform 1.9.8"}`)
		fmt.Println(strings.Repeat("x", 3<<20))
		fmt.Println(`{"type":"change_summary","changes":{"add":1}}`)
		fmt.Fprintln(os.Stderr, "DO_NOT_LEAK_HCLARO_TEST_SECRET")
		if err := os.WriteFile(out, []byte(strings.Join(args[4:], ",")), 0o644); err != nil {
			os.Exit(5)
		}
	case "lock":
		fmt.Fprintln(os.Stderr, "Error acquiring the state lock\nLock Info: DO_NOT_LEAK_HCLARO_TEST_SECRET")
		os.Exit(1)
	case "hang":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		fmt.Println(`{"type":"version"}`)
		select {
		case <-interrupted:
			_ = os.WriteFile(out+".interrupted", nil, 0o600)
			os.Exit(1)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func planCLI(t *testing.T, mode string) *CLI {
	t.Helper()
	t.Setenv("HCLARO_PLAN_HELPER", "1")
	t.Setenv("HCLARO_PLAN_MODE", mode)
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_plan", "TF_CLI_ARGS_show", "TF_CLI_ARGS_apply"} {
		t.Setenv(key, "")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &CLI{Name: "terraform", Executable: exe, Root: t.TempDir()}
}

func TestPlanStreamsAndWritesArtifact(t *testing.T) {
	c := planCLI(t, "ok")
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	var got [][]byte
	err := c.Plan(context.Background(), PlanRequest{Out: out, VarFiles: []string{"prod.tfvars", "env/eu-west.tfvars"}}, func(line []byte) { got = append(got, line) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[1]) != maxLine || string(got[2]) != `{"type":"change_summary","changes":{"add":1}}` {
		t.Fatalf("lines %d / %d", len(got), len(got[1]))
	}
	written, _ := os.ReadFile(out)
	// Root-relative paths in subfolders arrive as one argv element each.
	if string(written) != "-var-file=prod.tfvars,-var-file=env/eu-west.tfvars" {
		t.Fatalf("argv %q", written)
	}
	if b, err := c.ShowPlan(context.Background(), out); err != nil || string(b) != string(written) {
		t.Fatalf("show %q %v", b, err)
	}
}

// Each selected address becomes exactly one -replace argv element, with quotes and
// brackets passed verbatim and no -target.
func TestPlanReplaceArguments(t *testing.T) {
	c := planCLI(t, "ok")
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	addresses := []string{`module.compute.aws_instance.main[0]`, `module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]`}
	if err := c.Plan(context.Background(), PlanRequest{Out: out, VarFiles: []string{"prod.tfvars"}, Replace: addresses}, func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(out)
	want := "-var-file=prod.tfvars,-replace=" + addresses[0] + ",-replace=" + addresses[1]
	if string(written) != want {
		t.Fatalf("argv %q\nwant %q", written, want)
	}
}

func TestApplyPassesOnlySavedPlan(t *testing.T) {
	c := planCLI(t, "ok")
	path := filepath.Join(t.TempDir(), "plan.tfplan")
	if err := os.WriteFile(path, []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := c.Apply(context.Background(), path, func(line []byte) { got = append(got, string(line)) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != `{"type":"apply_start","plan":"reviewed"}` {
		t.Fatalf("%q", got)
	}
	if err := c.Apply(context.Background(), "relative.tfplan", func([]byte) {}); err == nil {
		t.Fatal("relative saved plan accepted")
	}
	t.Setenv("TF_CLI_ARGS_apply", "-auto-approve")
	if err := c.Apply(context.Background(), path, func([]byte) {}); !errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("injected apply flag accepted")
	}
	t.Setenv("TF_CLI_ARGS_apply", "")
	stale := planCLI(t, "stale")
	err := stale.Apply(context.Background(), path, func([]byte) {})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Hint != "stale_plan" {
		t.Fatalf("%v", err)
	}
}

func TestPlanFailureNeverReturnsStderr(t *testing.T) {
	c := planCLI(t, "lock")
	err := c.Plan(context.Background(), PlanRequest{Out: filepath.Join(t.TempDir(), "p")}, func([]byte) {})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || exit.Hint != "state_locked" || strings.Contains(fmt.Sprintf("%v %+v", err, exit), "DO_NOT_LEAK") {
		t.Fatalf("%v %+v", err, exit)
	}
}

func TestPlanCancelInterruptsGracefully(t *testing.T) {
	c := planCLI(t, "hang")
	out := filepath.Join(t.TempDir(), "p")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- c.Plan(ctx, PlanRequest{Out: out}, func([]byte) { close(started) })
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatal(err)
		}
	case <-time.After(gracePeriod + 5*time.Second):
		t.Fatal("cancel did not stop the engine")
	}
	// On Windows the interrupt is a CTRL_BREAK to the engine's own process group.
	if _, err := os.Stat(out + ".interrupted"); err != nil {
		t.Fatal("engine was not interrupted gracefully before being killed")
	}
}

func TestPlanGenerateConfigArgument(t *testing.T) {
	c := planCLI(t, "ok")
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	if err := c.Plan(context.Background(), PlanRequest{Out: out, VarFiles: []string{"prod.tfvars"}, GenerateConfigOut: "imports_generated.tf"}, func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	if written, _ := os.ReadFile(out); string(written) != "-var-file=prod.tfvars,-generate-config-out=imports_generated.tf" {
		t.Fatalf("argv %q", written)
	}
}

func TestPlanProfileOptions(t *testing.T) {
	c := planCLI(t, "ok")
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	if err := c.Plan(context.Background(), PlanRequest{Out: out, VarFiles: []string{"env/prod.tfvars"}, Parallelism: 4, LockTimeoutSeconds: 30}, func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	if written, _ := os.ReadFile(out); string(written) != "-var-file=env/prod.tfvars,-parallelism=4,-lock-timeout=30s" {
		t.Fatalf("argv %q", written)
	}
}

// A drift scan is a refresh-only plan with the same private output and profile options; it
// never becomes an apply or a state-writing refresh.
func TestPlanRefreshOnlyArguments(t *testing.T) {
	// filepath.Abs keeps the path absolute on Windows too, where "/tmp/..." is not.
	planOut, err := filepath.Abs(filepath.Join("x", "plan.tfplan"))
	if err != nil {
		t.Fatal(err)
	}
	args, err := planArgs(PlanRequest{Out: planOut, RefreshOnly: true, VarFiles: []string{"prod.tfvars"}, LockTimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "plan -input=false -json -out="+planOut+" -refresh-only -var-file=prod.tfvars -lock-timeout=30s" {
		t.Fatalf("argv %q", got)
	}
	c := planCLI(t, "ok")
	out := filepath.Join(t.TempDir(), "plan.tfplan")
	if err := c.Plan(context.Background(), PlanRequest{Out: out, RefreshOnly: true}, func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	if written, _ := os.ReadFile(out); string(written) != "-refresh-only" {
		t.Fatalf("argv %q", written)
	}
	for _, v := range []struct {
		name, version string
		want          bool
	}{{"terraform", "1.5.0", true}, {"terraform", "1.17.2", true}, {"tofu", "1.6.0", true}, {"tofu", "1.13.0", true}, {"terraform", "1.4.6", false}, {"tofu", "1.5.0", false}, {"terraform", "1.9.0-beta1", false}, {"terraform", "", false}} {
		if got := RefreshOnlySupport(v.name, v.version); got != v.want {
			t.Errorf("%s %s: %v", v.name, v.version, got)
		}
	}
}

func TestPlanRejectsUnsafeInputs(t *testing.T) {
	c := planCLI(t, "ok")
	for _, request := range []PlanRequest{
		{Out: "relative.tfplan"},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"../x.tfvars"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"-destroy"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"a b.tfvars"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"/etc/x.tfvars"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"env/.hidden/x.tfvars"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"env/x.tf"}},
		{Out: filepath.Join(t.TempDir(), "p"), Parallelism: -1},
		{Out: filepath.Join(t.TempDir(), "p"), Parallelism: 257},
		{Out: filepath.Join(t.TempDir(), "p"), LockTimeoutSeconds: 3601},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{""}},
		{Out: filepath.Join(t.TempDir(), "p"), Replace: []string{"-destroy"}},
		{Out: filepath.Join(t.TempDir(), "p"), Replace: []string{"data.aws_caller_identity.current"}},
		{Out: filepath.Join(t.TempDir(), "p"), Replace: []string{"a.b\n-target=c"}},
		{Out: filepath.Join(t.TempDir(), "p"), Replace: []string{"a.b", "a.b"}},
		{Out: filepath.Join(t.TempDir(), "p"), Replace: []string{""}},
		{Out: filepath.Join(t.TempDir(), "p"), GenerateConfigOut: "../outside.tf"},
		{Out: filepath.Join(t.TempDir(), "p"), GenerateConfigOut: "/tmp/abs.tf"},
		{Out: filepath.Join(t.TempDir(), "p"), GenerateConfigOut: "-destroy.tf"},
		{Out: filepath.Join(t.TempDir(), "p"), GenerateConfigOut: "gen.tfvars"},
		{Out: filepath.Join(t.TempDir(), "p"), GenerateConfigOut: "gen.tf", Replace: []string{"a.b"}},
		{Out: filepath.Join(t.TempDir(), "p"), RefreshOnly: true, Replace: []string{"a.b"}},
		{Out: filepath.Join(t.TempDir(), "p"), RefreshOnly: true, GenerateConfigOut: "gen.tf"},
	} {
		if err := c.Plan(context.Background(), request, func([]byte) {}); err == nil {
			t.Fatalf("%+v accepted", request)
		}
	}
	t.Setenv("TF_CLI_ARGS_plan", "-destroy")
	if err := c.Plan(context.Background(), PlanRequest{Out: filepath.Join(t.TempDir(), "p")}, func([]byte) {}); !errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("injected plan flag accepted")
	}
	t.Setenv("TF_CLI_ARGS_plan", "-no-color")
	if err := c.Plan(context.Background(), PlanRequest{Out: filepath.Join(t.TempDir(), "p")}, func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ShowPlan(context.Background(), "relative"); err == nil {
		t.Fatal("relative show path accepted")
	}
}

func TestClassify(t *testing.T) {
	for text, want := range map[string]string{
		"Error acquiring the state lock":                      "state_locked",
		"Backend initialization required, please run init":    "init_required",
		"No valid credential sources found":                   "credentials",
		"Error: The SSO session associated with this profile": "credentials",
		"dial tcp: lookup example: no such host":              "network",
		"Error: Saved plan is stale":                          "stale_plan",
		"Error: Invalid reference":                            "",
	} {
		if got := Classify(text); got != want {
			t.Errorf("%q: %q want %q", text, got, want)
		}
	}
}
