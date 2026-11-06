// Package operations runs explicitly requested engine operations one at a time: plans,
// and applies of a reviewed saved plan after a one-time approval (see apply.go).
package operations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/imports"
	"hclaro/internal/plan"
	"hclaro/internal/rootlock"
	"hclaro/internal/workspace"
)

const (
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
	Cancelled = "cancelled"
)

var (
	ErrBusy        = errors.New("another operation is running")
	ErrUnavailable = errors.New("planning is unavailable for this workspace")
	ErrLoading     = errors.New("the workspace is still being read")
	ErrVarFiles    = errors.New("invalid variable file selection")
	ErrReplace     = errors.New("invalid replace selection")
	ErrNotFound    = errors.New("operation not found")
	ErrNotRunning  = errors.New("operation is not running")
	ErrClosed      = errors.New("hclaro is shutting down")
)

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Operation struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	Stage       string   `json:"stage"`
	StartedAt   string   `json:"startedAt"`
	CompletedAt string   `json:"completedAt,omitempty"`
	DurationMs  int64    `json:"durationMs"`
	ExitCode    *int     `json:"exitCode,omitempty"`
	Command     []string `json:"command"`
	VarFiles    []string `json:"varFiles"`
	Replace     []string `json:"replace"`
	PlanID      string   `json:"planId,omitempty"`
	// Import is set for import plans.
	Import *ImportTarget `json:"import,omitempty"`
	// Outcome is set for applies: applied, not_applied or uncertain.
	Outcome         string             `json:"outcome,omitempty"`
	StateReloaded   bool               `json:"stateReloaded"`
	Diagnostic      *Diagnostic        `json:"diagnostic,omitempty"`
	Diagnostics     []EngineDiagnostic `json:"engineDiagnostics"`
	CancelRequested bool               `json:"cancelRequested"`
	LogDropped      int64              `json:"logDropped"`
	LogRetained     bool               `json:"logRetained"`
	// ProjectID, Notices and LockfileChanged are set for Hub initializations. Notices disclose
	// inherited settings that were ignored; LockfileChanged is set once the engine has run.
	ProjectID       string   `json:"projectId,omitempty"`
	Notices         []string `json:"notices,omitempty"`
	LockfileChanged *bool    `json:"lockfileChanged,omitempty"`
	// Workspace is set for Hub workspace commands.
	Workspace *WorkspaceResult `json:"workspace,omitempty"`
	// Tool is set for Hub project utilities (validate, fmt -check, providers, outputs).
	Tool *ToolResult `json:"tool,omitempty"`
	// Context is the execution profile context the operation was bound to, if any.
	Context *ExecutionContext `json:"executionContext,omitempty"`
	// DriftScanID is set for drift scans (kind "drift").
	DriftScanID string `json:"driftScanId,omitempty"`
}

// Plan is a reviewed, redacted plan. It is immutable once stored; whether its saved
// artifact can still be applied is tracked separately (see ApplyStatus).
type Plan struct {
	plan.Review
	ID          string   `json:"id"`
	OperationID string   `json:"operationId"`
	CreatedAt   string   `json:"createdAt"`
	Engine      string   `json:"engine"`
	Workspace   string   `json:"workspace"`
	VarFiles    []string `json:"varFiles"`
	// Kind is "plan", "replace" or "import"; Replace lists the requested instance addresses.
	Kind    string        `json:"kind"`
	Replace []string      `json:"replace"`
	Import  *ImportTarget `json:"import,omitempty"`
	// GeneratedConfig is the redacted display copy of configuration the engine generated
	// for an import; the file on disk is not changed.
	GeneratedConfig          string `json:"generatedConfig,omitempty"`
	GeneratedConfigTruncated bool   `json:"generatedConfigTruncated,omitempty"`
	ArtifactSHA256           string `json:"artifactSha256"`
	Fingerprint              string `json:"fingerprint"`
	// Destructive plans delete, replace or forget objects, or contain unrecognized actions.
	Destructive bool `json:"destructive"`
	// Context is the execution profile context the plan was created with, if any.
	Context *ExecutionContext `json:"executionContext,omitempty"`
}

type entry struct {
	op     Operation
	log    *Log
	cancel context.CancelFunc
	start  time.Time
	// release gives up the project root claim; it is called once the operation ends.
	release func()
	// environ is the composed child environment of a profile-bound operation; nil inherits
	// Hclaro's environment. It is never serialized.
	environ []string
}

