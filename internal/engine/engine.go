// Package engine exposes only the official read-only command subset.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/state"
)

type Adapter interface {
	Version(context.Context) (string, error)
	Workspace(context.Context) (string, error)
	State(context.Context) ([]byte, error)
}

type CLI struct{ Name, Executable, Root string }

var ErrUnsafeArgs = errors.New("inherited TF_CLI_ARGS contains unsupported arguments; only -no-color is accepted")

const readTimeout = 45 * time.Second

// Detect resolves an executable once. No executable path is accepted from HTTP.
func Detect(root, preferred string) (*CLI, error) {
	names := []string{"terraform", "tofu"}
	if preferred != "" {
		if preferred != "terraform" && preferred != "tofu" {
			return nil, errors.New("engine must be terraform or tofu")
		}
		names = []string{preferred}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return &CLI{Name: name, Executable: path, Root: root}, nil
		}
	}
	return nil, errors.New("install Terraform or OpenTofu on PATH, or select --engine")
}

type boundedBuffer struct {
	b   []byte
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(b.b)+len(p) > b.max {
		return 0, errors.New("engine output limit exceeded")
	}
	b.b = append(b.b, p...)
	return len(p), nil
}

// command applies the shared subprocess policy: argv only, the project root as working
// directory, no inherited logging and only -no-color from TF_CLI_ARGS.
func (c *CLI) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, c.Executable, args...)
	cmd.Dir = c.Root
	cmd.WaitDelay = time.Second
	// Environment values and CLI diagnostics can contain secrets. Inherited
	// logging is disabled so Terraform cannot persist raw state via TF_LOG_PATH.
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		if key == "TF_CLI_ARGS" || key == "TF_CLI_ARGS_"+args[0] {
			for _, arg := range strings.Fields(value) {
				if arg != "-no-color" {
					return nil, ErrUnsafeArgs
				}
			}
		}
		if strings.HasPrefix(key, "TF_LOG") || key == "TF_IN_AUTOMATION" || key == "CHECKPOINT_DISABLE" {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TF_LOG=OFF")
	return cmd, nil
}

func (c *CLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := c.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	buf := &boundedBuffer{max: state.MaxBytes}
	cmd.Stdout = buf
	// Never return raw stderr: even diagnostics from read-only commands can contain secrets.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("engine read timed out or was cancelled")
		}
		return nil, errors.New("engine read failed; check initialization, credentials, VPN and provider schema compatibility using the CLI")
	}
	return buf.b, nil
}

var versionPattern = regexp.MustCompile(`^1\.(\d+)\.(\d+)$`)

func Supported(name, version string) bool {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	minor, _ := strconv.Atoi(m[1])
	// Conservative compatibility range, exercised with official-format synthetic output.
	return name == "terraform" && minor >= 5 && minor <= 17 || name == "tofu" && minor >= 6 && minor <= 13
}

func (c *CLI) Version(ctx context.Context) (string, error) {
	b, err := c.run(ctx, readTimeout, "version", "-json")
	if err != nil {
		return "", err
	}
	var v struct {
		Terraform string `json:"terraform_version"`
		Tofu      string `json:"tofu_version"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", errors.New("engine version JSON is unsupported")
	}
	if c.Name == "tofu" && v.Tofu != "" {
		v.Terraform = v.Tofu
	}
	if !Supported(c.Name, v.Terraform) {
		return "", errors.New("unsupported engine version; use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13")
	}
	return v.Terraform, nil
}
func (c *CLI) Workspace(ctx context.Context) (string, error) {
	b, err := c.run(ctx, readTimeout, "workspace", "show")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if len(name) == 0 || len(name) > 90 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(name) || state.Suspicious(name) {
		return "", errors.New("engine returned an invalid workspace name")
	}
	return name, nil
}
func (c *CLI) State(ctx context.Context) ([]byte, error) {
	return c.run(ctx, readTimeout, "show", "-json")
}

// Mock is intentionally injectable by tests, never selectable through the public API.
type Mock struct {
	VersionValue, WorkspaceValue string
	JSON                         []byte
	Err                          error
	Calls                        []string
}

func (m *Mock) Version(context.Context) (string, error) {
	m.Calls = append(m.Calls, "version")
	return m.VersionValue, m.Err
}
func (m *Mock) Workspace(context.Context) (string, error) {
	m.Calls = append(m.Calls, "workspace")
	return m.WorkspaceValue, m.Err
}
func (m *Mock) State(context.Context) ([]byte, error) {
	m.Calls = append(m.Calls, "show")
	return m.JSON, m.Err
}
