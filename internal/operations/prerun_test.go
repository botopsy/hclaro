package operations

import (
	"context"
	"slices"
	"sync"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/engine/enginetest"
	"hclaro/internal/workspace"
)

// orderedFake records the shared pre-run steps in one order and can fail the workspace check.
type orderedFake struct {
	*enginetest.Fake
	mu    sync.Mutex
	order []string
	name  string
	err   error
	scope bool // Verify reports a scope change
}

func (f *orderedFake) note(step string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, step)
}

func (f *orderedFake) Workspace(ctx context.Context) (string, error) {
	f.note("workspace")
	if f.err != nil || f.name != "" {
		return f.name, f.err
	}
	return f.Fake.Workspace(ctx)
}

// start runs one operation of kind on m and returns its final state. An apply first plans
// and approves with the plain fake, then runs on f.
func startKind(t *testing.T, m *Manager, f *orderedFake, kind string) Operation {
	t.Helper()
	var op Operation
	var err error
	switch kind {
	case "plan":
		op, err = m.StartPlan(nil, nil)
	case "drift":
		op, _, err = m.StartDriftScan(DriftStart{})
	case "graph":
		op, _, err = m.StartGraph(GraphStart{})
	case "apply":
		id := planned(t, m)
		approval, aerr := approve(m, id)
		if aerr != nil {
			t.Fatal(aerr)
		}
		m.planner, m.applier = f, f
		op, err = m.StartApply(id, approval.Token, approval.Phrase)
	}
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, m, op.ID)
}

// B08: every Manager operation runs the same pre-run step. The Hub scope is revalidated
// before the first engine command (the workspace check), and a changed scope, a changed
// workspace or injected TF_CLI_ARGS stop it with the same codes as before.
func TestPrerunCodesAndOrderPerKind(t *testing.T) {
	for _, kind := range []string{"plan", "drift", "graph", "apply"} {
		for _, tc := range []struct {
			name, code string
			setup      func(*orderedFake)
		}{
			{"workspace changed", "workspace_changed", func(f *orderedFake) { f.name = "other" }},
			{"unsafe args", "unsafe_cli_args", func(f *orderedFake) { f.err = engine.ErrUnsafeArgs }},
			{"scope changed", "scope_changed", func(f *orderedFake) { f.scope = true }},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				fake := &enginetest.Fake{PlanJSON: fixtures.PlanAllActions, GraphDOT: []byte("digraph {}")}
				m, _ := setup(t, fake, workspace.Info{})
				f := &orderedFake{Fake: fake}
				m.Verify = func(_ context.Context, got string) *Diagnostic {
					if got != kind {
						return nil
					}
					f.note("verify")
					if f.scope {
						return &Diagnostic{Code: scopeChangedCode, Message: "synthetic scope change"}
					}
					return nil
				}
				if kind != "apply" {
					m.planner, m.applier = f, f
				}
				tc.setup(f)
				done := startKind(t, m, f, kind)
				if done.Diagnostic == nil || done.Diagnostic.Code != tc.code || done.Diagnostic.Message == "" {
					t.Fatalf("%+v %+v", done, done.Diagnostic)
				}
				want := []string{"verify", "workspace"}
				if tc.code == "scope_changed" {
					want = want[:1]
				}
				if f.mu.Lock(); !slices.Equal(f.order, want) {
					t.Errorf("order %q, want %q", f.order, want)
				}
				f.mu.Unlock()
				if kind == "apply" && applyCalls(fake) != 0 {
					t.Fatal("engine apply ran")
				}
			})
		}
	}
}

// B08: ResolveProfile no longer fills in the fingerprint itself; an unreviewed profile is
// refused, as for a plan.
func TestResolveProfileRequiresReviewedFingerprint(t *testing.T) {
	m, _ := setup(t, &enginetest.Fake{}, workspace.Info{})
	m.Binder = func(string) (Binding, error) {
		return Binding{Context: ExecutionContext{ProfileID: "p", Fingerprint: "fp"}}, nil
	}
	if _, _, err := m.ResolveProfile("p", ""); rejected(err) != "context_changed" {
		t.Fatalf("empty fingerprint: %v", err)
	}
	if c, _, err := m.ResolveProfile("p", "fp"); err != nil || c == nil || c.Fingerprint != "fp" {
		t.Fatalf("%+v %v", c, err)
	}
}
