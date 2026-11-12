package imports

import (
	"strings"
	"testing"
)

func TestSecretLines(t *testing.T) {
	text := []string{
		`resource "x" "y" {`,        // 0
		`  tags = {`,                // 1
		`    Name = "web"`,          // 2 ordinary block: shown
		`  }`,                       // 3
		`  passwords = [`,           // 4
		`    "a", "b",`,             // 5
		`  ]`,                       // 6
		`  note = "}]) # not code"`, // 7 brackets in a string are ignored
		`  url = "${var.a["k"]}"`,   // 8 nested quotes in a template
		`  # api_key = [`,           // 9 comment
		`  x = 1`,                   // 10
		`  env = [{ name = "LOG_LEVEL", value = "info" }]`, // 11
		`  user_data = <<-EOT`,                             // 12
		`    secret`,                                       // 13
		`  EOT`,                                            // 14
		`  after = 1`,                                      // 15
		`}`,                                                // 16
	}
	got := SecretLines(text)
	// 9: a commented-out secret assignment; 12: a secret-like name is assigned.
	want := map[int]bool{4: true, 5: true, 6: true, 9: true, 12: true, 13: true}
	for i := range text {
		if got[i] != want[i] {
			t.Errorf("line %d %q: got %v", i, text[i], got[i])
		}
	}
	// A pair whose value spans lines, a JSON key whose value starts on the next line, and a
	// block label that looks like a secret name.
	for _, lines := range [][]string{
		{"env {", `  name = "DB_PASSWORD"`, "  value = jsonencode({", "    a = 1234", "  })", "}"},
		{"{", `  "db_password":`, "  [", "    1234", "  ]", "}"},
		{`variable "db_password" {`, "  default = 1234", "}"},
		{"{", `  "db_password":`, `    "1234"`, "}"},
	} {
		got := SecretLines(lines)
		for i, l := range lines {
			if strings.Contains(l, "1234") && !got[i] {
				t.Errorf("%q: line %d not marked", lines, i)
			}
		}
	}
	// An escaped quote in a JSON-encoded template does not end the string.
	if got := SecretLines([]string{`{`, `  "a": "${var.m[\"k\"]}",`, `  "b": "c"`, `}`}); got[2] {
		t.Errorf("escaped template: %v", got)
	}
	// A closer with no opener, or an unterminated string, hides the rest.
	for _, lines := range [][]string{{`  "a",`, `  ]`, `  b = "c"`}, {`  a = "open`, `  b = "c"`}} {
		got := SecretLines(lines)
		if !got[len(got)-1] {
			t.Errorf("%q: %v", lines, got)
		}
	}
}

func TestRedactGeneratedMultiline(t *testing.T) {
	text := strings.Join([]string{
		`resource "aws_db_instance" "x" {`,
		`  master_password = {`,
		`    value = "hunter2"`,
		`  }`,
		`  passwords = [`,
		`    "hunter3",`,
		`  ]`,
		`  environment {`,
		`    value = "hunter4"`,
		`    name  = "DB_PASSWORD"`,
		`  }`,
		`  engine = "postgres"`,
		`}`,
	}, "\n")
	got := RedactGenerated(text)
	for _, leak := range []string{"hunter2", "hunter3", "hunter4"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s shown:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, `engine = "postgres"`) || !strings.Contains(got, `passwords = [`) {
		t.Errorf("ordinary lines hidden:\n%s", got)
	}
}

func TestHCLEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`"p\U00000061ss"`: `"pass"`,
		`"\\U00000061"`:   `"\\U00000061"`, // an escaped backslash, then U
		`"\U00000022"`:    `"\u0022"`,
		`"\U0011FFFF"`:    `"\U0011FFFF"`, // not a rune: kept, so JSON rejects it
		`"\U0000006"`:     `"\U0000006"`,
	} {
		if got := hclEscapes(in); got != want {
			t.Errorf("hclEscapes(%s) = %s, want %s", in, got, want)
		}
	}
}
