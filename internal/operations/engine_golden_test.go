package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/engine"
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
				"Plan: 5 to add, 3 to change, 6 to destroy.",
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

// Real failure streams (M27.04, fixtures/engines/local-project): the sanitized diagnostics are
// what classify reads, so the recorded text must reach the same code through translate, and
// the var-file value the engine echoes must be scrubbed.
func TestRealEngineFailureStreams(t *testing.T) {
	project := filepath.Join("..", "..", "fixtures", "engines", "local-project")
	k := collectKnown(project, []string{"missing.tfvars"}, nil)
	for _, recording := range []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"} {
		for file, want := range map[string]string{
			"local-missing-file.jsonl": "", // no shared class: the manager reports plan_failed
			"local-noinit.jsonl":       "init_required",
			"local-lockfile.jsonl":     "init_required",
		} {
			t.Run(recording+"/"+file, func(t *testing.T) {
				stream, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", recording, file))
				if err != nil {
					t.Fatal(err)
				}
				e := &entry{}
				for _, line := range bytes.Split(bytes.TrimSpace(stream), []byte("\n")) {
					_, text, diag, ok := translate(line, "plan", "Terraform", k)
					if !ok {
						t.Fatalf("real stream line not recognized: %s", line)
					}
					if strings.Contains(text, "DO_NOT_LEAK") {
						t.Fatalf("var-file value shown: %s", text)
					}
					if diag != nil {
						e.op.Diagnostics = append(e.op.Diagnostics, *diag)
					}
				}
				text := diagnosticsText(e)
				if len(e.op.Diagnostics) == 0 || strings.Contains(text, "DO_NOT_LEAK") {
					t.Fatalf("diagnostics %q", text)
				}
				if got := engine.Classify(text); got != want {
					t.Fatalf("classified %q, want %q: %s", got, want, text)
				}
			})
		}
	}
}
