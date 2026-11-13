package projects

// Scoped execution for Hub workspaces (ADR M24.03, docs/architecture/adr-m24-scoped-execution.md).
//
// An execution scope is created on request for one open read context. It owns its own
// operations.Manager, bound to exactly that context's root, engine executable, workspace and
// composed environment, and shares the process-wide root claims with the launch manager.
// Its identity is fixed when it is created and revalidated before every engine command. A
// scope is never retargeted: anything that would change its identity closes it.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"hclaro/internal/discovery"
	"hclaro/internal/drift"
	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/imports"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// Stable codes of scope refusals (ADR M24.03, API contract).
const (
	CodeScopedDisabled       = "scoped_execution_disabled"
	CodeExecutionUnsupported = "execution_unsupported"
	CodeScopeChanged         = "scope_changed"
	CodeCapacityExceeded     = "capacity_exceeded"
)

// Details of CodeScopeChanged.
const (
	DetailRootChanged        = "root_changed"
	DetailEngineChanged      = "engine_changed"
	DetailProfileChanged     = "profile_changed"
	DetailWorkspaceMissing   = "workspace_missing"
	DetailBackendChanged     = "backend_changed"
	DetailDataDirChanged     = "data_dir_changed"
	DetailEnvironmentChanged = "environment_changed"
)

// ScopeError is a scope refusal. Nothing ran. Messages never contain paths other than the
// context's own, executable paths or environment values.
type ScopeError struct {
	Code    string
	Detail  string
	Message string
}

func (e *ScopeError) Error() string { return e.Message }

var (
	// ErrScopedDisabled answers every scoped request while the gate is off.
	ErrScopedDisabled = &ScopeError{Code: CodeScopedDisabled, Message: "Plans and applies in Hub workspaces are not enabled in this session. Nothing ran."}
	errCapacity       = &ScopeError{Code: CodeCapacityExceeded, Message: "Every open Hub workspace is running an operation or keeps a reviewed plan. Wait for an operation to finish, or close a workspace view you no longer need, then try again. Nothing was closed."}
)

func unsupportedScope(message string) *ScopeError {
	return &ScopeError{Code: CodeExecutionUnsupported, Message: message}
}

var scopeMessages = map[string]string{
	DetailRootChanged:        "The project folder was moved, replaced or removed from the Hub since this workspace was opened. Nothing ran. Open the workspace again.",
	DetailEngineChanged:      "The Terraform/OpenTofu executable or its version changed since this workspace was opened. Nothing ran. Open the workspace again.",
	DetailProfileChanged:     "The execution profile of this workspace was edited or removed. Nothing ran. Open the workspace again.",
	DetailWorkspaceMissing:   "This workspace no longer exists in the project, or workspaces could not be listed. Nothing ran.",
	DetailBackendChanged:     "The backend or its initialization changed since this workspace was opened. Nothing ran. Open the workspace again.",
	DetailDataDirChanged:     "The data directory (TF_DATA_DIR) changed since this workspace was opened. Nothing ran. Restart Hclaro without an absolute TF_DATA_DIR.",
	DetailEnvironmentChanged: "Environment settings that select accounts, regions, backends or proxies changed since this workspace was opened. Nothing ran. Open the workspace again.",
}

func changed(detail string) *ScopeError {
	return &ScopeError{Code: CodeScopeChanged, Detail: detail, Message: scopeMessages[detail]}
}

// ExecutorFactory resolves the full engine (planner and applier) for an execution scope:
// the same root, engine preference, selected workspace and composed environment as the
// scope's read context.
type ExecutorFactory func(root, preference, selected string, environ []string) (name, executable string, planner engine.Planner, err error)

// DefaultExecutors uses the official CLI found on PATH, exactly as DefaultEngines does.
func DefaultExecutors(root, preference, selected string, environ []string) (string, string, engine.Planner, error) {
	cli, err := engine.Detect(root, preference)
	if err != nil {
		return "", "", nil, err
	}
	cli.SelectedWorkspace, cli.IgnoreInheritedWorkspace, cli.Environ = selected, true, environ
	return cli.Name, cli.Executable, cli, nil
}

