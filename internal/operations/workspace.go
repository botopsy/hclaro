package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

// WorkspaceRequest is a reviewed, pinned workspace command in one project root. The caller
// has already claimed the root exclusively; Release gives the claim up once it ends.
type WorkspaceRequest struct {
	ProjectID string
	Root      string
	Engine    string
	Action    engine.WorkspaceAction
	Name      string
	Manager   engine.WorkspaceManager
	// Environ is the pinned child environment; it is used here only to recognize secret
	// values the engine might echo.
	Environ []string
	Notices []string
	// Reviewed is the listing the user saw. The command runs only if a fresh listing matches.
	Reviewed engine.WorkspaceList
	// Revalidate runs before the command starts. A non-empty code refuses to run.
	Revalidate func() string
	// Finished receives the listing read after the command, or nil when it is unknown.
	Finished func(after *engine.WorkspaceList)
	Release  func()
}

// WorkspaceResult describes a workspace command and the CLI default before and after it.
// Verified is true when a listing after the command showed the expected effect.
type WorkspaceResult struct {
	Action           engine.WorkspaceAction `json:"action"`
	Name             string                 `json:"name"`
	CLIDefaultBefore string                 `json:"cliDefaultBefore,omitempty"`
	CLIDefaultAfter  string                 `json:"cliDefaultAfter,omitempty"`
	Workspaces       []string               `json:"workspaces,omitempty"`
	// Ran reports whether the engine's workspace command was started.
	Ran      bool `json:"ran"`
	Verified bool `json:"verified"`
}

// WorkspaceCommand is the argv shown to the user, without the executable path.
func WorkspaceCommand(engineName string, action engine.WorkspaceAction, name string) []string {
	return []string{engineName, "workspace", string(action), name}
}

var workspaceMessages = map[string]string{
	"unsafe_cli_args":        "Inherited TF_CLI_ARGS or TF_CLI_ARGS_workspace adds flags other than -no-color, which would change the reviewed command. Nothing was run. Restart Hclaro without them.",
	"engine_version":         "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13. Nothing was run.",
	"path_changed":           "The project folder, its engine or its environment changed after you reviewed this. Nothing was run. Review it again.",
	"workspace_list":         "Workspaces could not be listed just before running, so Hclaro did not run the command. Check backend access and credentials with the CLI, then review again.",
	"workspaces_changed":     "The workspace list or the CLI default changed after you reviewed this, for example in a terminal. Nothing was run. Review it again.",
	"state_unknown":          "The workspace's recorded state could not be read to confirm it is empty. Nothing was deleted.",
	"workspace_not_empty":    "The workspace's recorded state is not empty. Hclaro deletes only empty workspaces and never uses -force. Nothing was deleted.",
	"workspace_overridden":   "The engine reported that TF_WORKSPACE overrides the workspace selection. Hclaro does not pass it to workspace commands; check the CLI configuration in a terminal.",
	"workspaces_unsupported": "This backend does not support multiple workspaces. Nothing was changed.",
	"workspace_exists":       "A workspace with this name already exists. Nothing was created; open it instead.",
	"workspace_missing":      "The workspace does not exist. List workspaces again.",
	"workspace_active":       "The engine refused to delete the workspace because it is the CLI default. Set another workspace as CLI default first.",
	"workspace_default":      "The default workspace cannot be deleted.",
	"invalid_name":           "The engine rejected the workspace name. Choose a different name.",
	"state_locked":           "The state is locked by another operation. Hclaro never force-unlocks state. Wait for it to finish, then review again.",
	"credentials":            "The engine could not authenticate with the backend. Refresh your credentials (for example, an SSO login) in your terminal, then review again.",
	"network":                "The backend was unreachable. Check network and VPN access, then review again.",
	"workspace_failed":       "The workspace command failed. Review the diagnostics; lines that look like secrets are withheld. Nothing was retried.",
	"cancelled_before":       "Cancelled before the workspace command started. Nothing was changed.",
	"cancelled":              "Cancelled while the workspace command was running. It may or may not have taken effect. List workspaces again before doing anything else.",
	"timeout":                "The workspace command exceeded its time limit and was stopped. It may or may not have taken effect. List workspaces again.",
	"shutdown":               "Hclaro stopped while the workspace command was running. It may or may not have taken effect. List workspaces again.",
	"unverified":             "The engine reported success, but Hclaro could not list workspaces afterwards to confirm it. List workspaces again; do not repeat the command before checking.",
	"unexpected":             "The engine reported success, but the workspace list afterwards does not show the expected change. Check the project with your CLI.",
}

