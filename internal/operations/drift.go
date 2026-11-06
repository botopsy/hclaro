package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"hclaro/internal/drift"
	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

// Drift scan statuses. Only "completed" can carry a no-drift outcome.
const (
	ScanRunning   = "running"
	ScanCompleted = "completed"
	// ScanPartial: the engine reported errors or an incomplete plan, but some drift records
	// were read. Objects not listed were not necessarily checked.
	ScanPartial   = "partial"
	ScanFailed    = "failed"
	ScanCancelled = "cancelled"
)

// Outcomes of a completed scan.
const (
	OutcomeDrift = "drift_detected"
	// OutcomeCannotVerify: nothing known differs, but some values were unknown during the scan.
	OutcomeCannotVerify = "cannot_verify"
	// OutcomeNoDrift: an exhaustive, error-free scan found no differences.
	OutcomeNoDrift = "no_drift"
)

var (
	ErrScanNotFound = errors.New("drift scan not found")
	ErrScanStale    = errors.New("drift scan no longer matches the current context")
)

// DriftStart is a validated drift scan request; see PlanStart for the profile fields.
type DriftStart struct {
	VarFiles           []string
	ProfileID          string
	ContextFingerprint string
}

// DriftScan is the redacted record of one refresh-only scan of the launch workspace. It is
// kept in memory only, at most maxScans of them, for at most DriftMaxAge. No raw plan JSON,
// plan file or unredacted value is retained.
type DriftScan struct {
	ID          string `json:"id"`
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
	Outcome     string `json:"outcome,omitempty"`
	// Exhaustive is true only for a completed scan without error diagnostics: the only case in
	// which the absence of a record means the object matched.
	Exhaustive    bool                   `json:"exhaustive"`
	StartedAt     string                 `json:"startedAt"`
	CompletedAt   string                 `json:"completedAt,omitempty"`
	Engine        string                 `json:"engine"`
	EngineVersion string                 `json:"engineVersion"`
	Workspace     string                 `json:"workspace"`
	VarFiles      []string               `json:"varFiles"`
	Context       *ExecutionContext      `json:"executionContext,omitempty"`
	Summary       drift.Summary          `json:"summary"`
	Resources     []drift.ResourceChange `json:"resources"`
	Warnings      []string               `json:"warnings"`
	Diagnostic    *Diagnostic            `json:"diagnostic,omitempty"`
	Diagnostics   []EngineDiagnostic     `json:"engineDiagnostics"`
	fingerprint   string
	details       map[string]drift.Detail
	started       time.Time
}

// DriftScanView is a scan as returned to a client, with its freshness against the current
// launch context. A scan that no longer matches (another workspace, engine, configuration,
// variable files or execution profile) has its resources and details withheld.
type DriftScanView struct {
	DriftScan
	Current bool `json:"current"`
	// StaleReason explains why the scan no longer matches; empty while running.
	StaleReason string `json:"staleReason,omitempty"`
	Withheld    bool   `json:"withheld"`
}

const maxScans = 10

var driftMessages = map[string]string{
	"state_locked":      "The state is locked by another operation, so the scan could not read it. Hclaro never force-unlocks state. Wait for the other operation to finish, or investigate the lock with your CLI.",
	"credentials":       "The engine could not authenticate with the backend or a provider, or was denied access (for example, an expired SSO session or a missing permission). Sign in again in your terminal, check permissions, then scan again.",
	"init_required":     "This working directory needs initialization, or modules or providers are missing. Run init yourself, then read the project again. Nothing was initialized automatically.",
	"network":           "A backend or provider endpoint was unreachable. Check VPN and network access, then scan again.",
	"plan_failed":       "The refresh-only plan failed. Review the engine diagnostics; lines that look like secrets are withheld.",
	"timeout":           "The scan exceeded its time limit and was stopped. If the engine was stopped forcefully, check for a leftover state lock with your CLI.",
	"cancelled":         "The scan was cancelled. No state was written. If the engine had to be stopped forcefully, a state lock may remain; check with your CLI before retrying.",
	"unsafe_cli_args":   "Inherited TF_CLI_ARGS or TF_CLI_ARGS_plan contains flags other than -no-color. Injected flags could change what the scan does, so Hclaro refused to run. Values were not logged.",
	"workspace_error":   "The active workspace could not be confirmed before scanning. Check workspace selection with your CLI.",
	"plan_read":         "The refresh-only plan could not be read back as JSON. Check that the engine version supports `show -json` for saved plans.",
	"plan_format":       "The refresh-only plan JSON could not be safely decoded. It may use an unsupported format or contain secret-shaped resource addresses. No scan data was returned.",
	"not_refresh_only":  "The engine returned a plan that proposes resource changes, which a refresh-only plan never does. The result cannot be trusted as a drift scan and was discarded.",
	"shutdown":          "Hclaro stopped while the scan was running. No result was kept.",
	"workspace_changed": "The active workspace changed since Hclaro read this project. Read the project again before scanning.",
}

