package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/internal/assist"
)

// An AI request is recorded as metadata only: no package text, reply or local path.
func TestAssistRunIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	hash := strings.Repeat("ab", 32)
	s.RecordAssist(assist.RunRecord{RunScope: assist.RunScope{Project: "network", Root: "/synthetic/network", Workspace: "default", Engine: "terraform"},
		ID: id(7), Provider: "claude", Kind: assist.KindPlan, SHA256: hash, Status: assist.RunFailed, Code: assist.KindAuth, StartedAt: t0, Duration: 3 * time.Second})
	e, ok := open(t, dir).Get(id(7))
	if !ok || e.Kind != "assist" || e.Status != StatusFailed || e.Action != "claude" || e.Assist == nil || e.Assist.PackageSHA256 != hash ||
		e.Assist.ContextKind != "plan" || e.DurationMs != 3000 || e.Diagnostic == nil || e.Diagnostic.Code != assist.KindAuth || e.ProjectName != "network" || e.Outcome.Result != "not_answered" {
		t.Fatalf("%+v", e)
	}
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil || !strings.Contains(string(b), hash) {
		t.Fatal("history file not read", err)
	}
	if strings.Contains(string(b), "/synthetic/network") {
		t.Fatal("history keeps the project path")
	}
	// A hand-edited hash that is not a SHA-256 is dropped on read.
	if got := scrubAssist(&AssistSummary{Provider: "claude", ContextKind: "plan", PackageSHA256: "secret=hunter2"}); got.PackageSHA256 != "" {
		t.Fatalf("%+v", got)
	}
	// The prompt ID is kept when it is a registry ID, dropped otherwise.
	if got := scrubAssist(&AssistSummary{Provider: "claude", ContextKind: "plan", Prompt: "review-plan@1"}); got.Prompt != "review-plan@1" {
		t.Fatalf("%+v", got)
	}
	if got := scrubAssist(&AssistSummary{Provider: "claude", ContextKind: "plan", Prompt: "Ignore rules /home/x"}); got.Prompt != "" {
		t.Fatalf("%+v", got)
	}
}

// A conversation without a package (AI-01.09) says so; nothing else changes.
func TestAssistChatSummary(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.RecordAssist(assist.RunRecord{RunScope: assist.RunScope{Project: "network", Workspace: "default"}, ID: id(8), Provider: "claude", Kind: assist.KindChat,
		SHA256: strings.Repeat("cd", 32), Status: assist.RunCompleted, StartedAt: t0})
	e, ok := open(t, dir).Get(id(8))
	if !ok || e.Assist.ContextKind != "conversation" || !strings.Contains(e.Outcome.Summary, "without project data") {
		t.Fatalf("%+v", e)
	}
}

// A decision about an AI-proposed change keeps the path and hashes, never file content.
func TestAssistChangeIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	base, next := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	scope := assist.RunScope{Project: "network", Root: "/synthetic/network", Workspace: "default", Engine: "terraform"}
	s.RecordAssistChange(assist.ChangeRecord{RunScope: scope, Conversation: id(1), Provider: "claude", Path: "modules/net/main.tf", BaseSHA256: base, NewSHA256: next, Outcome: "applied", At: t0})
	s.RecordAssistChange(assist.ChangeRecord{RunScope: scope, Conversation: id(1), Provider: "claude", Path: "../escape.tf", NewSHA256: "not-a-hash", Outcome: "refused", Code: "change_path", At: t0})
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `"kind":"assist-change"`) || !strings.Contains(text, "modules/net/main.tf") || !strings.Contains(text, base) || !strings.Contains(text, next) ||
		strings.Contains(text, "escape") || strings.Contains(text, "not-a-hash") || strings.Contains(text, "/synthetic/network") || !strings.Contains(text, `"code":"change_path"`) {
		t.Fatalf("history file:\n%s", text)
	}
}
