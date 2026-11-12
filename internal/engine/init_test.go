package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"testing"
	"time"
)

// initHelper is the fake executable for init, entered from TestHelperProcess. It prints the
// argv and selected environment so the adapter's policy can be asserted.
func initHelper() {
	args := strings.Join(os.Args[1:], " ")
	if args == "version -json" {
		fmt.Println(`{"terraform_version":"1.9.8"}`)
		os.Exit(0)
	}
	ws, ok := os.LookupEnv("TF_WORKSPACE")
	if !ok {
		ws = "<unset>"
	}
	fmt.Printf("argv=%s\n", args)
	fmt.Printf("workspace=%s pinned=%s log=%s\n", ws, os.Getenv("HCLARO_PINNED"), os.Getenv("TF_LOG"))
	fmt.Fprintln(os.Stderr, "Error: Backend configuration changed; use -migrate-state")
	if os.Getenv("HCLARO_INIT_BLOCK") == "1" {
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		select {
		case <-interrupted:
			os.Exit(130)
		case <-time.After(30 * time.Second):
		}
	}
	os.Exit(1)
}

func initCLI(t *testing.T, extra ...string) *CLI {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The environment is pinned: nothing from the test process reaches the child.
	environ := append([]string{"HCLARO_INIT_HELPER=1", "HCLARO_PINNED=yes", "TF_WORKSPACE=inherited", "TF_LOG=TRACE"}, extra...)
	return &CLI{Name: "terraform", Executable: exe, Root: t.TempDir(), IgnoreInheritedWorkspace: true, Environ: environ}
}

func TestInitArgsAreFixed(t *testing.T) {
	if !slices.Equal(InitArgs(false), []string{"init", "-input=false", "-no-color"}) || !slices.Equal(InitArgs(true), []string{"init", "-input=false", "-json"}) {
		t.Fatalf("%v %v", InitArgs(false), InitArgs(true))
	}
	for _, json := range []bool{false, true} {
		for _, flag := range []string{"-upgrade", "-migrate-state", "-force-copy", "-reconfigure", "-lock=false", "-backend=false", "-backend-config"} {
			for _, arg := range InitArgs(json) {
				if strings.HasPrefix(arg, flag) {
					t.Fatalf("%s in %v", flag, InitArgs(json))
				}
			}
		}
	}
	for _, c := range []struct {
		name, version string
		json          bool
	}{{"terraform", "1.9.0", true}, {"terraform", "1.17.2", true}, {"terraform", "1.8.5", false}, {"tofu", "1.10.0", false}, {"terraform", "1.99.0", false}, {"terraform", "2.0.0", false}} {
		if InitJSON(c.name, c.version) != c.json {
			t.Errorf("InitJSON(%s, %s) != %v", c.name, c.version, c.json)
		}
	}
}

func TestInitStreamsPinnedEnvironmentWithoutWorkspace(t *testing.T) {
	c := initCLI(t)
	var out []string
	err := c.Init(context.Background(), InitRequest{}, func(stream string, b []byte) { out = append(out, stream+": "+string(b)) })
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || exit.Hint != "backend_migration" {
		t.Fatalf("%v", err)
	}
	text := strings.Join(out, "\n")
	for _, want := range []string{"stdout: argv=init -input=false -no-color", "workspace=<unset> pinned=yes log=OFF", "stderr: Error: Backend configuration changed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestInitRefusesHiddenFlags(t *testing.T) {
	for _, env := range []string{"TF_CLI_ARGS=-upgrade", "TF_CLI_ARGS_init=-reconfigure", "TF_CLI_ARGS_init=-no-color -migrate-state"} {
		c := initCLI(t, env)
		ran := false
		if err := c.Init(context.Background(), InitRequest{}, func(string, []byte) { ran = true }); !errors.Is(err, ErrUnsafeArgs) || ran {
			t.Fatalf("%s: %v ran=%v", env, err, ran)
		}
	}
	c := initCLI(t, "TF_CLI_ARGS_init=-no-color")
	if err := c.Init(context.Background(), InitRequest{}, func(string, []byte) {}); errors.Is(err, ErrUnsafeArgs) {
		t.Fatal("-no-color alone is accepted")
	}
}

func TestInitCancelInterruptsTheEngine(t *testing.T) {
	c := initCLI(t, "HCLARO_INIT_BLOCK=1")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Init(ctx, InitRequest{}, func(stream string, b []byte) {
			if stream == "stderr" {
				started <- struct{}{}
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not start")
	}
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("%v", err)
		}
		if time.Since(begin) > gracePeriod {
			t.Fatal("the engine was not interrupted; it was killed after the grace period")
		}
	case <-time.After(gracePeriod + 10*time.Second):
		t.Fatal("cancel did not stop the engine")
	}
}

func TestClassifyInit(t *testing.T) {
	for text, want := range map[string]string{
		`Backend configuration changed. Use "init -migrate-state"`:            "backend_migration",
		"If you wish to store the current configuration, use -reconfigure":    "backend_migration",
		"No value for required variable; input is disabled":                   "input_required",
		"Error acquiring the state lock":                                      "state_locked",
		"Inconsistent dependency lock file":                                   "lockfile_upgrade",
		"Unsupported Terraform Core version":                                  "version_mismatch",
		"Failed to query available provider packages":                         "registry",
		"could not connect to registry.terraform.io: no such host":            "registry",
		"No valid credential sources found":                                   "credentials",
		"ExpiredToken: the security token included in the request is expired": "credentials",
		"dial tcp 10.0.0.1:443: i/o timeout":                                  "network",
		"Invalid backend configuration":                                       "backend_config",
		"Something else entirely":                                             "",
	} {
		if got := ClassifyInit(text); got != want {
			t.Errorf("ClassifyInit(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestClassifyInitLockReleaseAndAssociation(t *testing.T) {
	for text, want := range map[string]string{
		"Error acquiring the state lock\nLock Info:":                  "state_locked",
		"Error releasing the state lock\nLock Info:":                  "",
		"Releasing state lock. This may take a few moments...\nError": "",
		"Error: aws_route_table_association.a: InvalidRouteTableID":    "",
	} {
		if got := ClassifyInit(text); got != want {
			t.Errorf("%q: %q want %q", text, got, want)
		}
	}
}