// DriftCapability reports whether a drift scan can run for the current launch snapshot and,
// when it cannot, why. It runs no command.
func (m *Manager) DriftCapability() (bool, string) {
	snap := m.store.Get()
	return m.driftCapability(snap)
}

func (m *Manager) driftCapability(snap workspace.Snapshot) (bool, string) {
	info := snap.Info
	switch {
	case m.planner == nil:
		return false, "Drift scans run only for a project opened from the terminal."
	case snap.Status == "loading":
		return false, "Hclaro is still reading the workspace."
	case info.ExecutionMode == "remote-exec":
		return false, "This workspace uses a remote or cloud backend that runs plans remotely. Hclaro cannot verify a refresh-only result produced there, so drift scans are unsupported. Use your CLI or the remote platform's own drift checks."
	case !info.Capabilities.CanPlan:
		reason := info.Capabilities.PlanReason
		if reason == "" {
			reason = "Planning is not available for this workspace."
		}
		return false, "A drift scan is a refresh-only plan. " + reason
	case !engine.RefreshOnlySupport(info.Engine, info.Version):
		return false, "This engine version is not in the range Hclaro has verified for refresh-only plans."
	case snap.Status == "empty":
		return false, "No state is recorded for this workspace, so there is nothing to compare with remote objects."
	}
	return true, ""
}

// StartDriftScan starts a refresh-only scan of the launch workspace in the background. It uses
// the same one-at-a-time operation slot and root claim as plans and applies. The refresh-only
// plan file is always deleted after its redacted result is extracted: it is never retained,
// listed as a plan or applicable, and the scan never runs refresh, apply or a normal plan.
func (m *Manager) StartDriftScan(req DriftStart) (Operation, DriftScan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Operation{}, DriftScan{}, ErrClosed
	}
	if m.running != nil {
		return Operation{}, DriftScan{}, ErrBusy
	}
	snap := m.store.Get()
	if snap.Status == "loading" {
		return Operation{}, DriftScan{}, ErrLoading
	}
	if ok, reason := m.driftCapability(snap); !ok {
		return Operation{}, DriftScan{}, reject("drift_unsupported", reason)
	}
	bound, environ, err := m.bind(snap, req.ProfileID, req.ContextFingerprint)
	if err != nil {
		return Operation{}, DriftScan{}, err
	}
	selected, err := m.selectVarFiles(req.VarFiles, bound)
	if err != nil {
		return Operation{}, DriftScan{}, err
	}
	release, ok := m.Locks.Acquire(m.root, "drift")
	if !ok {
		return Operation{}, DriftScan{}, ErrBusy
	}
	command := []string{snap.Info.Engine, "plan", "-input=false", "-json", "-out=<private temporary file>", "-refresh-only"}
	for _, name := range selected {
		command = append(command, "-var-file="+name)
	}
	if bound != nil {
		if bound.Parallelism > 0 {
			command = append(command, fmt.Sprintf("-parallelism=%d", bound.Parallelism))
		}
		if bound.LockTimeoutSeconds > 0 {
			command = append(command, fmt.Sprintf("-lock-timeout=%ds", bound.LockTimeoutSeconds))
		}
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(m.base, m.Timeout)
	op := Operation{ID: m.NewID(), Kind: "drift", Status: Running, Stage: "Starting", StartedAt: now.UTC().Format(time.RFC3339), Command: command, VarFiles: selected, Replace: []string{}, Diagnostics: []EngineDiagnostic{}, LogRetained: true, Context: bound}
	scan := &DriftScan{
		ID: m.NewID(), OperationID: op.ID, Status: ScanRunning, StartedAt: op.StartedAt, Engine: snap.Info.Engine,
		EngineVersion: snap.Info.Version, Workspace: snap.Info.Workspace, VarFiles: slices.Clone(selected), Context: bound.Clone(),
		Resources: []drift.ResourceChange{}, Warnings: []string{}, Diagnostics: []EngineDiagnostic{}, details: map[string]drift.Detail{}, started: now,
	}
	op.DriftScanID = scan.ID
	e := &entry{op: op, log: NewLog(logEvents, logBytes), cancel: cancel, start: now, release: release, environ: environ}
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.scans = append(m.scans, scan)
	if len(m.scans) > maxScans {
		m.scans = m.scans[len(m.scans)-maxScans:]
	}
	m.wg.Add(1)
	go m.runDrift(ctx, e, snap, scan)
	return m.snapshot(e), scan.clone(), nil
}

