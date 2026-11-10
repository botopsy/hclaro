package projects

import (
	"context"
	"errors"
	"hclaro/internal/gitctx"
	"hclaro/internal/history"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"hclaro/internal/discovery"
	"hclaro/internal/engine"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
	"hclaro/internal/profiles"
	"hclaro/internal/rootlock"
	"hclaro/internal/state"
	"hclaro/internal/workspace"
)

// Project status values, from a filesystem check only (no engine command).
const (
	StatusReady       = "ready"
	StatusMissing     = "missing"
	StatusUnreadable  = "unreadable"
	StatusNotProject  = "not_project"
	StatusPathChanged = "path_changed"
)

// ReadOnlyReason explains why Hub contexts cannot plan, apply or import in this release.
const ReadOnlyReason = "Hub workspaces are read-only. Plans, applies and imports run only for the project Hclaro was launched with, in its CLI-selected workspace."

var (
	ErrUnavailable      = errors.New("project is unavailable")
	ErrSharedDataDir    = errors.New("an absolute TF_DATA_DIR is shared by every project")
	ErrInvalidWorkspace = errors.New("invalid workspace name")
	ErrContextNotFound  = errors.New("context not found")
	ErrInvalidPath      = errors.New("invalid project path")
	ErrBrowse           = errors.New("folder cannot be listed")
)

// PathError carries an actionable message for a rejected path.
type PathError struct{ Message string }

func (e *PathError) Error() string { return e.Message }

// Adapter is what a Hub context needs from an engine: reads only.
type Adapter interface {
	engine.Adapter
	ListWorkspaces(context.Context) (engine.WorkspaceList, error)
}

// EngineFactory resolves an engine for a root and an optional preference. selected is the
// UI workspace for a context, or "" for an inspection (which never sets TF_WORKSPACE).
// environ, when non-nil, is an execution profile's composed child environment; nil inherits
// Hclaro's environment.
type EngineFactory func(root, preference, selected string, environ []string) (name, executable string, adapter Adapter, err error)

// DefaultEngines uses the official CLI found on PATH.
func DefaultEngines(root, preference, selected string, environ []string) (string, string, Adapter, error) {
	cli, err := engine.Detect(root, preference)
	if err != nil {
		return "", "", nil, err
	}
	cli.SelectedWorkspace, cli.IgnoreInheritedWorkspace, cli.Environ = selected, true, environ
	return cli.Name, cli.Executable, cli, nil
}

// Inspection is the result of an explicit, read-only CLI inspection: version and
// `workspace list`. It never selects or creates a workspace.
type Inspection struct {
	Engine      string                `json:"engine,omitempty"`
	Version     string                `json:"version,omitempty"`
	Workspaces  []string              `json:"workspaces"`
	CLIDefault  string                `json:"cliDefault,omitempty"`
	Hidden      int                   `json:"hiddenWorkspaces"`
	InspectedAt string                `json:"inspectedAt"`
	Diagnostic  *workspace.Diagnostic `json:"diagnostic,omitempty"`
}

// Summary is the API view of a registered project.
type Summary struct {
	ID               string      `json:"id"`
	Alias            string      `json:"alias"`
	RootPath         string      `json:"rootPath"`
	EnginePreference string      `json:"enginePreference"`
	Pinned           bool        `json:"pinned"`
	AddedAt          string      `json:"addedAt"`
	LastOpenedAt     string      `json:"lastOpenedAt,omitempty"`
	LastWorkspace    string      `json:"lastWorkspace,omitempty"`
	Status           string      `json:"status"`
	Problem          string      `json:"problem,omitempty"`
	Initialized      bool        `json:"initialized"`
	Backend          string      `json:"backend"`
	ExecutionMode    string      `json:"executionMode"`
	ProviderLock     bool        `json:"providerLock"`
	Launch           bool        `json:"launch"`
	Inspection       *Inspection `json:"inspection,omitempty"`
	// Busy names the operation that holds the project root: "init", "workspace" or "plan".
	Busy string `json:"busy,omitempty"`
	// WorkspaceActions says which workspace commands this project's backend allows.
	WorkspaceActions WorkspaceSupport `json:"workspaceActions"`
}

