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
	"hclaro/internal/plan"
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
	// Outcome is set for applies: applied, not_applied or uncertain.
	Outcome         string             `json:"outcome,omitempty"`
	StateReloaded   bool               `json:"stateReloaded"`
	Diagnostic      *Diagnostic        `json:"diagnostic,omitempty"`
	Diagnostics     []EngineDiagnostic `json:"engineDiagnostics"`
	CancelRequested bool               `json:"cancelRequested"`
	LogDropped      int64              `json:"logDropped"`
	LogRetained     bool               `json:"logRetained"`
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
	// Kind is "plan" or "replace"; Replace lists the requested instance addresses.
	Kind           string   `json:"kind"`
	Replace        []string `json:"replace"`
	ArtifactSHA256 string   `json:"artifactSha256"`
	Fingerprint    string   `json:"fingerprint"`
	// Destructive plans delete, replace or forget objects, or contain unrecognized actions.
	Destructive bool `json:"destructive"`
}

type entry struct {
	op     Operation
	log    *Log
	cancel context.CancelFunc
	start  time.Time
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
	Reload   func(context.Context)
	entries  []*entry
	plans    []*Plan
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
	allowed := workspace.VarFiles(m.root)
	if len(varFiles) > 8 {
		return Operation{}, ErrVarFiles
	}
	selected := []string{}
	for _, name := range varFiles {
		if !slices.Contains(allowed, name) || slices.Contains(selected, name) {
			return Operation{}, ErrVarFiles
		}
		selected = append(selected, name)
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
	command := []string{snap.Info.Engine, "plan", "-input=false", "-json", "-out=<private temporary file>"}
	for _, name := range selected {
		command = append(command, "-var-file="+name)
	}
	for _, address := range replace {
		command = append(command, "-replace="+address)
	}
	ctx, cancel := context.WithTimeout(m.base, m.Timeout)
	e := &entry{
		op:     Operation{ID: m.NewID(), Kind: "plan", Status: Running, Stage: "Starting", StartedAt: time.Now().UTC().Format(time.RFC3339), Command: command, VarFiles: selected, Replace: replace, Diagnostics: []EngineDiagnostic{}, LogRetained: true},
		log:    NewLog(logEvents, logBytes),
		cancel: cancel,
		start:  time.Now(),
	}
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.wg.Add(1)
	go m.run(ctx, e, snap)
	return m.snapshot(e), nil
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
	name, err := m.planner.Workspace(ctx)
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
	fingerprint := m.Fingerprint(snap, e.op.VarFiles)
	m.stage(e, "Planning")
	label := engineLabel(snap.Info.Engine)
	inputs := collectKnown(m.root, e.op.VarFiles, os.Environ())
	undecoded := false
	err = m.planner.Plan(ctx, engine.PlanRequest{Out: out, VarFiles: e.op.VarFiles, Replace: e.op.Replace}, func(line []byte) {
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
	document, err := m.planner.ShowPlan(ctx, out)
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
	result = &Plan{
		Review: review, ID: m.NewID(), OperationID: e.op.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Engine: snap.Info.Engine, Workspace: name, VarFiles: e.op.VarFiles, Kind: kind, Replace: e.op.Replace,
		ArtifactSHA256: digest, Fingerprint: fingerprint,
		Destructive: review.Summary[plan.Delete]+review.Summary[plan.Replace]+review.Summary[plan.Forget]+review.Summary[plan.Unknown] > 0,
	}
	// The artifact holds secrets in plaintext. It is kept only when it could be applied;
	// otherwise the deferred cleanup removes it now.
	if snap.Info.Capabilities.CanApply && m.applier != nil && ineligible(result) == "" {
		kept = &artifact{planID: result.ID, dir: dir, path: out, digest: digest, fingerprint: fingerprint, workspace: name, version: snap.Info.Version, engine: snap.Info.Engine, created: time.Now()}
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
	return workspace.Fingerprint(m.root, os.Environ(), append([]string{snap.Info.Engine, snap.Info.Version, snap.Info.Workspace}, varFiles...)...)
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
	op.Diagnostics = slices.Clone(op.Diagnostics)
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
