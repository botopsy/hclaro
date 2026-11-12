package engine

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"hclaro/internal/bounded"
)

// Grapher runs `graph` without arguments and nothing else. It is a separate interface so the
// graph operation cannot plan or apply through it.
type Grapher interface {
	Graph(ctx context.Context) ([]byte, error)
}

// GraphMaxBytes bounds the DOT output read from the engine.
const GraphMaxBytes = 16 << 20

const graphLimit = 3 * time.Minute

// ErrGraphTooLarge is returned when the engine wrote more than GraphMaxBytes.
var ErrGraphTooLarge = errors.New("graph output exceeds the size limit")

// GraphSupport reports whether `graph` is available with documented output. Every supported
// version has it; Terraform 1.7 and later print a resource-only graph by default, earlier
// Terraform and OpenTofu print the full walk graph. Both are parsed.
func GraphSupport(name, version string) bool { return Supported(name, version) }

// GraphArgs is the only argv the graph operation runs. No -plan, -type or -draw-cycles flag
// is passed, so no saved plan or user-chosen path ever reaches the engine.
func GraphArgs() []string { return []string{"graph"} }

// Graph runs `graph` in the project root with the shared subprocess policy and returns its
// stdout. Stderr is classified, never returned.
func (c *CLI) Graph(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, graphLimit)
	defer cancel()
	cmd, err := c.command(ctx, GraphArgs()...)
	if err != nil {
		return nil, err
	}
	prepareInterrupt(cmd)
	cmd.Cancel = func() error {
		if err := interrupt(cmd.Process); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = gracePeriod
	stdout := &bounded.Buffer{Limit: GraphMaxBytes}
	stderr := &boundedTail{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, ErrCancelled
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return nil, &ExitError{Code: code, Hint: Classify(stderr.String())}
	}
	if stdout.Overflow {
		return nil, ErrGraphTooLarge
	}
	return stdout.Bytes(), nil
}

// Graph returns GraphDOT, GraphErr, or waits for cancellation with BlockGraph.
func (m *Fake) Graph(ctx context.Context) ([]byte, error) {
	m.record("graph")
	if m.BlockGraph {
		<-ctx.Done()
		return nil, ErrCancelled
	}
	if m.GraphErr != nil {
		return nil, m.GraphErr
	}
	return m.GraphDOT, nil
}