func (m *Manager) runDrift(ctx context.Context, e *entry, snap workspace.Snapshot, scan *DriftScan) {
	defer m.wg.Done()
	defer e.cancel()
	result, fingerprint, partial, code, exitCode := m.executeDrift(ctx, e, snap)
	m.mu.Lock()
	o := &e.op
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	o.ExitCode = exitCode
	o.Stage = "Finished"
	scan.CompletedAt = o.CompletedAt
	scan.fingerprint = fingerprint
	scan.Diagnostics = slices.Clone(o.Diagnostics)
	errorDiagnostics := false
	for _, d := range o.Diagnostics {
		if d.Severity == "error" {
			errorDiagnostics = true
		}
	}
	if result != nil {
		scan.Summary, scan.Resources, scan.details = result.Summary, result.Resources, result.Details
		scan.Warnings = append(scan.Warnings, result.Warnings...)
	}
	switch {
	case result != nil && !partial && !result.Incomplete && !errorDiagnostics && result.Summary.Unrecognized == 0:
		o.Status, scan.Status, scan.Exhaustive = Succeeded, ScanCompleted, true
		switch {
		case result.Summary.Changed+result.Summary.Missing > 0:
			scan.Outcome = OutcomeDrift
		case result.Summary.CannotVerify > 0:
			scan.Outcome = OutcomeCannotVerify
		default:
			scan.Outcome = OutcomeNoDrift
		}
	case result != nil:
		// Some records were read, but the scan cannot prove anything about objects it did not
		// list. Never a no-drift outcome.
		o.Status, scan.Status = Failed, ScanPartial
		if code == "" {
			code = "plan_failed"
		}
		if !result.Incomplete {
			scan.Warnings = append(scan.Warnings, "The scan did not complete. Listed objects did drift; objects not listed were not necessarily checked.")
		}
	case code == "cancelled" || code == "shutdown":
		o.Status, scan.Status = Cancelled, ScanCancelled
	default:
		o.Status, scan.Status = Failed, ScanFailed
	}
	if code != "" {
		d := &Diagnostic{Code: code, Message: driftMessages[code]}
		if d.Message == "" {
			d.Message = driftMessages["plan_failed"]
		}
		o.Diagnostic, scan.Diagnostic = d, d
	}
	o.LogDropped = e.log.Dropped()
	m.running = nil
	e.release()
	status, outcome := scan.Status, scan.Outcome
	summary := scan.Summary
	m.mu.Unlock()
	switch {
	case status == ScanCompleted && outcome == OutcomeNoDrift:
		e.log.Append("info", "Scan complete: no drift found in the objects recorded in state.")
	case status == ScanCompleted:
		e.log.Append("info", fmt.Sprintf("Scan complete: %d changed, %d missing, %d cannot be verified.", summary.Changed, summary.Missing, summary.CannotVerify))
	case status == ScanPartial:
		e.log.Append("error", "Scan incomplete: "+driftMessage(code))
	case code != "":
		e.log.Append("error", driftMessage(code))
	}
	e.log.Append("info", "The refresh-only plan file was deleted. It cannot be applied, and no state was written.")
	e.log.Finish()
}

func driftMessage(code string) string {
	if message := driftMessages[code]; message != "" {
		return message
	}
	return driftMessages["plan_failed"]
}