func workspaceSuccess(action engine.WorkspaceAction, name string) string {
	switch action {
	case engine.WorkspaceNew:
		return fmt.Sprintf("Created workspace %s. The engine also made it the CLI default for this folder: terminal commands here now use it unless TF_WORKSPACE is set.", name)
	case engine.WorkspaceSelect:
		return fmt.Sprintf("%s is now the CLI default for this folder. Terminal commands here now use its state unless TF_WORKSPACE is set. Hclaro tabs keep their own workspaces.", name)
	}
	return fmt.Sprintf("Deleted the empty workspace %s. No infrastructure was changed.", name)
}

// StartWorkspace records the operation and runs it in the background.
func (r *Inits) StartWorkspace(req WorkspaceRequest) (Operation, error) {
	op := Operation{
		Kind: "workspace", Command: WorkspaceCommand(req.Engine, req.Action, req.Name), ProjectID: req.ProjectID, Notices: slices.Clone(req.Notices),
		Workspace: &WorkspaceResult{Action: req.Action, Name: req.Name, CLIDefaultBefore: req.Reviewed.Current},
	}
	return r.begin(op, req.Release, func(ctx context.Context, e *initEntry) { r.runWorkspace(ctx, e, req) })
}

func (r *Inits) runWorkspace(ctx context.Context, e *initEntry, req WorkspaceRequest) {
	code, exitCode, after := r.executeWorkspace(ctx, e, req)
	status := Failed
	switch code {
	case "succeeded":
		status = Succeeded
	case "unverified", "unexpected":
		status = Unknown
	case "cancelled", "cancelled_before", "shutdown":
		status = Cancelled
	}
	if req.Finished != nil {
		req.Finished(after)
	}
	message := workspaceMessages[code]
	if code == "succeeded" {
		message = workspaceSuccess(req.Action, req.Name)
	}
	r.mu.Lock()
	o := &e.op
	o.Status, o.Stage, o.ExitCode = status, "Finished", exitCode
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	if code != "succeeded" {
		o.Diagnostic = &Diagnostic{Code: code, Message: message}
	}
	if after != nil {
		o.Workspace.CLIDefaultAfter, o.Workspace.Workspaces = after.Current, slices.Clone(after.Names)
	}
	o.LogDropped = e.log.Dropped()
	r.mu.Unlock()
	e.release()
	level := "error"
	if status == Succeeded {
		level = "info"
	}
	e.log.Append(level, message)
	e.log.Finish()
}

