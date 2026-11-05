package engine

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

// WorkspaceAction is one of the three workspace commands Hclaro can run. There is no way to
// pass -force, -or-create, -lock=false or -state.
type WorkspaceAction string

const (
	// WorkspaceNew creates a workspace. The engine also makes it the CLI default.
	WorkspaceNew WorkspaceAction = "new"
	// WorkspaceSelect changes the CLI default recorded in the data directory.
	WorkspaceSelect WorkspaceAction = "select"
	// WorkspaceDelete deletes a workspace; without -force the engine refuses non-empty state.
	WorkspaceDelete WorkspaceAction = "delete"
)

// ValidAction reports whether a is a workspace action Hclaro runs.
func ValidAction(a WorkspaceAction) bool {
	return a == WorkspaceNew || a == WorkspaceSelect || a == WorkspaceDelete
}

// WorkspaceManager runs the workspace lifecycle commands. It is a separate interface so
// read-only, plan-only and init adapters cannot change workspaces.
type WorkspaceManager interface {
	Version(context.Context) (string, error)
	// ListWorkspaces runs `workspace list` without TF_WORKSPACE.
	ListWorkspaces(context.Context) (WorkspaceList, error)
	// WorkspaceState reads `show -json` for one workspace through a child TF_WORKSPACE.
	WorkspaceState(ctx context.Context, name string) ([]byte, error)
	// ChangeWorkspace runs `workspace <action> <name>` without TF_WORKSPACE and streams its
	// output lines like Init.
	ChangeWorkspace(ctx context.Context, action WorkspaceAction, name string, line func(stream string, b []byte)) error
}

// argName is the subset of names Hclaro passes as a command argument: it must start with a
// letter or digit, so it can never be read as a flag such as -force.
var argName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,89}$`)

// ValidArgWorkspace reports whether name may be passed as a workspace command argument.
func ValidArgWorkspace(name string) bool {
	return argName.MatchString(name) && ValidWorkspace(name)
}

// ErrInvalidWorkspaceArg rejects an action or name before any process starts.
var ErrInvalidWorkspaceArg = errors.New("invalid workspace action or name")

// WorkspaceArgs is the complete argv after the executable. The name is the last argument
// and cannot start with "-".
func WorkspaceArgs(action WorkspaceAction, name string) ([]string, error) {
	if !ValidAction(action) || !ValidArgWorkspace(name) {
		return nil, ErrInvalidWorkspaceArg
	}
	return []string{"workspace", string(action), name}, nil
}

// workspaceLimit bounds one workspace command; a remote backend may wait for a state lock.
const workspaceLimit = 5 * time.Minute

// ChangeWorkspace runs one workspace command. TF_WORKSPACE is never passed: the engines
// refuse to create or select a workspace while it overrides the selection, and the command
// must act on the data directory's own selection.
func (c *CLI) ChangeWorkspace(ctx context.Context, action WorkspaceAction, name string, line func(stream string, b []byte)) error {
	args, err := WorkspaceArgs(action, name)
	if err != nil {
		return err
	}
	return c.streamLines(ctx, workspaceLimit, true, "", args, ClassifyWorkspace, line)
}

// WorkspaceState reads the recorded state of one workspace without selecting it.
func (c *CLI) WorkspaceState(ctx context.Context, name string) ([]byte, error) {
	if !ValidWorkspace(name) {
		return nil, ErrInvalidWorkspaceArg
	}
	return c.runEnv(ctx, readTimeout, true, name, "show", "-json")
}

// ClassifyWorkspace maps workspace command diagnostics to a failure category without
// retaining them. The most specific match wins.
func ClassifyWorkspace(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "tf_workspace", "overridden"):
		return "workspace_overridden"
	case containsAny(lower, "workspaces not supported", "named states not supported", "does not support workspaces", "doesn't support workspaces", "multiple workspaces"):
		return "workspaces_unsupported"
	case containsAny(lower, "already exists"):
		return "workspace_exists"
	case containsAny(lower, "doesn't exist", "does not exist"):
		return "workspace_missing"
	case containsAny(lower, "your active workspace", "currently selected workspace", "cannot delete the currently"):
		return "workspace_active"
	case containsAny(lower, "default workspace", "default state"):
		return "workspace_default"
	case containsAny(lower, "not empty", "currently tracking", "lose track"):
		return "workspace_not_empty"
	case containsAny(lower, "not a valid workspace name", "invalid workspace name", "valid name"):
		return "invalid_name"
	case containsAny(lower, "error acquiring the state lock", "state lock", "lock info"):
		return "state_locked"
	}
	switch code := Classify(text); code {
	case "credentials", "network":
		return code
	}
	return ""
}

// ListWorkspaces returns the fake's workspaces; ListErr fails it.
func (m *Fake) ListWorkspaces(context.Context) (WorkspaceList, error) {
	m.record("workspace list")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return WorkspaceList{}, m.ListErr
	}
	return WorkspaceList{Names: slices.Clone(m.Workspaces), Current: m.CLIDefault}, nil
}

// WorkspaceState returns WorkspaceStates[name], or StateErr.
func (m *Fake) WorkspaceState(_ context.Context, name string) ([]byte, error) {
	m.record("show " + name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StateErr != nil {
		return nil, m.StateErr
	}
	return m.WorkspaceStates[name], nil
}

// ChangeWorkspace emulates a workspace command. On success it applies the effect the real
// engines have: new adds and selects, select selects, delete removes. ListErrAfter then
// makes later listings fail, for partial-success tests.
func (m *Fake) ChangeWorkspace(ctx context.Context, action WorkspaceAction, name string, line func(stream string, b []byte)) error {
	args, err := WorkspaceArgs(action, name)
	if err != nil {
		return err
	}
	m.record(strings.Join(args, " "))
	for _, l := range m.WorkspaceLines {
		line("stdout", l)
	}
	if m.BlockWorkspace {
		<-ctx.Done()
		return ErrCancelled
	}
	if m.WorkspaceErr != nil {
		return m.WorkspaceErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch action {
	case WorkspaceNew:
		m.Workspaces = append(m.Workspaces, name)
		m.CLIDefault = name
	case WorkspaceSelect:
		m.CLIDefault = name
	case WorkspaceDelete:
		m.Workspaces = slices.DeleteFunc(m.Workspaces, func(n string) bool { return n == name })
	}
	if m.ListErrAfter {
		m.ListErr = errors.New("list failed")
	}
	return nil
}