// Environment describes inherited settings that affect every project. Values that could
// be sensitive are not returned.
type Environment struct {
	DataDir            string `json:"dataDir"`
	DataDirShared      bool   `json:"dataDirShared"`
	InheritedWorkspace string `json:"inheritedWorkspace,omitempty"`
	WorkspaceInherited bool   `json:"workspaceInherited"`
	CLIArgs            bool   `json:"cliArgsInherited"`
}

// Context is an immutable read scope: one project root, engine executable, workspace and
// data directory, with its own snapshot.
type Context struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	Alias      string `json:"alias"`
	RootPath   string `json:"rootPath"`
	Engine     string `json:"engine"`
	Executable string `json:"-"`
	Workspace  string `json:"workspace"`
	DataDir    string `json:"dataDir"`
	CreatedAt  string `json:"createdAt"`
	// Execution is the profile context this read was opened with; nil means the inherited
	// environment.
	Execution *operations.ExecutionContext `json:"executionContext,omitempty"`
	store     *workspace.Store
	cancel    context.CancelFunc
	key       string
	profileID string
}

// Snapshot returns the context's current snapshot.
func (c *Context) Snapshot() workspace.Snapshot { return c.store.Get() }

// Hub owns the registry, inspection results and read contexts.
type Hub struct {
	Registry *Registry
	Engines  EngineFactory
	// Initers resolves engines for initialization; Inits runs them. Either nil disables init.
	Initers IniterFactory
	Inits   *operations.Inits
	// WorkspaceManagers resolves engines for workspace commands, which also run through Inits.
	// Nil disables them.
	WorkspaceManagers WorkspaceManagerFactory
	// Tools resolves engines for the read-only project utilities, which also run through
	// Inits. Nil disables them.
	Tools ToolsFactory
	// Locks is shared with the launch project's operation manager: one operation per root.
	Locks *rootlock.Set
	// Profiles holds execution profiles per project; nil disables them.
	Profiles *profiles.Store
	// History keeps operation metadata on disk; nil when it is not used. See UseHistory.
	History *history.Store
	// Git reads Git context, diffs and source locations of registered projects and opens
	// approved files in an editor; nil disables those routes.
	Git *gitctx.Service
	// Launch is the canonical root Hclaro was started with, or "".
	Launch string
	// LaunchChanged runs after an init of the launch root has ended and released the root,
	// so the launch project can be read again. Nil does nothing.
	LaunchChanged func()
	Now           func() time.Time
	// MaxContexts bounds snapshots kept in memory; the oldest context without a running
	// operation is closed first.
	MaxContexts int
	// ScopedExecution enables execution scopes for Hub workspaces (ADR M24.03). It is set
	// only when Hclaro starts and is off by default; with it off, Execution refuses with
	// scoped_execution_disabled and contexts stay read-only.
	ScopedExecution bool
	// Executors resolves the full engine of an execution scope; MaxScopes bounds scopes in
	// memory; TempDir is where scopes keep saved plans ("" is the OS temp directory).
	Executors ExecutorFactory
	// GitProbe and IdentityProbe gather apply preflight facts for execution scopes; nil
	// reports them as unavailable.
	GitProbe      preflight.GitProbe
	IdentityProbe preflight.IdentityProbe
	// Discovery is the session's AWS discovery service. A scope's verifications, scans and
	// prepared imports are dropped (running scans cancelled) when the scope closes.
	Discovery   *discovery.Service
	MaxScopes   int
	TempDir     string
	base        context.Context
	stop        context.CancelFunc
	mu          sync.Mutex
	inspections map[string]*Inspection
	contexts    map[string]*Context
	order       []string
	// closed remembers why recent contexts were closed, so the UI can say so.
	closed map[string]string
	// reviews holds reviewed workspace commands by context ID until they start or expire.
	reviews map[string]*pendingReview
	// comparisons are workspace comparisons in memory, oldest first in comparisonOrder.
	comparisons     map[string]*Comparison
	comparisonOrder []string
	// scopes are execution scopes by context ID, least recently used first in scopeOrder.
	scopes     map[string]*Scope
	scopeOrder []string
	// envKey keys the environment digests of scopes; it never leaves the process.
	envKey []byte
	loads  chan struct{}
	wg     sync.WaitGroup
}

