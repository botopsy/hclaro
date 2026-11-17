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
}
