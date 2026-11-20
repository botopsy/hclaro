package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"hclaro/internal/proc"
)

// Initer runs the standard, non-interactive init and nothing else: there is no way to pass
// -upgrade, -migrate-state, -force-copy, -reconfigure, -backend-config, -lock or -backend.
// It is a separate interface so read-only and plan-only adapters cannot initialize.
type Initer interface {
	Version(context.Context) (string, error)
	Init(ctx context.Context, request InitRequest, line func(stream string, b []byte)) error
}

// InitRequest selects the output format. JSON is used only where tfpolicy.Supports reports InitJSON.
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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	exited, err := startGroup(ctx, cmd, gracePeriod)
	if err != nil {
		return errors.New("engine could not be started")
	}
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
	exited()
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

// startGroup starts cmd in its own process group, so a terminal Ctrl+C aimed at Hclaro does
// not reach the engine: cancellation sends the one interrupt, to the engine and the plugins it
// started, and the group is killed only if it is still running after grace. Call the returned
// func once Wait has returned.
func startGroup(ctx context.Context, cmd *exec.Cmd, grace time.Duration) (func(), error) {
	proc.PrepareGroup(cmd)
	cmd.Cancel = func() error {
		if err := proc.InterruptGroup(cmd.Process); err != nil {
			return proc.KillGroup(cmd.Process)
		}
		return nil
	}
	cmd.WaitDelay = grace + 5*time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-exited:
			case <-time.After(grace):
				_ = proc.KillGroup(cmd.Process)
			}
		case <-exited:
		}
	}()
	return func() { close(exited) }, nil
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
	shared := Classify(text)
	switch {
	case containsAny(lower, "-migrate-state", "-reconfigure", "backend configuration changed", "backend type changed", "do you want to copy existing state", "do you want to migrate", "migrate all workspaces", "state migration"):
		return "backend_migration"
	case containsAny(lower, "input is disabled", "input was disabled", "interactive input", "no value for required variable"):
		return "input_required"
	case shared == "state_locked":
		return "state_locked"
	case containsAny(lower, "inconsistent dependency lock file", "locked provider", "does not match configured version constraint", "-upgrade"):
		return "lockfile_upgrade"
	case containsAny(lower, "unsupported terraform core version", "unsupported opentofu version", "unsupported tofu version", "required_version"):
		return "version_mismatch"
	case containsAny(lower, "failed to query available provider packages", "failed to install provider", "could not retrieve the list of available versions", "registry.terraform.io", "registry.opentofu.org", "provider registry", "failed to download module", "module not found", "failed to retrieve available versions"):
		return "registry"
	}
	switch shared {
	case "credentials", "network":
		return shared
	}
	if containsAny(lower, "error configuring the backend", "invalid backend configuration", "unsupported backend type", "error loading backend config", "backend initialization", "required field is not set", "error configuring s3 backend", "failed to get existing workspaces") {
		return "backend_config"
	}
	return ""
}