// identity is what a scope is. Every field is fixed at creation; see ADR M24.03, "Scope
// identity". It is never serialized.
type identity struct {
	projectID string
	root      string
	rootInfo  os.FileInfo
	engine    string
	// executable is the path the engine factory returned; resolved is it with symbolic
	// links evaluated, exeInfo its file identity and exeSHA its SHA-256.
	executable, resolved string
	exeInfo              os.FileInfo
	exeSHA               string
	version              string
	workspace            string
	profileID            string
	profileRevision      int
	profileFingerprint   string
	dataDir              string
	// envDigest is an HMAC with a per-process key; neither leaves the server.
	envDigest     string
	backend, mode string
}

// Scope is the execution scope of one open Hub read context.
type Scope struct {
	ContextID string
	hub       *Hub
	context   *Context
	id        identity
	// read runs the live version and workspace checks of revalidation; manager runs the
	// scope's operations.
	read    Adapter
	manager *operations.Manager
}

// ScopeView is the sanitized identity shown to clients: no executable path, environment
// or digest.
type ScopeView struct {
	ContextID     string        `json:"contextId"`
	ProjectID     string        `json:"projectId"`
	Alias         string        `json:"alias"`
	RootPath      string        `json:"rootPath"`
	Engine        string        `json:"engine"`
	Version       string        `json:"version"`
	Workspace     string        `json:"workspace"`
	Profile       *ScopeProfile `json:"profile"`
	Backend       string        `json:"backend"`
	ExecutionMode string        `json:"executionMode"`
}

// ScopeProfile names the execution profile of a scope.
type ScopeProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

// View returns the scope's display identity.
func (s *Scope) View() ScopeView {
	v := ScopeView{ContextID: s.ContextID, ProjectID: s.id.projectID, Alias: s.context.Alias, RootPath: s.id.root, Engine: s.id.engine, Version: s.id.version, Workspace: s.id.workspace, Backend: s.id.backend, ExecutionMode: s.id.mode}
	if s.id.profileID != "" && s.context.Execution != nil {
		v.Profile = &ScopeProfile{ID: s.id.profileID, Name: s.context.Execution.ProfileName, Revision: s.id.profileRevision}
	}
	return v
}

// Busy reports whether the scope is running or cancelling an operation.
func (s *Scope) Busy() bool { return s.manager.Running() }

// startPlan starts a plan in this scope. The execution profile and its fingerprint are the
// scope's own; a request cannot choose another target. It skips the reviewed-fingerprint
// and revalidation checks, so only StartReviewedPlan and tests call it.
func (s *Scope) startPlan(varFiles, replaceIDs []string) (operations.Operation, error) {
	req := operations.PlanStart{VarFiles: varFiles, Replace: replaceIDs}
	if s.id.profileID != "" {
		req.ProfileID, req.ContextFingerprint = s.id.profileID, s.id.profileFingerprint
	}
	return s.manager.StartPlanWith(req)
}

// StartReviewedPlan starts a plan requested over HTTP. reviewed is the execution profile
// fingerprint the user was shown ("" for a workspace opened without a profile); it must be
// the scope's own. The scope is revalidated first (files, registry, profile, environment,
// backend); a failure closes the context, as when the operation revalidates under the root
// claim. Nothing runs when an error is returned.
func (s *Scope) StartReviewedPlan(ctx context.Context, varFiles, replaceIDs []string, reviewed string) (operations.Operation, error) {
	if reviewed != s.id.profileFingerprint {
		return operations.Operation{}, &operations.RejectError{Code: "context_changed", Reason: "The execution context of this workspace is not the one you reviewed. Review the plan options again before planning."}
	}
	if err := s.checked(ctx); err != nil {
		return operations.Operation{}, err
	}
	return s.startPlan(varFiles, replaceIDs)
}

// checked revalidates the scope's files, registry, profile, environment and backend before
// a plan, preflight, approval or apply request is handled. A failure closes the context, so
// its saved plan, preflight binding and approval are deleted and cannot be replayed. While an
// operation runs, the request is refused but the context stays open: closing it would
// interrupt an apply that already passed its checks and leave its outcome uncertain. The
// next request after the operation ends closes it.
func (s *Scope) checked(ctx context.Context) error {
	if err := s.Revalidate(ctx); err != nil {
		if !s.Busy() {
			s.hub.closeContext(s.ContextID, CodeScopeChanged)
		}
		return err
	}
	return nil
}

