package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/engine"
	"hclaro/internal/workspace"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectKnownInputs(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"prod.tfvars": `db_password = "pa\"ss-from-prod" # trailing comment "comment-value"
tags = { owner = "team-ledger" }
cert = <<-EOT
  -----BEGIN CERT-----
  heredoc-line-value
  EOT
short = "ab"
`,
		"terraform.tfvars":       `auto_loaded = "auto-loaded-value"`,
		"extra.auto.tfvars.json": `{"api": {"keys": ["json-nested-value"]}, "n": 12345}`,
		"unselected.tfvars":      `other = "unselected-value"`,
	})
	k := collectKnown(root, []string{"prod.tfvars"}, []string{"TF_VAR_token=env-token-value", "TF_VAR_map={a=\"env-map-value\"}", "HOME=/home/someone"})
	text := strings.Join([]string{`pa"ss-from-prod`, "team-ledger", "heredoc-line-value", "auto-loaded-value", "json-nested-value", "env-token-value", "env-map-value"}, " ")
	if got := k.scrub(text); strings.Trim(strings.ReplaceAll(got, "[redacted]", ""), " ") != "" {
		t.Fatalf("not scrubbed: %q", got)
	}
	for _, kept := range []string{"unselected-value", "comment-value", "ab", "/home/someone", "12345"} {
		if k.scrub(kept) != kept {
			t.Errorf("%q should not be a known value", kept)
		}
	}
}

func TestKnownScrubPrefersLongestValue(t *testing.T) {
	k := collectKnown(t.TempDir(), nil, []string{"TF_VAR_a=secret", "TF_VAR_b=secret-value-long"})
	if got := k.scrub("x secret-value-long y"); got != "x [redacted] y" {
		t.Fatal(got)
	}
}

func TestDiagnosticsScrubKnownInputs(t *testing.T) {
	// A provider error echoing an input value that no line heuristic recognizes.
	diag := `{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Invalid value \"opaque-input-7731\"","detail":"The server rejected opaque-input-7731 for field name.\nother line"}}`
	logLine := `{"type":"log","@level":"error","@message":"request with opaque-input-7731 failed"}`
	fake := &engine.Fake{Lines: [][]byte{[]byte(diag), []byte(logLine)}, PlanErr: &engine.ExitError{Code: 1}}
	m, root := setup(t, fake, workspace.Info{})
	writeFiles(t, root, map[string]string{"prod.tfvars": `name = "opaque-input-7731"`})
	op, err := m.StartPlan([]string{"prod.tfvars"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	all := strings.Builder{}
	for _, e := range events {
		all.WriteString(e.Text + "\n")
	}
	for _, d := range done.Diagnostics {
		all.WriteString(d.Summary + "\n" + d.Detail + "\n")
	}
	if strings.Contains(all.String(), "opaque-input-7731") || !strings.Contains(all.String(), "other line") || len(done.Diagnostics) != 1 {
		t.Fatalf("%q", all.String())
	}
}
