package assist

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// counting records Detect and Run calls; release, when set, holds Detect until closed.
type counting struct {
	stub
	detects, runs atomic.Int32
	release       chan struct{}
}

func (c *counting) Detect(ctx context.Context) ProviderStatus {
	c.detects.Add(1)
	if c.release != nil {
		<-c.release
	}
	return c.stub.Detect(ctx)
}

func (c *counting) Run(ctx context.Context, payload []byte) (string, error) {
	c.runs.Add(1)
	return c.stub.Run(ctx, payload)
}

// enable turns AI on and waits for the background detection it starts.
func enable(t *testing.T, s *Session) Settings {
	t.Helper()
	if _, err := s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	return waitChecked(t, s)
}

func waitChecked(t *testing.T, s *Session) Settings {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st := s.Settings(); !st.Checking {
			return st
		}
	}
	t.Fatal("detection did not finish")
	return Settings{}
}

// settled asks for detection, as opening an AI surface does, and waits for it.
func settled(t *testing.T, s *Session) Settings {
	t.Helper()
	s.Detect()
	return waitChecked(t, s)
}

// OD-AI-13: on by default, but nothing is started until asked: Settings never detects, Detect
// starts it in the background without blocking, and nothing is ever run.
func TestDefaultOnDetectsOnlyOnRequest(t *testing.T) {
	claude := &counting{stub: stub{id: "claude", mode: ModeHeadless}, release: make(chan struct{})}
	s := NewSession(false, claude)
	defer s.Close()
	for range 3 {
		if st := s.Settings(); !st.Enabled || !st.Handoff || st.Headless || st.Checking || st.Checked || st.Accepted {
			t.Fatalf("%+v", st)
		}
	}
	// A negative check: give a wrongly started background detection time to show up.
	time.Sleep(20 * time.Millisecond)
	if claude.detects.Load() != 0 {
		t.Fatal("Settings started detection")
	}
	if st := s.Detect(); !st.Checking {
		t.Fatalf("%+v", st)
	}
	close(claude.release)
	if st := settled(t, s); !st.Headless || st.DefaultProvider != "claude" {
		t.Fatalf("%+v", st)
	}
	if claude.detects.Load() != 1 || claude.runs.Load() != 0 {
		t.Fatalf("detects %d runs %d", claude.detects.Load(), claude.runs.Load())
	}
	strict := &counting{stub: stub{id: "claude", mode: ModeHeadless}}
	ss := NewSession(true, strict)
	ss.SetEnabled(true)
	if st := ss.Detect(); !st.Enabled || st.Checking || st.Headless || strict.detects.Load() != 0 {
		t.Fatalf("strict local-only detected: %+v", st)
	}
	off := &counting{stub: stub{id: "claude", mode: ModeHeadless}}
	so := NewSession(false, off)
	so.SetEnabled(false)
	if st := so.Detect(); st.Checking || off.detects.Load() != 0 {
		t.Fatalf("AI off detected: %+v", st)
	}
}

// A detection that finishes after AI was turned off does not bring providers back.
func TestStaleBackgroundDetectionIsDiscarded(t *testing.T) {
	claude := &counting{stub: stub{id: "claude", mode: ModeHeadless}, release: make(chan struct{})}
	s := NewSession(false, claude)
	s.Detect()
	if _, err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	close(claude.release) // the detection finishes only after AI was turned off
	s.Close()
	if st := s.Settings(); st.Enabled || len(st.Providers) != 0 || st.DefaultProvider != "" {
		t.Fatalf("%+v", st)
	}
}

func TestDefaultAssistantChoice(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claude, codex string
		want          string
	}{
		{"claude eligible", ModeHeadless, ModeHeadless, "claude"},
		{"codex eligible", ModeHandoff, ModeHeadless, "codex"},
		{"codex copy-only", ModeUnsupported, ModeHandoff, "codex"},
		{"none installed", ModeUnsupported, ModeUnsupported, ""},
	} {
		s := NewSession(false, &stub{id: "claude", mode: tc.claude}, &stub{id: "codex", mode: tc.codex})
		if st := settled(t, s); st.DefaultProvider != tc.want {
			t.Errorf("%s: %q", tc.name, st.DefaultProvider)
		}
	}
}

