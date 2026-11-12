package projects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// ErrInvalidAction rejects an unknown workspace action.
var ErrInvalidAction = errors.New("invalid workspace action")

// WorkspaceManagerFactory resolves the engine for a workspace command with a pinned
// environment. It returns the executable path so its identity can be re-checked.
type WorkspaceManagerFactory func(root, preference string, environ []string) (name, executable string, manager engine.WorkspaceManager, err error)

// DefaultWorkspaceManagers uses the official CLI found on PATH. Inherited TF_WORKSPACE is
// never passed to these commands.
func DefaultWorkspaceManagers(root, preference string, environ []string) (string, string, engine.WorkspaceManager, error) {
	cli, err := engine.Detect(root, preference)
	if err != nil {
		return "", "", nil, err
	}
	cli.IgnoreInheritedWorkspace, cli.Environ = true, environ
	return cli.Name, cli.Executable, cli, nil
}

// WorkspaceSupport says which workspace commands a project's backend allows in Hclaro.
type WorkspaceSupport struct {
	// Change covers creating a workspace and setting the CLI default.
	Change       bool   `json:"change"`
	Reason       string `json:"reason,omitempty"`
	Delete       bool   `json:"delete"`
	DeleteReason string `json:"deleteReason,omitempty"`
}

// workspaceBackends support multiple CLI workspaces (Terraform/OpenTofu documentation).
// remote and cloud are excluded: their workspaces are managed by the remote service.
var workspaceBackends = map[string]bool{"local": true, "s3": true, "gcs": true, "azurerm": true, "consul": true, "cos": true, "kubernetes": true, "oss": true, "pg": true}

// workspaceSupport reads only the filesystem metadata.
func workspaceSupport(info workspace.Info) WorkspaceSupport {
	s := WorkspaceSupport{}
	switch {
	case !info.Initialized:
		s.Reason = "Initialize the project first; workspaces are managed through its initialized backend."
	case info.ExecutionMode == "remote-exec":
		s.Reason = "Workspaces of the " + info.Backend + " backend are managed by the remote service. Use the CLI or the service to change them."
	case info.Backend == "unknown":
		s.Reason = "The backend type could not be read from initialized metadata, so Hclaro cannot tell whether it supports workspaces."
	case !workspaceBackends[info.Backend]:
		s.Reason = "The " + info.Backend + " backend is not known to support multiple workspaces."
	default:
		s.Change = true
	}
	switch {
	case !s.Change:
		s.DeleteReason = s.Reason
	case info.Backend != "local":
		s.DeleteReason = "Deleting workspaces is available only for the local backend in this release. Use the CLI for the " + info.Backend + " backend."
	default:
		s.Delete = true
	}
	return s
}

// WorkspaceReview is the reviewed preflight of one workspace command. Producing it runs
// `version`, `workspace list` and, for delete, `show -json` for that workspace; it never
// changes anything.
type WorkspaceReview struct {
	ProjectID     string                 `json:"projectId"`
	Alias         string                 `json:"alias"`
	RootPath      string                 `json:"rootPath"`
	Action        engine.WorkspaceAction `json:"action"`
	Name          string                 `json:"name"`
	Engine        string                 `json:"engine,omitempty"`
	EngineVersion string                 `json:"engineVersion,omitempty"`
	Backend       string                 `json:"backend"`
	ExecutionMode string                 `json:"executionMode"`
	DataDir       string                 `json:"dataDir"`
	Workspaces    []string               `json:"workspaces"`
	CLIDefault    string                 `json:"cliDefault,omitempty"`
	Command       []string               `json:"command"`
	Notices       []string               `json:"notices"`
	// Empty is set for delete once the workspace's state was read.
	Empty     *bool            `json:"empty,omitempty"`
	Support   WorkspaceSupport `json:"support"`
	Launch    bool             `json:"launch"`
	CanRun    bool             `json:"canRun"`
	Reason    string           `json:"reason,omitempty"`
	Code      string           `json:"reasonCode,omitempty"`
	Busy      string           `json:"busy,omitempty"`
	Expires   string           `json:"expiresAt,omitempty"`
	ContextID string           `json:"contextId,omitempty"`
	// Execution is the selected execution profile context, if any.
	Execution *operations.ExecutionContext `json:"executionContext,omitempty"`
}

// pendingReview is what a confirmed start must match. It is single use.
type pendingReview struct {
	projectID string
	action    engine.WorkspaceAction
	name      string
	static    string
	listing   engine.WorkspaceList
	notices   []string
	expires   time.Time
	profileID string
	context   *operations.ExecutionContext
}

const (
	reviewTTL  = 10 * time.Minute
	maxReviews = 32
)

