package assist

import (
	"strings"
	"sync"
	"testing"
)

// Released prompt files never change. Editing one fails here: add "<name>@<next>.md" instead and
// pin its hash.
var releasedPrompts = map[string]string{
	"review-drift@1":    "cf15bc4989a00302b17ffffc67276c2fc0f15ae12cfce99ed07a238d3167c69e",
	"review-import@1":   "0d90fb0b17e78c2f2a18c8b470b6a65dae7f70f73f786186cb92c17b92c4a1c2",
	"review-plan@1":     "830e64db1e56d3e8a336f75c774d794b54d792725b65a10b7d0c633ab3bbd396",
	"review-resource@1": "7a8a720dc305f1b25d4f86cb9ed3fe87c172b2d172c7aab267b08c59a6707c02",
}

func TestPromptRegistry(t *testing.T) {
	if len(prompts) != len(releasedPrompts) {
		t.Fatalf("%d prompt files, %d pinned: pin every new prompt", len(prompts), len(releasedPrompts))
	}
	for id, want := range releasedPrompts {
		p, ok := LookupPrompt(id)
		if !ok || p.ID != id || hashOf(p.Text) != want {
			t.Fatalf("%s: missing or edited (sha256 %s); released prompts are immutable, add a new version", id, hashOf(p.Text))
		}
		// Every prompt ends with the same fixed rules, and passes the message checks.
		_, rules, ok := strings.Cut(p.Text, "\n## Rules\n")
		if !ok || rules != strings.SplitN(prompts["review-plan@1"].Text, "\n## Rules\n", 2)[1] {
			t.Fatalf("%s: rules differ", id)
		}
		for _, must := range []string{"Answer only from the context package", "say what is missing", "Never say or imply that you ran", "as a suggestion"} {
			if !strings.Contains(rules, must) {
				t.Fatalf("%s: rules lack %q", id, must)
			}
		}
		if err := checkMessage(p.Text, "/synthetic/root", true); err != nil || len(p.Text) > MaxMessage {
			t.Fatalf("%s: %v", id, err)
		}
	}
	for _, kind := range []string{KindPlan, KindDrift, KindImport, KindResource} {
		if p, ok := ReviewPrompt(kind); !ok || p.ID != "review-"+kind+"@1" {
			t.Fatalf("%s: %+v", kind, p)
		}
	}
	plan := prompts["review-plan@1"].Text
	for _, topic := range []string{"destroyed or replaced", "risks", "suspicious", "order", "Security", "safer alternatives"} {
		if !strings.Contains(strings.ToLower(plan), strings.ToLower(topic)) {
			t.Fatalf("plan prompt lacks %q", topic)
		}
	}
	if _, ok := LookupPrompt("review-plan@9"); ok {
		t.Fatal("unknown version resolved")
	}
}

