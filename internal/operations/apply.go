package operations

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/plan"
	"hclaro/internal/preflight"
)

// artifact is the single retained saved plan. The file holds plaintext secrets, so it lives
// only in a private 0700 temp directory and is applied at most once. It is deleted after an
// apply attempt, when it expires, when a newer plan supersedes it, and on shutdown.
type artifact struct {
	planID, dir, path, digest, fingerprint, workspace, version, engine string
	created                                                            time.Time
	timer                                                              *time.Timer
	approval                                                           *approval
	// preflight is the binding of the latest passed preflight; an approval needs it.
	preflight *preflightBinding
	// applying is set while an apply uses the file; expiry then waits for the apply.
	applying bool
	// environ and context pin a profile-bound plan's environment for its apply.
	environ []string
	context *ExecutionContext
}

type approval struct {
	token   string
	expires time.Time
	// binding is the preflight binding token the approval was issued for.
	binding string
}

// ApplyStatus reports, at request time, whether a plan's saved artifact can be applied.
type ApplyStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Code is the stable refusal code when Available is false, such as unknown_actions.
	Code             string `json:"code,omitempty"`
	ArtifactRetained bool   `json:"artifactRetained"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
	// ConfigurationReason is set when the configuration could not be read, now or when
	// planning, whichever refusal Code names.
	ConfigurationReason string `json:"configurationReason,omitempty"`
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
	// Context is the execution profile context the apply will use, if any.
	Context *ExecutionContext `json:"executionContext,omitempty"`
	// Acknowledged lists the preflight warnings acknowledged for this approval.
	Acknowledged []string `json:"acknowledged"`
}

// RejectError explains why an approval or apply was refused. Nothing ran.
type RejectError struct {
	Code   string
	Reason string
}

func (e *RejectError) Error() string { return e.Reason }

func reject(code, reason string) error { return &RejectError{code, reason} }

// unreadablePlan explains a plan whose configuration could not be fingerprinted when it ran.
const unreadablePlan = "Hclaro could not read the configuration when this plan was created, so it cannot confirm the plan is current. The plan log names the file or limit. Fix it, then run a new plan."

// unsupportedReason explains why a plan with unrecognized action combinations can never be
// applied, or returns "". It recomputes from the recorded action lists each time, so the
// refusal does not depend on a cached summary. Destructive confirmation cannot override it.
func unsupportedReason(p *Plan) string {
	resources, outs := plan.Unsupported(p.Changes, p.Outputs)
	if resources+outs == 0 {
		return ""
	}
	return fmt.Sprintf("This plan contains %d change(s) with an action combination Hclaro does not support (%d resource, %d output). Hclaro never applies such a plan, even with destructive confirmation. Inspect it with the CLI.", resources+outs, resources, outs)
}

func unsupportedCount(p *Plan) int {
	resources, outs := plan.Unsupported(p.Changes, p.Outputs)
	return resources + outs
}

// ineligible returns why a plan can never be applied, or "" when it can.
func ineligible(p *Plan) string {
	if reason := unsupportedReason(p); reason != "" {
		return reason
	}
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
func (m *Manager) retainLocked(p *Plan, kept *artifact, leftover bool) {
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
		if leftover {
			m.notes[p.ID] += " " + leftNote
		}
		return
	}
	m.releaseLocked("A newer plan replaced this one. Only the most recent applicable plan is kept.")
	if m.closed {
		m.notes[p.ID] = "Hclaro stopped. " + deletedNote(m.removeArtifactDir(kept.dir))
		return
	}
	m.artifact = kept
	kept.timer = time.AfterFunc(m.PlanMaxAge, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.artifact == kept && !kept.applying {
			m.releaseLocked(expiredReason(m.PlanMaxAge))
		}
	})
}

// releaseLocked deletes the retained artifact and records why, followed by whether the file
// was deleted. The plan is never applicable again either way. Caller holds mu.
func (m *Manager) releaseLocked(reason string) (deleted bool) {
	a := m.artifact
	if a == nil {
		return true
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	deleted = m.removeArtifactDir(a.dir)
	m.artifact = nil
	m.notes[a.planID] = reason + " " + deletedNote(deleted)
	return deleted
}

// Saved plan files hold plaintext secrets. They live in private directories named with these
// prefixes, which SweepStale uses to find the ones a killed Hclaro left behind.
const planDirPrefix, driftDirPrefix = "hclaro-plan-", "hclaro-drift-"

// leftNote replaces "deleted" wherever a saved plan file could not be removed. The folder is
// named only on the terminal: notes and logs avoid absolute temp paths (PRD S-04).
const leftNote = "Hclaro could not delete the saved plan file, which holds values in plaintext. The terminal running Hclaro names its folder; delete it yourself."

func deletedNote(deleted bool) string {
	if deleted {
		return "The saved plan file was deleted."
	}
	return leftNote
}

// newArtifactDir creates a private 0700 directory for a saved plan file.
func (m *Manager) newArtifactDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp(m.TempDir, prefix)
	if err != nil {
		return "", err
	}
	// MkdirTemp creates 0700 directories; enforce it in case of an unusual umask policy.
	if err := m.chmod(dir, 0o700); err != nil {
		m.removeArtifactDir(dir)
		return "", err
	}
	return dir, nil
}

// removeArtifactDir deletes a saved plan directory and reports whether it is gone. A failure
// is printed on the terminal with the folder, for the user to delete.
func (m *Manager) removeArtifactDir(dir string) bool {
	err := os.RemoveAll(dir)
	if err != nil && m.Terminal != nil {
		fmt.Fprintf(m.Terminal, "Hclaro could not delete a saved plan folder, which holds values in plaintext: %v\nDelete %s yourself.\n", err, dir)
	}
	return err == nil
}

// ApplyStatus reports whether p can be applied now. currentFingerprint is the context
// fingerprint recomputed by the caller.
func (m *Manager) ApplyStatus(p *Plan, currentFingerprint string) ApplyStatus {
	return m.applyStatus(p, currentFingerprint, nil)
}

func (m *Manager) applyStatus(p *Plan, currentFingerprint string, rej *RejectError) ApplyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(p, currentFingerprint, rej); err != nil {
		retained := m.artifact != nil && m.artifact.planID == p.ID
		status := ApplyStatus{Reason: err.Error(), ArtifactRetained: retained}
		var r *RejectError
		if errors.As(err, &r) {
			status.Code = r.Code
		}
		return status
	}
	return ApplyStatus{Available: true, ArtifactRetained: true, ExpiresAt: m.artifact.created.Add(m.PlanMaxAge).UTC().Format(time.RFC3339)}
}

// checkLocked verifies everything that can be checked without running the engine.
// Caller holds mu. rej is the refusal from recomputing the fingerprint, if any.
func (m *Manager) checkLocked(p *Plan, currentFingerprint string, rej *RejectError) error {
	snap := m.store.Get()
	a := m.artifact
	if reason := unsupportedReason(p); reason != "" {
		return reject("unknown_actions", reason)
	}
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
		return reject("plan_expired", expiredReason(m.PlanMaxAge))
	case rej != nil:
		return rej
	case snap.Info.Engine != a.engine || snap.Info.Version != a.version || snap.Info.Workspace != a.workspace:
		return reject("context_changed", "The engine, its version or the workspace changed since this plan was created. Run a new plan.")
	case a.fingerprint == "":
		return reject("configuration_unreadable", unreadablePlan)
	case currentFingerprint == "" || currentFingerprint != a.fingerprint:
		return reject("context_changed", "Configuration, variable files, the lock file or the planning environment changed since this plan was created. Run a new plan.")
	}
	return nil
}

// expiredReason names the age limit, so "expired" is not a mystery.
func expiredReason(limit time.Duration) string {
	age := fmt.Sprintf("%d minutes", int(limit.Minutes()))
	if limit == time.Hour {
		age = "1 hour"
	}
	return "This plan is older than " + age + ", the longest a saved plan stays applicable. Run a new plan; infrastructure may have changed since."
}

func applyCommand(a *artifact) []string {
	return []string{a.engine, "apply", "-input=false", "-json", "<reviewed saved plan " + a.digest[:12] + ">"}
}

// Approve issues a one-time confirmation token for applying plan id. It needs the binding
// of a passed preflight and an acknowledgement of each of its warnings, by check ID; there
// is no blanket acknowledgement. A new approval replaces any earlier unused one.
func (m *Manager) Approve(id string, req ApprovalRequest) (Approval, error) {
	p, ok := m.Plan(id)
	if !ok {
		return Approval{}, ErrNotFound
	}
	current, rej := m.currentFingerprint(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(p, current, rej); err != nil {
		return Approval{}, err
	}
	a := m.artifact
	b, err := m.checkBindingLocked(a, req.Binding)
	if err != nil {
		return Approval{}, err
	}
	if !sameSet(b.acknowledge, req.Acknowledged) {
		return Approval{}, reject("acknowledgement_required", "Acknowledge each preflight warning individually before approving. Nothing was approved.")
	}
	a.approval = &approval{token: newID() + newID(), expires: time.Now().Add(m.ApprovalTTL), binding: b.token}
	summary := map[string]int{}
	for k, v := range p.Summary {
		summary[k] = v
	}
	return Approval{
		Token: a.approval.token, ExpiresAt: a.approval.expires.UTC().Format(time.RFC3339), PlanID: p.ID,
		ArtifactSHA256: a.digest, Engine: a.engine, Workspace: a.workspace, Summary: summary,
		Replace: slices.Clone(p.Replace), Import: p.Import, Imports: p.Imports, Destructive: p.Destructive, Phrase: phrase(p), Command: applyCommand(a),
		Context: a.context.Clone(), Acknowledged: slices.Clone(b.acknowledge),
	}, nil
}

// StartApply consumes the approval token and applies the reviewed saved plan. The token is
// single-use: any attempt to use it, valid or not, invalidates it. Immediately before the
// apply starts, the whole preflight is evaluated again: a new blocker, or a warning that
// was not acknowledged, refuses the apply. Nothing is re-planned.
func (m *Manager) StartApply(id, token, typed string) (Operation, error) {
	p, ok := m.Plan(id)
	if !ok {
		return Operation{}, ErrNotFound
	}
	identity := notRequested()
	m.mu.Lock()
	if a := m.artifact; a != nil && a.planID == p.ID && a.preflight != nil {
		identity = a.preflight.identity
	}
	m.mu.Unlock()
	checks := preflight.Evaluate(m.facts(m.base, p, identity))
	blockers, ack := acknowledgements(checks)
	current, rej := m.currentFingerprint(p)
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
	if err := m.checkLocked(p, current, rej); err != nil {
		return Operation{}, err
	}
	b, err := m.checkBindingLocked(a, granted.binding)
	if err != nil {
		return Operation{}, err
	}
	if blockers > 0 {
		names := []string{}
		for _, c := range checks {
			if c.Blocks() {
				names = append(names, c.ID)
			}
		}
		a.preflight = nil
		return Operation{}, reject("preflight_blocked", "A preflight check now blocks this apply: "+titles(checks, names)+". Nothing was applied. Run the checks again.")
	}
	if !slices.Equal(ack, b.acknowledge) {
		a.preflight = nil
		return Operation{}, reject("preflight_changed", "The preflight warnings changed since you acknowledged them. Nothing was applied. Run the checks again.")
	}
	release, claimed := m.Locks.Acquire(m.root, "apply")
	if !claimed {
		return Operation{}, reject("operation_running", "Another operation, such as initialization, is running in this project. Wait for it to finish.")
	}
	// The binding is used once.
	a.preflight = nil
	a.applying = true
	ctx, cancel := context.WithTimeout(m.base, m.ApplyTimeout)
	summary := &PreflightSummary{CheckedAt: b.checkedAt.UTC().Format(time.RFC3339), Policy: b.policy, Acknowledged: slices.Clone(b.acknowledge), IdentityVerified: b.identity.State == "verified"}
	e := &entry{
		op:      Operation{ID: m.NewID(), Kind: "apply", Status: Running, Stage: "Starting", StartedAt: time.Now().UTC().Format(time.RFC3339), Command: applyCommand(a), VarFiles: slices.Clone(p.VarFiles), Replace: slices.Clone(p.Replace), Import: p.Import, PlanID: p.ID, Outcome: "not_applied", Diagnostics: []EngineDiagnostic{}, LogRetained: true, Context: a.context.Clone(), Preflight: summary},
		log:     NewLog(logEvents, logBytes),
		cancel:  cancel,
		start:   time.Now(),
		release: release,
		environ: slices.Clone(a.environ),
		meta:    recordMeta{m.root, a.engine, a.version, a.workspace},
	}
	e.log.Append("info", fmt.Sprintf("Preflight passed again before apply: %d checks, %d acknowledged warning(s), policy %s.", len(checks), len(b.acknowledge), b.policy))
	m.entries = append(m.entries, e)
	m.trim()
	m.running = e
	m.recordLocked(e)
	m.wg.Add(1)
	go m.runApply(ctx, e, a)
	return m.snapshot(e), nil
}

var applyMessages = map[string]string{
	"applied":                  "Apply finished. The state view was reloaded from the engine.",
	"workspace_changed":        "The active workspace no longer matches this plan. Nothing was applied.",
	"workspace_error":          "The active workspace could not be confirmed. Nothing was applied.",
	"artifact_changed":         "The saved plan file changed after review. Nothing was applied.",
	"unknown_actions":          "The plan contains an action combination Hclaro does not support. Nothing was applied.",
	"unsafe_cli_args":          "Inherited TF_CLI_ARGS or TF_CLI_ARGS_apply contains flags other than -no-color. Nothing was applied. Values were not logged.",
	"stale_plan":               "The engine refused the saved plan because state changed after it was created. Nothing was applied. Run a new plan and review it again.",
	"state_locked":             "The state is locked by another operation, so the engine did not apply. Hclaro never force-unlocks state.",
	"state_unlock_failed":      "The engine ran the apply but could not release the state lock afterwards. Changes may have been made; review the reloaded state. The lock may remain: investigate it with your CLI. Hclaro never force-unlocks state.",
	"credentials":              "The engine could not authenticate with the backend or a provider. Some changes may have been made before the failure. Review the reloaded state and refresh your credentials.",
	"network":                  "A backend or provider endpoint was unreachable. Some changes may have been made before the failure. Review the reloaded state.",
	"init_required":            "The engine reported missing initialization. Nothing was initialized automatically.",
	"apply_failed":             "The apply failed. Some changes may have been made before the error. Review the diagnostics and the reloaded state; the saved plan cannot be reused.",
	"apply_cancelled":          "The apply was interrupted. Infrastructure may be partly changed: resources being created or destroyed when it stopped can be incomplete. Review the reloaded state and run a new plan. If the engine had to be stopped forcefully, a state lock may remain.",
	"apply_timeout":            "The apply exceeded its time limit and was stopped. Infrastructure may be partly changed. Review the reloaded state and run a new plan.",
	"shutdown":                 "Hclaro stopped while the apply was running. Infrastructure may be partly changed. Check state with your CLI.",
	"scope_changed":            scopeChangedMessage + " Nothing was applied.",
	"configuration_unreadable": "Hclaro could not read the configuration right before applying, so it cannot confirm the plan is current. Nothing was applied. The log names the file or limit.",
	"context_changed":          "Configuration, variable files, the lock file, the execution profile or the planning environment changed after this plan was approved. Nothing was applied. Run a new plan.",
}

func (m *Manager) runApply(ctx context.Context, e *entry, a *artifact) {
	defer m.wg.Done()
	defer e.cancel()
	code, exitCode, started := m.executeApply(ctx, e, a)
	m.mu.Lock()
	// A saved plan is applied at most once: the file is deleted whatever the result.
	note := "This plan was applied. A saved plan can only be applied once."
	if code != "applied" {
		note = "An apply of this plan was attempted. Run a new plan."
	}
	if !started {
		note = "The apply was refused before the engine ran. Run a new plan."
	}
	var deleted bool
	if m.artifact == a {
		deleted = m.releaseLocked(note)
	} else {
		deleted = m.removeArtifactDir(a.dir)
	}
	m.mu.Unlock()
	reloaded, reloadFailure := false, ""
	if started && m.Reload != nil && m.base.Err() == nil {
		m.stage(e, "Reloading state")
		m.Reload(m.base)
		// A failed read leaves the state view without the applied changes; say so rather
		// than report it as reloaded.
		// No recorded state is a successful read (state_absent), not a failure.
		if snap := m.store.Get(); snap.Status == "error" && (snap.Diagnostic == nil || snap.Diagnostic.Code != "state_absent") {
			reloadFailure = "The state could not be read again after the apply, so the state view may be out of date. Read the workspace again, or check state with your CLI."
			if snap.Diagnostic != nil && snap.Diagnostic.Message != "" {
				reloadFailure += " " + snap.Diagnostic.Message
			}
		} else {
			reloaded = true
		}
	}
	m.mu.Lock()
	o := &e.op
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	o.ExitCode = exitCode
	o.Stage = "Finished"
	o.StateReloaded = reloaded
	o.StateReloadFailed = reloadFailure != ""
	o.PlanFileLeft = !deleted
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
	if code == "applied" && !reloaded {
		message = "Apply finished. The engine reported success."
	}
	switch code {
	case "unsafe_cli_args":
		message += " " + engine.UnsafeArgsHelp(environOr(e.environ), "apply", "workspace show")
	case "credentials":
		message += credentialHint(e)
	}
	if code != "applied" {
		message += " " + deletedNote(deleted)
		o.Diagnostic = &Diagnostic{Code: code, Message: message}
	}
	o.LogDropped = e.log.Dropped()
	m.running = nil
	e.release()
	m.mu.Unlock()
	level := "info"
	if code != "applied" {
		level = "error"
	}
	e.log.Append(level, message)
	if code == "applied" && !deleted {
		e.log.Append("error", leftNote)
	}
	if reloadFailure != "" {
		e.log.Append("warn", reloadFailure)
	}
	if code == "applied" {
		e.log.Append("info", "A successful apply means the engine recorded the changes in state. It does not check that the services are healthy.")
	}
	applied, _ := m.Plan(e.op.PlanID)
	m.recordFinal(e, applied, nil)
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
	planner := m.bound(e.environ, a.workspace)
	applier, _ := planner.(engine.Applier)
	if applier == nil {
		return "apply_failed", nil, false
	}
	name, err := planner.Workspace(ctx)
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
	// Defensive: re-verify immediately before the engine runs that the reviewed plan has no
	// unsupported action combination, independently of the earlier checks.
	p, ok := m.Plan(e.op.PlanID)
	if !ok || p.ID != a.planID || unsupportedReason(p) != "" {
		return "unknown_actions", nil, false
	}
	// A Hub workspace's scope is revalidated last, with the root claim held: project folder,
	// engine binary and version, profile, environment, data directory and backend.
	if ok, changed := m.verify(ctx, e, "apply"); !ok {
		if !changed {
			return stopped(), nil, false
		}
		return "scope_changed", nil, false
	}
	// The context fingerprint (configuration, variable files, lock file, data directory,
	// profile and planning environment) is recomputed last, with the root claim held, so an
	// edit made after approval refuses the apply.
	if current, rej := m.currentFingerprint(p); rej != nil && rej.Code == "configuration_unreadable" {
		e.log.Append("error", rej.Reason)
		return "configuration_unreadable", nil, false
	} else if current == "" || current != a.fingerprint {
		return "context_changed", nil, false
	}
	m.stage(e, "Applying")
	label := engineLabel(a.engine)
	environ := e.environ
	if environ == nil {
		environ = os.Environ()
	}
	inputs := collectKnown(m.root, e.op.VarFiles, environ)
	if inputs.incomplete {
		e.log.Append("warn", knownIncomplete)
	}
	undecoded := false
	err = applier.Apply(ctx, a.path, func(line []byte) {
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

// SweepStale deletes saved-plan and drift scan directories left behind by a Hclaro process that was killed
// instead of stopped. Another running instance may share the temp directory, so only
// directories older than the longest a live instance can still use one (plan expiry plus
// apply timeout) are removed. It returns the number removed.
func (m *Manager) SweepStale(now time.Time) int {
	base := m.TempDir
	if base == "" {
		base = os.TempDir()
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	cutoff := now.Add(-(m.PlanMaxAge + m.ApplyTimeout + engine.ApplyGracePeriod))
	removed := 0
	for _, e := range entries {
		// ReadDir does not follow symlinks, so a link named like a plan directory is skipped.
		if !e.IsDir() || !strings.HasPrefix(e.Name(), planDirPrefix) && !strings.HasPrefix(e.Name(), driftDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		// A folder that cannot be deleted is named on the terminal.
		if m.removeArtifactDir(filepath.Join(base, e.Name())) {
			removed++
		}
	}
	return removed
}
