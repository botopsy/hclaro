package graph

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// Real `graph` output recorded by scripts/fixtures/record-engine.sh. Terraform 1.5 and OpenTofu
// print the full walk graph; Terraform 1.7+ prints resources only. Both must yield the same
// resources and the one declared dependency.
func TestRealEngineGraphs(t *testing.T) {
	wantResources := []string{
		"data.terraform_remote_state.pending", "module.group.terraform_data.this",
		"terraform_data.created", "terraform_data.gone", "terraform_data.old_name", "terraform_data.renamed",
		"terraform_data.replaced", "terraform_data.replaced_first", "terraform_data.secret",
		"terraform_data.unchanged", "terraform_data.updated",
	}
	for engine, format := range map[string]string{"terraform-1.5.5": "full", "terraform-1.17.2": "resources", "tofu-1.13.9": "full"} {
		t.Run(engine, func(t *testing.T) {
			dot, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, "graph.dot"))
			if err != nil {
				t.Fatal(err)
			}
			g, err := Parse(dot)
			if err != nil {
				t.Fatalf("real graph rejected: %v", err)
			}
			if g.Format != format || g.Summary.Cycles != 0 || g.Summary.Unknown != 0 {
				t.Fatalf("format %s summary %+v", g.Format, g.Summary)
			}
			var got []string
			for _, n := range g.Nodes {
				if isResource(n.Kind) {
					got = append(got, n.DisplayName)
				}
			}
			sort.Strings(got)
			if !slices.Equal(got, wantResources) {
				t.Fatalf("resources:\n got %v\nwant %v", got, wantResources)
			}
			if links := resourceLinks(g, true); !slices.Contains(links, "data.terraform_remote_state.pending -> terraform_data.created (declared)") {
				t.Fatalf("links %v", links)
			}
		})
	}
}
