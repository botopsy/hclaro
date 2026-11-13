package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real engine `plan -json` streams recorded by scripts/fixtures/record-engine.sh.
func TestRealEngineStreams(t *testing.T) {
	for _, engine := range []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"} {
		for kind, want := range map[string][]string{
			"plan": {
				"Planning started",
				"terraform_data.replaced: plan to replace (cannot_update)",
				"terraform_data.renamed: plan to move",
				"data.terraform_remote_state.pending: plan to read (read_because_dependency_pending)",
				"Plan: 5 to add, 3 to change, 3 to destroy.",
				"Output changes: 3",
			},
			"drift": {"Drift scan started (refresh-only plan)", "Refresh-only plan complete."},
		} {
			t.Run(engine+"/"+kind, func(t *testing.T) {
				file := kind + ".jsonl"
				stream, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, file))
				if err != nil {
					t.Fatal(err)
				}
				var texts []string
				for _, line := range bytes.Split(bytes.TrimSpace(stream), []byte("\n")) {
					_, text, _, ok := translate(line, kind, "Terraform", nil)
					if !ok {
						t.Fatalf("real stream line not recognized: %s", line)
					}
					texts = append(texts, text)
				}
				all := strings.Join(texts, "\n")
				for _, w := range want {
					if !strings.Contains(all, w) {
						t.Errorf("missing %q in:\n%s", w, all)
					}
				}
			})
		}
	}
}
