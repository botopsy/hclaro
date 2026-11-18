package assist

import (
	"embed"
	"strconv"
	"strings"
)

// Prompt registry (AI-01.08, shared with AI-01.10). One embedded file per prompt, named
// "<name>@<version>.md". A released file never changes: a pinned SHA-256 test fails on any edit,
// so a change ships as a new version. The ID ("review-plan@1") is recorded with the turn.

//go:embed prompts/*.md
var promptFiles embed.FS

// Prompt is one versioned prompt; Text is the user message of the turn that uses it.
type Prompt struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	Text    string `json:"-"`
}

var prompts = loadPrompts()

func loadPrompts() map[string]Prompt {
	entries, err := promptFiles.ReadDir("prompts")
	if err != nil {
		panic(err)
	}
	out := map[string]Prompt{}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".md")
		name, ver, ok := strings.Cut(id, "@")
		n, err := strconv.Atoi(ver)
		if !ok || err != nil || n < 1 {
			panic("assist: bad prompt file name " + e.Name())
		}
		text, err := promptFiles.ReadFile("prompts/" + e.Name())
		if err != nil {
			panic(err)
		}
		out[id] = Prompt{ID: id, Name: name, Version: n, Text: string(text)}
	}
	return out
}

// LookupPrompt returns the prompt with this exact ID.
func LookupPrompt(id string) (Prompt, bool) {
	p, ok := prompts[id]
	return p, ok
}

// ReviewPrompt returns the latest "Review with AI" prompt for a package kind.
func ReviewPrompt(kind string) (Prompt, bool) {
	var best Prompt
	for _, p := range prompts {
		if p.Name == "review-"+kind && p.Version > best.Version {
			best = p
		}
	}
	return best, best.ID != ""
}

// Fits reports whether p may open a conversation about a package of kind: a review prompt for
// its own kind, or a quick action (AI-01.10, "qa-*") for a project package.
func (p Prompt) Fits(kind string) bool {
	return p.Name == "review-"+kind || kind == KindProject && strings.HasPrefix(p.Name, "qa-")
}

// FirstTurn is the exact stdin of a conversation's first turn when it starts with prompt p: what
// the preview shows, what Copy and Download hand over and what Send submits.
func FirstTurn(pkg string, p Prompt) string {
	return Transcript(pkg, []Turn{{Role: "user", Text: p.Text, Prompt: p.ID}})
}