// NewHub creates a Hub. Close cancels running reads.
func NewHub(registry *Registry, launch string) *Hub {
	base, stop := context.WithCancel(context.Background())
	return &Hub{Registry: registry, Engines: DefaultEngines, Initers: DefaultIniters, Inits: operations.NewInits(), WorkspaceManagers: DefaultWorkspaceManagers, Tools: DefaultTools, Locks: rootlock.New(), Launch: launch, Now: time.Now, MaxContexts: 8, Executors: DefaultExecutors, MaxScopes: 4, base: base, stop: stop, inspections: map[string]*Inspection{}, contexts: map[string]*Context{}, closed: map[string]string{}, reviews: map[string]*pendingReview{}, scopes: map[string]*Scope{}, envKey: newEnvKey(), loads: make(chan struct{}, 3)}
}

// Close cancels context reads, execution scopes and initializations and waits for them to
// stop. Scopes delete their saved plans.
func (h *Hub) Close() {
	h.stop()
	h.mu.Lock()
	for _, cid := range slices.Clone(h.scopeOrder) {
		h.closeScopeLocked(cid)
	}
	h.mu.Unlock()
	if h.Inits != nil {
		h.Inits.Close(20 * time.Second)
	}
	h.wg.Wait()
}

// dataDir returns the inherited TF_DATA_DIR for display and whether it is shared by all
// projects (an absolute path).
func dataDir() (string, bool) {
	d := os.Getenv("TF_DATA_DIR")
	if d == "" {
		return ".terraform", false
	}
	return d, filepath.IsAbs(d)
}

// Env reports inherited settings that affect Hub reads.
func Env() Environment {
	d, shared := dataDir()
	e := Environment{DataDir: d, DataDirShared: shared}
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		switch {
		case key == "TF_WORKSPACE":
			e.WorkspaceInherited = true
			if engine.ValidWorkspace(value) {
				e.InheritedWorkspace = value
			}
		case strings.HasPrefix(key, "TF_CLI_ARGS"):
			e.CLIArgs = true
		}
	}
	return e
}

// Canonical validates a path typed or chosen by the user: absolute after cleaning, symlinks
// resolved, a readable directory with Terraform/OpenTofu files. Nothing is executed.
func Canonical(path string) (string, error) {
	if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return "", &PathError{"Enter the absolute path of a project root folder."}
	}
	if strings.HasPrefix(path, "~") {
		return "", &PathError{"Use an absolute path; ~ is not expanded."}
	}
	if !filepath.IsAbs(path) {
		return "", &PathError{"Use an absolute path, for example /Users/me/infra/prod."}
	}
	root, err := workspace.Root(path)
	if err != nil {
		return "", &PathError{capitalize(err.Error()) + "."}
	}
	return root, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// sameDir reports whether a and b are the same existing directory.
