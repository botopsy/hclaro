package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/engine"
	"hclaro/internal/imports"
	"hclaro/internal/workspace"
)

// A credential env value echoed by the engine is scrubbed from plan, apply and drift logs,
// diagnostics and history records, not only from init and project utilities.
func TestCredentialEnvValuesScrubbedInPlanApplyDrift(t *testing.T) {
	const cred, token = "opaque-cred-4471", "opaque-registry-8812"
	t.Setenv("ARM_CLIENT_SECRET", cred)
	t.Setenv("TF_TOKEN_app_example_com", token)
	echo := lines([]byte(`{"type":"diagnostic","diagnostic":{"severity":"warning","summary":"backend said opaque-cred-4471","detail":"rejected opaque-registry-8812 for app"}}
{"type":"log","@level":"warn","@message":"retrying with opaque-cred-4471"}`))
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions, Lines: echo, ApplyLines: echo}
	m, _ := setup(t, fake, workspace.Info{})
	m.Reload = func(context.Context) {}
	rec := &records{}
	m.Recorder = rec
	id := planned(t, m)
	a, err := approve(m, id)
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.StartApply(id, a.Token, a.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
	scanAndWait(t, m, DriftStart{})
	kinds := map[string]bool{}
	values := []any{rec.all()}
	for _, o := range m.Operations() {
		kinds[o.Kind] = true
		_, log, _ := m.Operation(o.ID)
		events, _, _, _ := log.Since(0)
		values = append(values, o, events)
	}
	if !kinds["plan"] || !kinds["apply"] || !kinds["drift"] || len(rec.all()) < 3 {
		t.Fatalf("kinds %v records %d", kinds, len(rec.all()))
	}
	b, _ := json.Marshal(values)
	for _, leak := range []string{cred, token} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%q leaked", leak)
		}
	}
	if !strings.Contains(string(b), "backend said [redacted]") {
		t.Fatalf("diagnostic missing: %s", b)
	}
}

// A tfvars value equal to a key word must not break the assignment checks that run on the
// same text.
func TestKeyWordInputsDoNotWeakenRedaction(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"terraform.tfvars": `a = "password"
b = "name"
c = "value"
`})
	k := collectKnown(root, nil, nil)
	for _, line := range []string{`admin_password = "hunter2-x"`, `db_password: hunter2-x`, `{"name": "client_secret", "value": "hunter2-x"}`} {
		if got := sanitize(line, k, 1); strings.Contains(got, "hunter2") {
			t.Errorf("%q -> %q", line, got)
		}
	}
	if got := sanitize("value of name is fine", k, 1); got != "[redacted] of [redacted] is fine" {
		t.Errorf("known values still scrubbed from shown lines: %q", got)
	}
}

func TestKeyWordInputsDoNotWeakenGeneratedConfig(t *testing.T) {
	generated := "resource \"aws_db_instance\" \"main\" {\n  admin_password = \"hunter2-gen\"\n  identifier     = \"main\"\n}\n"
	fake := &engine.Fake{PlanJSON: fixtures.PlanImport, GeneratedConfig: []byte(generated)}
	m, _ := importSetup(t, fake, map[string]string{"main.tf": "terraform {}\n", "terraform.tfvars": "x = \"password\"\n"})
	op, err := m.StartImport(reviewed(t, m, imports.Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "legacy.tf", GenerateConfig: true}))
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	p, _ := m.Plan(done.PlanID)
	if strings.Contains(p.GeneratedConfig, "hunter2") || !strings.Contains(p.GeneratedConfig, `identifier     = "main"`) {
		t.Fatalf("%q", p.GeneratedConfig)
	}
}

// A generated secret that is neither a known input nor a known credential shape is withheld
// by the same random-string check the plan tree uses.
func TestSanitizeWithholdsRandomLookingSecrets(t *testing.T) {
	const secret = "wJalrXUtnFEMIK7MDENGbPxRfiCYzEXAMPLEKEY1"
	diag := fmt.Sprintf(`{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Provider error","detail":"The API returned key %s for this request.\nsecond line"}}`, secret)
	fake := &engine.Fake{Lines: [][]byte{[]byte(diag)}, PlanErr: &engine.ExitError{Code: 1}}
	m, _ := setup(t, fake, workspace.Info{})
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, m, op.ID)
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	b, _ := json.Marshal([]any{done, events})
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), "second line") {
		t.Fatalf("%s", b)
	}
}

// Human-format output quotes configuration source and expression values; those lines are
// dropped, as the JSON path drops the snippet.
func TestHumanLineDropsSnippets(t *testing.T) {
	for _, line := range []string{
		`│   12:   value = "hunter2"`,
		`│    3:   bucket_name = "hunter2"`,
		`│     │ var.db_password is "hunter2"`,
		`│     ├────────────────`,
		`  12:   value = "hunter2"`,
		`12:`,
	} {
		if _, text, diag := humanLine("stderr", line, nil); text != "" || diag != nil {
			t.Errorf("%q -> %q", line, text)
		}
	}
	for _, line := range []string{`│ An argument named "value" is not expected here.`, `│   on main.tf line 12, in resource "x" "y":`, `Upgraded at 12:30 today`} {
		if _, text, _ := humanLine("stderr", line, nil); text == "" {
			t.Errorf("%q dropped", line)
		}
	}
}

func TestKnownSetReportsIncomplete(t *testing.T) {
	root := t.TempDir()
	if k := collectKnown(root, nil, []string{"TF_VAR_a=value-one"}); k.incomplete {
		t.Fatal("complete set reported incomplete")
	}
	if k := collectKnown(root, []string{"missing.tfvars"}, nil); !k.incomplete {
		t.Error("unreadable selected file not reported")
	}
	writeFiles(t, root, map[string]string{"big.auto.tfvars": "x = \"" + strings.Repeat("a", maxKnownFile) + "\"\n"})
	if k := collectKnown(root, nil, nil); !k.incomplete {
		t.Error("truncated file not reported")
	}
	many := t.TempDir()
	var b strings.Builder
	for i := 0; i <= maxKnown; i++ {
		fmt.Fprintf(&b, "v%d = \"value-%d\"\n", i, i)
	}
	writeFiles(t, many, map[string]string{"terraform.tfvars": b.String()})
	if k := collectKnown(many, nil, nil); !k.incomplete {
		t.Error("cap not reported")
	}
	// Credential env values are added before variable files, so the cap cannot crowd them out.
	if k := collectKnown(many, nil, []string{"AWS_SECRET_ACCESS_KEY=opaque-cred-4471"}); k.scrub("x opaque-cred-4471") != "x [redacted]" {
		t.Error("credential env value crowded out by the cap")
	}
}

func TestPlanLogsIncompleteKnownSet(t *testing.T) {
	fake := &engine.Fake{PlanJSON: fixtures.PlanAllActions}
	m, root := setup(t, fake, workspace.Info{})
	writeFiles(t, root, map[string]string{"big.auto.tfvars": "x = \"" + strings.Repeat("a", maxKnownFile) + "\"\n"})
	op, err := m.StartPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, op.ID)
	_, log, _ := m.Operation(op.ID)
	events, _, _, _ := log.Since(0)
	for _, e := range events {
		if e.Level == "warn" && strings.Contains(e.Text, "could not all be read") {
			return
		}
	}
	t.Fatalf("no notice in %+v", events)
}