// Preflight evaluates the apply checks of one of this scope's plans. A plan of another
// scope or of the launch project is not found.
func (s *Scope) Preflight(ctx context.Context, planID string, req operations.PreflightRequest) (operations.PreflightReport, error) {
	if _, ok := s.manager.Plan(planID); !ok {
		return operations.PreflightReport{}, operations.ErrNotFound
	}
	if err := s.checked(ctx); err != nil {
		return operations.PreflightReport{}, err
	}
	return s.manager.Preflight(ctx, planID, req)
}

// Approve issues the one-time approval of one of this scope's plans. The token lives in
// this scope's manager only, bound to the plan, its saved file digest and the preflight.
func (s *Scope) Approve(ctx context.Context, planID string, req operations.ApprovalRequest) (operations.Approval, error) {
	if _, ok := s.manager.Plan(planID); !ok {
		return operations.Approval{}, operations.ErrNotFound
	}
	if err := s.checked(ctx); err != nil {
		return operations.Approval{}, err
	}
	return s.manager.Approve(planID, req)
}

// StartApply consumes the approval of one of this scope's plans and applies its saved
// file. The scope is revalidated here and again, fully (engine version, workspace list and
// executable SHA-256), with the root claim held immediately before the engine runs.
func (s *Scope) StartApply(ctx context.Context, planID, token, typed string) (operations.Operation, error) {
	if _, ok := s.manager.Plan(planID); !ok {
		return operations.Operation{}, operations.ErrNotFound
	}
	if err := s.checked(ctx); err != nil {
		return operations.Operation{}, err
	}
	return s.manager.StartApply(planID, token, typed)
}

// PreviewImport checks an import request against this workspace's snapshot and root. It
// writes nothing and runs no engine command. The scope is revalidated first, so a changed
// project is refused instead of previewed against another folder.
func (s *Scope) PreviewImport(ctx context.Context, req imports.Request) (imports.Preview, error) {
	if err := s.checked(ctx); err != nil {
		return imports.Preview{}, err
	}
	return s.manager.PreviewImport(req), nil
}

// StartImport writes the reviewed import block into this scope's root only and starts a full
// plan there; the import is recorded only by applying that saved plan. reviewed is the profile
// fingerprint the user was shown, as for StartReviewedPlan. The scope is revalidated now and
// again with the root claim held, immediately before the file is written; nothing is written
// when either fails.
func (s *Scope) StartImport(ctx context.Context, req imports.Request, previewSHA256 string, varFiles []string, reviewed string) (operations.Operation, error) {
	if reviewed != s.id.profileFingerprint {
		return operations.Operation{}, &operations.RejectError{Code: "context_changed", Reason: "The execution context of this workspace is not the one you reviewed. Check the import again."}
	}
	if err := s.checked(ctx); err != nil {
		return operations.Operation{}, err
	}
	return s.manager.StartImport(operations.ImportRequest{Request: req, PreviewSHA256: previewSHA256, VarFiles: varFiles, ProfileID: s.id.profileID, ContextFingerprint: s.id.profileFingerprint})
}

// reviewedContext refuses a request whose profile fingerprint is not the scope's own.
func (s *Scope) reviewedContext(reviewed, again string) error {
	if reviewed != s.id.profileFingerprint {
		return &operations.RejectError{Code: "context_changed", Reason: "The execution context of this workspace is not the one you reviewed. " + again}
	}
	return nil
}

// DriftCapability reports whether a drift scan can run in this scope and, if not, why.
func (s *Scope) DriftCapability() (bool, string) { return s.manager.DriftCapability() }

// StartDriftScan starts a refresh-only scan of this workspace only (M24.09). reviewed is the
// profile fingerprint the user was shown, as for StartReviewedPlan; the scope is revalidated
// first and again with the root claim held. The scan never writes state and its plan file is
// never kept or applicable.
func (s *Scope) StartDriftScan(ctx context.Context, varFiles []string, reviewed string) (operations.Operation, operations.DriftScan, error) {
	if err := s.reviewedContext(reviewed, "Review the scan options again."); err != nil {
		return operations.Operation{}, operations.DriftScan{}, err
	}
	if err := s.checked(ctx); err != nil {
		return operations.Operation{}, operations.DriftScan{}, err
	}
	return s.manager.StartDriftScan(operations.DriftStart{VarFiles: varFiles, ProfileID: s.id.profileID, ContextFingerprint: s.id.profileFingerprint})
}

