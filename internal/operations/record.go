package operations

import (
	"hclaro/internal/drift"
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
	Record(Record)
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
	m.Recorder.Record(Record{Operation: m.snapshot(e), Root: m.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace})
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
	m.Recorder.Record(r)
}

func (r *Inits) recordLocked(e *initEntry) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Record(Record{Operation: r.snapshot(e), Root: e.meta.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace})
}

func (r *Inits) recordFinal(e *initEntry) {
	if r.Recorder == nil {
		return
	}
	r.mu.Lock()
	rec := Record{Operation: r.snapshot(e), Root: e.meta.root, Engine: e.meta.engine, EngineVersion: e.meta.version, Workspace: e.meta.workspace, Final: true}
	r.mu.Unlock()
	rec.Excerpt = e.log.tail(excerptEvents)
	r.Recorder.Record(rec)
}
