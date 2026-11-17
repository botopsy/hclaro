package assist

import (
	"context"
	"sync"
	"time"
)

// DisclosureVersion changes whenever the disclosure text changes.
const DisclosureVersion = "3"

// Disclosure is shown next to every package and in Settings (R10).
type Disclosure struct {
	Version string   `json:"version"`
	Lines   []string `json:"lines"`
}

var disclosure = Disclosure{Version: DisclosureVersion, Lines: []string{
	"Hclaro builds this context on your machine and shares nothing by itself. Only Copy, Download or Send to an assistant hands it over.",
	"An AI assistant you give it to may send it to a cloud model, and its runs bill your own assistant account.",
	"Once copied or downloaded, the text is outside Hclaro: its redaction and blocking no longer protect it.",
	"Send runs a detected assistant once on this machine, with its tools off, in an empty temporary folder and without cloud or Terraform credentials. It sends exactly the previewed text to its cloud model, signed in as you.",
	"Anthropic bills Claude Code requests directly, according to your account, subscription and applicable usage limits. Hclaro does not cap, control or guarantee the cost of any request.",
	"The reply is unverified AI output. Hclaro shows it as text, keeps it in memory for this session only, and never runs, applies or approves anything it suggests.",
}}

// Settings is the session's AI state. Strict local-only mode keeps handoff available but
// never runs an assistant (OD-AI-4), because handoff leaves the data with the user.
type Settings struct {
	Enabled     bool       `json:"enabled"`
	StrictLocal bool       `json:"strictLocal"`
	Handoff     bool       `json:"handoff"`
	Headless    bool       `json:"headless"`
	Disclosure  Disclosure `json:"disclosure"`
	// Accepted is true once the current disclosure was accepted in this session.
	Accepted        bool             `json:"disclosureAccepted"`
	Providers       []ProviderStatus `json:"providers"`
	DefaultProvider string           `json:"defaultProvider,omitempty"`
}

const (
	// PackageTTL bounds how long a built package can be copied or downloaded.
	PackageTTL  = 10 * time.Minute
	maxPackages = 16 // with MaxBytes each, at most 2 MiB held
)

type stored struct {
	scope, kind, markdown string
	at                    time.Time
	fresh                 func() bool
}

// Session holds the master switch and the built packages, in memory only. Nothing is
// written to disk; disabling AI or closing Hclaro drops every package.
type Session struct {
	mu        sync.Mutex
	strict    bool
	enabled   bool
	packages  map[string]stored
	Now       func() time.Time
	providers []Assistant
	detected  []ProviderStatus
	preferred string
	accepted  bool
	run       *Run
	cancel    context.CancelFunc
	base      context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
	// Record receives metadata of each finished run (never payload or reply), e.g. history.
	Record func(RunRecord)
}

// NewSession returns a session with AI off. providers are detected only once AI is on.
func NewSession(strictLocal bool, providers ...Assistant) *Session {
	base, stop := context.WithCancel(context.Background())
	return &Session{strict: strictLocal, packages: map[string]stored{}, Now: time.Now, providers: providers, base: base, stop: stop}
}

func (s *Session) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Settings{Enabled: s.enabled, StrictLocal: s.strict, Handoff: s.enabled, Disclosure: disclosure, Accepted: s.accepted,
		Providers: append([]ProviderStatus{}, s.detected...), DefaultProvider: s.preferred}
	st.Headless = s.enabled && !s.strict && s.eligible(s.preferred)
	return st
}

func (s *Session) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled turns AI on or off for this session. Turning it on detects the providers (unless
// strict local-only). Turning it off drops every package, the reply and the acceptance, and
// cancels a running request.
func (s *Session) SetEnabled(on bool) Settings {
	var found []ProviderStatus
	if on && !s.strict {
		ctx, cancel := context.WithTimeout(s.base, 30*time.Second)
		for _, p := range s.providers {
			found = append(found, p.Detect(ctx))
		}
		cancel()
	}
	s.mu.Lock()
	s.enabled = on
	if on {
		s.detected = found
		if !s.eligible(s.preferred) {
			s.preferred = ""
			for _, d := range found {
				if d.Mode == ModeHeadless {
					s.preferred = d.ID
					break
				}
			}
		}
	} else {
		s.packages, s.accepted, s.run, s.detected = map[string]stored{}, false, nil, nil
		if s.cancel != nil {
			s.cancel()
			s.cancel = nil
		}
	}
	s.mu.Unlock()
	return s.Settings()
}

// Put keeps p for scope until PackageTTL. fresh reports whether the project context the
// package was built from is still current; a package that is no longer fresh is refused.
func (s *Session) Put(scope string, p Package, fresh func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	key := scope + "\x00" + p.SHA256
	if _, ok := s.packages[key]; !ok && len(s.packages) >= maxPackages {
		oldest := ""
		for k, v := range s.packages {
			if oldest == "" || v.at.Before(s.packages[oldest].at) {
				oldest = k
			}
		}
		delete(s.packages, oldest)
	}
	s.packages[key] = stored{scope: scope, kind: p.Kind, markdown: p.Markdown, at: s.Now(), fresh: fresh}
}

// Get returns the package with this hash built in scope. found is false for an unknown,
// expired or other-scope hash and while AI is off; fresh is false when its context changed.
func (s *Session) Get(scope, hash string) (kind, markdown string, found, fresh bool) {
	s.mu.Lock()
	s.expire()
	p, ok := s.packages[scope+"\x00"+hash]
	on := s.enabled
	s.mu.Unlock()
	if !ok || !on {
		return "", "", false, false
	}
	return p.kind, p.markdown, true, p.fresh == nil || p.fresh()
}

func (s *Session) expire() {
	now := s.Now()
	for k, v := range s.packages {
		if now.Sub(v.at) > PackageTTL {
			delete(s.packages, k)
		}
	}
}