type Manager struct {
	mu      sync.Mutex
	store   *workspace.Store
	planner engine.Planner
	applier engine.Applier
	root    string
	TempDir string
	Timeout time.Duration
	// ApplyTimeout bounds an apply; PlanMaxAge bounds how long a saved plan stays
	// applicable; ApprovalTTL bounds how long a confirmation token is valid.
	ApplyTimeout time.Duration
	PlanMaxAge   time.Duration
	ApprovalTTL  time.Duration
	// Reload re-reads state after an apply. It is nil in tests that do not need it.
	Reload func(context.Context)
	// Reinspect reads the launch project's folder metadata again. With Reload it lets
	// Reread pick up changes made outside Hclaro's plans, such as an init. Nil disables Reread.
	Reinspect func() workspace.Info
	// Locks, when set, is shared with Hub initialization so a plan, import or apply never
	// runs in a root that is being initialized, and the other way around.
	Locks *rootlock.Set
	// Binder resolves execution profiles of the launch project; nil disables profiles.
	// Bind returns the planner for a profile's environment and workspace; nil keeps the
	// launch planner (tests).
	Binder   Binder
	Bind     func(environ []string, workspace string) engine.Planner
	entries  []*entry
	plans    []*Plan
	scans    []*DriftScan
	running  *entry
	artifact *artifact
	// notes explains why a plan cannot be applied; bounded by the plan history.
	notes  map[string]string
	base   context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	closed bool
	// NewID creates operation and plan IDs. Synthetic helpers replace it for determinism.
	NewID func() string
}

const (
	maxOperations = 100
	maxLogs       = 10
	maxPlans      = 10
	maxDiags      = 50
	logEvents     = 10_000
	logBytes      = 2 << 20
)

func NewManager(store *workspace.Store, planner engine.Planner, root string) *Manager {
	base, stop := context.WithCancel(context.Background())
	applier, _ := planner.(engine.Applier)
	return &Manager{store: store, planner: planner, applier: applier, root: root, Timeout: time.Hour, ApplyTimeout: 2 * time.Hour, PlanMaxAge: time.Hour, ApprovalTTL: 5 * time.Minute, notes: map[string]string{}, base: base, stop: stop, NewID: newID}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func engineLabel(name string) string {
	if name == "tofu" {
		return "OpenTofu"
	}
	return "Terraform"
}

// StartPlan validates the request and starts a plan in the background. replaceIDs are
// opaque resource IDs from the current snapshot; only managed instances are accepted and
// each becomes one -replace argument with the exact address from state.
func (m *Manager) StartPlan(varFiles, replaceIDs []string) (Operation, error) {
	return m.StartPlanWith(PlanStart{VarFiles: varFiles, Replace: replaceIDs})
}

// StartPlanWith starts a plan, bound to an execution profile when req.ProfileID is set.
func (m *Manager) StartPlanWith(req PlanStart) (Operation, error) {
	varFiles, replaceIDs := req.VarFiles, req.Replace
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Operation{}, ErrClosed
	}
	if m.running != nil {
		return Operation{}, ErrBusy
	}
	snap := m.store.Get()
	if snap.Status == "loading" {
		return Operation{}, ErrLoading
	}
	if m.planner == nil || !snap.Info.Capabilities.CanPlan {
		return Operation{}, ErrUnavailable
	}
	bound, environ, err := m.bind(snap, req.ProfileID, req.ContextFingerprint)
	if err != nil {
		return Operation{}, err
	}
	selected, err := m.selectVarFiles(varFiles, bound)
	if err != nil {
		return Operation{}, err
	}
	if len(replaceIDs) > engine.MaxReplace {
		return Operation{}, ErrReplace
	}
	replace := []string{}
	for _, id := range replaceIDs {
		resource, ok := snap.Index.ByID[id]
		if !ok || resource.Mode != "managed" || slices.Contains(replace, resource.Address) {
			return Operation{}, ErrReplace
		}
		replace = append(replace, resource.Address)
	}
	release, ok := m.Locks.Acquire(m.root, "plan")
	if !ok {
		return Operation{}, ErrBusy
	}
	return m.startPlanLocked(snap, Operation{VarFiles: selected, Replace: replace, Context: bound}, release, environ), nil
}