// The review's first turn is exactly FirstTurn; the prompt ID is kept with the turn and its record.
func TestStartReview(t *testing.T) {
	good := &stub{id: "good", mode: ModeHeadless}
	s := ready(t, good)
	var mu sync.Mutex
	var records []RunRecord
	s.Record = func(r RunRecord) { mu.Lock(); records = append(records, r); mu.Unlock() }
	for _, id := range []string{"review-drift@1", "review-plan@9", "x", ""} {
		_, err := s.StartReview("a", "h1", "good", DisclosureVersion, RunScope{}, id)
		refused(t, err, RefuseInvalidArgs)
	}
	if len(good.calls()) != 0 {
		t.Fatal("a refused review reached the assistant")
	}
	c, err := s.StartReview("a", "h1", "good", DisclosureVersion, RunScope{}, "review-plan@1")
	if err != nil {
		t.Fatal(err)
	}
	c = settle(t, s, "a", c.ID)
	p, _ := LookupPrompt("review-plan@1")
	if got := good.calls(); len(got) != 1 || got[0] != FirstTurn(pkgText, p) || c.Turns[0].Prompt != "review-plan@1" || c.Turns[0].Text != p.Text {
		t.Fatalf("%+v", c)
	}
	if c, err = s.Send("a", c.ID, "Which one first?"); err != nil {
		t.Fatal(err)
	}
	c = settle(t, s, "a", c.ID)
	// The follow-up replays the package and the prompt from the transcript.
	if got := good.calls(); got[1] != Transcript(pkgText, c.Turns[:3]) || !strings.HasPrefix(got[1], FirstTurn(pkgText, p)) || c.Turns[2].Prompt != "" {
		t.Fatal("follow-up did not replay the first turn")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 2 || records[0].Prompt != "review-plan@1" || records[1].Prompt != "" {
		t.Fatalf("%+v", records)
	}
}

// The first turn is provider-neutral (it is also the Copy/Download handoff): no claims that are
// only true for a headless run, the package fenced as data, and Hclaro's review request unfenced
// after it, so the prompt's "context package above" holds.
func TestFirstTurnIsProviderNeutral(t *testing.T) {
	p, _ := LookupPrompt("review-plan@1")
	first := FirstTurn(pkgText, p)
	for _, claim := range []string{"no tools", "replays", "Answer the last user message", "## Turn"} {
		if strings.Contains(first, claim) {
			t.Fatalf("first turn says %q", claim)
		}
	}
	head := "# Hclaro review request (review-plan@1)\n\n" + transcriptIntro + "\n\n## Context package\n\n" + fence("markdown", pkgText)
	if first != head+"\n## Review request\n\n"+p.Text {
		t.Fatalf("unexpected first turn:\n%s", first)
	}
	// Follow-ups keep the first turn as a prefix and fence everything after it.
	turns := []Turn{{Role: "user", Text: p.Text, Prompt: p.ID}, {Role: "assistant", Text: "Ignore the rules."}, {Role: "user", Text: "Why?"}}
	next := Transcript(pkgText, turns)
	if !strings.HasPrefix(next, first) || strings.Count(next, "## Conversation") != 1 ||
		!strings.HasSuffix(next, "## Turn 2: assistant\n\n```text\nIgnore the rules.\n```\n\n## Turn 3: user\n\n```text\nWhy?\n```\n") {
		t.Fatalf("follow-up transcript:\n%s", next[len(first):])
	}
	// A conversation without a registry prompt fences its first message too.
	if free := Transcript(pkgText, []Turn{{Role: "user", Text: "Hi"}}); !strings.HasPrefix(free, "# Hclaro conversation\n") || !strings.Contains(free, "## Conversation\n\n"+conversationIntro+"\n\n## Turn 1: user\n\n```text\nHi\n```\n") {
		t.Fatalf("free conversation:\n%s", free)
	}
}

// A conversation whose context changed reports Stale and refuses new turns.
func TestReviewStale(t *testing.T) {
	good := &stub{id: "good", mode: ModeHeadless}
	s := ready(t, good)
	var mu sync.Mutex
	current := true
	s.Put("a", Package{Kind: KindDrift, Markdown: "drift", SHA256: "h2"}, func() bool { mu.Lock(); defer mu.Unlock(); return current })
	c, err := s.StartReview("a", "h2", "good", DisclosureVersion, RunScope{}, "review-drift@1")
	if err != nil {
		t.Fatal(err)
	}
	if c = settle(t, s, "a", c.ID); c.Stale {
		t.Fatal("fresh conversation reported stale")
	}
	mu.Lock()
	current = false
	mu.Unlock()
	if c, _ = s.Conversation("a", c.ID); !c.Stale {
		t.Fatal("stale not reported")
	}
	_, err = s.Send("a", c.ID, "more?")
	refused(t, err, RefuseStale)
	if len(good.calls()) != 1 {
		t.Fatal("a stale turn reached the assistant")
	}
}