// executeWorkspace returns a code from workspaceMessages (or "succeeded"), the engine exit
// code when the command ran to completion and the listing read afterwards, if any.
func (r *Inits) executeWorkspace(ctx context.Context, e *initEntry, req WorkspaceRequest) (code string, exitCode *int, after *engine.WorkspaceList) {
	ran := false
	stopped := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case e.op.CancelRequested && ran:
			return "cancelled"
		case e.op.CancelRequested:
			return "cancelled_before"
		case r.closed:
			return "shutdown"
		}
		return "timeout"
	}
	for _, n := range req.Notices {
		e.log.Append("warn", n)
	}
	r.stage(e, "Checking engine version")
	_, err := req.Manager.Version(ctx)
	switch {
	case ctx.Err() != nil:
		return stopped(), nil, nil
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args", nil, nil
	case err != nil:
		return "engine_version", nil, nil
	}
	r.stage(e, "Checking project")
	if code := req.Revalidate(); code != "" {
		return code, nil, nil
	}
	r.stage(e, "Listing workspaces")
	before, err := req.Manager.ListWorkspaces(ctx)
	switch {
	case ctx.Err() != nil:
		return stopped(), nil, nil
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args", nil, nil
	case err != nil:
		return "workspace_list", nil, nil
	case !sameListing(before, req.Reviewed):
		return "workspaces_changed", nil, &before
	}
	if req.Action == engine.WorkspaceDelete {
		r.stage(e, "Checking that the workspace is empty")
		b, err := req.Manager.WorkspaceState(ctx, req.Name)
		if ctx.Err() != nil {
			return stopped(), nil, &before
		}
		if err != nil {
			return "state_unknown", nil, &before
		}
		empty, err := WorkspaceEmpty(b)
		switch {
		case err != nil:
			return "state_unknown", nil, &before
		case !empty:
			return "workspace_not_empty", nil, &before
		}
	}
	if ctx.Err() != nil {
		return stopped(), nil, &before
	}
	r.stage(e, "Running workspace "+string(req.Action))
	r.update(e, func(o *Operation) { o.Workspace.Ran = true })
	ran = true
	k := initKnown(req.Root, req.Environ)
	var shown strings.Builder
	err = req.Manager.ChangeWorkspace(ctx, req.Action, req.Name, func(stream string, line []byte) {
		level, text, diag := humanLine(stream, string(line), k)
		if text != "" {
			e.log.Append(level, text)
			if shown.Len() < 64<<10 {
				shown.WriteString(text + "\n")
			}
		}
		if diag != nil {
			r.update(e, func(o *Operation) {
				if len(o.Diagnostics) < maxDiags {
					o.Diagnostics = append(o.Diagnostics, *diag)
				}
			})
		}
	})
	if err != nil {
		var exit *engine.ExitError
		switch {
		case ctx.Err() != nil || errors.Is(err, engine.ErrCancelled):
			return stopped(), nil, r.listAfter(e, req)
		case errors.Is(err, engine.ErrUnsafeArgs):
			return "unsafe_cli_args", nil, &before
		case errors.As(err, &exit):
			code := exit.Code
			class := engine.ClassifyWorkspace(shown.String())
			if class == "" {
				class = exit.Hint
			}
			if class == "" {
				class = "workspace_failed"
			}
			return class, &code, r.listAfter(e, req)
		}
		return "workspace_failed", nil, r.listAfter(e, req)
	}
	zero := 0
	after = r.listAfter(e, req)
	if after == nil {
		return "unverified", &zero, nil
	}
	ok := false
	switch req.Action {
	case engine.WorkspaceNew:
		ok = slices.Contains(after.Names, req.Name)
	case engine.WorkspaceSelect:
		ok = after.Current == req.Name
	case engine.WorkspaceDelete:
		ok = !slices.Contains(after.Names, req.Name)
	}
	if !ok {
		return "unexpected", &zero, after
	}
	r.update(e, func(o *Operation) { o.Workspace.Verified = true })
	return "succeeded", &zero, after
}

// listAfter lists workspaces after the command with a fresh context, so a cancelled
// command's effect is still reported. It returns nil when listing fails.
func (r *Inits) listAfter(e *initEntry, req WorkspaceRequest) *engine.WorkspaceList {
	r.stage(e, "Listing workspaces again")
	ctx, cancel := context.WithTimeout(context.Background(), afterListLimit)
	defer cancel()
	list, err := req.Manager.ListWorkspaces(ctx)
	if err != nil {
		e.log.Append("warn", "Workspaces could not be listed after the command.")
		return nil
	}
	return &list
}

// afterListLimit bounds the listing that reports a command's effect.
const afterListLimit = 30 * time.Second

func sameListing(a, b engine.WorkspaceList) bool {
	return a.Current == b.Current && a.Hidden == b.Hidden && slices.Equal(a.Names, b.Names)
}

// WorkspaceEmpty reports whether `show -json` output describes a workspace with no resource
// instances (managed or data) and no outputs. A workspace without any state yet is empty.
// Anything Hclaro cannot parse is an error, never "empty".
func WorkspaceEmpty(b []byte) (bool, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return false, errors.New("no state output")
	}
	idx, err := state.Parse(b)
	if errors.Is(err, state.ErrNoState) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var doc struct {
		Values struct {
			Outputs map[string]json.RawMessage `json:"outputs"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return false, err
	}
	return len(idx.Resources) == 0 && len(doc.Values.Outputs) == 0, nil
}
