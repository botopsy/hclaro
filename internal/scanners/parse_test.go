package scanners_test

import (
	"strings"
	"testing"

	"hclaro/internal/scanners"
)

func TestParseTFLint(t *testing.T) {
	out := `{"issues":[{"rule":{"name":"terraform_unused_declarations","severity":"warning","link":"https://x"},"message":"variable \"a\" is declared but not used","range":{"filename":"main.tf","start":{"line":3,"column":1},"end":{"line":3,"column":9}},"callers":[]},{"rule":{"name":"r2","severity":"error"},"message":"m","range":{"filename":"x.tf","start":{"line":1}}}],"errors":[]}`
	got, hidden, err := scanners.Parse("tflint", []byte(out))
	if err != nil || hidden != 0 || len(got) != 2 {
		t.Fatalf("%v %d %v", got, hidden, err)
	}
	if got[0] != (scanners.Issue{Severity: "medium", Rule: "terraform_unused_declarations", Message: `variable "a" is declared but not used`, File: "main.tf", Line: 3}) || got[1].Severity != "high" {
		t.Errorf("issues = %+v", got)
	}
	// tflint reported an error of its own: a partial result must not read as clean.
	if _, _, err := scanners.Parse("tflint", []byte(`{"issues":[],"errors":[{"message":"boom"}]}`)); err == nil {
		t.Error("an error entry parsed as a result")
	}
}

func TestParseTrivy(t *testing.T) {
	out := `{"Results":[{"Target":"main.tf","Class":"config","Type":"terraform","Misconfigurations":[{"ID":"AVD-AWS-0107","Title":"An ingress rule allows traffic from /0.","Message":"Security group rule allows ingress from public internet.","Severity":"CRITICAL","CauseMetadata":{"StartLine":12,"Code":{"Lines":[{"Content":"secret = \"hunter2\""}]}}}]}]}`
	got, _, err := scanners.Parse("trivy", []byte(out))
	if err != nil || len(got) != 1 || got[0] != (scanners.Issue{Severity: "critical", Rule: "AVD-AWS-0107", Message: "An ingress rule allows traffic from /0.", File: "main.tf", Line: 12}) {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _, err := scanners.Parse("trivy", []byte(`{"SchemaVersion":2}`)); err != nil || len(got) != 0 {
		t.Errorf("clean report: %v %v", got, err)
	}
}

func TestParseCheckov(t *testing.T) {
	one := `{"check_type":"terraform","results":{"passed_checks":[],"failed_checks":[{"check_id":"CKV_AWS_18","check_name":"Ensure the S3 bucket has access logging enabled","file_path":"/main.tf","file_line_range":[5,9],"severity":null}]},"summary":{}}`
	want := scanners.Issue{Severity: "unrated", Rule: "CKV_AWS_18", Message: "Ensure the S3 bucket has access logging enabled", File: "main.tf", Line: 5}
	for _, doc := range []string{one, "[" + one + "]"} {
		got, _, err := scanners.Parse("checkov", []byte(doc))
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("%.20s: %+v %v", doc, got, err)
		}
	}
}

func TestParseRejectsMalformedAndCapsOversized(t *testing.T) {
	for _, name := range scanners.Names {
		for _, bad := range []string{"", "not json", "{", `{"issues":"x"}`} {
			if _, _, err := scanners.Parse(name, []byte(bad)); err == nil {
				t.Errorf("%s accepted %q", name, bad)
			}
		}
	}
	var b strings.Builder
	b.WriteString(`{"issues":[`)
	for i := 0; i < 5100; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"rule":{"name":"r","severity":"notice"},"message":"m","range":{"filename":"a.tf","start":{"line":1}}}`)
	}
	b.WriteString(`],"errors":[]}`)
	got, hidden, err := scanners.Parse("tflint", []byte(b.String()))
	if err != nil || len(got) != 5000 || hidden != 100 {
		t.Fatalf("%d %d %v", len(got), hidden, err)
	}
}
