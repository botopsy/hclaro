package assist

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"hclaro/internal/safefile"
)

// DisclosureVersion changes whenever the disclosure text changes.
const DisclosureVersion = "10"

// Disclosure is shown next to every package and in Settings (R10).
type Disclosure struct {
	Version string   `json:"version"`
	Lines   []string `json:"lines"`
}

var disclosure = Disclosure{Version: DisclosureVersion, Lines: []string{
	"Hclaro builds this context on your machine and shares nothing by itself. Only Copy, Download, Review with an assistant or a message you send from the AI Assistant hands anything over.",
	"Project context holds the text of this project's .tf, .tofu and JSON configuration files: the root module and local modules inside the project folder. It never holds state, .tfvars files, .terraform, saved plans, untracked files Git ignores, files in nested repositories or submodules, symbolic links or anything outside the project folder. A file already tracked by Git is included even if it matches a .gitignore pattern; backend blocks and variable defaults that may be secret are removed.",
	"Secret detection is heuristic. Hclaro redacts values that look like credentials, random keys or local paths and leaves out files that contain them, but a secret written as ordinary text, or in a comment, can pass. Read the preview before you share it.",
	"An AI assistant you give it to may send it to a cloud model, and its runs bill your own assistant account.",
	"Once copied or downloaded, the text is outside Hclaro: its redaction and blocking no longer protect it.",
	sendLine + "Review with an assistant and the AI Assistant's quick actions start a conversation: Hclaro sends the previewed text, the context followed by a fixed review request or quick action prompt. The AI Assistant sends your message, with no project data unless you preview and attach the project context. Each message runs a detected assistant once on this machine, with its tools off, in an empty temporary folder and without cloud or Terraform credentials, and sends the conversation to its cloud model, signed in as you.",
	sendLine + CostLine,
	sendLine + "Every follow-up resends the whole conversation (the context, your messages and the replies), so usage and cost grow with each message. A conversation stops at a size limit; Hclaro then asks you to start a new one and never drops messages silently.",
	claudeLine + "Anthropic bills Claude Code requests directly, according to your account, subscription and applicable usage limits. Hclaro does not cap, control or guarantee the cost of any request.",
	codexLine + "OpenAI Codex runs only on macOS, signed in with a personal ChatGPT plan (Free, Go, Plus or Pro), after Hclaro's local check that it offers no tools; its messages count against your plan's usage limits. Hclaro reads only the sign-in type, plan and expiry from Codex's auth.json to decide, and never stores or shares them. Hclaro does not cap, control or guarantee the cost of any request.",
	sendLine + "The reply is unverified AI output. Hclaro shows it as text and never runs, applies or approves anything it suggests. A reply can propose a whole .tf or .tofu file: Hclaro shows its own diff and writes that file only when you apply that one proposal, keeping a backup for Undo; it never runs a plan or apply for it. It keeps the conversation in memory for this session only, unless you tick Save conversation: then the context, your messages and the replies are stored as plain text in your Hclaro configuration folder on this machine until you delete them or they are unused for 30 days (set retentionDays in ai-conversations/settings.json there).",
}}

// CostLine is the disclosure of the optional cost section (AI-01.10); the preview's checkbox
// repeats it.
const CostLine = "Cost analysis can attach this session's existing Infracost estimate, only if you tick it in the preview. " +
	"It shares the estimate's workspace, Infracost version, pricing basis and finish time, its totals, coverage, per-type counts and monthly change, and its caveats. " +
	"Individual cost line items are never shared. Hclaro reuses the latest completed estimate of the launch project and never starts a new Infracost run for it. " +
	"A line that looks secret is hidden and the summary is marked incomplete."

// Line markers, stripped before display: sendLine lines apply only when an assistant can be run
// headless, claudeLine and codexLine lines only when that assistant is Claude Code or Codex. The version stays the
// same; SetEnabled drops an acceptance when the shown lines change.
const (
	sendLine   = "\x00send\x00"
	claudeLine = "\x00claude\x00"
	codexLine  = "\x00codex\x00"
)