// DriftScans lists this scope's scans, newest first. A scope keeps at most as many scans
// as the launch project, for as long, and drops them all when it is closed or evicted.
func (s *Scope) DriftScans() []operations.DriftScanView { return s.manager.DriftScans() }

// DriftScan returns one of this scope's scans; a scan of another scope is not found.
func (s *Scope) DriftScan(id string) (operations.DriftScanView, bool) { return s.manager.DriftScan(id) }

// DriftDetail returns one resource of one of this scope's current scans.
func (s *Scope) DriftDetail(id, change string) (drift.Detail, error) {
	return s.manager.DriftDetail(id, change)
}

// GraphCapability reports whether a graph can be generated in this scope and, if not, why.
func (s *Scope) GraphCapability() (bool, string) { return s.manager.GraphCapability() }

// StartGraph runs `graph` for this workspace only (M24.09), after the same checks as
// StartDriftScan. It never plans, refreshes or applies.
func (s *Scope) StartGraph(ctx context.Context, reviewed string) (operations.Operation, operations.GraphRun, error) {
	if err := s.reviewedContext(reviewed, "Review the graph options again."); err != nil {
		return operations.Operation{}, operations.GraphRun{}, err
	}
	if err := s.checked(ctx); err != nil {
		return operations.Operation{}, operations.GraphRun{}, err
	}
	return s.manager.StartGraph(operations.GraphStart{ProfileID: s.id.profileID, ContextFingerprint: s.id.profileFingerprint})
}

// Graphs lists this scope's graphs, newest first; see DriftScans for retention.
func (s *Scope) Graphs() []operations.GraphView { return s.manager.Graphs() }

// Graph returns one of this scope's graphs, overlaid with one of this scope's plans.
func (s *Scope) Graph(id, planID string) (operations.GraphView, error) {
	return s.manager.Graph(id, planID)
}

// GraphImpact returns one node's neighbourhood in one of this scope's graphs.
func (s *Scope) GraphImpact(id, node, planID string) (operations.GraphImpact, error) {
	return s.manager.GraphImpact(id, node, planID)
}

// DiscoveryPin returns the AWS discovery context of this scope and the environment its AWS
// CLI calls use (nil: Hclaro's own). It runs nothing. With check set, for a request that
// contacts AWS, the reviewed fingerprint must be the scope's and the scope is revalidated
// first, as for a drift scan; a changed scope closes the context.
func (s *Scope) DiscoveryPin(ctx context.Context, check bool, reviewed string) (discovery.Pin, []string, error) {
	if check {
		if err := s.reviewedContext(reviewed, "Review the discovery context again."); err != nil {
			return discovery.Pin{}, nil, err
		}
		if err := s.checked(ctx); err != nil {
			return discovery.Pin{}, nil, err
		}
	}
	p, err := s.hub.Registry.Get(s.id.projectID)
	if err != nil || p.RootPath != s.id.root {
		return discovery.Pin{}, nil, changed(DetailRootChanged)
	}
	_, environ, execution, err := s.hub.execution(p, s.id.profileID)
	if err != nil || (execution == nil) != (s.id.profileID == "") || execution != nil && execution.Fingerprint != s.id.profileFingerprint {
		return discovery.Pin{}, nil, changed(DetailProfileChanged)
	}
	pin := discovery.Pin{Context: s.ContextID, ProjectID: s.id.projectID, Workspace: s.id.workspace, Engine: s.id.engine, ContextFingerprint: "inherited"}
	if execution != nil {
		pin.ProfileID, pin.ProfileName, pin.ContextFingerprint = execution.ProfileID, execution.ProfileName, execution.Fingerprint
		pin.AWSProfile, pin.AWSRegion = execution.AWSProfile, execution.AWSRegion
	}
	return pin, environ, nil
}

// VarFiles lists the variable files of the scope's root that a plan may select.
func (s *Scope) VarFiles() []string { return s.manager.VarFiles() }

// ProfileFingerprint is the fingerprint of the scope's execution profile, or "".
func (s *Scope) ProfileFingerprint() string { return s.id.profileFingerprint }

// Execution is the sanitized execution profile context of the scope, or nil.
func (s *Scope) Execution() *operations.ExecutionContext { return s.context.Execution.Clone() }

