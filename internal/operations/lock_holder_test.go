package operations

import (
	"context"
	"encoding/json"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/workspace"
)

// lockLine is a plan -json diagnostic for a lock failure with the given Lock Info body.
func lockLine(body string) [][]byte {
	b, _ := json.Marshal(map[string]any{"@level": "error", "type": "diagnostic", "diagnostic": map[string]any{"severity": "error", "summary": "Error acquiring the state lock", "detail": "Error message: ConditionalCheckFailedException\nLock Info:\n" + body}})
	return [][]byte{b}
}

func TestLockHolderOnFailedPlan(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want *engine.LockInfo
	}{
		"terraform": {"  ID:        1234\n  Path:      s3://bucket/state\n  Operation: OperationTypeApply\n  Who:       alice@build-7\n  Version:   1.9.8\n  Created:   2026-11-05 09:00:00 +0000 UTC\n  Info:      \n", &engine.LockInfo{ID: "1234", Who: "alice@build-7", Operation: "apply", Created: "2026-11-05 09:00:00 +0000 UTC"}},
		"tofu":      {"  ID:        9\n  Path:      terraform.tfstate\n  Operation: OperationTypePlan\n  Who:       bob@laptop\n  Created:   2026-11-05 10:00:00 +0000 UTC\n", &engine.LockInfo{ID: "9", Who: "bob@laptop", Operation: "plan", Created: "2026-11-05 10:00:00 +0000 UTC"}},
		"garbled":   {"  Who: someone@host\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := setup(t, &enginetest.Fake{Lines: lockLine(tc.body), PlanErr: &engine.ExitError{Code: 1}}, workspace.Info{})
			op, err := m.StartPlan(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := wait(t, m, op.ID)
			if done.Diagnostic == nil || done.Diagnostic.Code != "state_locked" {
				t.Fatalf("%+v", done.Diagnostic)
			}
			if tc.want == nil && done.Lock != nil || tc.want != nil && (done.Lock == nil || *done.Lock != *tc.want) {
				t.Fatalf("lock %+v, want %+v", done.Lock, tc.want)
			}
		})
	}
}

func TestLockHolderOnFailedApply(t *testing.T) {
	fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions, ApplyErr: &engine.ExitError{Code: 1, Hint: "state_locked"}, ApplyLines: lockLine("  ID: 7\n  Operation: OperationTypeApply\n  Who: carol@ci\n")}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	done := wait(t, m, approveAndApply(t, m, planned(t, m)).ID)
	if done.Lock == nil || done.Lock.Who != "carol@ci" || done.Lock.Operation != "apply" {
		t.Fatalf("%+v", done.Lock)
	}
}