// shownDisclosure is the disclosure without the lines that do not apply to detected.
func shownDisclosure(detected []ProviderStatus) Disclosure {
	send, claude, codex := false, false, false
	for _, d := range detected {
		if d.Mode == ModeHeadless {
			send, claude, codex = true, claude || d.ID == Claude.ID, codex || d.ID == codexAdapter.ID
		}
	}
	out := Disclosure{Version: disclosure.Version, Lines: []string{}}
	for _, l := range disclosure.Lines {
		if line, ok := strings.CutPrefix(l, claudeLine); ok {
			if claude {
				out.Lines = append(out.Lines, line)
			}
		} else if line, ok := strings.CutPrefix(l, codexLine); ok {
			if codex {
				out.Lines = append(out.Lines, line)
			}
		} else if line, ok := strings.CutPrefix(l, sendLine); ok {
			if send {
				out.Lines = append(out.Lines, line)
			}
		} else {
			out.Lines = append(out.Lines, l)
		}
	}
	return out
}

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
	// Checked is true once the assistants were detected; Checking while that runs. Detection
	// runs only on an explicit request (Detect, switching on, choosing an assistant).
	Checked  bool `json:"checked"`
	Checking bool `json:"checking"`
	// Warning is set when the saved AI settings could not be read and defaults apply.
	Warning string `json:"warning,omitempty"`
}

const (
	// PackageTTL bounds how long a built package can be copied or downloaded.
	PackageTTL  = 10 * time.Minute
	maxPackages = 16 // with MaxBytes each, at most 2 MiB held
)

type stored struct {
	scope, kind, markdown string
	fingerprint           string // of a project package that can be re-checked after a restart
	at                    time.Time
	fresh                 func() bool
}

// Session holds the master switch and the built packages, in memory only. Only conversations
// the user saves are written (Store, AI-01.11); disabling AI or closing Hclaro drops the rest.
type Session struct {
	mu        sync.Mutex
	strict    bool
	enabled   bool
	packages  map[string]stored
	Now       func() time.Time
	providers []Assistant
	detected  []ProviderStatus
	preferred string
	// chosen is the assistant the user picked (saved in ai.json); preferred falls back from it.
	chosen   string
	accepted bool
	// Background detection (AI-01.14): checking while it runs; gen discards a stale result.
	checking, checked bool
	gen               int
	prefs, warning    string
	// conversations are the open conversations by ID (AI-01.07).
	conversations map[string]*conversation
	base          context.Context
	stop          context.CancelFunc
	wg            sync.WaitGroup
	// Record receives metadata of each finished turn (never transcript or reply), e.g. history.
	Record func(RunRecord)
	// RecordChange receives metadata of each decision about a proposed change (AI-01.12).
	RecordChange func(ChangeRecord)
	// Store keeps saved conversations; nil when the configuration folder is unavailable.
	Store *Store
}

// NewSession returns a session with AI on (OD-AI-13). Being on starts nothing and shares nothing:
// providers are detected (their --version run) only on an explicit request, never under strict
// local-only, and no assistant runs before an accepted, explicit send.
func NewSession(strictLocal bool, providers ...Assistant) *Session {
	base, stop := context.WithCancel(context.Background())
	return &Session{strict: strictLocal, enabled: true, packages: map[string]stored{}, conversations: map[string]*conversation{}, Now: time.Now, providers: providers, base: base, stop: stop}
}

// prefsFile holds the switch and the chosen assistant, never a path (OD-AI-3).
const prefsFile = "ai.json"

type savedPrefs struct {
	Enabled   *bool  `json:"enabled"`
	Assistant string `json:"assistant"`
}

