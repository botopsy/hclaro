package operations

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/graph"
	"hclaro/internal/profiles"
	"hclaro/internal/workspace"
)

func graphAndWait(t *testing.T, m *Manager, req GraphStart) (Operation, GraphView) {
	t.Helper()
	op, run, err := m.StartGraph(req)
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != "graph" || op.GraphID != run.ID || run.Status != GraphRunning {
		t.Fatalf("%+v %+v", op, run)
	}
	done := wait(t, m, op.ID)
	view, err := m.Graph(run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return done, view
}

func nodeNamed(t *testing.T, v GraphView, name string) graph.Node {
	t.Helper()
	for _, n := range v.Nodes {
		if n.DisplayName == name {
			return n
		}
	}
	t.Fatalf("no node %s", name)
	return graph.Node{}
}

// A graph runs `graph` only: never plan, refresh, apply or a state command.
func assertOnlyGraph(t *testing.T, fake *engine.Fake) {
	t.Helper()
	for _, call := range fake.Calls() {
		if call != "version" && call != "workspace" && call != "show" && call != "graph" && call != "plan" && call != "show-plan" {
			t.Fatalf("unexpected engine call %q", call)
		}
		if call == "apply" {
			t.Fatal("graph ran apply")
		}
	}
}

func addresses(changes []graph.PlannedChange) []string {
	out := []string{}
	for _, c := range changes {
		out = append(out, c.Address)
	}
	slices.Sort(out)
	return out
}

func TestGraphGeneratesAnnotatesAndOverlaysAPlan(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform, PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	planOp, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	planOp = wait(t, m, planOp.ID)
	callsBefore := len(fake.Requests())
	op, view := graphAndWait(t, m, GraphStart{})
	if op.Status != Succeeded || !slices.Equal(op.Command, []string{"terraform", "graph"}) || view.Status != GraphCompleted || !view.Current || view.Format != "resources" {
		t.Fatalf("%+v %+v", op, view.GraphRun)
	}
	if len(fake.Requests()) != callsBefore || slices.Contains(fake.Calls(), "apply") {
		t.Fatal("generating a graph planned or applied")
	}
	assertOnlyGraph(t, fake)
	if n := nodeNamed(t, view, "module.network.aws_subnet.main"); n.Confidence != graph.Exact || n.InstanceCount != 52 {
		t.Fatalf("%+v", n)
	}
	if n := nodeNamed(t, view, "aws_security_group.web"); n.Confidence != graph.Configuration || len(n.Planned) != 0 {
		t.Fatalf("%+v", n)
	}
	// Overlaying the reviewed plan marks planned nodes only.
	withPlan, err := m.Graph(view.ID, planOp.PlanID)
	if err != nil || withPlan.Plan == nil || withPlan.Plan.ID != planOp.PlanID || len(withPlan.Plan.Unmapped) != 0 {
		t.Fatalf("%v %+v", err, withPlan.Plan)
	}
	if n := nodeNamed(t, withPlan, "aws_security_group.web"); len(n.Planned) != 1 || n.Planned[0].Action != "update" {
		t.Fatalf("%+v", n)
	}
	sg := nodeNamed(t, withPlan, "aws_security_group.web")
	impact, err := m.GraphImpact(view.ID, sg.ID, planOp.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if impact.DownstreamResources != 6 || impact.Plan == nil {
		t.Fatalf("%+v", impact)
	}
	want := []string{"aws_db_instance.primary", "aws_instance.retired", "aws_security_group.web", "module.compute.aws_instance.main[0]", `module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]`}
	if got := addresses(impact.Plan.InImpact); !slices.Equal(got, want) {
		t.Fatalf("in impact %v", got)
	}
	// Changes outside the reachable set are listed separately, never as consequences.
	if got := addresses(impact.Plan.Elsewhere); !slices.Contains(got, "aws_s3_bucket.logs") || slices.Contains(got, "aws_security_group.web") {
		t.Fatalf("elsewhere %v", got)
	}
	// Without a plan, impact is reachability only.
	plain, _ := m.GraphImpact(view.ID, sg.ID, "")
	if plain.Plan != nil {
		t.Fatal("plan data without a plan")
	}
	if _, err := m.GraphImpact(view.ID, "n99999", ""); !errors.Is(err, ErrNodeNotFound) {
		t.Fatal(err)
	}
	if _, err := m.Graph(view.ID, "00000000000000000000000000000000"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatal(err)
	}
	// The graph's own list entry carries no nodes.
	if list := m.Graphs(); len(list) != 1 || len(list[0].Nodes) != 0 || !list[0].Current {
		t.Fatalf("%+v", list)
	}
	assertTempEmptyExceptPlan(t, m)
}

// assertTempEmptyExceptPlan allows only the retained saved plan of the M02 plan flow.
func assertTempEmptyExceptPlan(t *testing.T, m *Manager) {
	t.Helper()
	entries, _ := os.ReadDir(m.TempDir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "hclaro-plan-") {
			t.Fatalf("graph left %s behind", e.Name())
		}
	}
}

func TestGraphRefusesPlansThatAreNotFullAndCurrent(t *testing.T) {
	// Exit 0 with an errored document: the plan is stored but is not a full plan.
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform, PlanJSON: fixtures.PlanErrored}
	m, root := setup(t, fake, workspace.Info{})
	errored, _ := m.StartPlan(nil, nil)
	errored = wait(t, m, errored.ID)
	fake.PlanJSON = fixtures.PlanAllActions
	good, _ := m.StartPlan(nil, nil)
	good = wait(t, m, good.ID)
	_, view := graphAndWait(t, m, GraphStart{})
	var rejected *RejectError
	if errored.PlanID == "" {
		t.Fatalf("errored plan not stored: %+v", errored)
	}
	if _, err := m.Graph(view.ID, errored.PlanID); !errors.As(err, &rejected) || rejected.Code != "plan_not_current" || !strings.Contains(rejected.Reason, "incomplete") {
		t.Fatalf("errored plan accepted: %v", err)
	}
	if _, err := m.Graph(view.ID, good.PlanID); err != nil {
		t.Fatal(err)
	}
	// After an edit, a new graph is current but the plan is not.
	writeFile(t, root, "main.tf", "# edited\n")
	_, fresh := graphAndWait(t, m, GraphStart{})
	if _, err := m.Graph(fresh.ID, good.PlanID); !errors.As(err, &rejected) || rejected.Code != "plan_not_current" || !strings.Contains(rejected.Reason, "changed") {
		t.Fatalf("stale plan accepted: %v", err)
	}
	sg := nodeNamed(t, fresh, "aws_security_group.web")
	if _, err := m.GraphImpact(fresh.ID, sg.ID, good.PlanID); !errors.As(err, &rejected) {
		t.Fatal(err)
	}
}

