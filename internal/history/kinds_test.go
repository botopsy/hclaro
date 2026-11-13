package history

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/internal/operations"
)

// An operation kind history does not know may change infrastructure; it is never a plan.
func TestUnknownOperationKindIsNeverRecordedAsAPlan(t *testing.T) {
	s := open(t, t.TempDir())
	for n, status := range []string{operations.Succeeded, operations.Failed, operations.Cancelled} {
		finished(s, n+1, "destroy", status, t0)
		e, _ := s.Get(id(n + 1))
		if e.Kind != KindUnknown || e.Outcome.Result != "unknown" || e.Outcome.Verified || !strings.Contains(e.Outcome.Summary, "may have changed infrastructure") || strings.Contains(e.Outcome.Summary, "Planning") {
			t.Fatalf("%s: %+v", status, e)
		}
		if !strings.Contains(NewReport([]HistoryEntry{e}, Filter{}, "test", t0).Markdown(), "Unknown operation") {
			t.Fatal("the report does not label an unknown kind")
		}
	}
	// A plan record whose plan claims a non-plan kind is still only a plan.
	finished(s, 9, "plan", operations.Succeeded, t0, func(r *operations.Record) { r.Plan = &operations.Plan{ID: id(19), Kind: "apply"} })
	if e, _ := s.Get(id(9)); e.Kind != "plan" {
		t.Fatalf("plan recorded as %s", e.Kind)
	}
}

func TestUnknownKindIsKeptAndMarkedOnReRead(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Succeeded, t0)
	var doc map[string]any
	_ = json.Unmarshal([]byte(readFile(t, s)), &doc)
	e := doc["entries"].([]any)[0].(map[string]any)
	e["kind"], e["projectName"] = "destroy", "password="+canary
	b, _ := json.Marshal(doc)
	_ = os.WriteFile(s.path, b, 0o600)
	got := open(t, dir).List(Filter{})
	if len(got) != 1 || got[0].Kind != KindUnknown {
		t.Fatalf("%+v", got)
	}
	// An entry that used to be dropped is now served, so it must pass the same scrubbing.
	if b, _ := json.Marshal(got); strings.Contains(string(b), canary) {
		t.Fatalf("canary served from a kept unknown entry: %s", b)
	}
	if got := open(t, dir).List(Filter{Kind: KindUnknown}); len(got) != 1 {
		t.Fatal("unknown entries cannot be filtered")
	}
}

func TestInterruptedUnknownKindMayHaveChangedInfrastructure(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.Record(operations.Record{Operation: operations.Operation{ID: id(1), Kind: "destroy", Status: operations.Running, StartedAt: t0.Format(time.RFC3339)}, Root: "/synthetic/network", Engine: "terraform"})
	e, _ := open(t, dir).Get(id(1))
	if e.Kind != KindUnknown || e.Outcome.Result != "interrupted" || strings.Contains(e.Outcome.Summary, "did not change infrastructure") || !strings.Contains(e.Outcome.Summary, "may be partly changed") {
		t.Fatalf("%+v", e)
	}
}

func TestEveryOperationKindHasAHistoryKind(t *testing.T) {
	for _, k := range operations.Kinds {
		if got := kind(operations.Operation{Kind: k}, nil); got == KindUnknown || !slices.Contains(Kinds, got) {
			t.Errorf("operation kind %q is recorded as %q", k, got)
		}
	}
	if !slices.Contains(Kinds, KindUnknown) {
		t.Fatal("unknown is not a history kind")
	}
}

// The UI keeps its own label map; it must name exactly the history kinds, with the same labels.
func TestUILabelsMatchTheKindTable(t *testing.T) {
	b, err := os.ReadFile("../../web/src/history/HistoryView.tsx")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const kinds: Record<HistoryKind, string> = \{(.*?)\};`).FindSubmatch(b)
	if block == nil {
		t.Fatal("kinds map not found in HistoryView.tsx")
	}
	ui := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+): '([^']*)'`).FindAllSubmatch(block[1], -1) {
		ui[string(m[1])] = string(m[2])
	}
	if len(ui) != len(kindTable) {
		t.Fatalf("UI has %d kinds, history %d", len(ui), len(kindTable))
	}
	for _, k := range kindTable {
		if ui[k.Kind] != k.Label {
			t.Errorf("%s: UI %q, history %q", k.Kind, ui[k.Kind], k.Label)
		}
	}
}