// maxVarFiles bounds one plan's variable file selection.
const maxVarFiles = 16

// selectVarFiles validates a variable file selection against the files in the root and,
// with a profile, the profile's approved files. A profile reference becomes the canonical
// root-relative path it resolved to, which is what the engine receives.
func (m *Manager) selectVarFiles(varFiles []string, c *ExecutionContext) ([]string, error) {
	allowed := workspace.VarFiles(m.root)
	if len(varFiles) > maxVarFiles {
		return nil, ErrVarFiles
	}
	selected := []string{}
	for _, name := range varFiles {
		path := name
		if c != nil && c.approved[name] != "" {
			path = c.approved[name]
		} else if !slices.Contains(allowed, name) {
			return nil, ErrVarFiles
		}
		if slices.Contains(selected, path) {
			return nil, ErrVarFiles
		}
		selected = append(selected, path)
	}
	return selected, nil
}

// startPlanLocked starts a validated plan operation. Caller holds mu.
func (m *Manager) startPlanLocked(snap workspace.Snapshot, op Operation, release func(), environ []string) Operation {
	command := []string{snap.Info.Engine, "plan", "-input=false", "-json", "-out=<private temporary file>"}
	for _, name := range op.VarFiles {
		command = append(command, "-var-file="+name)
	}
	if c := op.Context; c != nil {
		if c.Parallelism > 0 {
			command = append(command, fmt.Sprintf("-parallelism=%d", c.Parallelism))
		}
		if c.LockTimeoutSeconds > 0 {
			command = append(command, fmt.Sprintf("-lock-timeout=%ds", c.LockTimeoutSeconds))
		}
	}
	for _, address := range op.Replace {
		command = append(command, "-replace="+address)
	}
	if op.Import != nil && op.Import.GeneratedFile != "" {
		command = append(command, "-generate-config-out="+op.Import.GeneratedFile)
	}
	ctx, cancel := context.WithTimeout(m.base, m.Timeout)
	op.ID, op.Kind, op.Status, op.Stage = m.NewID(), "plan", Running, "Starting"
	op.StartedAt, op.Command, op.Diagnostics, op.LogRetained = time.Now().UTC().Format(time.RFC3339), command, []EngineDiagnostic{}, true
	e := &entry{op: op, log: NewLog(logEvents, logBytes), cancel: cancel, start: time.Now(), release: release, environ: environ}
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.wg.Add(1)
	go m.run(ctx, e, snap)
	return m.snapshot(e)
}

// trim keeps bounded history; logs are dropped first, metadata later. Caller holds mu.
func (m *Manager) trim() {
	if len(m.entries) > maxOperations {
		m.entries = m.entries[len(m.entries)-maxOperations:]
	}
	for i := 0; i < len(m.entries)-maxLogs; i++ {
		if m.entries[i] != m.running {
			m.entries[i].log = nil
			m.entries[i].op.LogRetained = false
		}
	}
}

func (m *Manager) update(e *entry, change func(*Operation)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(&e.op)
}

func (m *Manager) stage(e *entry, stage string) {
	m.update(e, func(o *Operation) { o.Stage = stage })
	e.log.Append("info", stage+"…")
}

var messages = map[string]string{
	"state_locked":    "The state is locked by another operation. Hclaro never force-unlocks state. Wait for the other operation to finish, or investigate the lock with your CLI.",
	"credentials":     "The engine could not authenticate with the backend or a provider. Refresh your credentials (for example, an SSO login) in your terminal, then run the plan again. Hclaro inherits credentials and never stores them.",
	"init_required":   "This working directory needs initialization, or modules or providers are missing. Run init yourself, then restart Hclaro. Nothing was initialized automatically.",
	"network":         "A backend or provider endpoint was unreachable. Check VPN and network access, then run the plan again.",
	"plan_failed":     "The plan failed. Review the engine diagnostics; lines that look like secrets are withheld.",
	"timeout":         "The plan exceeded its time limit and was stopped. Planning does not change infrastructure; if the engine was stopped forcefully, check for a leftover state lock with your CLI.",
	"cancelled":       "The plan was cancelled and no plan was kept. Planning does not change infrastructure. If the engine had to be stopped forcefully, a state lock may remain; check with your CLI before retrying, and never force-unlock while another operation may be running.",
	"unsafe_cli_args": "Inherited TF_CLI_ARGS or TF_CLI_ARGS_plan contains flags other than -no-color. Injected flags could change what a reviewed plan contains, so Hclaro refused to run. Values were not logged.",
	"workspace_error": "The active workspace could not be confirmed before planning. Check workspace selection with your CLI.",
	"plan_read":       "The saved plan could not be read back as JSON. Check that the engine version supports `show -json` for saved plans.",
	"plan_format":     "The plan JSON could not be safely decoded. It may use an unsupported format or contain secret-shaped resource addresses. No plan data was returned.",
	"shutdown":        "Hclaro stopped while the plan was running. No plan was kept.",
}