func TestGraphStalenessAndWorkspaceIsolation(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTofu}
	m, root := setup(t, fake, workspace.Info{})
	_, view := graphAndWait(t, m, GraphStart{})
	if view.Format != "full" || len(view.Nodes) == 0 {
		t.Fatalf("%+v", view.GraphRun)
	}
	node := view.Nodes[0].ID
	writeFile(t, root, "main.tf", "# edited\n")
	stale, err := m.Graph(view.ID, "")
	if err != nil || stale.Current || !stale.Withheld || len(stale.Nodes)+len(stale.Edges)+len(stale.ResourceEdges) != 0 || !strings.Contains(stale.StaleReason, "Configuration") {
		t.Fatalf("%v %+v", err, stale)
	}
	if _, err := m.GraphImpact(view.ID, node, ""); !errors.Is(err, ErrGraphStale) {
		t.Fatal(err)
	}
	if err := os.Remove(root + "/main.tf"); err != nil {
		t.Fatal(err)
	}
	if again, _ := m.Graph(view.ID, ""); !again.Current {
		t.Fatalf("%+v", again.StaleReason)
	}
	fake.SetWorkspace("other")
	m.store.Load(t.Context(), fake, "terraform")
	other, _ := m.Graph(view.ID, "")
	if other.Current || len(other.Nodes) != 0 || !strings.Contains(other.StaleReason, "workspace demo") {
		t.Fatalf("%+v", other)
	}
	if _, err := m.GraphImpact(view.ID, node, ""); !errors.Is(err, ErrGraphStale) {
		t.Fatal(err)
	}
}