// Snapshot is the scope's read snapshot.
func (s *Scope) Snapshot() workspace.Snapshot { return s.context.Snapshot() }

// ContextStatus reports whether a plan's context changed and whether it could be applied.
func (s *Scope) ContextStatus(p *operations.Plan) (bool, operations.ApplyStatus) {
	return s.manager.ContextStatus(p)
}

// SourceStatus compares a plan's source fingerprint with the scope's root now.
func (s *Scope) SourceStatus(p *operations.Plan) gitctx.SourceStatus {
	return s.manager.SourceStatus(p)
}

// Holder names the kind of exclusive operation holding the scope's root, or "".
func (s *Scope) Holder() string {
	kind, _ := s.hub.Locks.Holder(s.id.root)
	return kind
}

// Plans returns this scope's reviewed plans, oldest first.
func (s *Scope) Plans() []*operations.Plan { return s.manager.Plans() }

// Plan returns one of this scope's plans. A plan of another scope or of the launch project
// is not found.
func (s *Scope) Plan(id string) (*operations.Plan, bool) { return s.manager.Plan(id) }

// Operations returns this scope's operations.
func (s *Scope) Operations() []operations.Operation { return s.manager.Operations() }

// Operation returns one of this scope's operations and its log.
func (s *Scope) Operation(id string) (operations.Operation, *operations.Log, bool) {
	return s.manager.Operation(id)
}

// Cancel cancels one of this scope's running operations.
func (s *Scope) Cancel(id string) (operations.Operation, error) { return s.manager.Cancel(id) }

// Execution returns the execution scope of an open read context, creating it on first use.
// It fails closed: the gate must be on, the context open and loaded, and its backend a
// known local-execution backend; the scope's identity must match the read context.
// Creating a scope may close the least recently used scope that is idle and keeps no saved
// plan, so only requests that start or prepare work call it; reads use Preview.
func (h *Hub) Execution(cid string) (*Scope, error) { return h.scope(cid, true) }

// Preview returns the execution scope of an open read context, or, when it has none, a
// scope built for display only: its identity is checked as for Execution, but it is not
// kept, closes nothing and cannot run anything. Options requests use it, so a GET never
// creates or evicts a scope.
func (h *Hub) Preview(cid string) (*Scope, error) { return h.scope(cid, false) }

