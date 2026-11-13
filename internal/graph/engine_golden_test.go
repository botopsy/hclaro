package graph

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"hclaro/internal/state"
)

// Real `graph` output recorded by scripts/fixtures/record-engine.sh. Terraform 1.5 and OpenTofu
// print the full walk graph, with one "(orphan)" node per instance recorded only in state;
// Terraform 1.7+ prints configuration-level resources only. Both must yield resource nodes, the
// declared dependency, and exact state mappings for the state-only resources.
func TestRealEngineGraphs(t *testing.T) {
	common := []string{
		"data.terraform_remote_state.pending", "module.group.terraform_data.this",
		"terraform_data.created", "terraform_data.gone", "terraform_data.old_name", "terraform_data.renamed",
		"terraform_data.replaced", "terraform_data.replaced_first", "terraform_data.secret",
		"terraform_data.unchanged", "terraform_data.updated",
	}
	full := append([]string{`module.old["x"].terraform_data.this`, "terraform_data.counted_gone[0]", "terraform_data.counted_gone[1]"}, common...)
	resources := append([]string{"module.old.terraform_data.this", "terraform_data.counted_gone"}, common...)
	for engine, want := range map[string]struct {
		format    string
		resources []string
	}{
		"terraform-1.5.5":  {"full", full},
		"terraform-1.17.2": {"resources", resources},
		"tofu-1.13.9":      {"full", full},
	} {
		t.Run(engine, func(t *testing.T) {
			dir := filepath.Join("..", "..", "fixtures", "engines", engine)
			dot, err := os.ReadFile(filepath.Join(dir, "graph.dot"))
			if err != nil {
				t.Fatal(err)
			}
			g, err := Parse(dot)
			if err != nil {
				t.Fatalf("real graph rejected: %v", err)
			}
			if g.Format != want.format || g.Summary.Cycles != 0 || g.Summary.Unknown != 0 {
				t.Fatalf("format %s summary %+v", g.Format, g.Summary)
			}
			var got []string
			for _, n := range g.Nodes {
				if isResource(n.Kind) {
					got = append(got, n.DisplayName)
				}
			}
			sort.Strings(got)
			sort.Strings(want.resources)
			if !slices.Equal(got, want.resources) {
				t.Fatalf("resources:\n got %v\nwant %v", got, want.resources)
			}
			if links := resourceLinks(g, true); !slices.Contains(links, "data.terraform_remote_state.pending -> terraform_data.created (declared)") {
				t.Fatalf("links %v", links)
			}
			recorded, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			index, err := state.Parse(recorded)
			if err != nil {
				t.Fatal(err)
			}
			mapped := map[string]int{}
			for _, n := range g.Annotate(index) {
				if n.Confidence == Exact {
					mapped[n.DisplayName] = n.InstanceCount
				}
			}
			for name, count := range map[string]int{"terraform_data.gone": 1, "terraform_data.old_name": 1} {
				if mapped[name] != count {
					t.Errorf("%s mapped to %d instances, want %d (%v)", name, mapped[name], count, mapped)
				}
			}
			counted, module := mapped["terraform_data.counted_gone[0]"]+mapped["terraform_data.counted_gone[1]"], mapped[`module.old["x"].terraform_data.this`]
			if want.format == "resources" {
				counted, module = mapped["terraform_data.counted_gone"], mapped["module.old.terraform_data.this"]
			}
			if counted != 2 || module != 1 {
				t.Errorf("keyed orphans mapped to %d and %d instances (%v)", counted, module, mapped)
			}
		})
	}
}
