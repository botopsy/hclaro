package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/plan"
	"hclaro/internal/state"
)

func mustParse(t *testing.T, dot []byte) *Graph {
	t.Helper()
	g, err := Parse(dot)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func byName(t *testing.T, g *Graph, name string) int {
	t.Helper()
	for i, n := range g.Nodes {
		if n.DisplayName == name {
			return i
		}
	}
	t.Fatalf("node %q not found in %v", name, names(g, nil))
	return -1
}

func names(g *Graph, ids []string) []string {
	out := []string{}
	if ids == nil {
		for _, n := range g.Nodes {
			out = append(out, n.DisplayName)
		}
	}
	for _, id := range ids {
		out = append(out, g.Nodes[g.NodeIndex(id)].DisplayName)
	}
	slices.Sort(out)
	return out
}

func reachNames(g *Graph, list []Reach, resourcesOnly bool) []string {
	out := []string{}
	for _, r := range list {
		n := g.Nodes[g.NodeIndex(r.Node)]
		if !resourcesOnly || isResource(n.Kind) {
			out = append(out, n.DisplayName)
		}
	}
	slices.Sort(out)
	return out
}

// resourceLinks returns resource edges as "from -> to (confidence)" for comparison.
func resourceLinks(g *Graph, withConfidence bool) []string {
	out := []string{}
	for _, e := range g.ResourceEdges {
		s := g.Nodes[g.NodeIndex(e.From)].DisplayName + " -> " + g.Nodes[g.NodeIndex(e.To)].DisplayName
		if withConfidence {
			s += " (" + e.Confidence + ")"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func TestParseEdgeCases(t *testing.T) {
	g := mustParse(t, fixtures.GraphEdgeCases)
	quoted := byName(t, g, `module.workers["blue.eu"].aws_instance.node["quote\"key"]`)
	if n := g.Nodes[quoted]; n.Kind != KindResource || n.ModulePath != `module.workers["blue.eu"]` || n.Type != "aws_instance" || !n.parsed.instanced {
		t.Fatalf("%+v", n)
	}
	kinds := map[string]string{
		"var.region": KindVariable, "local.tags": KindLocal, "output.id": KindOutput, "check.health": KindCheck,
		"ephemeral.aws_secret.tmp": KindEphemeral, "data.aws_region.current": KindData, "concatenated.name": KindResource,
		"lone.resource": KindResource, "aws_iam_role.lonely": KindResource, "module.net.module.inner.aws_subnet.a": KindResource,
		"my_type.name (weird phase)": KindUnknown,
	}
	for name, kind := range kinds {
		if got := g.Nodes[byName(t, g, name)].Kind; got != kind {
			t.Errorf("%s: kind %s, want %s", name, got, kind)
		}
	}
	if m := g.Nodes[byName(t, g, "module.a.module.b.aws_s3_bucket.deep")].ModulePath; m != "module.a.module.b" {
		t.Fatal(m)
	}
	// Edge to a subgraph: one edge per member.
	front := byName(t, g, "aws_lb.front")
	if got := names(g, g.Analyze(front).DirectDependencies); !slices.Equal(got, []string{"aws_instance.web[0]", "aws_instance.web[1]"}) {
		t.Fatal(got)
	}
	// A chained edge statement is two edges; the self-loop is dropped.
	vpc := byName(t, g, "aws_vpc.main")
	if got := names(g, g.Analyze(vpc).DirectDependencies); !slices.Equal(got, []string{"data.aws_region.current"}) {
		t.Fatal(got)
	}
	if g.Nodes[vpc].InCycle || g.Summary.Cycles != 0 {
		t.Fatal("self-loop counted as a cycle")
	}
	// The port suffix is not part of the node ID.
	byName(t, g, "module.net.aws_route_table.rt")
	// Disconnected nodes are kept.
	if im := g.Analyze(byName(t, g, "aws_iam_role.lonely")); len(im.Upstream)+len(im.Downstream) != 0 {
		t.Fatalf("%+v", im)
	}
	if g.Summary.Redacted != 1 || g.Summary.Unknown != 2 {
		t.Fatalf("%+v", g.Summary)
	}
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "password=") {
		t.Fatalf("secret-shaped node name leaked: %s", b)
	}
	if !strings.Contains(strings.Join(g.Warnings, " "), "withheld") {
		t.Fatal(g.Warnings)
	}
	if g.Format != "resources" {
		t.Fatal(g.Format)
	}
}

func TestParseFullFormat(t *testing.T) {
	g := mustParse(t, fixtures.GraphTofu)
	if g.Format != "full" || g.Summary.Resources != 17 || g.Summary.DataSources != 1 || g.Summary.Dropped == 0 {
		t.Fatalf("%s %+v", g.Format, g.Summary)
	}
	for _, n := range g.Nodes {
		if strings.Contains(n.DisplayName, "(close)") || strings.Contains(n.DisplayName, "(expand)") || n.DisplayName == "root" || strings.HasPrefix(n.DisplayName, "meta.") {
			t.Fatalf("bookkeeping node kept: %q", n.DisplayName)
		}
	}
	// Close and root nodes depend on everything; keeping them would make every node a cycle.
	if g.Summary.Cycles != 0 {
		t.Fatalf("%d cycles", g.Summary.Cycles)
	}
	provider := g.Nodes[byName(t, g, `provider["registry.terraform.io/hashicorp/aws"]`)]
	if provider.Kind != KindProvider || provider.ModulePath != "" {
		t.Fatalf("%+v", provider)
	}
	if n := g.Nodes[byName(t, g, "module.network.output.aws_subnet_id")]; n.Kind != KindOutput || n.ModulePath != "module.network" {
		t.Fatalf("%+v", n)
	}
	if n := g.Nodes[byName(t, g, "module.compute.module.workers")]; n.Kind != KindModule {
		t.Fatalf("%+v", n)
	}
	links := resourceLinks(g, true)
	for _, want := range []string{
		"module.compute.aws_instance.main -> module.network.aws_subnet.main (derived)",
		"aws_db_instance.primary -> module.network.aws_subnet.main (derived)",
		"aws_db_instance.primary -> aws_security_group.web (declared)",
	} {
		if !slices.Contains(links, want) {
			t.Errorf("missing %s in %v", want, links)
		}
	}
	if g.Summary.DerivedEdges == 0 || !strings.Contains(strings.Join(g.Warnings, " "), "derived") {
		t.Fatal(g.Summary, g.Warnings)
	}
}

// The full walk graph and the resource-only graph of the same configuration describe the same
// resource dependencies.
func TestFormatsAgree(t *testing.T) {
	full, resources := mustParse(t, fixtures.GraphTofu), mustParse(t, fixtures.GraphTerraform)
	if a, b := resourceLinks(full, false), resourceLinks(resources, false); !slices.Equal(a, b) {
		t.Fatalf("full:\n%s\nresources:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	for _, g := range []*Graph{full, resources} {
		sg := g.Analyze(byName(t, g, "aws_security_group.web"))
		if got := reachNames(g, sg.Downstream, true); !slices.Equal(got, []string{"aws_db_instance.primary", "aws_instance.retired", "aws_lb.edge", "module.compute.aws_instance.main", "module.compute.module.workers.aws_instance.node", "module.edge.aws_lb.main"}) {
			t.Errorf("%s downstream %v", g.Format, got)
		}
		if got := reachNames(g, sg.Upstream, true); !slices.Equal(got, []string{"aws_vpc.imported"}) {
			t.Errorf("%s upstream %v", g.Format, got)
		}
		if sg.DownstreamResources != 6 {
			t.Errorf("%s fan-out %d", g.Format, sg.DownstreamResources)
		}
	}
	// Distances: the worker node is two hops from the security group in the resource graph.
	sg := resources.Analyze(byName(t, resources, "aws_security_group.web"))
	for _, r := range sg.Downstream {
		if n := resources.Nodes[resources.NodeIndex(r.Node)]; n.DisplayName == "module.compute.module.workers.aws_instance.node" && r.Distance != 2 {
			t.Fatal(r)
		}
	}
	if got := names(resources, sg.DirectDependents); !slices.Equal(got, []string{"aws_db_instance.primary", "aws_instance.retired", "aws_lb.edge", "module.compute.aws_instance.main"}) {
		t.Fatal(got)
	}
}

func TestCyclesAreBounded(t *testing.T) {
	g := mustParse(t, fixtures.GraphCycle)
	if g.Summary.Cycles != 2 || g.Summary.CycleNodes != 5 {
		t.Fatalf("%+v", g.Summary)
	}
	a := byName(t, g, "aws_instance.a")
	im := g.Analyze(a)
	if !im.InCycle || !slices.Equal(reachNames(g, im.Downstream, false), []string{"aws_instance.b", "aws_instance.c", "aws_instance.d"}) {
		t.Fatalf("%+v", im)
	}
	// a is reachable from itself through the cycle, but never listed twice or as its own dependent.
	if slices.Contains(reachNames(g, im.Upstream, false), "aws_instance.a") || len(im.Upstream) != 2 {
		t.Fatalf("%+v", im.Upstream)
	}
	if g.Nodes[byName(t, g, "aws_instance.d")].InCycle || g.Nodes[byName(t, g, "aws_iam_role.alone")].InCycle {
		t.Fatal("node outside a cycle marked")
	}
	if !strings.Contains(strings.Join(g.Warnings, " "), "2 cycles") {
		t.Fatal(g.Warnings)
	}
	if len(g.Reachable(a)) != 3 {
		t.Fatal(g.Reachable(a))
	}
}

func TestParseRejectsInvalidInput(t *testing.T) {
	for name, input := range map[string]string{
		"malformed":    string(fixtures.GraphMalformed),
		"undirected":   `graph { "a.b" -- "c.d" }`,
		"no graph":     `{"format_version":"1.0"}`,
		"unquoted":     `digraph { aws_instance.web -> x }`,
		"unterminated": `digraph { "a.b`,
		"trailing":     `digraph { } digraph { }`,
		"empty":        ``,
		"plus":         `digraph { "a" + b }`,
	} {
		if _, err := Parse([]byte(input)); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var b strings.Builder
	b.WriteString("digraph {\n")
	for i := 0; i <= MaxDOTNodes; i++ {
		fmt.Fprintf(&b, "\"aws_instance.n%d\"\n", i)
	}
	b.WriteString("}\n")
	if _, err := Parse([]byte(b.String())); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	if _, err := Parse(make([]byte, MaxInputBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	deep := strings.Repeat("subgraph { ", maxNesting+1) + strings.Repeat("} ", maxNesting+1)
	if _, err := Parse([]byte("digraph { " + deep + "}")); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
}

func TestAnnotateMapsOnlyProvenAddresses(t *testing.T) {
	index, err := state.Parse(fixtures.State)
	if err != nil {
		t.Fatal(err)
	}
	g := mustParse(t, fixtures.GraphTerraform)
	nodes := g.Annotate(index)
	get := func(name string) Node { return nodes[byName(t, g, name)] }
	subnet := get("module.network.aws_subnet.main")
	if subnet.Confidence != Exact || subnet.InstanceCount != 52 || len(subnet.Instances) != maxInstances || subnet.Instances[0].Address != "module.network.aws_subnet.main[0]" {
		t.Fatalf("%+v", subnet)
	}
	if _, ok := index.ByID[subnet.Instances[0].ResourceID]; !ok {
		t.Fatal("instance ID is not an explorer ID")
	}
	if n := get("data.aws_caller_identity.current"); n.Confidence != Exact || n.InstanceCount != 1 {
		t.Fatalf("%+v", n)
	}
	// Not recorded in state: a configuration resource, never mapped by similarity.
	if n := get("aws_security_group.web"); n.Confidence != Configuration || n.InstanceCount != 0 || len(n.Instances) != 0 {
		t.Fatalf("%+v", n)
	}
	// The worker instances are keyed by a for_each module ("blue.eu"); the configuration node
	// still maps because keys are removed by grammar, not by guessing.
	if n := get("module.compute.module.workers.aws_instance.node"); n.Confidence != Exact {
		t.Fatalf("%+v", n)
	}
	full := mustParse(t, fixtures.GraphTofu)
	for _, n := range full.Annotate(index) {
		if !isResource(n.Kind) && (n.Confidence != Internal || n.InstanceCount != 0) {
			t.Fatalf("internal node mapped: %+v", n)
		}
	}
	// An instanced node maps to its exact instance only.
	edge := mustParse(t, fixtures.GraphEdgeCases)
	custom := state.Index{Resources: []state.Resource{{ID: "a", Address: "aws_instance.web[0]", Mode: "managed"}, {ID: "b", Address: "aws_instance.web[1]", Mode: "managed"}, {ID: "c", Address: "aws_vpc.main", Mode: "data"}}}
	annotated := edge.Annotate(custom)
	if n := annotated[byName(t, edge, "aws_instance.web[0]")]; n.InstanceCount != 1 || n.Instances[0].ResourceID != "a" {
		t.Fatalf("%+v", n)
	}
	// A managed node never maps to a data instance of the same address.
	if n := annotated[byName(t, edge, "aws_vpc.main")]; n.Confidence != Configuration {
		t.Fatalf("%+v", n)
	}
	for _, n := range annotated {
		if n.Kind == KindUnknown && n.Confidence != Unknown {
			t.Fatalf("%+v", n)
		}
	}
}

func TestOverlayUsesOnlyPlannedChanges(t *testing.T) {
	review, err := plan.Parse(fixtures.PlanAllActions)
	if err != nil {
		t.Fatal(err)
	}
	var changes []PlannedChange
	for _, c := range review.Changes {
		if c.Action != plan.NoOp {
			changes = append(changes, PlannedChange{ChangeID: c.ID, Address: c.Address, Mode: c.Mode, Action: c.Action})
		}
	}
	changes = append(changes, PlannedChange{ChangeID: "x", Address: "aws_sns_topic.unrelated", Mode: "managed", Action: "create"})
	g := mustParse(t, fixtures.GraphTerraform)
	nodes := g.Annotate(state.Empty())
	unmapped := g.Overlay(nodes, changes)
	if len(unmapped) != 1 || unmapped[0].Address != "aws_sns_topic.unrelated" {
		t.Fatalf("%+v", unmapped)
	}
	worker := nodes[byName(t, g, "module.compute.module.workers.aws_instance.node")]
	if len(worker.Planned) != 1 || worker.Planned[0].Action != plan.Replace {
		t.Fatalf("%+v", worker)
	}
	if n := nodes[byName(t, g, "data.aws_caller_identity.current")]; len(n.Planned) != 1 || n.Planned[0].Action != plan.Read {
		t.Fatalf("%+v", n)
	}
	// The no-op subnet instance is not a planned change; the updated one is.
	if n := nodes[byName(t, g, "module.network.aws_subnet.main")]; len(n.Planned) != 1 || n.Planned[0].Address != "module.network.aws_subnet.main[0]" {
		t.Fatalf("%+v", n)
	}
	// A node with no planned change stays unmarked even when it is downstream of one.
	if n := nodes[byName(t, g, "module.edge.aws_lb.main")]; len(n.Planned) != 0 {
		t.Fatalf("%+v", n)
	}
}

func TestLargeGraphStaysFast(t *testing.T) {
	start := time.Now()
	g := mustParse(t, fixtures.GraphLarge)
	if g.Summary.Resources != 1000 || len(g.Edges) == 0 {
		t.Fatalf("%+v", g.Summary)
	}
	for i := range g.Nodes {
		g.Analyze(i)
	}
	g.Annotate(state.Empty())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("parse and 1,000 analyses took %s", elapsed)
	}
	// Truncated reach lists keep their totals.
	var b strings.Builder
	b.WriteString("digraph {\n")
	for i := 0; i < MaxReach+10; i++ {
		fmt.Fprintf(&b, "\"aws_instance.n%d\" -> \"aws_vpc.hub\"\n", i)
	}
	b.WriteString("}\n")
	hub := mustParse(t, []byte(b.String()))
	im := hub.Analyze(byName(t, hub, "aws_vpc.hub"))
	if !im.Truncated || im.DownstreamTotal != MaxReach+10 || len(im.Downstream) != MaxReach {
		t.Fatalf("%v %d %d", im.Truncated, im.DownstreamTotal, len(im.Downstream))
	}
}

func TestConfigAddress(t *testing.T) {
	for in, want := range map[string]string{
		`aws_instance.web[0]`: `aws_instance.web`,
		`module.a["x.y"].module.b[2].aws_s3_bucket.c["k\"]"]`: `module.a.module.b.aws_s3_bucket.c`,
		`data.aws_ami.x`: `data.aws_ami.x`,
	} {
		if got, ok := ConfigAddress(in); !ok || got != want {
			t.Errorf("%s: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{``, `a..b`, `a.b[`, `a.b.`, `a[0]x`} {
		if _, ok := ConfigAddress(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
