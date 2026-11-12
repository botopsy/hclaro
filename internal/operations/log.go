package operations

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"hclaro/internal/state"
)

// Event is one sanitized log line. Seq increases by one per event within an operation.
type Event struct {
	Seq   int64  `json:"seq"`
	Time  string `json:"time"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Log is a bounded event ring: the oldest events are dropped once either limit is reached.
type Log struct {
	mu        sync.Mutex
	events    []Event
	bytes     int
	maxEvents int
	maxBytes  int
	next      int64
	dropped   int64
	done      bool
	changed   chan struct{}
}

func NewLog(maxEvents, maxBytes int) *Log {
	return &Log{maxEvents: maxEvents, maxBytes: maxBytes, next: 1, changed: make(chan struct{})}
}

func (l *Log) Append(level, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	e := Event{Seq: l.next, Time: time.Now().UTC().Format(time.RFC3339), Level: level, Text: text}
	l.next++
	l.events = append(l.events, e)
	l.bytes += len(text)
	for len(l.events) > l.maxEvents || l.bytes > l.maxBytes {
		l.bytes -= len(l.events[0].Text)
		l.events[0] = Event{}
		l.events = l.events[1:]
		l.dropped++
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// Finish marks the log complete and wakes waiting readers.
func (l *Log) Finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.done = true
		close(l.changed)
	}
}

// Since returns retained events after seq, whether older events were dropped, whether the
// log is complete, and a channel closed on the next change.
func (l *Log) Since(after int64) (events []Event, dropped bool, done bool, wait <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.Seq > after {
			events = append(events, e)
		}
	}
	first := l.next - int64(len(l.events))
	return events, after+1 < first && l.dropped > 0, l.done, l.changed
}

// Dropped reports how many events were discarded to stay within bounds.
func (l *Log) Dropped() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// EngineDiagnostic is a sanitized engine diagnostic.
type EngineDiagnostic struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	Address  string `json:"address,omitempty"`
}

type uiResource struct {
	Addr string `json:"addr"`
}

// uiMessage is the subset of the machine-readable UI (`plan -json`) that Hclaro renders.
// Free-text fields such as @message are not echoed except through sanitize.
type uiMessage struct {
	Level   string `json:"@level"`
	Message string `json:"@message"`
	Type    string `json:"type"`
	Hook    struct {
		Resource uiResource `json:"resource"`
		Action   string     `json:"action"`
	} `json:"hook"`
	Change struct {
		Resource uiResource `json:"resource"`
		Action   string     `json:"action"`
		Reason   string     `json:"reason"`
	} `json:"change"`
	Changes struct {
		Add, Change, Import, Remove int
		Operation                   string
	} `json:"changes"`
	Outputs    map[string]json.RawMessage `json:"outputs"`
	Diagnostic struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
		Address  string `json:"address"`
	} `json:"diagnostic"`
}

var (
	wordPattern = regexp.MustCompile(`^[a-z_-]{1,40}$`)
	ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// translate converts one engine UI line into a log event and, for diagnostics, a record.
// kind is "plan", "apply", "init" or "drift". ok is false for lines that are not part of the rendered
// subset; ok with empty text means a recognized event that is deliberately not shown.
func translate(line []byte, kind, engineLabel string, k *known) (level, text string, diag *EngineDiagnostic, ok bool) {
	var m uiMessage
	if json.Unmarshal(line, &m) != nil {
		return "", "", nil, false
	}
	switch m.Type {
	case "version":
		switch kind {
		case "apply":
			return "info", "Apply started", nil, true
		case "init":
			return "info", "Initialization started", nil, true
		case "drift":
			return "info", "Drift scan started (refresh-only plan)", nil, true
		}
		return "info", "Planning started", nil, true
	case "apply_start":
		return "info", address(m.Hook.Resource.Addr) + ": " + word(m.Hook.Action) + " started", nil, true
	case "apply_complete":
		return "info", address(m.Hook.Resource.Addr) + ": " + word(m.Hook.Action) + " complete", nil, true
	case "apply_errored":
		return "error", address(m.Hook.Resource.Addr) + ": " + word(m.Hook.Action) + " failed", nil, true
	case "apply_progress", "provision_start", "provision_progress", "provision_complete", "ephemeral_op_start", "ephemeral_op_complete":
		// Progress ticks and provisioner output (which can echo secrets) are not shown.
		return "", "", nil, true
	case "provision_errored":
		return "error", address(m.Hook.Resource.Addr) + ": provisioner failed", nil, true
	case "refresh_start":
		return "info", address(m.Hook.Resource.Addr) + ": refreshing state", nil, true
	case "refresh_complete":
		return "info", address(m.Hook.Resource.Addr) + ": refresh complete", nil, true
	case "planned_change":
		text := address(m.Change.Resource.Addr) + ": plan to " + word(m.Change.Action)
		if m.Change.Reason != "" {
			text += " (" + word(m.Change.Reason) + ")"
		}
		return "info", text, nil, true
	case "resource_drift":
		return "warn", address(m.Change.Resource.Addr) + ": changed outside " + engineLabel + " (" + word(m.Change.Action) + ")", nil, true
	case "change_summary":
		if kind == "drift" {
			// A refresh-only plan proposes no configuration changes; its counts would mislead.
			return "info", "Refresh-only plan complete.", nil, true
		}
		if m.Changes.Operation == "apply" {
			return "info", fmt.Sprintf("Apply complete: %d added, %d changed, %d destroyed.", m.Changes.Add, m.Changes.Change, m.Changes.Remove), nil, true
		}
		text := fmt.Sprintf("Plan: %d to add, %d to change, %d to destroy", m.Changes.Add, m.Changes.Change, m.Changes.Remove)
		if m.Changes.Import > 0 {
			text += fmt.Sprintf(", %d to import", m.Changes.Import)
		}
		return "info", text + ".", nil, true
	case "outputs":
		return "info", fmt.Sprintf("Output changes: %d", len(m.Outputs)), nil, true
	case "diagnostic":
		d := &EngineDiagnostic{Severity: word(m.Diagnostic.Severity), Summary: sanitize(m.Diagnostic.Summary, k, 1), Detail: sanitize(m.Diagnostic.Detail, k, 20)}
		if m.Diagnostic.Address != "" {
			d.Address = address(m.Diagnostic.Address)
		}
		level := "warn"
		if d.Severity == "error" {
			level = "error"
		}
		label := "Warning"
		if level == "error" {
			label = "Error"
		}
		return level, label + ": " + d.Summary, d, true
	case "log", "init_output":
		level := "info"
		if m.Level == "warn" || m.Level == "error" {
			level = m.Level
		}
		return level, sanitize(m.Message, k, 1), nil, true
	}
	return "", "", nil, false
}

func address(s string) string {
	if s == "" {
		return "(unnamed resource)"
	}
	if len(s) > 512 || state.Suspicious(s) {
		return "[redacted address]"
	}
	return stripControls(s)
}

func word(s string) string {
	if wordPattern.MatchString(s) {
		return s
	}
	return "unknown"
}

// sanitize keeps at most maxLines lines of free text. Any line that looks like a credential
// (including long random-looking strings) or an assignment to a risky attribute name is
// withheld; known input values are then replaced in the lines that are kept. The checks run
// on the unscrubbed line, so a known value equal to a key word such as "password" cannot hide
// the name that makes the line risky. The checks are heuristic: a secret that is neither a
// known input nor recognizable can still pass.
func sanitize(text string, k *known, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(k.scrubSpanning(stripControls(text))), "\n")
	out := make([]string, 0, min(len(lines), maxLines))
	for i, line := range lines {
		if i == maxLines {
			out = append(out, "…")
			break
		}
		lower := strings.ToLower(line)
		switch {
		case state.LooksSecret(line):
			out = append(out, "[line withheld: looks like a secret]")
		case state.RiskyKey(lower) && strings.ContainsAny(line, "=:\""):
			out = append(out, "[line withheld: references a sensitive attribute]")
		default:
			// Scrubbed before the cut, so a known value at the boundary is not half shown.
			if line = k.scrub(line); len(line) > 500 {
				line = line[:500] + "…"
			}
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripControls(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, s)
}
