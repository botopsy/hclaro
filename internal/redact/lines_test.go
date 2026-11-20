package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/state"
)

func TestGenerated(t *testing.T) {
	got := Generated(string(fixtures.ImportGenerated), false)
	if strings.Contains(got, "DO_NOT_LEAK_HCLARO_TEST_SECRET") {
		t.Fatalf("canary shown:\n%s", got)
	}
	for _, want := range []string{`bucket        = "hclaro-legacy-logs"`, `replication_token = "[redacted]"`, "# [redacted heredoc line]", "env = \"demo\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if got := Generated(`  password = null`, false); got != `  password = null` {
		t.Fatal(got)
	}
	if got := Generated(`  description = "AKIAIOSFODNN7EXAMPLE"`, false); strings.Contains(got, "AKIA") {
		t.Fatal(got)
	}
}

func TestLineRedactor(t *testing.T) {
	cases := map[string]string{
		`  password = "hunter2"`:                       `  password = "[redacted]"`,
		`  name     = "web"`:                           `  name     = "web"`,
		`  "client_secret": "abc",`:                    `  "client_secret": "[redacted]",`,
		`  key = "AKIAABCDEFGHIJKLMNOP"`:               `  key = "[redacted]"`,
		`  url = "https://user:pa55@example.com/db"`:   `  url = "[redacted]"`,
		`  db_password = var.db_password`:              `  db_password = var.db_password`,
		`  api_token = 12345`:                          `  api_token = [redacted]`,
		"  description = \"x\x1b[31m\u202ey\"":         `  description = "x[31my"`,
		`# export TOKEN=abc`:                           `[redacted]`,
		`  token_ref = aws_secretsmanager_secret.x.id`: `  token_ref = aws_secretsmanager_secret.x.id`,
	}
	for in, want := range cases {
		got, _ := (&LineRedactor{}).Line(in)
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	// Heredocs under a secret-like name are withheld until the marker.
	r := &LineRedactor{}
	lines := []string{"  private_key = <<-EOT", "abc", "def", "  EOT", `  name = "x"`}
	got := []string{}
	for _, l := range lines {
		out, _ := r.Line(l)
		got = append(got, out)
	}
	if got[1] != state.Redacted || got[2] != state.Redacted || got[3] != "  EOT" || got[4] != `  name = "x"` {
		t.Fatalf("heredoc %q", got)
	}
	long, _ := (&LineRedactor{}).Line(strings.Repeat("a", 1000))
	if len([]rune(long)) != maxLineRunes+1 {
		t.Fatalf("long %d", len(long))
	}
}

// A random string inside a template sits between two quotes the plain literal regex would
// pair up wrongly; the line redactor reads strings the way SecretLines does.
func TestLineRedactorReadsTemplates(t *testing.T) {
	const secret = "Xy9kQ2mL8pZ4rT7vB3nW6hJ1"
	if !state.LooksSecret(secret) {
		t.Fatal("fixture is not random-looking")
	}
	for _, in := range []string{
		`  description = "${"` + secret + `"}"`,
		`  description = "a-${upper("` + secret + `")}-b"`,
		`  description = "open ${"` + secret, // unterminated: the rest is still checked
	} {
		got, red := (&LineRedactor{}).Line(in)
		if strings.Contains(got, secret) || !red {
			t.Errorf("%q: shown as %q", in, got)
		}
	}
	// A template secret does not stop the whole-line check for other credentials.
	for _, in := range []string{
		`  description = "${"` + secret + `"}" # ghp_abcdefghijklmnopqrstuv`,
		`  description = "${"` + secret + `"}" # see https://u:p@h/x`,
		`  tags = { "note": "${"` + secret + `"}", "db_password": "hunter2" }`,
	} {
		if got, _ := (&LineRedactor{}).Line(in); got != "  "+state.Redacted {
			t.Errorf("%q: shown as %q", in, got)
		}
	}
	// Each literal of a line is still judged on its own.
	if got, _ := (&LineRedactor{}).Line(`  tags = { a = "web", b = "` + secret + `" }`); got != `  tags = { a = "web", b = "[redacted]" }` {
		t.Errorf("got %q", got)
	}
}

// testdata/golden.json holds the output of the two line redactors that B05 merged here
// (imports.RedactGenerated and gitctx.LineRedactor), recorded over every configuration file
// in fixtures/, also with CRLF line endings and a BOM. The merge changed no redaction rule.
func TestGoldenOutputOfMergedRedactors(t *testing.T) {
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]string
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	var files []string
	_ = filepath.Walk("../../fixtures", func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			for _, ext := range []string{".tf", ".tf.json", ".hcl", ".tfvars", ".tofu"} {
				if strings.HasSuffix(p, ext) {
					files = append(files, p)
					break
				}
			}
		}
		return nil
	})
	sort.Strings(files)
	seen := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(f), "../../fixtures/")
		for variant, text := range map[string]string{"": string(b), " crlf+bom": "\ufeff" + strings.ReplaceAll(string(b), "\n", "\r\n")} {
			want, ok := golden[name+variant]
			if !ok {
				continue // a fixture added after B05 has no recorded output
			}
			seen++
			lines := strings.Split(text, "\n")
			got := map[string]string{"generated": Generated(text, false), "generated truncated": Generated(text, true)}
			for mode, forced := range map[string][]bool{"lines plain": make([]bool, len(lines)), "lines forced": SecretLines(lines), "lines forced truncated": SecretLinesTruncated(lines)} {
				r := &LineRedactor{}
				var s strings.Builder
				for i, l := range lines {
					out, red := r.Next(l, forced[i])
					if red {
						s.WriteString("! ")
					} else {
						s.WriteString("  ")
					}
					s.WriteString(out + "\n")
				}
				got[mode] = s.String()
			}
			for mode, w := range want {
				if got[mode] != w {
					t.Errorf("%s%s, %s: output changed", name, variant, mode)
				}
			}
		}
	}
	if seen != len(golden) {
		t.Fatalf("checked %d of %d recorded inputs: a fixture was removed or renamed", seen, len(golden))
	}
}
