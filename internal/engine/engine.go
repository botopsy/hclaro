// Package engine exposes only the official read-only command subset.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
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
	// Environ, when non-nil, replaces Hclaro's environment as the base for every child, so an
	// operation runs with the environment pinned when it was reviewed.
	Environ []string
}

// WithEnvironment returns a copy whose children start from environ instead of Hclaro's
// environment and, when workspace is set, get TF_WORKSPACE=<workspace> in place of any
// inherited value. An execution profile binds one operation this way; c is not changed.
func (c *CLI) WithEnvironment(environ []string, workspace string) *CLI {
	copy := *c
	copy.Environ = append([]string(nil), environ...)
	if workspace != "" {
		copy.SelectedWorkspace = workspace
	}
	return &copy
}

var ErrUnsafeArgs = errors.New("inherited TF_CLI_ARGS contains unsupported arguments; only -no-color is accepted")

// Read failures the CLI's stderr was classified as; the stderr text itself is never kept.
var (
	ErrReadCredentials  = errors.New("engine read failed: credentials were rejected or have expired")
	ErrReadNetwork      = errors.New("engine read failed: the backend or a provider could not be reached")
	ErrReadInitRequired = errors.New("engine read failed: initialization is required")
	ErrReadTooLarge     = errors.New("engine read failed: the output exceeds the size limit")
)

// readLimit bounds what one engine read (state, saved plan JSON) may write. A variable only
// so tests can shorten it.
var readLimit = state.MaxBytes

// ReadAdvice explains a classified read failure to the user, or returns fallback.
func ReadAdvice(err error, fallback string) string {
	switch {
	case errors.Is(err, ErrReadCredentials):
		return "The CLI reported rejected or expired credentials. Sign in again in a terminal (for example aws sso login), then try again. Nothing was changed."
	case errors.Is(err, ErrReadNetwork):
		return "The CLI could not reach the backend or a provider. Check your network, VPN or proxy, then try again. Nothing was changed."
	case errors.Is(err, ErrReadTooLarge):
		return fmt.Sprintf("The CLI's JSON output is larger than the %d MiB Hclaro reads, so none of it was used. Nothing was changed.", readLimit>>20)
	case errors.Is(err, ErrReadInitRequired):
		return "The CLI says this folder needs init (backend, modules or providers are not installed). Run init from the project's Hub page or in a terminal, then try again. Nothing was initialized automatically."
	}
	return fallback
}

// flagName matches a bare flag name; anything after "=" is a value and is never reported.
var flagName = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9_-]{0,40}$`)

// unsafeArgsVar reports whether the variable key is read for one of commands, each a
// subcommand as typed ("workspace select"). The engines read TF_CLI_ARGS and
// TF_CLI_ARGS_<subcommand> with words joined by "_" and dashes turned into "_"
// (TF_CLI_ARGS_workspace_select); every leading word group counts, so TF_CLI_ARGS_workspace
// does too. No commands means any TF_CLI_ARGS*. Names compare without case, as Windows does.
func unsafeArgsVar(key string, commands []string) bool {
	upper := strings.ToUpper(key)
	if upper == "TF_CLI_ARGS" || len(commands) == 0 && strings.HasPrefix(upper, "TF_CLI_ARGS") {
		return true
	}
	for _, c := range commands {
		name := "TF_CLI_ARGS"
		for _, word := range strings.Fields(c) {
			name += "_" + strings.ToUpper(strings.ReplaceAll(word, "-", "_"))
			if upper == name {
				return true
			}
		}
	}
	return false
}

// UnsafeArgs reports an inherited TF_CLI_ARGS variable read for one of commands (see
// unsafeArgsVar) that adds anything other than -no-color.
func UnsafeArgs(environ []string, commands ...string) bool {
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if unsafeArgsVar(key, commands) && slices.ContainsFunc(strings.Fields(value), func(arg string) bool { return arg != "-no-color" }) {
			return true
		}
	}
	return false
}

// UnsafeArgsHelp explains which inherited TF_CLI_ARGS variables block the commands (see
// unsafeArgsVar) and how to remove them. It names variables and flag names only, never values;
// arguments that are not a plain flag are counted, not shown. It returns "" when none block.
// shortcut: a secret passed space-separated after a flag and itself shaped like "-name" would
// be named; such values are not expected from TF_CLI_ARGS.
func UnsafeArgsHelp(environ []string, commands ...string) string {
	found, unset := []string{}, []string{}
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if !unsafeArgsVar(key, commands) {
			continue
		}
		names, other := []string{}, 0
		for _, arg := range strings.Fields(value) {
			name, _, _ := strings.Cut(arg, "=")
			switch {
			case arg == "-no-color":
			case flagName.MatchString(name):
				names = append(names, name)
			default:
				other++
			}
		}
		if other > 0 {
			names = append(names, fmt.Sprintf("%d other argument(s)", other))
		}
		if len(names) > 0 {
			found = append(found, key+" adds "+strings.Join(names, ", "))
			unset = append(unset, key)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "Found: " + strings.Join(found, "; ") + ". Hidden flags such as -target, -replace, -lock=false or -auto-approve would change what runs without appearing in the reviewed command, so only -no-color is accepted. Unset them in the shell or environment that starts Hclaro (for example `unset " + strings.Join(unset, " ") + "`) and restart Hclaro. An execution profile cannot override inherited TF_CLI_ARGS settings, and Hclaro never removes them for you."
}

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

// boundedBuffer fails the command once its output passes max (unlike bounded.Buffer, which
// only flags overflow): a state larger than the limit is refused, never read in part.
type boundedBuffer struct {
	b        []byte
	max      int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(b.b)+len(p) > b.max {
		b.overflow = true
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
	environ := c.Environ
	if environ == nil {
		environ = os.Environ()
	}
	// The subcommand is every argument before the first flag; extra trailing words such as a
	// workspace name only make the check stricter.
	words := args
	if i := slices.IndexFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "-") }); i >= 0 {
		words = args[:i]
	}
	if UnsafeArgs(environ, strings.Join(words, " ")) {
		return nil, ErrUnsafeArgs
	}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(key), "TF_LOG") || key == "TF_IN_AUTOMATION" || key == "CHECKPOINT_DISABLE" || dropWorkspace && strings.EqualFold(key, "TF_WORKSPACE") {
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
	buf := &boundedBuffer{max: readLimit}
	cmd.Stdout = buf
	// Never return raw stderr: even diagnostics from read-only commands can contain secrets.
	// Only its classification leaves this function.
	stderr := &boundedTail{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("engine read timed out or was cancelled")
		}
		// Checked before stderr: once the write fails the engine usually dies on a closed pipe.
		if buf.overflow {
			return nil, ErrReadTooLarge
		}
		switch Classify(stderr.String()) {
		case "credentials":
			return nil, ErrReadCredentials
		case "network":
			return nil, ErrReadNetwork
		case "init_required":
			return nil, ErrReadInitRequired
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

// RefreshOnlySupport reports whether `plan -refresh-only` with -out and -json is available.
// Terraform added it in 0.15.4 and OpenTofu has it in every release, so it holds for the whole
// Supported range; the check stays explicit so the range can widen safely.
func RefreshOnlySupport(name, version string) bool {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil || !Supported(name, version) {
		return false
	}
	minor, _ := strconv.Atoi(m[1])
	return name == "terraform" && minor >= 5 || name == "tofu" && minor >= 6
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