func TestGraphFailures(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		fake       *engine.Fake
	}{
		{"init required", "init_required", &engine.Fake{GraphErr: &engine.ExitError{Code: 1, Hint: "init_required"}}},
		{"credentials", "credentials", &engine.Fake{GraphErr: &engine.ExitError{Code: 1, Hint: "credentials"}}},
		{"unclassified", "graph_failed", &engine.Fake{GraphErr: &engine.ExitError{Code: 1}}},
		{"malformed", "graph_format", &engine.Fake{GraphDOT: fixtures.GraphMalformed}},
		{"plan JSON instead of DOT", "graph_format", &engine.Fake{GraphDOT: fixtures.PlanAllActions}},
		{"too large", "graph_too_large", &engine.Fake{GraphErr: engine.ErrGraphTooLarge}},
		{"unsafe args", "unsafe_cli_args", &engine.Fake{GraphErr: engine.ErrUnsafeArgs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := setup(t, tc.fake, workspace.Info{})
			op, view := graphAndWait(t, m, GraphStart{})
			if op.Status != Failed || view.Status != GraphFailed || view.Diagnostic == nil || view.Diagnostic.Code != tc.code || len(view.Nodes) != 0 || view.Withheld {
				t.Fatalf("%+v %+v", op, view)
			}
			if _, err := m.GraphImpact(view.ID, "n0", ""); !errors.Is(err, ErrGraphNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestGraphCancelAndBusy(t *testing.T) {
	fake := &engine.Fake{BlockGraph: true, PlanJSON: fixtures.PlanAllActions}
	m, _ := setup(t, fake, workspace.Info{})
	op, run, err := m.StartGraph(GraphStart{})
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, m, op.ID, "Generating graph")
	if _, err := m.StartPlan(nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, _, err := m.StartGraph(GraphStart{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := m.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	view, _ := m.Graph(run.ID, "")
	if done.Status != Cancelled || view.Status != GraphCancelled || view.Diagnostic.Code != "cancelled" {
		t.Fatalf("%+v %+v", done, view)
	}
	// The slot is free again.
	fake.BlockGraph, fake.GraphDOT = false, fixtures.GraphCycle
	_, again := graphAndWait(t, m, GraphStart{})
	if again.Summary.Cycles != 2 {
		t.Fatalf("%+v", again.Summary)
	}

	// Shutdown while generating.
	blocked := &engine.Fake{BlockGraph: true}
	m2, _ := setup(t, blocked, workspace.Info{})
	op2, run2, _ := m2.StartGraph(GraphStart{})
	waitStage(t, m2, op2.ID, "Generating graph")
	m2.Close(5 * time.Second)
	if v, _ := m2.Graph(run2.ID, ""); v.Status != GraphCancelled || v.Diagnostic.Code != "shutdown" {
		t.Fatalf("%+v", v)
	}
}

func TestGraphUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		info workspace.Info
		want string
	}{
		{"remote execution", workspace.Info{Project: "x", Initialized: true, Backend: "remote", ExecutionMode: "remote-exec"}, "remotely"},
		{"unknown backend", workspace.Info{Project: "x", Initialized: true, Backend: "unknown", ExecutionMode: "unsupported"}, "initialized working directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform}
			m, _ := setup(t, fake, tc.info)
			ok, reason := m.GraphCapability()
			if ok || !strings.Contains(reason, tc.want) {
				t.Fatalf("%v %q", ok, reason)
			}
			_, _, err := m.StartGraph(GraphStart{})
			var rejected *RejectError
			if !errors.As(err, &rejected) || rejected.Code != "graph_unsupported" {
				t.Fatal(err)
			}
			if slices.Contains(fake.Calls(), "graph") || len(m.Graphs()) != 0 {
				t.Fatal("unsupported graph ran or was recorded")
			}
		})
	}
	// An empty state still has a configuration graph.
	m, _ := setup(t, &engine.Fake{StateJSON: fixtures.Empty, GraphDOT: fixtures.GraphTerraform}, workspace.Info{})
	if ok, reason := m.GraphCapability(); !ok {
		t.Fatal(reason)
	}
	if _, _, err := NewManager(workspace.New(workspace.Info{}), nil, "").StartGraph(GraphStart{}); err == nil {
		t.Fatal("graph accepted without a planner")
	}
}

func TestGraphRedactsSecretShapedNodes(t *testing.T) {
	dot := []byte(`digraph { "aws_instance.a[\"` + canary + `\"]" -> "aws_vpc.b"; "aws_vpc.b" -> "aws_secret.x[\"token=abc\"]" }`)
	m, _ := setup(t, &engine.Fake{GraphDOT: dot}, workspace.Info{})
	op, view := graphAndWait(t, m, GraphStart{})
	if view.Summary.Redacted != 2 {
		t.Fatalf("%+v", view.Summary)
	}
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	assertNoCanary(t, op, view, m.Graphs(), events)
	for _, n := range view.Nodes {
		if strings.Contains(n.DisplayName, "token=") {
			t.Fatalf("%+v", n)
		}
		impact, _ := m.GraphImpact(view.ID, n.ID, "")
		assertNoCanary(t, impact)
	}
}

func TestGraphWithProfile(t *testing.T) {
	fake := &engine.Fake{GraphDOT: fixtures.GraphTerraform}
	m, _, store, rec := profileSetup(t, fake, []string{"PATH=/bin", "AWS_PROFILE=inherited"})
	p, err := store.Create(testProject, profiles.Input{Name: "prod", AWSProfile: "demo-profile"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.StartGraph(GraphStart{ProfileID: p.ID, ContextFingerprint: "x"}); err == nil {
		t.Fatal("unreviewed context accepted")
	}
	op, view := graphAndWait(t, m, GraphStart{ProfileID: p.ID, ContextFingerprint: reviewedFingerprint(t, m, p.ID)})
	if view.Context == nil || view.Context.ProfileID != p.ID || !view.Current || op.Context == nil {
		t.Fatalf("%+v", view.GraphRun)
	}
	if env, ws := rec.last(); ws != "demo" || !slices.Contains(env, "AWS_PROFILE=demo-profile") {
		t.Fatalf("%v %s", env, ws)
	}
	if _, err := store.Update(testProject, p.ID, p.Revision, profiles.Input{Name: "prod", AWSProfile: "other"}); err != nil {
		t.Fatal(err)
	}
	if stale, _ := m.Graph(view.ID, ""); stale.Current || !strings.Contains(stale.StaleReason, "execution profile") {
		t.Fatalf("%+v", stale.StaleReason)
	}
}

func TestGraphRetentionIsBounded(t *testing.T) {
	m, _ := setup(t, &engine.Fake{GraphDOT: fixtures.GraphCycle}, workspace.Info{})
	for i := 0; i < maxGraphs+2; i++ {
		graphAndWait(t, m, GraphStart{})
	}
	if n := len(m.Graphs()); n != maxGraphs {
		t.Fatal(n)
	}
	m.mu.Lock()
	m.graphs[0].started = time.Now().Add(-GraphMaxAge - time.Minute)
	m.mu.Unlock()
	if n := len(m.Graphs()); n != maxGraphs-1 {
		t.Fatal(n)
	}
}