func (m *Manager) run(ctx context.Context, e *entry, snap workspace.Snapshot) {
	defer m.wg.Done()
	defer e.cancel()
	result, kept, code, exitCode := m.execute(ctx, e, snap)
	m.mu.Lock()
	o := &e.op
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	o.ExitCode = exitCode
	o.Stage = "Finished"
	switch {
	case result != nil:
		o.Status = Succeeded
		o.PlanID = result.ID
		m.plans = append(m.plans, result)
		if len(m.plans) > maxPlans {
			for _, old := range m.plans[:len(m.plans)-maxPlans] {
				delete(m.notes, old.ID)
			}
			m.plans = m.plans[len(m.plans)-maxPlans:]
		}
		m.retainLocked(result, kept)
	case code == "cancelled" || code == "shutdown":
		o.Status = Cancelled
	default:
		o.Status = Failed
	}
	message := messages[code]
	if code != "" {
		if code == "workspace_changed" {
			message = fmt.Sprintf("The active workspace changed since Hclaro started (was %q). Restart Hclaro to review the current workspace.", snap.Info.Workspace)
		}
		o.Diagnostic = &Diagnostic{Code: code, Message: message}
	}
	o.LogDropped = e.log.Dropped()
	m.running = nil
	e.release()
	m.mu.Unlock()
	switch {
	case result != nil:
		e.log.Append("info", fmt.Sprintf("Plan ready for review: %d resource change(s), %d output change(s).", len(result.Changes), len(result.Outputs)))
		if kept == nil {
			e.log.Append("info", "The saved plan file was deleted after review data was extracted; this plan cannot be applied.")
		}
	case code != "":
		e.log.Append("error", message)
	}
	e.log.Finish()
}