// workspaceNotices disclose inherited settings without their values.
func workspaceNotices(environ []string, action engine.WorkspaceAction, launch bool) []string {
	notices := []string{}
	for _, item := range environ {
		if key, _, _ := strings.Cut(item, "="); key == "TF_WORKSPACE" {
			notices = append(notices, "Inherited TF_WORKSPACE is not passed: the engines refuse to create or select a workspace while it overrides the selection. Terminals that set TF_WORKSPACE keep using it whatever the CLI default is.")
			break
		}
	}
	if launch && action != engine.WorkspaceDelete {
		notices = append(notices, "This project is also open from the terminal. Its plans and applies check the CLI-selected workspace and will refuse to run after the CLI default changes, until Hclaro is restarted.")
	}
	return notices
}

// staticContext hashes everything a workspace command depends on that can be checked
// without running the engine: project, root, engine choice and executable identity, data
// directory, backend metadata, relevant environment and the configuration fingerprint.
func staticContext(p Record, name, executable string, environ []string, action engine.WorkspaceAction, target, profile string) string {
	fingerprint := workspace.Fingerprint(p.RootPath, environ, "workspace")
	if fingerprint == "" {
		return ""
	}
	info := workspace.Inspect(p.RootPath)
	env := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if initEnvKey(key) {
			env = append(env, item)
		}
	}
	sort.Strings(env)
	h := sha256.New()
	for _, field := range append([]string{p.ID, p.RootPath, p.EnginePreference, name, executable, fileIdentity(executable), fingerprint, info.Backend, info.ExecutionMode, strconv.FormatBool(info.Initialized), string(action), target, "profile:" + profile}, env...) {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func listingID(static string, l engine.WorkspaceList) string {
	h := sha256.New()
	for _, field := range append([]string{static, l.Current, strconv.Itoa(l.Hidden)}, l.Names...) {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReviewWorkspace checks a workspace command against a fresh listing and returns what it
// will do. Refusals are returned in the review with CanRun false. Nothing is changed.
func (h *Hub) ReviewWorkspace(ctx context.Context, id string, action engine.WorkspaceAction, target string) (WorkspaceReview, error) {
	return h.ReviewWorkspaceWith(ctx, id, action, target, "")
}

// ReviewWorkspaceWith reviews a workspace command run with an optional execution profile's
// environment and engine preference.
func (h *Hub) ReviewWorkspaceWith(ctx context.Context, id string, action engine.WorkspaceAction, target, profileID string) (WorkspaceReview, error) {
	if !engine.ValidAction(action) {
		return WorkspaceReview{}, ErrInvalidAction
	}
	if !engine.ValidWorkspace(target) || action == engine.WorkspaceNew && !engine.ValidArgWorkspace(target) {
		return WorkspaceReview{}, ErrInvalidWorkspace
	}
	p, err := h.Registry.Get(id)
	if err != nil {
		return WorkspaceReview{}, err
	}
	inherited := os.Environ()
	environ := inherited
	data, shared := dataDir()
	launch := h.Launch != "" && h.Launch == p.RootPath
	r := WorkspaceReview{ProjectID: p.ID, Alias: p.Alias, RootPath: p.RootPath, Action: action, Name: target, Backend: "unknown", ExecutionMode: "unsupported", DataDir: data, Workspaces: []string{}, Notices: workspaceNotices(environ, action, launch), Launch: launch}
	preference, composed, execution, profileErr := h.execution(p, profileID)
	profile := ""
	if execution != nil {
		environ, profile, r.Execution = composed, execution.Fingerprint, execution
		r.Notices = append(r.Notices, execution.Notices...)
	} else {
		preference = p.EnginePreference
	}
	name := preference
	if name == "" {
		name = "terraform"
	}
	r.Command = operations.WorkspaceCommand(name, action, target)
	refuse := func(code, message string) (WorkspaceReview, error) {
		r.Code, r.Reason = code, message
		return r, nil
	}
	status, problem := check(p.RootPath)
	if status != StatusReady {
		return refuse(status, problem)
	}
	var refusal *ProfileRefusal
	if errors.As(profileErr, &refusal) {
		return refuse(refusal.Code, refusal.Message)
	}
	info := workspace.Inspect(p.RootPath)
	r.Backend, r.ExecutionMode, r.Support = info.Backend, info.ExecutionMode, workspaceSupport(info)
	r.Busy, _ = h.Locks.Holder(p.RootPath)
	switch {
	case h.WorkspaceManagers == nil || h.Inits == nil:
		return refuse("unavailable", "Workspace commands are not available in this session.")
	case r.Busy != "":
		return refuse("busy", "Another operation is running in this project. Wait for it to finish.")
	case shared:
		return refuse("shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory and CLI default workspace. Restart Hclaro without it, or with a relative TF_DATA_DIR.")
	case !info.Initialized:
		return refuse("uninitialized", r.Support.Reason)
	case !r.Support.Change:
		return refuse("workspaces_unsupported", r.Support.Reason)
	case action == engine.WorkspaceDelete && !r.Support.Delete:
		return refuse("delete_unsupported", r.Support.DeleteReason)
	case !engine.ValidArgWorkspace(target):
		return refuse("unsafe_name", "This name starts with a dash, so it could be read as a command flag. Manage it with the CLI.")
	case engine.UnsafeArgs(inherited, "workspace list", "workspace "+string(action), "show"):
		return refuse("unsafe_cli_args", "Inherited TF_CLI_ARGS, TF_CLI_ARGS_show or a TF_CLI_ARGS_workspace* setting adds flags other than -no-color, which would change the reviewed command. Restart Hclaro without them.")
	}
	name, executable, manager, err := h.WorkspaceManagers(p.RootPath, preference, environ)
	if err != nil {
		return refuse("missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	}
	r.Engine, r.Command = name, operations.WorkspaceCommand(name, action, target)
	r.EngineVersion, err = manager.Version(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			return refuse("unsafe_cli_args", "Inherited TF_CLI_ARGS settings contain unsupported flags. Only -no-color is accepted. Restart Hclaro without them.")
		}
		return refuse("engine_version", "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13, and check that this project's engine is the right one.")
	}
	list, err := manager.ListWorkspaces(ctx)
	if err != nil {
		return refuse("workspace_list", "Unable to list workspaces, so Hclaro cannot check this command. "+engine.ReadAdvice(err, "Check backend access, credentials and SSO/VPN with the CLI, then review again."))
	}
	r.Workspaces, r.CLIDefault = slices.Clone(list.Names), list.Current
	h.remember(p.ID, name, r.EngineVersion, list)
	exists := slices.Contains(list.Names, target)
	switch {
	case action == engine.WorkspaceNew && exists:
		return refuse("workspace_exists", "A workspace named "+target+" already exists. Open it instead.")
	case action != engine.WorkspaceNew && !exists:
		return refuse("workspace_missing", "There is no workspace named "+target+". List workspaces again.")
	case action == engine.WorkspaceSelect && list.Current == target:
		return refuse("already_default", target+" is already the CLI default.")
	case action == engine.WorkspaceDelete && target == "default":
		return refuse("workspace_default", "The default workspace cannot be deleted.")
	case action == engine.WorkspaceDelete && list.Current == "":
		return refuse("default_unknown", "The CLI default could not be determined, so Hclaro cannot be sure this workspace is not in use.")
	case action == engine.WorkspaceDelete && list.Current == target:
		return refuse("workspace_active", target+" is the CLI default. Set another workspace as the CLI default first.")
	case action == engine.WorkspaceDelete && inheritedWorkspace(inherited) == target:
		return refuse("workspace_active", "Hclaro was started with TF_WORKSPACE set to "+target+". Restart it without that setting first.")
	}
	if action == engine.WorkspaceDelete {
		b, err := manager.WorkspaceState(ctx, target)
		if err != nil {
			return refuse("state_unknown", "The workspace's recorded state could not be read, so Hclaro cannot confirm it is empty. Deletion is disabled.")
		}
		empty, err := operations.WorkspaceEmpty(b)
		if err != nil {
			return refuse("state_unknown", "The workspace's recorded state could not be parsed, so Hclaro cannot confirm it is empty. Deletion is disabled.")
		}
		r.Empty = &empty
		if !empty {
			return refuse("workspace_not_empty", "This workspace's recorded state still has resources or outputs. Hclaro deletes only empty workspaces and never uses -force.")
		}
	}
	static := staticContext(p, name, executable, environ, action, target, profile)
	if static == "" {
		return refuse("too_large", "The project has too many files to fingerprint safely, so Hclaro cannot detect changes made after review. Use the CLI.")
	}
	r.ContextID = listingID(static, list)
	expires := h.Now().Add(reviewTTL)
	r.Expires = expires.UTC().Format(time.RFC3339)
	h.mu.Lock()
	for k, v := range h.reviews {
		if h.Now().After(v.expires) || len(h.reviews) >= maxReviews {
			delete(h.reviews, k)
		}
	}
	h.reviews[r.ContextID] = &pendingReview{projectID: p.ID, action: action, name: target, static: static, listing: list, notices: slices.Clone(r.Notices), expires: expires, profileID: profileID, context: execution}
	h.mu.Unlock()
	r.CanRun = true
	return r, nil
}

func inheritedWorkspace(environ []string) string {
	for _, item := range environ {
		if key, value, _ := strings.Cut(item, "="); key == "TF_WORKSPACE" {
			return value
		}
	}
	return ""
}

// remember stores a fresh listing as the project's inspection.
func (h *Hub) remember(id, name, version string, list engine.WorkspaceList) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inspections[id] = &Inspection{Engine: name, Version: version, Workspaces: slices.Clone(list.Names), CLIDefault: list.Current, Hidden: list.Hidden, InspectedAt: h.Now().UTC().Format(time.RFC3339)}
}

// StartWorkspace runs a reviewed workspace command. The review is consumed; the root is
// claimed until the command ends; the engine, root, data directory and environment must be
// unchanged, and the runner lists workspaces again before running.
func (h *Hub) StartWorkspace(id string, action engine.WorkspaceAction, target, expected string) (operations.Operation, error) {
	return h.StartWorkspaceWith(id, action, target, expected, "")
}

// StartWorkspaceWith runs a reviewed workspace command; profileID must be the profile the
// review used.
func (h *Hub) StartWorkspaceWith(id string, action engine.WorkspaceAction, target, expected, profileID string) (operations.Operation, error) {
	p, err := h.Registry.Get(id)
	if err != nil {
		return operations.Operation{}, err
	}
	h.mu.Lock()
	review, ok := h.reviews[expected]
	if ok && (review.projectID != id || review.action != action || review.name != target || review.profileID != profileID || h.Now().After(review.expires)) {
		ok = false
	}
	h.mu.Unlock()
	if !ok || h.WorkspaceManagers == nil || h.Inits == nil {
		return operations.Operation{}, ErrContextChanged
	}
	release, acquired := h.Locks.Acquire(p.RootPath, "workspace")
	if !acquired {
		return operations.Operation{}, ErrBusy
	}
	h.mu.Lock()
	if h.reviews[expected] != review {
		h.mu.Unlock()
		release()
		return operations.Operation{}, ErrContextChanged
	}
	delete(h.reviews, expected)
	h.mu.Unlock()
	preference, environ, execution, err := h.execution(p, profileID)
	profile := ""
	switch {
	case err != nil:
		release()
		return operations.Operation{}, ErrContextChanged
	case execution != nil:
		profile = execution.Fingerprint
	default:
		environ = os.Environ()
	}
	name, executable, manager, err := h.WorkspaceManagers(p.RootPath, preference, environ)
	if err != nil {
		release()
		return operations.Operation{}, ErrContextChanged
	}
	rootInfo, statErr := statIdentity(p.RootPath)
	exeInfo, exeErr := statIdentity(executable)
	if statErr != nil || exeErr != nil || staticContext(p, name, executable, environ, action, target, profile) != review.static {
		release()
		return operations.Operation{}, ErrContextChanged
	}
	version := ""
	h.mu.Lock()
	if inspection := h.inspections[id]; inspection != nil && inspection.Engine == name {
		version = inspection.Version
	}
	h.mu.Unlock()
	return h.Inits.StartWorkspace(operations.WorkspaceRequest{
		ProjectID: p.ID, Root: p.RootPath, Engine: name, EngineVersion: version, Action: action, Name: target, Manager: manager, Environ: environ, Context: execution,
		Notices: review.notices, Reviewed: review.listing,
		Revalidate: func() string {
			now, err := os.Stat(p.RootPath)
			resolved, evalErr := filepath.EvalSymlinks(p.RootPath)
			exe, exeErr := os.Stat(executable)
			if err != nil || evalErr != nil || exeErr != nil || resolved != p.RootPath || !os.SameFile(rootInfo, now) || !os.SameFile(exeInfo, exe) || staticContext(p, name, executable, environ, action, target, profile) != review.static {
				return "path_changed"
			}
			return ""
		},
		Finished: func(after *engine.WorkspaceList) {
			h.mu.Lock()
			if after == nil {
				delete(h.inspections, p.ID)
			}
			// Open contexts read their own workspace through TF_WORKSPACE, so they stay valid,
			// except those of a deleted workspace.
			if action == engine.WorkspaceDelete && (after == nil || !slices.Contains(after.Names, target)) {
				for cid, c := range h.contexts {
					if c.ProjectID == p.ID && c.Workspace == target {
						h.closeLocked(cid, "workspace_deleted")
					}
				}
			}
			h.mu.Unlock()
			if after != nil {
				version := ""
				if in, ok := h.inspection(p.ID); ok {
					version = in.Version
				}
				h.remember(p.ID, name, version, *after)
			}
		},
		Release: release,
	})
}

func (h *Hub) inspection(id string) (Inspection, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	in, ok := h.inspections[id]
	if !ok {
		return Inspection{}, false
	}
	return *in, true
}

// LatestWorkspaceOperation returns the project's most recent workspace command, if any.
func (h *Hub) LatestWorkspaceOperation(id string) (operations.Operation, bool) {
	if h.Inits == nil {
		return operations.Operation{}, false
	}
	return h.Inits.Latest(id, "workspace")
}
