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

// CLI runs the official executable in Root. An empty SelectedWorkspace keeps the inherited CLI
// workspace selection (the launch project). A non-empty SelectedWorkspace is a Hub context: every
// child gets TF_WORKSPACE=<SelectedWorkspace> in its own environment, replacing any inherited
// value. Hclaro's process environment is never changed and `workspace select` never runs.
type CLI struct {
	Name, Executable, Root string
	SelectedWorkspace      string
	// IgnoreInheritedWorkspace drops an inherited TF_WORKSPACE even without a selection, so
	// Hub inspections never depend on how Hclaro was started.
	IgnoreInheritedWorkspace bool
}

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
	return c.commandEnv(ctx, c.SelectedWorkspace != "" || c.IgnoreInheritedWorkspace, c.SelectedWorkspace, args...)
}

// commandEnv builds the command. dropWorkspace removes an inherited TF_WORKSPACE; a
// non-empty selected is then passed as the child's TF_WORKSPACE.
func (c *CLI) commandEnv(ctx context.Context, dropWorkspace bool, selected string, args ...string) (*exec.Cmd, error) {
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
		if strings.HasPrefix(key, "TF_LOG") || key == "TF_IN_AUTOMATION" || key == "CHECKPOINT_DISABLE" || dropWorkspace && key == "TF_WORKSPACE" {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TF_LOG=OFF")
	if selected != "" {
		if !ValidWorkspace(selected) {
			return nil, errors.New("invalid workspace name")
		}
		cmd.Env = append(cmd.Env, "TF_WORKSPACE="+selected)
	}
	return cmd, nil
}

func (c *CLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	return c.runEnv(ctx, timeout, c.SelectedWorkspace != "" || c.IgnoreInheritedWorkspace, c.SelectedWorkspace, args...)
}

func (c *CLI) runEnv(ctx context.Context, timeout time.Duration, dropWorkspace bool, selected string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := c.commandEnv(ctx, dropWorkspace, selected, args...)
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

// ImportSupport reports declarative import capabilities for a supported version. Import
// blocks and -generate-config-out arrived in Terraform 1.5 and OpenTofu 1.6, the lowest
// versions Supported accepts; the check stays explicit so the range can widen safely.
func ImportSupport(name, version string) (blocks, generate bool) {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil || !Supported(name, version) {
		return false, false
	}
	minor, _ := strconv.Atoi(m[1])
	ok := name == "terraform" && minor >= 5 || name == "tofu" && minor >= 6
	return ok, ok
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

var workspaceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,90}$`)

// ValidWorkspace accepts the conservative workspace name subset Hclaro displays and passes
// to TF_WORKSPACE.
func ValidWorkspace(name string) bool {
	return workspaceName.MatchString(name) && !state.Suspicious(name)
}

func (c *CLI) Workspace(ctx context.Context) (string, error) {
	b, err := c.run(ctx, readTimeout, "workspace", "show")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if !ValidWorkspace(name) {
		return "", errors.New("engine returned an invalid workspace name")
	}
	return name, nil
}

// MaxWorkspaces bounds a workspace listing.
const MaxWorkspaces = 1000

// WorkspaceList is the parsed `workspace list` output. Current is the CLI-default workspace
// recorded in the project's data directory; Hidden counts names Hclaro does not display.
type WorkspaceList struct {
	Names   []string
	Current string
	Hidden  int
}

// ListWorkspaces runs `workspace list` without any TF_WORKSPACE, so the marked entry is the
// persisted CLI default rather than an inherited or Hub selection.
func (c *CLI) ListWorkspaces(ctx context.Context) (WorkspaceList, error) {
	b, err := c.runEnv(ctx, readTimeout, true, "", "workspace", "list")
	if err != nil {
		return WorkspaceList{}, err
	}
	return ParseWorkspaceList(b)
}

// ParseWorkspaceList reads the text format shared by Terraform and OpenTofu: one name per
// line, the selected one prefixed by "* ". Unusual names are counted, never returned.
func ParseWorkspaceList(b []byte) (WorkspaceList, error) {
	list := WorkspaceList{Names: []string{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		current := strings.HasPrefix(line, "* ")
		name := strings.TrimSpace(strings.TrimPrefix(line, "* "))
		if name == "" {
			continue
		}
		if !ValidWorkspace(name) || seen[name] {
			list.Hidden++
			continue
		}
		if len(list.Names) == MaxWorkspaces {
			return WorkspaceList{}, errors.New("too many workspaces")
		}
		seen[name] = true
		list.Names = append(list.Names, name)
		if current {
			list.Current = name
		}
	}
	if len(list.Names) == 0 {
		return WorkspaceList{}, errors.New("engine returned no workspaces")
	}
	return list, nil
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