// executeDrift runs one refresh-only plan. The private temp directory is removed on every
// return path. partial is set when the engine failed but its plan could still be read.
func (m *Manager) executeDrift(ctx context.Context, e *entry, snap workspace.Snapshot) (result *drift.Result, fingerprint string, partial bool, code string, exitCode *int) {
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
	switch {
	case ctx.Err() != nil:
		return nil, "", false, stopped(), nil
	case errors.Is(err, engine.ErrUnsafeArgs):
		return nil, "", false, "unsafe_cli_args", nil
	case err != nil:
		return nil, "", false, "workspace_error", nil
	case name != snap.Info.Workspace:
		return nil, "", false, "workspace_changed", nil
	}
	dir, err := os.MkdirTemp(m.TempDir, "hclaro-drift-")
	if err != nil {
		return nil, "", false, "plan_failed", nil
	}
	// The refresh-only plan holds refreshed values in plaintext and, applied, would write
	// state. It is never kept.
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	out := filepath.Join(dir, "drift.tfplan")
	// Taken before the engine runs, so edits made while scanning make the result stale.
	fingerprint = m.fingerprintFor(snap, e.op.VarFiles, "", bound, e.environ)
	m.stage(e, "Scanning")
	environ := e.environ
	if environ == nil {
		environ = os.Environ()
	}
	inputs := collectKnown(m.root, e.op.VarFiles, environ)
	request := engine.PlanRequest{Out: out, VarFiles: e.op.VarFiles, RefreshOnly: true}
	if bound != nil {
		request.Parallelism, request.LockTimeoutSeconds = bound.Parallelism, bound.LockTimeoutSeconds
	}
	undecoded := false
	label := engineLabel(snap.Info.Engine)
	err = planner.Plan(ctx, request, func(line []byte) {
		level, text, diag, ok := translate(line, "drift", label, inputs)
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
			return nil, fingerprint, false, stopped(), nil
		case errors.Is(err, engine.ErrUnsafeArgs):
			return nil, fingerprint, false, "unsafe_cli_args", nil
		case errors.As(err, &exit):
			n := exit.Code
			code := m.classify(e, exit.Hint)
			// Some engines save an errored plan. Its records are real observations, but the scan
			// is incomplete; without a readable file there is no result at all.
			if _, statErr := os.Stat(out); statErr == nil {
				m.stage(e, "Reading partial result")
				if r, _, readErr := m.readDrift(ctx, planner, out); readErr == "" {
					return r, fingerprint, true, code, &n
				}
			}
			return nil, fingerprint, false, code, &n
		}
		return nil, fingerprint, false, "plan_failed", nil
	}
	zero := 0
	m.stage(e, "Reading scan result")
	r, stoppedRead, readErr := m.readDrift(ctx, planner, out)
	if stoppedRead {
		return nil, fingerprint, false, stopped(), &zero
	}
	if readErr != "" {
		return nil, fingerprint, false, readErr, &zero
	}
	return r, fingerprint, false, "", &zero
}

// readDrift reads a saved refresh-only plan back as JSON and classifies it.
func (m *Manager) readDrift(ctx context.Context, planner engine.Planner, path string) (*drift.Result, bool, string) {
	_ = os.Chmod(path, 0o600)
	document, err := planner.ShowPlan(ctx, path)
	if ctx.Err() != nil {
		return nil, true, ""
	}
	if err != nil {
		return nil, false, "plan_read"
	}
	r, err := drift.Analyze(document)
	switch {
	case errors.Is(err, drift.ErrNotRefreshOnly):
		return nil, false, "not_refresh_only"
	case err != nil:
		return nil, false, "plan_format"
	}
	return &r, false, ""
}

func (s *DriftScan) clone() DriftScan {
	out := *s
	out.VarFiles = slices.Clone(s.VarFiles)
	out.Context = s.Context.Clone()
	out.Resources = slices.Clone(s.Resources)
	out.Warnings = slices.Clone(s.Warnings)
	out.Diagnostics = slices.Clone(s.Diagnostics)
	if s.Diagnostic != nil {
		d := *s.Diagnostic
		out.Diagnostic = &d
	}
	out.details = nil
	return out
}

// DriftMaxAge bounds how long a scan result is kept. Results describe a moment in time.
const DriftMaxAge = 12 * time.Hour

// expireScansLocked drops scans older than DriftMaxAge. Caller holds mu.
func (m *Manager) expireScansLocked(now time.Time) {
	m.scans = slices.DeleteFunc(m.scans, func(s *DriftScan) bool {
		return s.Status != ScanRunning && now.Sub(s.started) > DriftMaxAge
	})
}