func (h *Hub) scope(cid string, keep bool) (*Scope, error) {
	if !h.ScopedExecution {
		return nil, ErrScopedDisabled
	}
	if h.base.Err() != nil {
		return nil, operations.ErrClosed
	}
	c, _, err := h.Context(cid)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if s := h.scopes[cid]; s != nil {
		if keep {
			h.touchScopeLocked(cid)
		}
		h.mu.Unlock()
		return s, nil
	}
	h.mu.Unlock()
	snap := c.Snapshot()
	if snap.Status == "loading" {
		return nil, operations.ErrLoading
	}
	if !snap.Info.Capabilities.CanPlan {
		reason := snap.Info.Capabilities.PlanReason
		if reason == "" {
			reason = "Planning is not available for this workspace."
		}
		return nil, unsupportedScope(reason)
	}
	s, err := h.newScope(c, snap, keep)
	if err != nil {
		return nil, err
	}
	if !keep {
		s.manager.Close(0)
		return s, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, open := h.contexts[cid]; !open || h.base.Err() != nil {
		s.manager.Close(0)
		return nil, ErrContextNotFound
	}
	if existing := h.scopes[cid]; existing != nil {
		s.manager.Close(0)
		h.touchScopeLocked(cid)
		return existing, nil
	}
	for len(h.scopeOrder) >= h.MaxScopes {
		victim := h.idleScopeLocked()
		if victim == "" {
			s.manager.Close(0)
			return nil, errCapacity
		}
		h.closeScopeLocked(victim)
	}
	h.scopes[cid] = s
	h.scopeOrder = append(h.scopeOrder, cid)
	return s, nil
}

// ExistingScope returns the execution scope of an open read context without creating one;
// nil means the context has none (no plan was requested in it yet, or it was evicted). It
// fails like Execution when the gate is off or the context is closed.
func (h *Hub) ExistingScope(cid string) (*Scope, error) {
	if !h.ScopedExecution {
		return nil, ErrScopedDisabled
	}
	if _, _, err := h.Context(cid); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.scopes[cid], nil
}

// newScope resolves and pins the identity of c and builds its manager. Nothing runs except
// hashing the engine executable, which a display-only scope (hash false) skips: it never
// applies.
func (h *Hub) newScope(c *Context, snap workspace.Snapshot, hash bool) (*Scope, error) {
	p, err := h.Registry.Get(c.ProjectID)
	if err != nil || p.RootPath != c.RootPath {
		return nil, changed(DetailRootChanged)
	}
	if status, _ := check(p.RootPath); status != StatusReady {
		return nil, changed(DetailRootChanged)
	}
	rootInfo, err := statIdentity(p.RootPath)
	if err != nil {
		return nil, changed(DetailRootChanged)
	}
	preference, environ, execution, err := h.execution(p, c.profileID)
	if err != nil {
		return nil, changed(DetailProfileChanged)
	}
	if (execution == nil) != (c.Execution == nil) || execution != nil && (execution.Fingerprint != c.Execution.Fingerprint || execution.Revision != c.Execution.Revision) {
		return nil, changed(DetailProfileChanged)
	}
	data, shared := dataDir()
	if shared {
		return nil, unsupportedScope("TF_DATA_DIR is an absolute path, so every project would share one data directory. Restart Hclaro without it.")
	}
	if data != c.DataDir {
		return nil, changed(DetailDataDirChanged)
	}
	info := workspace.Inspect(p.RootPath)
	switch {
	case !info.Initialized:
		return nil, unsupportedScope("Backend initialization was not detected. Initialize the project first.")
	case info.ExecutionMode == "remote-exec":
		return nil, unsupportedScope("Remote execution backends are read-only in Hclaro.")
	case info.ExecutionMode != "local-cli" || info.Backend == "unknown" || info.Backend == "":
		return nil, unsupportedScope("The backend type could not be verified from initialization metadata, so local planning is not enabled.")
	case info.Backend != snap.Info.Backend || info.ExecutionMode != snap.Info.ExecutionMode:
		return nil, changed(DetailBackendChanged)
	}
	if snap.Info.Version == "" || snap.Info.Workspace != c.Workspace {
		return nil, unsupportedScope("This workspace has not been read successfully. Read it again first.")
	}
	if h.Executors == nil || h.Engines == nil {
		return nil, unsupportedScope("Engine execution is not available in this session.")
	}
	name, executable, planner, err := h.Executors(p.RootPath, preference, c.Workspace, environ)
	if err != nil || planner == nil {
		return nil, unsupportedScope("Terraform/OpenTofu was not found on PATH. Install a supported CLI, or change this project's engine.")
	}
	if name != c.Engine || executable != c.Executable || executable == "" {
		return nil, changed(DetailEngineChanged)
	}
	readName, readExecutable, read, err := h.Engines(p.RootPath, preference, c.Workspace, environ)
	if err != nil || read == nil || readName != name || readExecutable != executable {
		return nil, changed(DetailEngineChanged)
	}
	resolved, exeInfo, digest, err := executableIdentity(executable, hash)
	if err != nil {
		return nil, changed(DetailEngineChanged)
	}
	id := identity{
		projectID: p.ID, root: p.RootPath, rootInfo: rootInfo, engine: name, executable: executable, resolved: resolved, exeInfo: exeInfo, exeSHA: digest,
		version: snap.Info.Version, workspace: c.Workspace, profileID: c.profileID, dataDir: data, envDigest: h.envDigest(environ),
		backend: info.Backend, mode: info.ExecutionMode,
	}
	if execution != nil {
		id.profileRevision, id.profileFingerprint = execution.Revision, execution.Fingerprint
	}
	m := operations.NewManager(c.store, planner, p.RootPath)
	m.Locks = h.Locks
	m.TempDir = h.TempDir
	m.Git = h.Git
	if h.History != nil {
		m.Recorder = h.History
	}
	// The profile is the scope's own: any other profile is refused, and its planner is the
	// scope's, already bound to the profile's environment and the workspace.
	profileID := c.profileID
	m.Binder = func(requested string) (operations.Binding, error) {
		if requested != profileID {
			return operations.Binding{}, &operations.RejectError{Code: "profile_scope", Reason: "This workspace was opened with another execution profile. Open it with the profile you want to use."}
		}
		_, environ, execution, err := h.execution(p, requested)
		if err != nil || execution == nil {
			return operations.Binding{}, &operations.RejectError{Code: "context_changed", Reason: scopeMessages[DetailProfileChanged]}
		}
		return operations.Binding{Context: *execution, Environ: environ}, nil
	}
	m.Bind = func([]string, string) engine.Planner { return planner }
	// Apply preflight facts, as for the launch project. The identity probe contacts AWS only
	// when the user asks for it in the preflight.
	m.GitProbe, m.IdentityProbe = h.GitProbe, h.IdentityProbe
	// After an apply, only this workspace's snapshot is read again, with the root claim
	// still held; other contexts and the launch project are not touched.
	m.Reload = func(ctx context.Context) { c.store.Load(ctx, readOnly{read}, name) }
	s := &Scope{ContextID: c.ID, hub: h, context: c, id: id, read: read, manager: m}
	m.Verify = s.verify
	m.ImportBinding = c.ID
	return s, nil
}

// executableIdentity resolves an engine executable and returns its file identity and,
// when hash is set, its SHA-256.
func executableIdentity(path string, hash bool) (resolved string, info os.FileInfo, digest string, err error) {
	if !filepath.IsAbs(path) {
		return "", nil, "", errors.New("engine executable path is not absolute")
	}
	resolved, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, "", err
	}
	info, err = statIdentity(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, "", errors.New("engine executable is not a regular file")
	}
	if !hash {
		return resolved, info, "", nil
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", nil, "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", nil, "", err
	}
	return resolved, info, hex.EncodeToString(h.Sum(nil)), nil
}

