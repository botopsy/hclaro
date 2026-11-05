package operations

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/plan"
)

// artifact is the single retained saved plan. The file holds plaintext secrets, so it lives
// only in a private 0700 temp directory and is applied at most once. It is deleted after an
// apply attempt, when it expires, when a newer plan supersedes it, and on shutdown.
type artifact struct {
	planID, dir, path, digest, fingerprint, workspace, version, engine string
	created                                                            time.Time
	timer                                                              *time.Timer
	approval                                                           *approval
	// applying is set while an apply uses the file; expiry then waits for the apply.
	applying bool
}

type approval struct {
	token   string
	expires time.Time
}

// ApplyStatus reports, at request time, whether a plan's saved artifact can be applied.
type ApplyStatus struct {
	Available        bool   `json:"available"`
	Reason           string `json:"reason,omitempty"`
	ArtifactRetained bool   `json:"artifactRetained"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
}

// Approval is a one-time, short-lived confirmation token bound to one plan and its saved
// artifact digest. Phrase, when set, must be typed back exactly.
type Approval struct {
	Token          string         `json:"confirmationToken"`
	ExpiresAt      string         `json:"expiresAt"`
	PlanID         string         `json:"planId"`
	ArtifactSHA256 string         `json:"artifactSha256"`
	Engine         string         `json:"engine"`
	Workspace      string         `json:"workspace"`
	Summary        map[string]int `json:"summary"`
	Replace        []string       `json:"replace"`
	Import         *ImportTarget  `json:"import,omitempty"`
	Imports        int            `json:"imports"`
	Destructive    bool           `json:"destructive"`
	Phrase         string         `json:"phrase,omitempty"`
	Command        []string       `json:"command"`
}

// RejectError explains why an approval or apply was refused. Nothing ran.
type RejectError struct {
	Code   string
	Reason string
}

func (e *RejectError) Error() string { return e.Reason }

func reject(code, reason string) error { return &RejectError{code, reason} }

// ineligible returns why a plan can never be applied, or "" when it can.
func ineligible(p *Plan) string {
	changes := 0
	for action, n := range p.Summary {
		if action != plan.NoOp && action != plan.Read {
			changes += n
		}
	}
	for _, o := range p.Outputs {
		if o.Action != plan.NoOp {
			changes++
		}
	}
	// An import with no attribute change is a no-op action that still writes state.
	changes += p.Imports
	switch {
	case p.Errored:
		return "The engine reported this plan as errored. It is incomplete and cannot be applied."
	case p.Deferred > 0:
		return "This plan has deferred changes, so it is incomplete. Hclaro does not apply incomplete plans."
	case !p.Applyable:
		return "The engine marked this plan as not applyable."
	case changes == 0:
		return "This plan has no changes to apply."
	}
	return ""
}

// replaceWarnings flags requested replacements the plan does not actually perform.
func replaceWarnings(review plan.Review, requested []string) []string {
	warnings := []string{}
	for _, address := range requested {
		found := false
		for _, c := range review.Changes {
			if c.Address == address && c.Deposed == "" && c.Action == plan.Replace {
				found = true
				break
			}
		}
		if !found {
			warnings = append(warnings, fmt.Sprintf("Replacement of %s was requested, but this plan does not replace it. Check that the instance still exists in configuration.", address))
		}
	}
	return warnings
}

// phrase is required for destructive plans and for imports, which write state.
func phrase(p *Plan) string {
	if !p.Destructive && p.Imports == 0 {
		return ""
	}
	return "apply " + p.Workspace
}

// retainLocked records a new plan's artifact, superseding any previous one. Caller holds mu.
func (m *Manager) retainLocked(p *Plan, kept *artifact) {
	if kept == nil {
		snap := m.store.Get()
		switch reason := ineligible(p); {
		case reason != "":
			m.notes[p.ID] = reason
		case !snap.Info.Capabilities.CanApply:
			m.notes[p.ID] = snap.Info.Capabilities.ApplyReason
		default:
			m.notes[p.ID] = "The saved plan file was not kept, so this plan cannot be applied."
		}
		return
	}
	m.releaseLocked("A newer plan replaced this one. Only the most recent applicable plan is kept.")
	if m.closed {
		_ = os.RemoveAll(kept.dir)
		m.notes[p.ID] = "Hclaro stopped and deleted the saved plan."
		return
	}
	m.artifact = kept
	kept.timer = time.AfterFunc(m.PlanMaxAge, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.artifact == kept && !kept.applying {
			m.releaseLocked("This plan expired and its saved file was deleted. Run a new plan; infrastructure may have changed since.")
		}
	})
}

// releaseLocked deletes the retained artifact and records why. Caller holds mu.
func (m *Manager) releaseLocked(note string) {
	a := m.artifact
	if a == nil {
		return
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	_ = os.RemoveAll(a.dir)
	m.artifact = nil
	m.notes[a.planID] = note
}

// ApplyStatus reports whether p can be applied now. currentFingerprint is the context
// fingerprint recomputed by the caller.
func (m *Manager) ApplyStatus(p *Plan, currentFingerprint string) ApplyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(p, currentFingerprint); err != nil {
		retained := m.artifact != nil && m.artifact.planID == p.ID
		return ApplyStatus{Reason: err.Error(), ArtifactRetained: retained}
	}
	return ApplyStatus{Available: true, ArtifactRetained: true, ExpiresAt: m.artifact.created.Add(m.PlanMaxAge).UTC().Format(time.RFC3339)}
}

// checkLocked verifies everything that can be checked without running the engine.
// Caller holds mu.
func (m *Manager) checkLocked(p *Plan, currentFingerprint string) error {
	snap := m.store.Get()
	a := m.artifact
	switch {
	case m.notes[p.ID] != "":
		return reject("not_applicable", m.notes[p.ID])
	case a == nil || a.planID != p.ID:
		return reject("not_applicable", "The saved plan file is no longer kept. Run a new plan.")
	case m.closed:
		return reject("shutting_down", "Hclaro is shutting down.")
	case m.running != nil:
		return reject("operation_running", "Another operation is running. Wait for it to finish.")
	case snap.Status == "loading":
		return reject("workspace_loading", "Hclaro is still reading the workspace.")
	case !snap.Info.Capabilities.CanApply:
		return reject("not_applicable", snap.Info.Capabilities.ApplyReason)
	case time.Since(a.created) >= m.PlanMaxAge:
		return reject("plan_expired", "This plan expired. Run a new plan; infrastructure may have changed since.")
	case snap.Info.Engine != a.engine || snap.Info.Version != a.version || snap.Info.Workspace != a.workspace:
		return reject("context_changed", "The engine, its version or the workspace changed since this plan was created. Run a new plan.")
	case currentFingerprint == "" || currentFingerprint != a.fingerprint:
		return reject("context_changed", "Configuration, variable files, the lock file or the planning environment changed since this plan was created. Run a new plan.")
	}
	return nil
}

func (m *Manager) currentFingerprint(p *Plan) string {
	return m.Fingerprint(m.store.Get(), p.VarFiles)
}

func applyCommand(a *artifact) []string {
	return []string{a.engine, "apply", "-input=false", "-json", "<reviewed saved plan " + a.digest[:12] + ">"}
}

// Approve issues a one-time confirmation token for applying plan id. A new approval
// replaces any earlier unused one.
func (m *Manager) Approve(id string) (Approval, error) {
	p, ok := m.Plan(id)
	if !ok {
		return Approval{}, ErrNotFound
	}
	current := m.currentFingerprint(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(p, current); err != nil {
		return Approval{}, err
	}
	a := m.artifact
	a.approval = &approval{token: newID() + newID(), expires: time.Now().Add(m.ApprovalTTL)}
	summary := map[string]int{}
	for k, v := range p.Summary {
		summary[k] = v
	}
	return Approval{
		Token: a.approval.token, ExpiresAt: a.approval.expires.UTC().Format(time.RFC3339), PlanID: p.ID,
		ArtifactSHA256: a.digest, Engine: a.engine, Workspace: a.workspace, Summary: summary,
		Replace: slices.Clone(p.Replace), Import: p.Import, Imports: p.Imports, Destructive: p.Destructive, Phrase: phrase(p), Command: applyCommand(a),
	}, nil
}

// StartApply consumes the approval token and applies the reviewed saved plan. The token is
// single-use: any attempt to use it, valid or not, invalidates it.
func (m *Manager) StartApply(id, token, typed string) (Operation, error) {
	p, ok := m.Plan(id)
	if !ok {
		return Operation{}, ErrNotFound
	}
	current := m.currentFingerprint(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.artifact
	if a == nil || a.planID != p.ID || a.approval == nil {
		return Operation{}, reject("approval_required", "Approve this plan before applying it.")
	}
	granted := a.approval
	a.approval = nil
	if subtle.ConstantTimeCompare([]byte(token), []byte(granted.token)) != 1 || time.Now().After(granted.expires) {
		return Operation{}, reject("approval_invalid", "The confirmation expired or was already used. Review the plan and confirm again.")
	}
	if want := phrase(p); want != "" && subtle.ConstantTimeCompare([]byte(typed), []byte(want)) != 1 {
		return Operation{}, reject("confirmation_mismatch", "The typed confirmation did not match. Nothing was applied.")
	}
	if err := m.checkLocked(p, current); err != nil {
		return Operation{}, err
	}
	a.applying = true
	ctx, cancel := context.WithTimeout(m.base, m.ApplyTimeout)
	e := &entry{
		op:     Operation{ID: m.NewID(), Kind: "apply", Status: Running, Stage: "Starting", StartedAt: time.Now().UTC().Format(time.RFC3339), Command: applyCommand(a), VarFiles: slices.Clone(p.VarFiles), Replace: slices.Clone(p.Replace), Import: p.Import, PlanID: p.ID, Outcome: "not_applied", Diagnostics: []EngineDiagnostic{}, LogRetained: true},
		log:    NewLog(logEvents, logBytes),
		cancel: cancel,
		start:  time.Now(),
	}
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.wg.Add(1)
	go m.runApply(ctx, e, a)
	return m.snapshot(e), nil
}

var applyMessages = map[string]string{
	"applied":           "Apply finished. The state view was reloaded from the engine.",
	"workspace_changed": "The active workspace no longer matches this plan. Nothing was applied.",
	"workspace_error":   "The active workspace could not be confirmed. Nothing was applied.",
	"artifact_changed":  "The saved plan file changed after review. Nothing was applied, and the file was deleted.",
	"unsafe_cli_args":   "Inherited TF_CLI_ARGS or TF_CLI_ARGS_apply contains flags other than -no-color. Nothing was applied. Values were not logged.",
	"stale_plan":        "The engine refused the saved plan because state changed after it was created. Nothing was applied. Run a new plan and review it again.",
	"state_locked":      "The state is locked by another operation, so the engine did not apply. Hclaro never force-unlocks state.",
	"credentials":       "The engine could not authenticate with the backend or a provider. Some changes may have been made before the failure. Review the reloaded state and refresh your credentials.",
	"network":           "A backend or provider endpoint was unreachable. Some changes may have been made before the failure. Review the reloaded state.",
	"init_required":     "The engine reported missing initialization. Nothing was initialized automatically.",
	"apply_failed":      "The apply failed. Some changes may have been made before the error. Review the diagnostics and the reloaded state; the saved plan cannot be reused.",
	"apply_cancelled":   "The apply was interrupted. Infrastructure may be partly changed: resources being created or destroyed when it stopped can be incomplete. Review the reloaded state and run a new plan. If the engine had to be stopped forcefully, a state lock may remain.",
	"apply_timeout":     "The apply exceeded its time limit and was stopped. Infrastructure may be partly changed. Review the reloaded state and run a new plan.",
	"shutdown":          "Hclaro stopped while the apply was running. Infrastructure may be partly changed. Check state with your CLI.",
}

func (m *Manager) runApply(ctx context.Context, e *entry, a *artifact) {
	defer m.wg.Done()
	defer e.cancel()
	code, exitCode, started := m.executeApply(ctx, e, a)
	m.mu.Lock()
	// A saved plan is applied at most once: the file is deleted whatever the result.
	note := "This plan was applied. A saved plan can only be applied once."
	if code != "applied" {
		note = "An apply of this plan was attempted. The saved file was deleted; run a new plan."
	}
	if !started {
		note = "The apply was refused before the engine ran, and the saved file was deleted. Run a new plan."
	}
	if m.artifact == a {
		m.releaseLocked(note)
	} else {
		_ = os.RemoveAll(a.dir)
	}
	m.mu.Unlock()
	reloaded := false
	if started && m.Reload != nil && m.base.Err() == nil {
		m.stage(e, "Reloading state")
		m.Reload(m.base)
		reloaded = true
	}
	m.mu.Lock()
	o := &e.op
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	o.ExitCode = exitCode
	o.Stage = "Finished"
	o.StateReloaded = reloaded
	switch {
	case code == "applied":
		o.Status = Succeeded
		o.Outcome = "applied"
	case code == "apply_cancelled" || code == "shutdown":
		o.Status = Cancelled
	default:
		o.Status = Failed
	}
	if code != "applied" && started && code != "stale_plan" && code != "state_locked" {
		o.Outcome = "uncertain"
	}
	message := applyMessages[code]
	if message == "" {
		message = applyMessages["apply_failed"]
	}
	if code != "applied" {
		o.Diagnostic = &Diagnostic{Code: code, Message: message}
	}
	o.LogDropped = e.log.Dropped()
	m.running = nil
	m.mu.Unlock()
	level := "info"
	if code != "applied" {
		level = "error"
	}
	e.log.Append(level, message)
	e.log.Finish()
}

// executeApply re-verifies the workspace and the artifact digest, then applies. started
// reports whether the engine apply command was run.
func (m *Manager) executeApply(ctx context.Context, e *entry, a *artifact) (code string, exitCode *int, started bool) {
	stopped := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case e.op.CancelRequested:
			return "apply_cancelled"
		case m.closed:
			return "shutdown"
		}
		return "apply_timeout"
	}
	m.stage(e, "Checking workspace")
	name, err := m.planner.Workspace(ctx)
	switch {
	case ctx.Err() != nil:
		return stopped(), nil, false
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args", nil, false
	case err != nil:
		return "workspace_error", nil, false
	case name != a.workspace:
		return "workspace_changed", nil, false
	}
	m.stage(e, "Verifying saved plan")
	if digest, err := sha256File(a.path); err != nil || digest != a.digest {
		return "artifact_changed", nil, false
	}
	m.stage(e, "Applying")
	label := engineLabel(a.engine)
	inputs := collectKnown(m.root, e.op.VarFiles, os.Environ())
	undecoded := false
	err = m.applier.Apply(ctx, a.path, func(line []byte) {
		level, text, diag, ok := translate(line, "apply", label, inputs)
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
			return stopped(), nil, true
		case errors.Is(err, engine.ErrUnsafeArgs):
			return "unsafe_cli_args", nil, false
		case errors.As(err, &exit):
			code := exit.Code
			class := m.classify(e, exit.Hint)
			if class == "plan_failed" {
				class = "apply_failed"
			}
			return class, &code, true
		}
		return "apply_failed", nil, true
	}
	zero := 0
	return "applied", &zero, true
}
