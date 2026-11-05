package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// planHelper is the fake engine for plan tests, entered from TestHelperProcess. It verifies
// the exact argv it receives; anything unexpected exits 2.
func planHelper() {
	args := os.Args[1:]
	if len(args) == 3 && args[0] == "show" && args[1] == "-json" {
		b, err := os.ReadFile(args[2])
		if err != nil {
			os.Exit(4)
		}
		os.Stdout.Write(b)
		os.Exit(0)
	}
	if len(args) < 4 || strings.Join(args[:3], " ") != "plan -input=false -json" || !strings.HasPrefix(args[3], "-out=") || !filepath.IsAbs(strings.TrimPrefix(args[3], "-out=")) {
		os.Exit(2)
	}
	for _, a := range args[4:] {
		if !strings.HasPrefix(a, "-var-file=") {
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
	for _, key := range []string{"TF_CLI_ARGS", "TF_CLI_ARGS_plan", "TF_CLI_ARGS_show"} {
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
	err := c.Plan(context.Background(), PlanRequest{Out: out, VarFiles: []string{"prod.tfvars", "a b.tfvars"}}, func(line []byte) { got = append(got, line) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[1]) != maxLine || string(got[2]) != `{"type":"change_summary","changes":{"add":1}}` {
		t.Fatalf("lines %d / %d", len(got), len(got[1]))
	}
	written, _ := os.ReadFile(out)
	// Names with spaces arrive as one argv element: no shell splitting is involved.
	if string(written) != "-var-file=prod.tfvars,-var-file=a b.tfvars" {
		t.Fatalf("argv %q", written)
	}
	if b, err := c.ShowPlan(context.Background(), out); err != nil || string(b) != string(written) {
		t.Fatalf("show %q %v", b, err)
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
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(out + ".interrupted"); err != nil {
			t.Fatal("engine was not interrupted gracefully before being killed")
		}
	}
}

func TestPlanRejectsUnsafeInputs(t *testing.T) {
	c := planCLI(t, "ok")
	for _, request := range []PlanRequest{
		{Out: "relative.tfplan"},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"../x.tfvars"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{"-destroy"}},
		{Out: filepath.Join(t.TempDir(), "p"), VarFiles: []string{""}},
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
		"Error: Invalid reference":                            "",
	} {
		if got := Classify(text); got != want {
			t.Errorf("%q: %q want %q", text, got, want)
		}
	}
}