// DriftScans lists retained scans, newest first, with their freshness.
func (m *Manager) DriftScans() []DriftScanView {
	m.mu.Lock()
	m.expireScansLocked(time.Now())
	scans := make([]DriftScan, 0, len(m.scans))
	for i := len(m.scans) - 1; i >= 0; i-- {
		scans = append(scans, m.scans[i].clone())
	}
	m.mu.Unlock()
	out := make([]DriftScanView, 0, len(scans))
	for _, s := range scans {
		out = append(out, m.view(s))
	}
	return out
}

// DriftScan returns one scan with its freshness. A scan that no longer matches the current
// context has its resources withheld.
func (m *Manager) DriftScan(id string) (DriftScanView, bool) {
	s, ok := m.findScan(id)
	if !ok {
		return DriftScanView{}, false
	}
	return m.view(s), true
}

// DriftDetail returns the attribute observations of one resource of a current scan.
func (m *Manager) DriftDetail(id, changeID string) (drift.Detail, error) {
	m.mu.Lock()
	var scan *DriftScan
	for _, s := range m.scans {
		if s.ID == id {
			scan = s
		}
	}
	if scan == nil {
		m.mu.Unlock()
		return drift.Detail{}, ErrScanNotFound
	}
	copy := scan.clone()
	detail, found := scan.details[changeID]
	m.mu.Unlock()
	if view := m.view(copy); !view.Current {
		return drift.Detail{}, ErrScanStale
	}
	if !found {
		return drift.Detail{}, ErrScanNotFound
	}
	detail.Attributes = slices.Clone(detail.Attributes)
	detail.ResourceID = m.stateResourceID(detail.Address, detail.Deposed)
	return detail, nil
}

func (m *Manager) findScan(id string) (DriftScan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireScansLocked(time.Now())
	for _, s := range m.scans {
		if s.ID == id {
			return s.clone(), true
		}
	}
	return DriftScan{}, false
}

// view checks a scan against the current launch context: the same workspace, engine and
// version, and an unchanged context fingerprint (configuration, variable files, data
// directory, environment and execution profile). A running scan is reported as current.
func (m *Manager) view(s DriftScan) DriftScanView {
	v := DriftScanView{DriftScan: s}
	if s.Status == ScanRunning {
		v.Current = true
		return v
	}
	snap := m.store.Get()
	switch {
	case snap.Status == "loading":
		v.StaleReason = "The project is being read again. Scan again once it is ready."
	case snap.Info.Workspace != s.Workspace:
		v.StaleReason = fmt.Sprintf("This scan was for workspace %s; the project now reads workspace %s.", s.Workspace, snap.Info.Workspace)
	case snap.Info.Engine != s.Engine || snap.Info.Version != s.EngineVersion:
		v.StaleReason = "The engine or its version changed since this scan."
	case s.fingerprint == "":
		v.StaleReason = "This scan has no recorded context."
	default:
		current, reason := m.contextFingerprint(s.VarFiles, s.Context, "scan", "Scan again with the current profile.")
		switch {
		case reason != "":
			v.StaleReason = reason
		case current != s.fingerprint:
			v.StaleReason = "Configuration, variable files, the data directory or the planning environment changed since this scan. Scan again to see current drift."
		}
	}
	v.Current = v.StaleReason == ""
	if !v.Current {
		v.Resources, v.Withheld = []drift.ResourceChange{}, len(s.Resources) > 0
		return v
	}
	resources := make([]drift.ResourceChange, len(s.Resources))
	for i, r := range s.Resources {
		r.ResourceID = m.stateResourceID(r.Address, r.Deposed)
		resources[i] = r
	}
	v.Resources = resources
	return v
}

// stateResourceID returns the opaque explorer ID of an address in the current snapshot, or ""
// when it is not recorded there. Deposed objects have no explorer entry.
func (m *Manager) stateResourceID(address, deposed string) string {
	if deposed != "" {
		return ""
	}
	hash := sha256.Sum256([]byte(address))
	id := hex.EncodeToString(hash[:16])
	if r, ok := m.store.Get().Index.ByID[id]; ok && r.Address == address && r.Mode == "managed" {
		return id
	}
	return ""
}
