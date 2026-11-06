package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"
)

// graphHelper is the fake engine for graph tests; only the exact argv "graph" is accepted.
func graphHelper(args []string) {
	if len(args) != 1 {
		os.Exit(2)
	}
	switch os.Getenv("HCLARO_PLAN_MODE") {
	case "graph-ok":
		fmt.Println(`digraph G { "aws_instance.a" -> "aws_vpc.b"; }`)
		fmt.Fprintln(os.Stderr, "DO_NOT_LEAK_HCLARO_TEST_SECRET")
	case "graph-init":
		fmt.Fprintln(os.Stderr, "Error: Backend initialization required, please run \"terraform init\"\nDO_NOT_LEAK_HCLARO_TEST_SECRET")
		os.Exit(1)
	case "graph-big":
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < GraphMaxBytes>>20+2; i++ {
			fmt.Print(chunk)
		}
	case "graph-hang":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		select {
		case <-interrupted:
			os.Exit(1)
		case <-time.After(30 * time.Second):
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func TestGraphRunsOnlyGraph(t *testing.T) {
	if got := GraphArgs(); len(got) != 1 || got[0] != "graph" {
		t.Fatal(got)
	}
	c := planCLI(t, "graph-ok")
	out, err := c.Graph(context.Background())
	if err != nil || !strings.Contains(string(out), `"aws_instance.a" -> "aws_vpc.b"`) || strings.Contains(string(out), "DO_NOT_LEAK") {
		t.Fatalf("%q %v", out, err)
	}
	c = planCLI(t, "graph-init")
	_, err = c.Graph(context.Background())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Hint != "init_required" || exit.Code != 1 || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatalf("%#v", err)
	}
	c = planCLI(t, "graph-big")
	if _, err := c.Graph(context.Background()); !errors.Is(err, ErrGraphTooLarge) {
		t.Fatal(err)
	}
	c = planCLI(t, "graph-hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Graph(ctx); !errors.Is(err, ErrCancelled) || time.Since(start) > 20*time.Second {
		t.Fatal(err)
	}
	c = planCLI(t, "graph-ok")
	t.Setenv("TF_CLI_ARGS_graph", "-type=apply")
	if _, err := c.Graph(context.Background()); !errors.Is(err, ErrUnsafeArgs) {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name, version string
		want          bool
	}{{"terraform", "1.5.0", true}, {"terraform", "1.17.0", true}, {"tofu", "1.13.1", true}, {"terraform", "1.4.0", false}, {"tofu", "", false}} {
		if GraphSupport(v.name, v.version) != v.want {
			t.Errorf("%+v", v)
		}
	}
}
