package assist

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Problem is a sanitized run failure.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Run is the session's one headless request. Reply is untrusted text, kept in memory only.
type Run struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"`
	Kind       string   `json:"kind"`
	SHA256     string   `json:"sha256"`
	Status     string   `json:"status"`
	Reply      string   `json:"reply,omitempty"`
	Error      *Problem `json:"error,omitempty"`
	StartedAt  string   `json:"startedAt"`
	DurationMs int64    `json:"durationMs"`
}

// RunScope identifies the project a run was about, for metadata records only.
type RunScope struct {
	Project, Root, Workspace, Engine string
}

// RunRecord is the metadata kept about a finished run: never the payload or the reply.
type RunRecord struct {
	RunScope
	ID, Provider, Kind, SHA256, Status, Code string
	StartedAt                                time.Time
	Duration                                 time.Duration
}

// Refusal codes of Start and the settings changes.
const (
	RefuseDisabled    = "ai_disabled"
	RefuseStrict      = "strict_local"
	RefuseDisclosure  = "disclosure_required"
	RefuseProvider    = "provider_unavailable"
	RefuseNotFound    = "package_not_found"
	RefuseStale       = "context_changed"
	RefuseBusy        = "run_in_progress"
	RefuseNoRun       = "run_not_found"
	RefuseInvalidArgs = "invalid_request"
)

var refusalMessages = map[string]string{
	RefuseDisabled:    "AI assistance is off. Turn it on in Settings first.",
	RefuseStrict:      "Strict local-only mode: Hclaro never runs an assistant itself. Copy or Download still work.",
	RefuseDisclosure:  "Review and accept the AI disclosure in Settings first.",
	RefuseProvider:    "This assistant cannot run headless here. Copy or Download still work.",
	RefuseNotFound:    "This context package expired or was not built here. Open the preview again.",
	RefuseStale:       "The workspace changed since this package was built. Open the preview again.",
	RefuseBusy:        "An assistant request is already running. Wait for it or cancel it.",
	RefuseNoRun:       "No assistant request is kept in this session.",
	RefuseInvalidArgs: "The request was refused. Reload the page and try again.",
}

// Refusal is a request Hclaro will not start.
type Refusal struct{ Code string }

func (r *Refusal) Error() string { return refusalMessages[r.Code] }

func refuse(code string) error { return &Refusal{Code: code} }

// eligible reports whether id was detected as headless-eligible. Callers hold mu.
func (s *Session) eligible(id string) bool {
	for _, d := range s.detected {
		if d.ID == id {
			return d.Mode == ModeHeadless
		}
	}
	return false
}

// SetDefault picks the default assistant among the detected headless-eligible ones.
func (s *Session) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return refuse(RefuseDisabled)
	}
	if !s.eligible(id) {
		return refuse(RefuseProvider)
	}
	s.preferred = id
	return nil
}

// Accept records that the user accepted disclosure version for this session.
func (s *Session) Accept(version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.enabled:
		return refuse(RefuseDisabled)
	case version != DisclosureVersion:
		return refuse(RefuseDisclosure)
	}
	s.accepted = true
	return nil
}

// Start sends the package with hash, built in scope key, to provider. It refuses unless AI is
// on, not strict local-only, the current disclosure version was accepted and is the one named,
// the provider is headless-eligible, the package is known and fresh, and no run is going.
func (s *Session) Start(key, hash, provider, disclosureVersion string, scope RunScope) (Run, error) {
	kind, markdown, found, fresh := s.Get(key, hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	var p Assistant
	for _, a := range s.providers {
		if a.ID() == provider {
			p = a
		}
	}
	switch {
	case !s.enabled:
		return Run{}, refuse(RefuseDisabled)
	case s.strict:
		return Run{}, refuse(RefuseStrict)
	case !s.accepted || disclosureVersion != DisclosureVersion:
		return Run{}, refuse(RefuseDisclosure)
	case p == nil || !s.eligible(provider):
		return Run{}, refuse(RefuseProvider)
	case !found:
		return Run{}, refuse(RefuseNotFound)
	case !fresh:
		return Run{}, refuse(RefuseStale)
	case s.run != nil && s.run.Status == RunRunning:
		return Run{}, refuse(RefuseBusy)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	started := s.Now()
	run := &Run{ID: hex.EncodeToString(id), Provider: provider, Kind: kind, SHA256: hash, Status: RunRunning, StartedAt: started.UTC().Format(time.RFC3339)}
	ctx, cancel := context.WithCancel(s.base)
	s.run, s.cancel = run, cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		reply, err := p.Run(ctx, []byte(markdown))
		code := ""
		if err != nil {
			code = KindOf(err)
		}
		elapsed := s.Now().Sub(started)
		status := RunCompleted
		switch {
		case code == KindCancelled:
			status = RunCancelled
		case code != "":
			status = RunFailed
		}
		// Recorded before the result is published, so a finished run always has its record.
		if s.Record != nil {
			s.Record(RunRecord{RunScope: scope, ID: run.ID, Provider: provider, Kind: kind, SHA256: hash, Status: status, Code: code, StartedAt: started, Duration: elapsed})
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		// A run dropped by switching AI off is not shown again.
		if s.run == run {
			done := *run
			done.Status, done.DurationMs = status, elapsed.Milliseconds()
			if code == "" {
				done.Reply = reply
			} else {
				done.Error = &Problem{Code: code, Message: Message(code)}
			}
			s.run, s.cancel = &done, nil
		}
	}()
	return *run, nil
}

// Current returns the session's latest run.
func (s *Session) Current() (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return Run{}, false
	}
	return *s.run, true
}

// Cancel stops the running request; false when none is running.
func (s *Session) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return false
	}
	s.cancel()
	return true
}

// Close cancels any running request and waits for it to end.
func (s *Session) Close() {
	s.stop()
	s.wg.Wait()
}