// execute runs one plan. The private temp directory is removed on every return path,
// except when the plan is eligible for apply: then it is returned as an artifact.
func (m *Manager) execute(ctx context.Context, e *entry, snap workspace.Snapshot) (result *Plan, kept *artifact, code string, exitCode *int) {
	stopped := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case e.op.CancelRequested:
			return "cancelled"
		case m.closed:
			return "shutdown"
		}
		return "timeout"
	}
	m.stage(e, "Checking workspace")
	bound := e.op.Context
	planner := m.bound(e.environ, snap.Info.Workspace)
	if bound != nil {
		e.log.Append("info", "Execution profile "+bound.ProfileName+" (revision "+fmt.Sprint(bound.Revision)+"), workspace "+bound.Workspace+". Identity not verified.")
	}
	name, err := planner.Workspace(ctx)
	if ctx.Err() != nil {
		return nil, nil, stopped(), nil
	}
	if errors.Is(err, engine.ErrUnsafeArgs) {
		return nil, nil, "unsafe_cli_args", nil
	}
	if err != nil {
		return nil, nil, "workspace_error", nil
	}
	if name != snap.Info.Workspace {
		return nil, nil, "workspace_changed", nil
	}
	dir, err := os.MkdirTemp(m.TempDir, "hclaro-plan-")
	if err != nil {
		return nil, nil, "plan_failed", nil
	}
	defer func() {
		if kept == nil {
			_ = os.RemoveAll(dir)
		}
	}()
	// MkdirTemp creates 0700 directories; enforce it in case of an unusual umask policy.
	_ = os.Chmod(dir, 0o700)
	out := filepath.Join(dir, "plan.tfplan")
	// Taken before the engine runs, so edits made while planning count as a context change.
	// An engine-generated import configuration file is excluded here and checked below.
	generated := ""
	if e.op.Import != nil {
		generated = e.op.Import.GeneratedFile
	}
	fingerprint := m.fingerprintFor(snap, e.op.VarFiles, generated, bound, e.environ)
	m.stage(e, "Planning")
	label := engineLabel(snap.Info.Engine)
	environ := e.environ
	if environ == nil {
		environ = os.Environ()
	}
	inputs := collectKnown(m.root, e.op.VarFiles, environ)
	undecoded := false
	request := engine.PlanRequest{Out: out, VarFiles: e.op.VarFiles, Replace: e.op.Replace, GenerateConfigOut: generated}
	if bound != nil {
		request.Parallelism, request.LockTimeoutSeconds = bound.Parallelism, bound.LockTimeoutSeconds
	}
	err = planner.Plan(ctx, request, func(line []byte) {
		level, text, diag, ok := translate(line, "plan", label, inputs)
		if !ok {
			if !undecoded {
				undecoded = true
				e.log.Append("warn", "Some engine output was not in the expected machine-readable format and was withheld.")
			}
			return
		}
		if text != "" {
			e.log.Append(level, text)
		}
		if diag != nil {
			m.update(e, func(o *Operation) {
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
			return nil, nil, stopped(), nil
		case errors.Is(err, engine.ErrUnsafeArgs):
			return nil, nil, "unsafe_cli_args", nil
		case errors.As(err, &exit):
			code := exit.Code
			return nil, nil, m.classify(e, exit.Hint), &code
		}
		return nil, nil, "plan_failed", nil
	}
	zero := 0
	m.stage(e, "Reading plan")
	_ = os.Chmod(out, 0o600)
	digest, err := sha256File(out)
	if err != nil {
		return nil, nil, "plan_read", &zero
	}
	document, err := planner.ShowPlan(ctx, out)
	if ctx.Err() != nil {
		return nil, nil, stopped(), &zero
	}
	if err != nil {
		return nil, nil, "plan_read", &zero
	}
	review, err := plan.Parse(document)
	if err != nil {
		return nil, nil, "plan_format", &zero
	}
	kind := "plan"
	if len(e.op.Replace) > 0 {
		kind = "replace"
		review.Warnings = append(review.Warnings, replaceWarnings(review, e.op.Replace)...)
	}
	var generatedConfig string
	var generatedTruncated bool
	if e.op.Import != nil {
		kind = "import"
		review.Warnings = append(review.Warnings, importWarnings(review, e.op.Import.Address)...)
		if generated != "" {
			// The generated file is new; any other edit made while planning still counts.
			if m.fingerprintFor(snap, e.op.VarFiles, generated, bound, e.environ) == fingerprint {
				fingerprint = m.fingerprintFor(snap, e.op.VarFiles, "", bound, e.environ)
			}
			text, truncated, err := imports.ReadGenerated(m.root, generated)
			if err != nil {
				review.Warnings = append(review.Warnings, fmt.Sprintf("The engine did not write %s, or it could not be read. Check the plan diagnostics.", generated))
			}
			generatedConfig, generatedTruncated = imports.RedactGenerated(inputs.scrub(text)), truncated
		}
	}
	result = &Plan{
		Review: review, ID: m.NewID(), OperationID: e.op.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Engine: snap.Info.Engine, Workspace: name, VarFiles: e.op.VarFiles, Kind: kind, Replace: e.op.Replace,
		Import: e.op.Import, GeneratedConfig: generatedConfig, GeneratedConfigTruncated: generatedTruncated,
		ArtifactSHA256: digest, Fingerprint: fingerprint, Context: bound.Clone(),
		Destructive: review.Summary[plan.Delete]+review.Summary[plan.Replace]+review.Summary[plan.Forget]+review.Summary[plan.Unknown] > 0,
	}
	// The artifact holds secrets in plaintext. It is kept only when it could be applied;
	// otherwise the deferred cleanup removes it now.
	_, applies := planner.(engine.Applier)
	if snap.Info.Capabilities.CanApply && m.applier != nil && applies && ineligible(result) == "" {
		kept = &artifact{planID: result.ID, dir: dir, path: out, digest: digest, fingerprint: fingerprint, workspace: name, version: snap.Info.Version, engine: snap.Info.Engine, created: time.Now(), environ: slices.Clone(e.environ), context: bound.Clone()}
	}
	return result, kept, "", &zero
}

// classify prefers the structured diagnostics over the stderr hint.
func (m *Manager) classify(e *entry, hint string) string {
	m.mu.Lock()
	var text strings.Builder
	for _, d := range e.op.Diagnostics {
		text.WriteString(d.Summary + "\n" + d.Detail + "\n")
	}
	m.mu.Unlock()
	if code := engine.Classify(text.String()); code != "" {
		return code
	}
	if hint != "" {
		return hint
	}
	return "plan_failed"
}

// Fingerprint identifies the plan context: engine, version, workspace, variable file
// selection, configuration files, engine data directory and planning environment.
func (m *Manager) Fingerprint(snap workspace.Snapshot, varFiles []string) string {
	return m.fingerprintExcept(snap, varFiles, "")
}

func (m *Manager) fingerprintExcept(snap workspace.Snapshot, varFiles []string, skip string) string {
	return workspace.FingerprintExcept(m.root, os.Environ(), map[string]bool{skip: skip != ""}, append([]string{snap.Info.Engine, snap.Info.Version, snap.Info.Workspace}, varFiles...)...)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 1<<30)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VarFiles lists the variable files a plan request may select.
func (m *Manager) VarFiles() []string { return workspace.VarFiles(m.root) }

// Cancel requests cancellation of a running operation.
func (m *Manager) Cancel(id string) (Operation, error) {
	m.mu.Lock()
	e := m.find(id)
	if e == nil {
		m.mu.Unlock()
		return Operation{}, ErrNotFound
	}
	if e != m.running || e.op.Status != Running {
		op := e.op
		m.mu.Unlock()
		return op, ErrNotRunning
	}
	e.op.CancelRequested = true
	op := e.op
	m.mu.Unlock()
	e.log.Append("warn", "Cancellation requested. Waiting for the engine to stop…")
	e.cancel()
	return op, nil
}

func (m *Manager) find(id string) *entry {
	for _, e := range m.entries {
		if e.op.ID == id {
			return e
		}
	}
	return nil
}

func (m *Manager) Operations() []Operation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Operation, 0, len(m.entries))
	for i := len(m.entries) - 1; i >= 0; i-- {
		out = append(out, m.snapshot(m.entries[i]))
	}
	return out
}

