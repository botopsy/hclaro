package assist

import (
	"sync"
	"time"
)

// DisclosureVersion changes whenever the disclosure text changes.
const DisclosureVersion = "1"

// Disclosure is shown next to every package and in Settings (R10).
type Disclosure struct {
	Version string   `json:"version"`
	Lines   []string `json:"lines"`
}

var disclosure = Disclosure{Version: DisclosureVersion, Lines: []string{
	"Hclaro builds this context on your machine and shares nothing by itself. Only Copy or Download hands it over.",
	"An AI assistant you give it to may send it to a cloud model, and its runs bill your own assistant account.",
	"Once copied or downloaded, the text is outside Hclaro: its redaction and blocking no longer protect it.",
}}

// Settings is the session's AI state. Only handoff exists in AI-01.03; strict local-only mode
// keeps handoff available (OD-AI-4), because the user carries the data out themselves.
type Settings struct {
	Enabled     bool       `json:"enabled"`
	StrictLocal bool       `json:"strictLocal"`
	Handoff     bool       `json:"handoff"`
	Headless    bool       `json:"headless"`
	Disclosure  Disclosure `json:"disclosure"`
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
	mu       sync.Mutex
	strict   bool
	enabled  bool
	packages map[string]stored
	Now      func() time.Time
}

// NewSession returns a session with AI off.
func NewSession(strictLocal bool) *Session {
	return &Session{strict: strictLocal, packages: map[string]stored{}, Now: time.Now}
}

func (s *Session) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Settings{Enabled: s.enabled, StrictLocal: s.strict, Handoff: s.enabled, Disclosure: disclosure}
}

func (s *Session) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled turns AI on or off for this session. Turning it off drops every package.
func (s *Session) SetEnabled(on bool) Settings {
	s.mu.Lock()
	s.enabled = on
	if !on {
		s.packages = map[string]stored{}
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
