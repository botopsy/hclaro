package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Planner is the only engine surface that can run a plan. It has no apply method.
type Planner interface {
	Workspace(context.Context) (string, error)
	Plan(ctx context.Context, request PlanRequest, line func([]byte)) error
	ShowPlan(ctx context.Context, path string) ([]byte, error)
}

// PlanRequest holds validated inputs. Out is an absolute path in a private temp directory;
// VarFiles are bare file names inside the project root.
type PlanRequest struct {
	Out      string
	VarFiles []string
}

// ExitError reports a failed plan without engine output. Hint is a classification derived
// from stderr keywords; the stderr text itself is never returned.
type ExitError struct {
	Code int
	Hint string
}

func (e *ExitError) Error() string { return "engine plan failed" }

var ErrCancelled = errors.New("engine plan was cancelled or timed out")

const (
	maxLine       = 1 << 20
	gracePeriod   = 15 * time.Second
	showPlanLimit = 5 * time.Minute
)

func planArgs(request PlanRequest) ([]string, error) {
	if !filepath.IsAbs(request.Out) {
		return nil, errors.New("plan output must be an absolute private path")
	}
	args := []string{"plan", "-input=false", "-json", "-out=" + request.Out}
	for _, name := range request.VarFiles {
		if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
			return nil, errors.New("invalid variable file name")
		}
		args = append(args, "-var-file="+name)
	}
	return args, nil
}

// Plan streams machine-readable UI lines to line. Lines longer than 1 MiB are truncated,
// which makes them invalid JSON for the consumer rather than buffering without bound.
func (c *CLI) Plan(ctx context.Context, request PlanRequest, line func([]byte)) error {
	args, err := planArgs(request)
	if err != nil {
		return err
	}
	cmd, err := c.command(ctx, args...)
	if err != nil {
		return err
	}
	// Interrupt first so the engine can stop providers and release a state lock;
	// the process is killed only after the grace period.
	prepareInterrupt(cmd)
	cmd.Cancel = func() error {
		if err := interrupt(cmd.Process); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = gracePeriod
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &boundedTail{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return errors.New("engine could not be started")
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	for {
		chunk, readErr := readLine(reader)
		if len(chunk) > 0 {
			line(chunk)
		}
		if readErr != nil {
			break
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return &ExitError{Code: code, Hint: Classify(stderr.String())}
	}
	return nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if len(out) < maxLine {
			out = append(out, part[:min(len(part), maxLine-len(out))]...)
		}
		if err != nil || !isPrefix {
			return out, err
		}
	}
}

func (c *CLI) ShowPlan(ctx context.Context, path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("plan path must be absolute")
	}
	return c.run(ctx, showPlanLimit, "show", "-json", path)
}

// Classify maps engine diagnostics text to a coarse failure category without retaining it.
func Classify(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "state lock") || strings.Contains(lower, "lock info") || strings.Contains(lower, "conditionalcheckfailed"):
		return "state_locked"
	case strings.Contains(lower, "backend initialization required") || strings.Contains(lower, "module not installed") || strings.Contains(lower, "required plugins are not installed") || strings.Contains(lower, "inconsistent dependency lock file") || strings.Contains(lower, "please run \"terraform init\"") || strings.Contains(lower, "please run \"tofu init\""):
		return "init_required"
	case strings.Contains(lower, "credential") || strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "token has expired") || strings.Contains(lower, "sso") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authenticat") || strings.Contains(lower, "access denied") || strings.Contains(lower, "accessdenied") || strings.Contains(lower, "forbidden"):
		return "credentials"
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "tls handshake timeout"):
		return "network"
	}
	return ""
}

// boundedTail keeps the last max bytes written; it never fails the writer.
type boundedTail struct {
	b   []byte
	max int
}

func (t *boundedTail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = bytes.Clone(t.b[len(t.b)-t.max:])
	}
	return len(p), nil
}
func (t *boundedTail) String() string { return string(t.b) }

// Fake implements Adapter and Planner for tests and synthetic helpers. It is never
// selectable through the API or CLI flags.
type Fake struct {
	mu             sync.Mutex
	VersionValue   string
	WorkspaceValue string
	StateJSON      []byte
	Lines          [][]byte
	PlanJSON       []byte
	PlanErr        error
	ShowErr        error
	// Block makes Plan wait for cancellation, to exercise cancel paths.
	Block    bool
	Delay    time.Duration
	calls    []string
	requests []PlanRequest
}

func (m *Fake) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

// Calls returns the engine methods invoked so far.
func (m *Fake) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// Requests returns the validated plan requests received so far.
func (m *Fake) Requests() []PlanRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PlanRequest(nil), m.requests...)
}

func (m *Fake) Version(context.Context) (string, error) {
	m.record("version")
	return m.VersionValue, nil
}
func (m *Fake) State(context.Context) ([]byte, error) {
	m.record("show")
	return m.StateJSON, nil
}
func (m *Fake) Workspace(context.Context) (string, error) {
	m.record("workspace")
	return m.WorkspaceValue, nil
}
func (m *Fake) Plan(ctx context.Context, request PlanRequest, line func([]byte)) error {
	m.record("plan")
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if _, err := planArgs(request); err != nil {
		return err
	}
	for _, l := range m.Lines {
		line(l)
		if m.Delay > 0 {
			select {
			case <-time.After(m.Delay):
			case <-ctx.Done():
				return ErrCancelled
			}
		}
	}
	if m.Block {
		<-ctx.Done()
		return ErrCancelled
	}
	if m.PlanErr != nil {
		return m.PlanErr
	}
	// A real engine writes a binary artifact; the mock writes the JSON it will show.
	return os.WriteFile(request.Out, m.PlanJSON, 0o600)
}
func (m *Fake) ShowPlan(_ context.Context, path string) ([]byte, error) {
	m.record("show-plan")
	if m.ShowErr != nil {
		return nil, m.ShowErr
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}
