package assist

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSessionScopesCapAndFreshness(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewSession(true)
	s.Now = func() time.Time { return now }
	s.SetEnabled(false)
	if st := s.Settings(); st.Enabled || st.Handoff || st.Headless || !st.StrictLocal {
		t.Fatalf("%+v", st)
	}
	p := Package{Kind: KindPlan, Markdown: "x", SHA256: "h0"}
	s.Put("a", p, nil)
	if _, _, found, _ := s.Get("a", "h0"); found {
		t.Fatal("a package is readable while AI is off")
	}
	s.SetEnabled(true)
	// Strict local-only mode keeps handoff (OD-AI-4) and never headless.
	if st := s.Settings(); !st.Handoff || st.Headless {
		t.Fatalf("%+v", st)
	}
	s.Put("a", p, nil)
	if _, _, found, _ := s.Get("b", "h0"); found {
		t.Fatal("a package crossed scopes")
	}
	stale := false
	s.Put("a", Package{Kind: KindDrift, Markdown: "y", SHA256: "h1"}, func() bool { return !stale })
	stale = true
	if _, _, found, fresh := s.Get("a", "h1"); !found || fresh {
		t.Fatal("a stale package is fresh")
	}
	for i := 2; i < maxPackages+2; i++ {
		now = now.Add(time.Second)
		s.Put("a", Package{SHA256: fmt.Sprint("h", i)}, nil)
	}
	if len(s.packages) != maxPackages {
		t.Fatalf("%d packages held", len(s.packages))
	}
	if _, _, found, _ := s.Get("a", "h0"); found {
		t.Fatal("the oldest package was not evicted")
	}
	if _, md, found, fresh := s.Get("a", fmt.Sprint("h", maxPackages+1)); !found || !fresh || md != "" {
		t.Fatal("the newest package is missing")
	}
}

// AI-01.05, AI-01.14: the Anthropic and OpenAI lines show only while Claude Code or Codex can be
// run headless, and the run lines only while some assistant can. The version and its gate do
// not change.
func TestDisclosureFollowsDetectedProviders(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		strict                 bool
		providers              []Assistant
		send, anthropic, codex bool
	}{
		{"codex copy-only", false, []Assistant{&stub{id: "codex", mode: ModeHandoff}}, false, false, false},
		{"none", false, nil, false, false, false},
		{"claude handoff-only", false, []Assistant{&stub{id: "claude", mode: ModeHandoff}, &stub{id: "codex", mode: ModeHandoff}}, false, false, false},
		{"claude missing", false, []Assistant{&stub{id: "claude", mode: ModeUnsupported}}, false, false, false},
		{"claude headless", false, []Assistant{&stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHandoff}}, true, true, false},
		{"codex headless", false, []Assistant{&stub{id: "claude", mode: ModeUnsupported}, &stub{id: "codex", mode: ModeHeadless}}, true, false, true},
		{"both headless", false, []Assistant{&stub{id: "claude", mode: ModeHeadless}, &stub{id: "codex", mode: ModeHeadless}}, true, true, true},
		{"claude headless, strict local-only", true, []Assistant{&stub{id: "claude", mode: ModeHeadless}}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession(tc.strict, tc.providers...)
			st, _ := s.SetEnabled(true)
			d := st.Disclosure
			text := strings.Join(d.Lines, "\n")
			if d.Version != DisclosureVersion || len(d.Lines) < 3 || strings.Contains(text, "\x00") {
				t.Fatalf("%+v", d)
			}
			if strings.Contains(text, "OpenAI Codex runs only") != tc.codex || strings.Contains(text, "Anthropic") != tc.anthropic || strings.Contains(text, "quick actions start a conversation") != tc.send || strings.Contains(text, CostLine) != tc.send || strings.Contains(text, "resends the whole conversation") != tc.send || strings.Contains(text, "unverified AI output") != tc.send {
				t.Fatalf("lines %q", d.Lines)
			}
			if tc.anthropic && tc.codex && len(d.Lines) != len(disclosure.Lines) {
				t.Fatalf("headless Claude and Codex must see the full disclosure: %q", d.Lines)
			}
		})
	}
}

// Accepting with only handoff assistants does not carry over to a Claude Code detected later.
func TestAcceptanceResetsWhenDisclosureGrows(t *testing.T) {
	claude := &stub{id: "claude", mode: ModeUnsupported}
	s := NewSession(false, claude, &stub{id: "codex", mode: ModeHandoff})
	s.SetEnabled(true)
	if err := s.Accept(DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.SetEnabled(true); !st.Accepted {
		t.Fatal("re-detecting the same providers dropped the acceptance")
	}
	claude.mode = ModeHeadless
	st, _ := s.SetEnabled(true)
	if st.Accepted || !strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "Anthropic") {
		t.Fatalf("acceptance carried over to new lines: %+v", st)
	}
	s.Put("k", Package{Kind: KindPlan, Markdown: "plan", SHA256: "h"}, nil)
	_, err := s.StartConversation("k", "h", "claude", DisclosureVersion, RunScope{}, "")
	refused(t, err, RefuseDisclosure)
}