func sameDir(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// statIdentity stats path and records its file identity now. On Windows, os.Stat leaves the
// identity unloaded and os.SameFile loads it later from the path, so a FileInfo kept to
// detect a replaced folder or executable would describe whatever is at the path at
// comparison time. Comparing the FileInfo with itself loads and caches it.
func statIdentity(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	os.SameFile(info, info)
	return info, nil
}

// check reports the filesystem status of a stored canonical root.
func check(root string) (status, problem string) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return StatusMissing, "The folder no longer exists at this path. Repair the path if the project moved, or remove it from Hclaro. Nothing was deleted."
	} else if err != nil {
		return StatusUnreadable, "Hclaro cannot open this folder. Check its permissions, then refresh."
	}
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return StatusPathChanged, "This path now resolves to a different location through a symbolic link. Repair the path or remove the entry; Hclaro does not follow it."
	}
	if _, err := workspace.Root(root); err != nil {
		if _, readErr := os.ReadDir(root); readErr != nil {
			return StatusUnreadable, "Hclaro cannot list this folder. Check its permissions, then refresh."
		}
		return StatusNotProject, "No .tf, .tofu or state files were found in this folder. Repair the path or remove the entry."
	}
	return StatusReady, ""
}

func (h *Hub) summary(p Record) Summary {
	s := Summary{ID: p.ID, Alias: p.Alias, RootPath: p.RootPath, EnginePreference: p.EnginePreference, Pinned: p.Pinned, AddedAt: p.AddedAt, LastOpenedAt: p.LastOpenedAt, LastWorkspace: p.LastWorkspace, Backend: "unknown", ExecutionMode: "unsupported", Launch: h.Launch != "" && h.Launch == p.RootPath}
	s.Status, s.Problem = check(p.RootPath)
	s.Busy, _ = h.Locks.Holder(p.RootPath)
	if s.Status == StatusReady {
		info := workspace.Inspect(p.RootPath)
		s.Initialized, s.Backend, s.ExecutionMode, s.ProviderLock = info.Initialized, info.Backend, info.ExecutionMode, info.ProviderLock
		s.WorkspaceActions = workspaceSupport(info)
	} else {
		s.WorkspaceActions = WorkspaceSupport{Reason: s.Problem, DeleteReason: s.Problem}
	}
	h.mu.Lock()
	if in, ok := h.inspections[p.ID]; ok {
		clone := *in
		clone.Workspaces = slices.Clone(in.Workspaces)
		s.Inspection = &clone
	}
	h.mu.Unlock()
	return s
}

// List returns project summaries in stored order, with the registry status.
func (h *Hub) List() ([]Summary, error) {
	records, err := h.Registry.List()
	out := make([]Summary, 0, len(records))
	for _, p := range records {
		out = append(out, h.summary(p))
	}
	return out, err
}

// Summary returns one project.
func (h *Hub) Summary(id string) (Summary, error) {
	p, err := h.Registry.Get(id)
	if err != nil {
		return Summary{}, err
	}
	return h.summary(p), nil
}

// Add validates and registers a project. Nothing in the project is executed or written.
func (h *Hub) Add(path, alias, preference string) (Summary, error) {
	root, err := Canonical(path)
	if err != nil {
		return Summary{}, err
	}
	if alias == "" {
		alias = filepath.Base(root)
		if state.Suspicious(alias) || validAlias(alias) != nil {
			alias = "Project"
		}
		if len([]rune(alias)) > maxAlias {
			alias = string([]rune(alias)[:maxAlias])
		}
	}
	record := Record{ID: NewID(), Alias: alias, RootPath: root, EnginePreference: preference, AddedAt: h.Now().UTC().Format(time.RFC3339)}
	record, err = h.Registry.Add(record, func(existing string) bool { return sameDir(existing, root) })
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return Summary{ID: record.ID}, err
		}
		return Summary{}, err
	}
	return h.summary(record), nil
}

// Update edits metadata or repairs the root path. A changed root or engine preference
// closes the project's contexts and discards its inspection.
func (h *Hub) Update(id string, c Change) (Summary, error) {
	if c.RootPath != nil {
		root, err := Canonical(*c.RootPath)
		if err != nil {
			return Summary{}, err
		}
		c.RootPath = &root
	}
	same := func(existing string) bool { return c.RootPath != nil && sameDir(existing, *c.RootPath) }
	record, err := h.Registry.Update(id, c, same)
	if err != nil {
		return Summary{}, err
	}
	if c.RootPath != nil || c.EnginePreference != nil {
		h.forget(id, "project_changed")
	}
	return h.summary(record), nil
}