func (m *Manager) snapshot(e *entry) Operation {
	op := e.op
	op.Command = slices.Clone(op.Command)
	op.VarFiles = slices.Clone(op.VarFiles)
	op.Replace = slices.Clone(op.Replace)
	if op.Import != nil {
		target := *op.Import
		op.Import = &target
	}
	op.Diagnostics = slices.Clone(op.Diagnostics)
	op.Context = op.Context.Clone()
	if op.Status == Running {
		op.DurationMs = time.Since(e.start).Milliseconds()
	}
	if e.log != nil {
		op.LogDropped = e.log.Dropped()
	}
	return op
}

func (m *Manager) Operation(id string) (Operation, *Log, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil {
		return Operation{}, nil, false
	}
	return m.snapshot(e), e.log, true
}

func (m *Manager) Plans() []*Plan {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Plan, 0, len(m.plans))
	for i := len(m.plans) - 1; i >= 0; i-- {
		out = append(out, m.plans[i])
	}
	return out
}

func (m *Manager) Plan(id string) (*Plan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plans {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}

// Close cancels any running operation, waits (bounded) for its cleanup and deletes any
// retained saved plan.
// Reread inspects the launch project's folder again and reads its state in the background,
// as at startup. It runs no init, plan or refresh. It is refused while a plan, apply or
// import runs, while another operation (such as a Hub init) holds the root, and while a
// read is already in progress.
func (m *Manager) Reread() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.closed:
		return ErrClosed
	case m.root == "" || m.Reinspect == nil || m.Reload == nil:
		return ErrUnavailable
	case m.running != nil:
		return ErrBusy
	case m.store.Get().Status == "loading":
		return ErrLoading
	}
	if _, busy := m.Locks.Holder(m.root); busy {
		return ErrBusy
	}
	m.store.Reset(m.Reinspect())
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.Reload(m.base)
	}()
	return nil
}

func (m *Manager) Close(wait time.Duration) {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.stop()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
	m.mu.Lock()
	m.releaseLocked("Hclaro stopped and deleted the saved plan.")
	m.mu.Unlock()
}
