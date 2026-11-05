package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Initer runs the standard, non-interactive init and nothing else: there is no way to pass
// -upgrade, -migrate-state, -force-copy, -reconfigure, -backend-config, -lock or -backend.
// It is a separate interface so read-only and plan-only adapters cannot initialize.
type Initer interface {
	Version(context.Context) (string, error)
	Init(ctx context.Context, request InitRequest, line func(stream string, b []byte)) error
}

// InitRequest selects the output format. JSON is used only where InitJSON reports support.
type InitRequest struct {
	JSON bool
}

// InitArgs is the complete argv after the executable. Nothing from a request is appended.
func InitArgs(json bool) []string {
	if json {
		return []string{"init", "-input=false", "-json"}
	}
	return []string{"init", "-input=false", "-no-color"}
}

// InitJSON reports whether machine-readable init output is used. Terraform added
// `init -json` in 1.9; OpenTofu is kept on human output because its support has not been
// verified here.
func InitJSON(name, version string) bool {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil || !Supported(name, version) {
		return false
	}
	minor, _ := strconv.Atoi(m[1])
	return name == "terraform" && minor >= 9
}

// initLimit bounds one init; provider downloads can be slow, prompts never happen.
const initLimit = 30 * time.Minute

// Init runs init in Root and passes each output line to line with its stream ("stdout" or
// "stderr"); see streamLines.
func (c *CLI) Init(ctx context.Context, request InitRequest, line func(stream string, b []byte)) error {
	return c.streamLines(ctx, initLimit, c.SelectedWorkspace != "" || c.IgnoreInheritedWorkspace, c.SelectedWorkspace, InitArgs(request.JSON), ClassifyInit, line)
}

// streamLines runs one long-running command and passes each output line to line with its stream
// ("stdout" or "stderr"). The callback is never called concurrently. The engine runs in its
// own process group: cancellation interrupts the group, then kills it after the grace
// period. A non-zero exit becomes an *ExitError whose hint comes from classify, applied to a
// bounded tail of the output that is never returned.
func (c *CLI) streamLines(ctx context.Context, limit time.Duration, dropWorkspace bool, selected string, args []string, classify func(string) string, line func(stream string, b []byte)) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd, err := c.commandEnv(ctx, dropWorkspace, selected, args...)
	if err != nil {
		return err
	}
	prepareGroup(cmd)
	cmd.Cancel = func() error {
		if err := interruptGroup(cmd.Process); err != nil {
			return killGroup(cmd.Process)
		}
		return nil
	}
	cmd.WaitDelay = gracePeriod + 5*time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("engine could not be started")
	}
	exited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-exited:
			case <-time.After(gracePeriod):
				_ = killGroup(cmd.Process)
			}
		case <-exited:
		}
	}()
	tail := &boundedTail{max: 64 << 10}
	var mu sync.Mutex
	var readers sync.WaitGroup
	read := func(name string, r io.Reader) {
		defer readers.Done()
		reader := bufio.NewReaderSize(r, 64<<10)
		for {
			chunk, readErr := readLine(reader)
			if len(chunk) > 0 {
				mu.Lock()
				_, _ = tail.Write(append(chunk, '\n'))
				line(name, chunk)
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}
	readers.Add(2)
	go read("stdout", stdout)
	go read("stderr", stderr)
	readers.Wait()
	err = cmd.Wait()
	close(exited)
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return &ExitError{Code: code, Hint: classify(tail.String())}
	}
	return nil
}

func containsAny(text string, parts ...string) bool {
	for _, p := range parts {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// ClassifyInit maps init diagnostics text to a failure category without retaining it. The
// order matters: the most specific remediation wins.
func ClassifyInit(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "-migrate-state", "-reconfigure", "backend configuration changed", "backend type changed", "do you want to copy existing state", "do you want to migrate", "migrate all workspaces", "state migration"):
		return "backend_migration"
	case containsAny(lower, "input is disabled", "input was disabled", "interactive input", "no value for required variable"):
		return "input_required"
	case containsAny(lower, "error acquiring the state lock", "state lock", "lock info"):
		return "state_locked"
	case containsAny(lower, "inconsistent dependency lock file", "locked provider", "does not match configured version constraint", "-upgrade"):
		return "lockfile_upgrade"
	case containsAny(lower, "unsupported terraform core version", "unsupported opentofu version", "unsupported tofu version", "required_version"):
		return "version_mismatch"
	case containsAny(lower, "failed to query available provider packages", "failed to install provider", "could not retrieve the list of available versions", "registry.terraform.io", "registry.opentofu.org", "provider registry", "failed to download module", "module not found", "failed to retrieve available versions"):
		return "registry"
	}
	switch code := Classify(text); code {
	case "credentials", "network":
		return code
	}
	if containsAny(lower, "error configuring the backend", "invalid backend configuration", "unsupported backend type", "error loading backend config", "backend initialization", "required field is not set", "error configuring s3 backend", "failed to get existing workspaces") {
		return "backend_config"
	}
	return ""
}

// Init emulates init for tests. It writes nothing unless InitDataDir is set, in which case
// that directory is created on success, like the engine's data directory.
func (m *Fake) Init(ctx context.Context, request InitRequest, line func(stream string, b []byte)) error {
	m.record("init")
	m.mu.Lock()
	m.inits = append(m.inits, InitArgs(request.JSON))
	m.mu.Unlock()
	for _, l := range m.InitLines {
		line("stdout", l)
	}
	for _, l := range m.InitStderr {
		line("stderr", l)
	}
	if m.BlockInit {
		<-ctx.Done()
		return ErrCancelled
	}
	if m.InitErr != nil {
		return m.InitErr
	}
	if m.InitDataDir != "" {
		return mkdirAll(m.InitDataDir)
	}
	return nil
}

// InitCalls returns the argv of each Init call.
func (m *Fake) InitCalls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]string(nil), m.inits...)
}