// Remove unregisters a project and closes its contexts, cancelling reads in progress.
func (h *Hub) Remove(id string) error {
	if _, err := h.Registry.Remove(id); err != nil {
		return err
	}
	h.forget(id, "project_removed")
	// The project's execution profiles are Hclaro settings, not project files; they go too.
	// A failure leaves orphaned entries that no project can reach.
	if h.Profiles != nil {
		_ = h.Profiles.RemoveProject(id)
	}
	return nil
}

// Reorder sets the stored order.
func (h *Hub) Reorder(ids []string) error { return h.Registry.Reorder(ids) }

func (h *Hub) forget(id, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.inspections, id)
	for cid, c := range h.contexts {
		if c.ProjectID == id {
			h.closeLocked(cid, reason)
		}
	}
}

func (h *Hub) closeLocked(cid, reason string) {
	c, ok := h.contexts[cid]
	if !ok {
		return
	}
	c.cancel()
	h.closeScopeLocked(cid)
	delete(h.contexts, cid)
	h.order = slices.DeleteFunc(h.order, func(id string) bool { return id == cid })
	if len(h.closed) >= 256 {
		clear(h.closed)
	}
	h.closed[cid] = reason
}

func diag(code, message string) *workspace.Diagnostic {
	return &workspace.Diagnostic{Code: code, Message: message}
}

// Inspect runs the engine's version and `workspace list` reads for an initialized project.
// It is only ever started by an explicit request. TF_WORKSPACE is not passed, so the
// marked workspace is the persisted CLI default.
func (h *Hub) Inspect(ctx context.Context, id string) (Summary, error) {
	p, err := h.Registry.Get(id)
	if err != nil {
		return Summary{}, err
	}
	// The claim is held for the whole inspection, so no init, plan or workspace command can
	// start in between (ADR M24.03, G1).
	release, ok := h.Locks.AcquireShared(p.RootPath)
	if !ok {
		return Summary{}, ErrBusy
	}
	defer release()
	in := &Inspection{Workspaces: []string{}, InspectedAt: h.Now().UTC().Format(time.RFC3339)}
	status, problem := check(p.RootPath)
	switch {
	case status != StatusReady:
		in.Diagnostic = diag(status, problem)
	default:
		in.Engine, in.Version, in.Workspaces, in.CLIDefault, in.Hidden, in.Diagnostic = h.inspect(ctx, p)
	}
	h.mu.Lock()
	h.inspections[id] = in
	h.mu.Unlock()
	return h.summary(p), nil
}

func (h *Hub) inspect(ctx context.Context, p Record) (name, version string, names []string, current string, hidden int, d *workspace.Diagnostic) {
	names = []string{}
	name, _, adapter, err := h.Engines(p.RootPath, p.EnginePreference, "", nil)
	if err != nil {
		return p.EnginePreference, "", names, "", 0, diag("missing_cli", "Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	}
	version, err = adapter.Version(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			return name, "", names, "", 0, diag("unsafe_cli_args", "Inherited TF_CLI_ARGS settings contain unsupported flags. Only -no-color is accepted. Restart Hclaro without them.")
		}
		return name, "", names, "", 0, diag("engine_version", "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13.")
	}
	if _, shared := dataDir(); shared {
		return name, version, names, "", 0, diag("shared_data_dir", "TF_DATA_DIR is an absolute path, so every project would share one data directory and backend initialization. Restart Hclaro without it, or with a relative TF_DATA_DIR.")
	}
	if !workspace.Inspect(p.RootPath).Initialized {
		return name, version, names, "", 0, diag("uninitialized", "Backend initialization was not detected. Review and run init from this project's page, or run it yourself, then list workspaces again. Nothing was initialized automatically.")
	}
	list, err := adapter.ListWorkspaces(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrUnsafeArgs) {
			return name, version, names, "", 0, diag("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_workspace settings and restart Hclaro. Only -no-color is accepted.")
		}
		return name, version, names, "", 0, diag("workspace_list", "Unable to list workspaces. Check backend access, credentials and SSO/VPN with the CLI, then inspect again.")
	}
	return name, version, list.Names, list.Current, list.Hidden, nil
}

