package operations

import (
	"context"
	"testing"

	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

// The Verify hook, not a later ctx.Err() check, decides whether an operation stopped for a
// scope change. A Hub scope closes itself on a change, which cancels the operation's context,
// possibly before the manager looks at it (seen on macOS CI as "cancelled" instead of
// "scope_changed"). These cases cancel first, deterministically.
func TestVerifyResultIsDecidedByTheHook(t *testing.T) {
	for _, tc := range []struct {
		name, hookCode, status, code string
	}{
		{"scope change, then the close cancels", "scope_changed", Failed, "scope_changed"},
		{"cancelled while revalidating", "stopped", Cancelled, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &engine.Fake{Lines: lines(nil)}
			m, _ := setup(t, fake, workspace.Info{})
			m.Verify = func(ctx context.Context, kind string) *Diagnostic {
				if kind != "plan" {
					t.Errorf("kind %q", kind)
				}
				// The running operation is cancelled before the hook returns, as closing the
				// scope (or a user cancel) does.
				if _, err := m.Cancel(m.Operations()[0].ID); err != nil {
					t.Error(err)
				}
				<-ctx.Done()
				return &Diagnostic{Code: tc.hookCode, Message: "synthetic " + tc.hookCode}
			}
			before := len(fake.Calls())
			op, err := m.StartPlan(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := wait(t, m, op.ID)
			if done.Status != tc.status || done.Diagnostic == nil || done.Diagnostic.Code != tc.code || done.PlanID != "" {
				t.Fatalf("%+v %+v", done, done.Diagnostic)
			}
			if calls := fake.Calls()[before:]; len(calls) != 0 {
				t.Fatalf("engine ran after verify failed: %q", calls)
			}
		})
	}
}