// UsePrefs reads the saved switch and assistant from configDir and saves later changes there. A
// missing file keeps the defaults; an unreadable one keeps them with a warning. An unknown
// assistant (e.g. a removed "copilot") is treated as unset.
func (s *Session) UsePrefs(configDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefs = configDir
	r, err := os.OpenRoot(configDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var b []byte
	if err == nil {
		b, err = readFile(r, prefsFile, 4<<10)
		r.Close()
		if errors.Is(err, os.ErrNotExist) {
			return
		}
	}
	var p savedPrefs
	if err != nil || json.Unmarshal(b, &p) != nil || p.Enabled == nil {
		s.warning = "Saved AI settings could not be read, so the defaults apply. Change a setting to save it again."
		return
	}
	s.enabled = *p.Enabled
	if s.providerKnown(p.Assistant) {
		s.chosen = p.Assistant
	}
}

// savePrefs writes the switch and the chosen assistant. Callers hold mu.
func (s *Session) savePrefs(on bool, chosen string) error {
	if s.prefs == "" {
		return nil
	}
	b, _ := json.Marshal(savedPrefs{Enabled: &on, Assistant: chosen})
	if err := os.MkdirAll(s.prefs, 0o700); err != nil {
		return err
	}
	r, err := os.OpenRoot(s.prefs)
	if err != nil {
		return err
	}
	defer r.Close()
	return safefile.WriteAtomicIn(r, prefsFile, tempPrefix, append(b, '\n'))
}

// ErrPrefs is returned when a settings change could not be saved; nothing changed.
var ErrPrefs = errors.New("the AI settings could not be saved in the configuration folder, so nothing changed")

func (s *Session) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Settings{Enabled: s.enabled, StrictLocal: s.strict, Handoff: s.enabled, Disclosure: shownDisclosure(s.detected), Accepted: s.accepted,
		Providers: append([]ProviderStatus{}, s.detected...), DefaultProvider: s.preferred, Checked: s.checked, Checking: s.checking, Warning: s.warning}
	st.Headless = s.enabled && !s.strict && s.eligible(s.preferred)
	return st
}

// Detect starts detecting the providers in the background when the user asks for AI (opening an
// AI preview or the AI Assistant page, or Check in Settings) and they were not detected yet. It
// does nothing while AI is off or in strict local-only mode. Detection runs only each adapter's
// allowlisted --version, never a conversation, and sends no project data.
func (s *Session) Detect() Settings {
	s.mu.Lock()
	if s.enabled && !s.strict && !s.checked && !s.checking {
		s.detectInBackground()
	}
	s.mu.Unlock()
	return s.Settings()
}

// detectInBackground runs one detection without blocking the request. Callers hold mu.
func (s *Session) detectInBackground() {
	s.checking = true
	gen := s.gen
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		found := s.detect()
		s.mu.Lock()
		defer s.mu.Unlock()
		if gen == s.gen {
			s.checking = false
			s.applyDetected(found)
		}
	}()
}

func (s *Session) detect() []ProviderStatus {
	ctx, cancel := context.WithTimeout(s.base, 30*time.Second)
	defer cancel()
	var found []ProviderStatus
	for _, p := range s.providers {
		found = append(found, p.Detect(ctx))
	}
	return found
}

// applyDetected records a detection and picks the effective assistant: the chosen one when
// installed, else Claude Code, else Codex when headless-eligible, else the first installed.
// Callers hold mu.
func (s *Session) applyDetected(found []ProviderStatus) {
	// Acceptance covers the lines that were shown; new ones (e.g. Claude Code installed since)
	// must be accepted again.
	if !slices.Equal(shownDisclosure(found).Lines, shownDisclosure(s.detected).Lines) {
		s.accepted = false
	}
	s.detected, s.checked = found, true
	s.preferred = ""
	for _, d := range found {
		if d.ID == s.chosen && d.Mode != ModeUnsupported {
			s.preferred = d.ID
			return
		}
	}
	for _, d := range found {
		if d.Mode == ModeHeadless {
			s.preferred = d.ID
			return
		}
	}
	for _, d := range found {
		if d.Mode != ModeUnsupported {
			s.preferred = d.ID
			return
		}
	}
}

func (s *Session) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled turns AI on or off and saves the choice. Turning it on re-detects the providers
// (unless strict local-only). Turning it off drops every package, conversation and the
// acceptance, and cancels running turns. When saving fails nothing changes.
func (s *Session) SetEnabled(on bool) (Settings, error) {
	s.mu.Lock()
	err := s.savePrefs(on, s.chosen)
	s.mu.Unlock()
	if err != nil {
		return s.Settings(), ErrPrefs
	}
	var found []ProviderStatus
	if on && !s.strict {
		found = s.detect()
	}
	s.mu.Lock()
	s.enabled, s.warning = on, ""
	s.gen++
	s.checking = false
	if on {
		if s.strict {
			s.checked = true
		} else {
			s.applyDetected(found)
		}
	} else {
		s.packages, s.accepted, s.detected, s.preferred, s.checked = map[string]stored{}, false, nil, "", false
		s.dropConversations()
	}
	s.mu.Unlock()
	return s.Settings(), nil
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
	s.packages[key] = stored{scope: scope, kind: p.Kind, markdown: p.Markdown, fingerprint: p.Fingerprint, at: s.Now(), fresh: fresh}
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