// targetVariable reports whether an environment variable can change which account, region,
// backend, endpoint or engine behavior a command targets. See ADR M24.03, "Environment digest".
func targetVariable(key string) bool {
	upper := strings.ToUpper(key)
	for _, prefix := range []string{"TF_", "AWS_", "GOOGLE_", "CLOUDSDK_", "ARM_", "AZURE_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	switch upper {
	case "KUBECONFIG", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE":
		return true
	}
	return false
}

// envDigest is an HMAC over the target-relevant entries of a child environment (nil means
// Hclaro's own). TF_WORKSPACE is excluded: Hub children always replace it with the scope's
// workspace.
func (h *Hub) envDigest(environ []string) string {
	if environ == nil {
		environ = os.Environ()
	}
	keep := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "TF_WORKSPACE") || !targetVariable(key) {
			continue
		}
		keep = append(keep, item)
	}
	sort.Strings(keep)
	mac := hmac.New(sha256.New, h.envKey)
	for _, item := range keep {
		mac.Write([]byte(item))
		mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func newEnvKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// Revalidation depth.
const (
	// checkFiles reads the registry, profiles, environment and file metadata only.
	checkFiles = iota
	// checkEngine also runs `version` and `workspace list`.
	checkEngine
	// checkApply also hashes the engine executable again.
	checkApply
)

// Revalidate checks that the scope's identity still holds. It runs no engine command; the
// engine checks run in the operation itself (see verify). A failure is a *ScopeError.
func (s *Scope) Revalidate(ctx context.Context) error { return s.revalidate(ctx, checkFiles) }

func (s *Scope) revalidate(ctx context.Context, depth int) error {
	h, id := s.hub, s.id
	p, err := h.Registry.Get(id.projectID)
	if err != nil || p.RootPath != id.root {
		return changed(DetailRootChanged)
	}
	if status, _ := check(id.root); status != StatusReady {
		return changed(DetailRootChanged)
	}
	if now, err := os.Stat(id.root); err != nil || !os.SameFile(id.rootInfo, now) {
		return changed(DetailRootChanged)
	}
	resolved, exeInfo, _, err := executableIdentity(id.executable, false)
	if err != nil || resolved != id.resolved || !os.SameFile(id.exeInfo, exeInfo) || exeInfo.Size() != id.exeInfo.Size() || !exeInfo.ModTime().Equal(id.exeInfo.ModTime()) {
		return changed(DetailEngineChanged)
	}
	_, environ, execution, err := h.execution(p, id.profileID)
	if err != nil {
		return changed(DetailProfileChanged)
	}
	if id.profileID != "" && (execution == nil || execution.Revision != id.profileRevision || execution.Fingerprint != id.profileFingerprint) {
		return changed(DetailProfileChanged)
	}
	if data, shared := dataDir(); shared || data != id.dataDir {
		return changed(DetailDataDirChanged)
	}
	if h.envDigest(environ) != id.envDigest {
		return changed(DetailEnvironmentChanged)
	}
	if info := workspace.Inspect(id.root); !info.Initialized || info.Backend != id.backend || info.ExecutionMode != id.mode {
		return changed(DetailBackendChanged)
	}
	if depth < checkEngine {
		return nil
	}
	if version, err := s.read.Version(ctx); err != nil || version != id.version {
		return changed(DetailEngineChanged)
	}
	if list, err := s.read.ListWorkspaces(ctx); err != nil || !slices.Contains(list.Names, id.workspace) {
		return changed(DetailWorkspaceMissing)
	}
	if depth < checkApply {
		return nil
	}
	if _, _, digest, err := executableIdentity(id.executable, true); err != nil || digest != id.exeSHA {
		return changed(DetailEngineChanged)
	}
	return nil
}

// verify is the manager's Verify hook: it runs with the root claim held, before the first
// engine command of an operation. A scope change (code scope_changed) closes the context, so
// it cannot be used again; a failure after the operation's context ended is reported with
// another code and closes nothing.
func (s *Scope) verify(ctx context.Context, kind string) *operations.Diagnostic {
	depth := checkEngine
	if kind == "apply" {
		depth = checkApply
	}
	err := s.revalidate(ctx, depth)
	if err == nil {
		return nil
	}
	message := scopeMessages[DetailRootChanged]
	var scopeErr *ScopeError
	if errors.As(err, &scopeErr) {
		message = scopeErr.Message
	}
	if ctx.Err() != nil {
		// The operation was cancelled, timed out or shut down while revalidating; the
		// failure says nothing about the scope.
		return &operations.Diagnostic{Code: "stopped", Message: "The operation stopped before the engine ran."}
	}
	// Closing waits for this operation, so it cannot run on the operation's goroutine. It
	// cancels the operation's context, so the result is decided here, before that.
	go s.hub.closeContext(s.ContextID, CodeScopeChanged)
	return &operations.Diagnostic{Code: CodeScopeChanged, Message: message}
}

// closeContext closes a read context and its scope.
func (h *Hub) closeContext(cid, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeLocked(cid, reason)
}

// scopeCloseWait bounds how long closing a scope waits for its operation to stop before its
// saved plan is deleted. Shutdown uses the same bound as the launch manager.
const scopeCloseWait = 20 * time.Second

// closeScopeLocked removes the scope of cid. Its manager is closed in the background: the
// running operation is cancelled, then the retained plan file is deleted and its approvals
// dropped. The root claim is released when the operation ends. Caller holds h.mu.
func (h *Hub) closeScopeLocked(cid string) {
	s := h.scopes[cid]
	if s == nil {
		return
	}
	delete(h.scopes, cid)
	h.scopeOrder = slices.DeleteFunc(h.scopeOrder, func(id string) bool { return id == cid })
	if h.Discovery != nil {
		h.Discovery.Drop(cid)
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		s.manager.Close(scopeCloseWait)
	}()
}

func (h *Hub) touchScopeLocked(cid string) {
	h.scopeOrder = append(slices.DeleteFunc(h.scopeOrder, func(id string) bool { return id == cid }), cid)
}

// held reports whether closing the scope would lose work: an operation is running, or a
// reviewed saved plan (and with it any preflight or approval) is kept. Only an explicit
// close, a change of the project or profile, or expiry ends a held scope.
func (s *Scope) held() bool { return s.Busy() || s.manager.KeepsPlan() }

// idleScopeLocked returns the least recently used scope that is not held, or "".
func (h *Hub) idleScopeLocked() string {
	for _, cid := range h.scopeOrder {
		if !h.scopes[cid].held() {
			return cid
		}
	}
	return ""
}

// idleContextLocked returns the oldest read context whose scope, if any, is not held, or "".
// A context still loading is idle: closing it cancels the read, as before.
func (h *Hub) idleContextLocked() string {
	for _, cid := range h.order {
		if s := h.scopes[cid]; s == nil || !s.held() {
			return cid
		}
	}
	return ""
}