func writePrefs(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, prefsFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPrefsLoad(t *testing.T) {
	for _, tc := range []struct {
		name, file    string
		enabled       bool
		want, warning string
	}{
		{"missing", "", true, "claude", ""},
		{"off", `{"enabled":false,"assistant":"codex"}`, false, "", ""},
		{"codex", `{"enabled":true,"assistant":"codex"}`, true, "codex", ""},
		{"leftover copilot", `{"enabled":true,"assistant":"copilot"}`, true, "claude", ""},
		{"unknown", `{"enabled":true,"assistant":"/usr/bin/evil"}`, true, "claude", ""},
		{"corrupt", `{"enabled":tru`, true, "claude", "x"},
		{"no enabled", `{"assistant":"codex"}`, true, "claude", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession(false, &stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff})
			s.UsePrefs(writePrefs(t, tc.file))
			st := s.Settings()
			if st.Enabled != tc.enabled || (st.Warning != "") != (tc.warning != "") {
				t.Fatalf("%+v", st)
			}
			if tc.enabled {
				if st = settled(t, s); st.DefaultProvider != tc.want {
					t.Fatalf("%+v", st)
				}
			} else if st.Checking || len(st.Providers) != 0 {
				t.Fatalf("off session detected: %+v", st)
			}
		})
	}
}

// Off and the assistant survive a restart; the file is 0600 and holds no path.
func TestPrefsPersistAcrossSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hclaro")
	s := NewSession(false, &stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff})
	s.UsePrefs(dir)
	settled(t, s)
	if err := s.SetDefault("copilot"); err == nil {
		t.Fatal("an unknown assistant was accepted")
	}
	if err := s.SetDefault("codex"); err != nil {
		t.Fatal(err)
	}
	// A copy-only choice is kept but never makes Send available.
	if st := s.Settings(); st.DefaultProvider != "codex" || st.Headless {
		t.Fatalf("%+v", st)
	}
	if _, err := s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, prefsFile))
	if err != nil || string(b) != `{"enabled":false,"assistant":"codex"}`+"\n" {
		t.Fatalf("%q %v", b, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, prefsFile)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	next := NewSession(false, &stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff})
	next.UsePrefs(dir)
	if st := next.Settings(); st.Enabled || st.Checking {
		t.Fatalf("off did not survive a restart: %+v", st)
	}
	if st := enable(t, next); st.DefaultProvider != "codex" {
		t.Fatalf("the assistant did not survive a restart: %+v %v", st, err)
	}
}

// Two processes share ai.json: one choosing an assistant keeps the other's Off.
func TestPrefsKeepOtherProcessChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hclaro")
	open := func() *Session {
		s := NewSession(false, &stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff})
		s.UsePrefs(dir)
		settled(t, s)
		return s
	}
	a, b := open(), open()
	if _, err := a.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := b.SetDefault("codex"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, prefsFile))
	if err != nil || string(got) != `{"enabled":false,"assistant":"codex"}`+"\n" {
		t.Fatalf("%q %v", got, err)
	}
}

// A failed write returns an error and changes nothing.
func TestPrefsWriteFailureLeavesState(t *testing.T) {
	dir := writePrefs(t, "")
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSession(false, &stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff})
	s.UsePrefs(blocked) // a file where the folder should be: every write fails
	settled(t, s)
	if _, err := s.SetEnabled(false); err != ErrPrefs {
		t.Fatalf("%v", err)
	}
	if err := s.SetDefault("codex"); err != ErrPrefs {
		t.Fatalf("%v", err)
	}
	if st := s.Settings(); !st.Enabled || st.DefaultProvider != "claude" {
		t.Fatalf("state changed after a failed write: %+v", st)
	}
}

// An acceptance during detection would not cover the lines detection adds, so it is refused
// until detection finishes (B07 review: SetEnabled detects in the background).
func TestAcceptRefusedWhileChecking(t *testing.T) {
	claude := &counting{stub: stub{id: "claude", mode: ModeHeadless}, release: make(chan struct{})}
	s := NewSession(false, claude)
	defer s.Close()
	if st, err := s.SetEnabled(true); err != nil || !st.Checking {
		t.Fatalf("%+v %v", st, err)
	}
	refused(t, s.Accept(DisclosureVersion), RefuseChecking)
	close(claude.release)
	waitChecked(t, s)
	if err := s.Accept(DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	if st := s.Settings(); !st.Accepted || !strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "Anthropic") {
		t.Fatalf("%+v", st)
	}
}
