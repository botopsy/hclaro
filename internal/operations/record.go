package operations

import (
	"slices"

	"hclaro/internal/drift"
	"hclaro/internal/history"
	"hclaro/internal/plan"
)

// Record is what an operation hands to a Recorder when it starts and when it ends. It holds
// only data that operations already show in the UI: sanitized metadata, the redacted plan
// review and sanitized log events. Raw output, plan files, state and environment values are
// never part of it. Root identifies the project and must not be persisted as is.
type Record struct {
	Operation     Operation
	Root          string
	Engine        string
	EngineVersion string
	Workspace     string
	// Plan is the reviewed plan the operation produced or, for an apply, the plan it applied.
	Plan *Plan
	// Drift is the summary of a finished drift scan; DriftOutcome is its outcome.
	Drift        *drift.Summary
	DriftOutcome string
	DriftStatus  string
	// Excerpt is the tail of the sanitized log; it is set only when Final.
	Excerpt []Event
	Final   bool
}

// Recorder keeps operation metadata, for example as local history. It is called
// synchronously, in order, once when an operation starts and once when it ends. It must not
// call back into the runner.
type Recorder interface {
	Record(history.Event)
}

// Event converts the record for a Recorder.
func (r Record) Event() history.Event {
	op := r.Operation
	ev := history.Event{
		ID: op.ID, Kind: op.Kind, Status: op.Status, Final: r.Final, StartedAt: op.StartedAt, FinishedAt: op.CompletedAt, DurationMs: op.DurationMs,
		ExitCode: op.ExitCode, Root: r.Root, ProjectID: op.ProjectID, Engine: r.Engine, EngineVersion: r.EngineVersion, Workspace: r.Workspace,
		Command: op.Command, PlanID: op.PlanID, StateReloaded: op.StateReloaded, Outcome: op.Outcome, Ran: op.LockfileChanged != nil,
	}
	if op.Kind == "plan" {
		switch {
		case r.Plan != nil && slices.Contains([]string{"plan", "replace", "import"}, r.Plan.Kind):
			ev.Kind = r.Plan.Kind
		case op.Import != nil:
			ev.Kind = "import"
		case len(op.Replace) > 0:
			ev.Kind = "replace"
		}
	}
	switch {
	case op.Tool != nil:
		ev.Action, ev.Outcome = string(op.Tool.Tool), op.Tool.Outcome
	case op.Workspace != nil:
		ev.Action, ev.Ran, ev.Verified = string(op.Workspace.Action), op.Workspace.Ran, op.Workspace.Verified
	}
	if b := op.Branch; b != nil {
		ev.From, ev.To, ev.Ran = b.From, b.To, b.Ran
	}
	if c := op.Context; c != nil {
		ev.Context = &history.ContextSummary{Fingerprint: c.Fingerprint, ProfileName: c.ProfileName, ProfileRevision: c.Revision}
	}
	if d := op.Diagnostic; d != nil {
		ev.Diagnostic = &history.Diagnostic{Code: d.Code, Message: d.Message}
	}
	if op.Kind == "drift" {
		if r.Drift != nil {
			ev.Drift = &history.DriftCounts{Changed: r.Drift.Changed, Missing: r.Drift.Missing, CannotVerify: r.Drift.CannotVerify}
		}
		switch r.DriftStatus {
		case ScanCompleted:
			ev.Outcome = r.DriftOutcome
		case ScanPartial:
			ev.Outcome = "partial"
		default:
			ev.Outcome = ""
		}
	}
	if p := r.Plan; p != nil {
		s := &history.PlanSummary{PlanID: p.ID, Kind: p.Kind, Counts: p.Summary, Errored: p.Errored, Destructive: p.Destructive, Drift: len(p.Drift),
			VarFiles: p.VarFiles, Replace: p.Replace, Fingerprint: p.Fingerprint}
		for _, c := range p.Changes {
			action := c.Action
			if c.Importing && action == plan.NoOp {
				action = "import"
			}
			s.Changes = append(s.Changes, history.PlannedChange{Address: c.Address, Action: action})
		}
		for _, o := range p.Outputs {
			if o.Action != plan.NoOp {
				s.Outputs++
			}
		}
		if p.Import != nil {
			s.Import = p.Import.Address
		}
		ev.Plan = s
	}
	for _, e := range r.Excerpt {
		ev.Excerpt = append(ev.Excerpt, history.Line{Time: e.Time, Level: e.Level, Text: e.Text})
	}
	if len(r.Excerpt) > 0 {
		ev.ExcerptOmitted = r.Excerpt[0].Seq - 1
	}
	return ev
}

// excerptEvents bounds the log tail handed to a recorder.
const excerptEvents = 40

// recordMeta is the context an operation runs in, kept for its recorder.
type recordMeta struct {
	root, engine, version, workspace string
}

func (l *Log) tail(n int) []Event {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	start := max(0, len(l.events)-n)
	out := make([]Event, len(l.events)-start)
	copy(out, l.events[start:])
	return out
}

// recordLocked hands the operation to the recorder. Caller holds mu; used only when an
// operation starts, before its goroutine runs, so start and end are recorded in order.
func (m *Manager) recordLocked(e *entry) {
	if m.Recorder == nil {
		return
	}
	m.Recorder.Record(Record{Operation: m.snapshot(e), Root: m.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace}.Event())
}

// recordFinal hands a finished operation to the recorder. Caller does not hold mu.
func (m *Manager) recordFinal(e *entry, p *Plan, extra func(*Record)) {
	if m.Recorder == nil {
		return
	}
	m.mu.Lock()
	r := Record{Operation: m.snapshot(e), Root: m.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace, Plan: p, Final: true}
	m.mu.Unlock()
	r.Excerpt = e.log.tail(excerptEvents)
	if extra != nil {
		extra(&r)
	}
	m.Recorder.Record(r.Event())
}

func (r *Inits) recordLocked(e *initEntry) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Record(Record{Operation: r.snapshot(e), Root: e.meta.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace}.Event())
}

func (r *Inits) recordFinal(e *initEntry) {
	if r.Recorder == nil {
		return
	}
	r.mu.Lock()
	rec := Record{Operation: r.snapshot(e), Root: e.meta.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace, Final: true}
	r.mu.Unlock()
	rec.Excerpt = e.log.tail(excerptEvents)
	r.Recorder.Record(rec.Event())
}