// Open returns the read context for a project workspace, creating one and starting its
// state read if needed. reload always creates a new context with a fresh read.
func (h *Hub) Open(projectID, ws string, reload bool) (*Context, bool, error) {
	return h.OpenWith(projectID, ws, "", reload)
}

// OpenWith is Open with an optional execution profile. The profile's environment (AWS
// profile and region, no inherited TF_WORKSPACE) is used for every read of the context, and a
// profile that selects a workspace can only open that workspace.
func (h *Hub) OpenWith(projectID, ws, profileID string, reload bool) (*Context, bool, error) {
	if !engine.ValidWorkspace(ws) {
		return nil, false, ErrInvalidWorkspace
	}
	p, err := h.Registry.Get(projectID)
	if err != nil {
		return nil, false, err
	}
	if status, problem := check(p.RootPath); status != StatusReady {
		return nil, false, &PathError{problem}
	}
	// A shared claim is held from here until the read ends, so no init, plan or workspace
	// command runs in this root while it is read (ADR M24.03, G1). Every return before the
	// read starts releases it.
	release, claimed := h.Locks.AcquireShared(p.RootPath)
	if !claimed {
		return nil, false, ErrBusy
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	data, shared := dataDir()
	if shared {
		return nil, false, ErrSharedDataDir
	}
	preference, environ, execution, err := h.execution(p, profileID)
	if err != nil {
		return nil, false, err
	}
	if execution != nil {
		if execution.Workspace != "" && execution.Workspace != ws {
			return nil, false, &ProfileRefusal{"profile_workspace", "Profile " + execution.ProfileName + " selects workspace " + execution.Workspace + ". Open that workspace, or choose another profile for " + ws + "."}
		}
		execution.Workspace = ws
	}
	name, executable, adapter, err := h.Engines(p.RootPath, preference, ws, environ)
	if err != nil {
		name, executable, adapter = preference, "", nil
	}
	fingerprint := ""
	if execution != nil {
		fingerprint = execution.Fingerprint
	}
	key := strings.Join([]string{p.ID, p.RootPath, name, executable, ws, data, profileID, fingerprint}, "\x00")
	h.mu.Lock()
	if !reload {
		for _, c := range h.contexts {
			if c.key == key {
				h.mu.Unlock()
				return c, true, nil
			}
		}
	}
	for cid, c := range h.contexts {
		if c.key == key {
			if s := h.scopes[cid]; s != nil && s.Busy() {
				h.mu.Unlock()
				return nil, false, ErrBusy
			}
		}
	}
	for cid, c := range h.contexts {
		if c.key == key {
			h.closeLocked(cid, "reloaded")
		}
	}
	// Contexts whose scope runs an operation are never evicted (ADR M24.03, G4).
	for len(h.order) >= h.MaxContexts {
		victim := h.idleContextLocked()
		if victim == "" {
			h.mu.Unlock()
			return nil, false, errCapacity
		}
		h.closeLocked(victim, "evicted")
	}
	rootInfo, statErr := statIdentity(p.RootPath)
	ctx, cancel := context.WithCancel(h.base)
	info := workspace.Inspect(p.RootPath)
	info.Project = p.Alias
	c := &Context{ID: NewID(), ProjectID: p.ID, Alias: p.Alias, RootPath: p.RootPath, Engine: name, Executable: executable, Workspace: ws, DataDir: data, CreatedAt: h.Now().UTC().Format(time.RFC3339), Execution: execution, cancel: cancel, key: key, profileID: profileID}
	scope := workspace.Scope{Workspace: ws, ReadOnlyReason: ReadOnlyReason, Executable: h.ScopedExecution}
	scope.Check = func(ctx context.Context) *workspace.Diagnostic {
		list, err := adapter.ListWorkspaces(ctx)
		if err != nil {
			if errors.Is(err, engine.ErrUnsafeArgs) {
				return diag("unsafe_cli_args", "Remove unsupported TF_CLI_ARGS_workspace settings and restart Hclaro. Only -no-color is accepted.")
			}
			return diag("workspace_list", "Unable to list workspaces. Check backend access, credentials and SSO/VPN with the CLI, then read this workspace again.")
		}
		if !slices.Contains(list.Names, ws) {
			return diag("workspace_missing", "This workspace does not exist in the project. Choose another one, or create it from the project page.")
		}
		return nil
	}
	// The directory must still be the one that was opened when the snapshot is published.
	scope.Verify = func() *workspace.Diagnostic {
		if ctx.Err() != nil {
			return diag("cancelled", "The read was cancelled.")
		}
		now, err := os.Stat(p.RootPath)
		resolved, evalErr := filepath.EvalSymlinks(p.RootPath)
		if statErr != nil || err != nil || evalErr != nil || resolved != p.RootPath || !os.SameFile(rootInfo, now) {
			return diag("path_changed", "The project folder changed while it was being read, so the snapshot was discarded. Read the workspace again.")
		}
		return nil
	}
	c.store = workspace.NewScoped(info, scope)
	h.contexts[c.ID] = c
	h.order = append(h.order, c.ID)
	h.wg.Add(1)
	started = true
	h.mu.Unlock()
	_ = h.Registry.Touch(p.ID, ws, h.Now())
	go func() {
		defer h.wg.Done()
		defer release()
		select {
		case h.loads <- struct{}{}:
			defer func() { <-h.loads }()
		case <-ctx.Done():
			return
		}
		var read engine.Adapter
		if adapter != nil {
			read = readOnly{adapter}
		}
		c.store.Load(ctx, read, name)
	}()
	return c, false, nil
}

// readOnly hides every engine method except the three state reads, so a Hub context can
// never be type-asserted into a planner or applier.
type readOnly struct{ a Adapter }

func (r readOnly) Version(ctx context.Context) (string, error)   { return r.a.Version(ctx) }
func (r readOnly) Workspace(ctx context.Context) (string, error) { return r.a.Workspace(ctx) }
func (r readOnly) State(ctx context.Context) ([]byte, error)     { return r.a.State(ctx) }

// Context returns an open context, or the reason it was closed.
func (h *Hub) Context(cid string) (*Context, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.contexts[cid]; ok {
		return c, "", nil
	}
	return nil, h.closed[cid], ErrContextNotFound
}

// Listing is one directory in the folder chooser.
type Listing struct {
	Path      string   `json:"path"`
	Parent    string   `json:"parent,omitempty"`
	Home      string   `json:"home,omitempty"`
	Project   bool     `json:"project"`
	Entries   []string `json:"entries"`
	Truncated bool     `json:"truncated"`
}

const maxEntries = 500

// Browse lists the subdirectory names of one folder (the home directory by default) for
// the folder chooser. It never recurses, skips hidden names, and returns no file names.
func Browse(path string) (Listing, error) {
	home, _ := os.UserHomeDir()
	if path == "" {
		path = home
	}
	if path == "" || !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return Listing{}, ErrBrowse
	}
	dir, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return Listing{}, ErrBrowse
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Listing{}, ErrBrowse
	}
	l := Listing{Path: dir, Home: home, Entries: []string{}}
	if parent := filepath.Dir(dir); parent != dir {
		l.Parent = parent
	}
	_, rootErr := workspace.Root(dir)
	l.Project = rootErr == nil
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			fi, err := os.Stat(filepath.Join(dir, name))
			isDir = err == nil && fi.IsDir()
		}
		if !isDir {
			continue
		}
		if len(l.Entries) == maxEntries {
			l.Truncated = true
			break
		}
		l.Entries = append(l.Entries, name)
	}
	sort.Strings(l.Entries)
	return l, nil
}
