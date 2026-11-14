package gitctx

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"hclaro/internal/imports"
)

var (
	leakMark = regexp.MustCompile(`leak[0-9]{2}`)
	keepMark = regexp.MustCompile(`keep[0-9]{2}`)
)

// views renders text the three ways configuration reaches the browser: a Git diff that
// replaces every line (both sides shown), a file new to Git, and the import preview.
func views(t *testing.T, text string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var raw strings.Builder
	fmt.Fprintf(&raw, "@@ -1,%d +1,%d @@\n", len(lines), len(lines))
	for _, l := range lines {
		raw.WriteString("-" + l + "\n")
	}
	for _, l := range lines {
		raw.WriteString("+" + l + "\n")
	}
	var d GitDiff
	if err := ParseDiff([]byte(raw.String()), false, &d); err != nil {
		t.Fatal(err)
	}
	var diff, added strings.Builder
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			diff.WriteString(l.Kind + " " + l.Text + "\n")
		}
	}
	nd := GitDiff{}
	for _, l := range newFileHunk(text, &nd).Lines {
		added.WriteString(l.Text + "\n")
	}
	return map[string]string{"diff": diff.String(), "new file": added.String(), "import preview": imports.RedactGenerated(text, false)}
}

// Adversarial configuration: every literal marked leakNN must be hidden in every view and
// every literal marked keepNN must stay visible, also with CRLF line endings and a BOM.
func TestAdversarialFixtures(t *testing.T) {
	for _, name := range []string{"adversarial.tf", "adversarial.tf.json"} {
		b, err := os.ReadFile("../../fixtures/redaction/" + name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		leaks, keeps := leakMark.FindAllString(text, -1), keepMark.FindAllString(text, -1)
		if len(leaks) == 0 || len(keeps) == 0 {
			t.Fatal("fixture without markers")
		}
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				for _, leak := range leaks {
					if strings.Contains(got, leak) {
						t.Errorf("%s %s %s: %s shown", name, variant, view, leak)
					}
				}
				for _, keep := range keeps {
					if !strings.Contains(got, keep) {
						t.Errorf("%s %s %s: %s hidden", name, variant, view, keep)
					}
				}
			}
		}
	}
}

// Malformed input fails closed: after a closer without an opener, an unterminated string
// or an unterminated template, nothing that follows is shown.
func TestMalformedFailsClosed(t *testing.T) {
	for name, text := range map[string]string{
		"stray closer":  "}\nname = \"leak01\"\n",
		"open string":   "a = \"x\nname = \"leak01\"\n",
		"open template": "a = \"${x\nname = \"leak01\"\n",
		"invalid utf-8": "a = \"\xff\xfe\"\n}\nname = \"leak01\"\n",
		"nul":           "a = \"\x00\"\n}\nname = \"leak01\"\n",
		"open heredoc":  "user_data = <<EOF\nleak01\n",
		"open comment":  "/*\nname = \"leak01\"\n",
	} {
		for view, got := range views(t, text) {
			if strings.Contains(got, "leak01") {
				t.Errorf("%s %s:\n%s", name, view, got)
			}
		}
	}
}

// Unquoted values inside blocks hidden whole by a secret-like label stay hidden.
func TestUnquotedInSecretBlocks(t *testing.T) {
	b, err := os.ReadFile("../../fixtures/redaction/adversarial.tf")
	if err != nil {
		t.Fatal(err)
	}
	for view, got := range views(t, string(b)) {
		for _, n := range []string{"3600123", "4242424"} {
			if strings.Contains(got, n) {
				t.Errorf("%s: %s shown", view, n)
			}
		}
	}
}

// Escaped names count as secret-like in their raw form too: decoding \r or \t in
// "sec\ret" or "\token" removes a letter that master's raw comparison kept (found by the
// independent review). Every value must stay hidden in every view.
func TestEscapedNamesRawOrDecoded(t *testing.T) {
	for _, text := range []string{
		`"sec\ret" = [
  "hunter2",
]`,
		`"\token" = [
  "hunter2",
]`,
		`"c\redential" = [
  "hunter2",
]`,
		`"clie\nt_key" = [
  "hunter2",
]`,
		`"connec\tion_string" = [
  "hunter2",
]`,
		`x = {
  "au\thorization" = {
    a = "hunter2"
  }
}`,
		`resource "x" "sec\ret" {
  a = "hunter2"
}`,
		`variable "db_\token" {
  default = "hunter2"
}`,
		`{
  "\token": {
    "a": "hunter2"
  }
}`,
		`{
  "\token":
    "hunter2"
}`,
		`{
  "sec\ret": [
    "hunter2"
  ]
}`,
		`env {
  name  = "DB_\tOKEN"
  value = "hunter2"
}`,
		`env {
  value = "hunter2"
  name  = "sec\ret"
}`,
		`x = [{
  "name": "DB_\tOKEN",
  "value": "hunter2"
}]`,
	} {
		for view, got := range views(t, text+"\n") {
			if strings.Contains(got, "hunter2") {
				t.Errorf("%s: %q shown:\n%s", view, text, got)
			}
		}
	}
}

// Heredoc and key gaps: an unterminated heredoc or an opener HCL rejects hides what follows,
// an anchor with a dash is a heredoc, and a key keeps its value across a line break before
// its ":"/"=", a blank line or a comment.
func TestHeredocAndKeyGaps(t *testing.T) {
	for name, text := range map[string]string{
		"open heredoc, ordinary name": "note = <<EOF\nok\npassword = \"leak01\"\n",
		"open heredoc, dash":          "note = <<-EOF\nok\n  b = \"leak01\"\n",
		"opener with comment":         "user_data = <<EOF # c\nleak01\nEOF\nx = \"leak02\"\n",
		"opener with space":           "user_data = <<EOF \nleak01\nEOF\n",
		"dashed anchor":               "user_data = <<-EO-F\nleak01\nEO-F\n",
		"json key, colon next line":   "{\n  \"db_password\"\n  : \"leak01\"\n}\n",
		"json key, blank, colon":      "{\n  \"db_password\"\n\n  :\n\n  \"leak01\"\n}\n",
		"hcl key, blank, value":       "password =\n\n  \"leak01\"\n",
		"hcl key, comment, value":     "password =\n  # the value\n  \"leak01\"\n",
		"json key, comment, value":    "{\n  \"db_password\":\n  /* v */\n  \"leak01\"\n}\n",
		"operator, blank, value":      "password = \"a\" +\n\n  \"leak01\"\n",
		"ternary over lines":          "password = cond ?\n  \"a\"\n  : \"b\"\n  + \"leak01\"\n",
		"comment, then operator":      "password = var.a\n/* c */ + \"leak01\"\n",
		"value before bad opener":     "env {\n  value = \"leak01\"\n  x = <<EOF # c\n  name = \"DB_PASSWORD\"\n}\n",
		"value in open heredoc file":  "env {\n  value = \"leak01\"\n  x = <<EOF\n}\n",
		"restored key, carried name":  "name = \"a\"\n\"x\"\n= \"DB_PASSWORD\"\nvalue = \"leak01\"\n",
		"carried key over restored":   "region = \"us-east-1\"\n\"db_password\"\n:\n\"leak01\"\n",
		"carried name after secret":   "password = \"a\"\nx\n/*\nc */ = \"b\"\n+ \"leak01\"\n",
		// Heredoc bodies are opaque text (JSON, YAML, shell): hidden under any name.
		"heredoc json pair":        "container_definitions = <<EOF\n[{\"environment\": [{\"name\": \"DB_PASSWORD\", \"value\": \"leak01\"}]}]\nEOF\n",
		"heredoc json pair, lines": "container_definitions = <<-EOF\n  [{\n    \"name\": \"DB_PASSWORD\",\n    \"value\": \"leak01\"\n  }]\n  EOF\n",
		"heredoc yaml secret":      "content = <<EOT\nclient_secret: leak01\nEOT\n",
		"heredoc plain body":       "user_data = <<EOF\nexport X=leak01\nEOF\n",
	} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if leakMark.MatchString(got) {
					t.Errorf("%s %s %s:\n%s", name, variant, view, got)
				}
			}
		}
	}
	// A dashed anchor ends its heredoc, and an ordinary key split from its ":" stays shown.
	for _, text := range []string{"note = <<EO-F\nok\nEO-F\nx = \"keep01\"\n", "content = <<EOT\nclient_secret: x\nEOT\nname = \"keep01\"\n", "{\n  \"name\"\n  : \"keep01\"\n}\n",
		"token = var.a\n/*\n*/\n// c\nx = \"keep01\"\n"} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if !strings.Contains(got, "keep01") {
					t.Errorf("%q %s %s:\n%s", text, variant, view, got)
				}
			}
		}
	}
}

// variants returns text with LF and CRLF line endings and with a BOM.
func variants(text string) map[string]string {
	return map[string]string{"lf": text, "crlf": strings.ReplaceAll(text, "\n", "\r\n"), "bom": "\ufeff" + text}
}

// A statement continues after a closing bracket that ends with an operator, and quoted text
// at the start of a hidden line is not kept as if it were a key: it may be a map key or a
// list item, which can be the secret itself.
func TestBracketContinuationAndQuotedKeys(t *testing.T) {
	for name, text := range map[string]string{
		"call, operator":        "password = upper(\"a\") +\n  \"leak01\"\n",
		"list, operator":        "password = join(\",\", [\"a\"]) +\n  \"leak01\"\n",
		"multiline call":        "password = format(\n  \"%s\",\n  var.a,\n) +\n  \"leak01\"\n",
		"index, ternary":        "token = var.m[\"a\"] != \"\" ?\n  \"leak01\" : null\n",
		"secret map key":        "passwords = {\n  \"leak01\" = \"admin\"\n}\n",
		"secret map key, colon": "{\n  \"tokens\": {\n    \"leak01\": \"ci\"\n  }\n}\n",
		"secret list, compare":  "secrets = [\n  \"leak01\" == var.x ? 1 : 2,\n]\n",
		"pair, quoted key":      "env {\n  name = \"DB_PASSWORD\"\n  value = {\n    \"leak01\" = 1\n  }\n}\n",
	} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if leakMark.MatchString(got) {
					t.Errorf("%s %s %s:\n%s", name, variant, view, got)
				}
			}
		}
	}
	for _, text := range []string{
		"name = upper(\"a\")\nx = \"keep01\"\n",
		"x = f(\"a\") + \"b\"\ny = \"keep01\"\n",
		"tags = {\n  \"keep01\" = \"x\"\n}\n",
		"password = f(var.a)\nx = \"keep01\"\n",
	} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if !strings.Contains(got, "keep01") {
					t.Errorf("%q %s %s:\n%s", text, variant, view, got)
				}
			}
		}
	}
}

// Commented-out values that open on a secret-like assignment, \U escapes and look-alike
// letters in names.
func TestCommentedItemsAndEscapes(t *testing.T) {
	for name, text := range map[string]string{
		"commented list":         "# passwords = [\n#   \"leak01\",\n# ]\nx = 1\n",
		"commented map, slashes": "// tokens = {\n//   ci = \"leak01\"\n// }\n",
		"commented heredoc":      "# user_data = <<EOF\n# leak01\n# EOF\n",
		"commented value next":   "# password =\n#   \"leak01\"\n",
		"U escape":               "\"p\\U00000061ssword\" = [\n  \"leak01\",\n]\n",
		"U escape, pair":         "env {\n  name  = \"DB_P\\U00000041SSWORD\"\n  value = \"leak01\"\n}\n",
		"cyrillic a":             "pаssword = [\n  \"leak01\",\n]\n",
		"cyrillic quoted":        "{\n  \"pаsswоrd\": [\n    \"leak01\"\n  ]\n}\n",
		"greek omicron, pair":    "env {\n  name  = \"DB_PASSWΟRD\"\n  value = \"leak01\"\n}\n",
		"fullwidth":              "ｔｏｋｅｎ = [\n  \"leak01\",\n]\n",
		"math bold":              "𝐬𝐞𝐜𝐫𝐞𝐭 = [\n  \"leak01\",\n]\n",
		"commented look-alike":   "# tоken = [\n#   \"leak01\",\n# ]\n",
	} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if leakMark.MatchString(got) {
					t.Errorf("%s %s %s:\n%s", name, variant, view, got)
				}
			}
		}
	}
	for _, text := range []string{
		"# passwords = [\n#   \"x\",\n# ]\nx = \"keep01\"\n",
		"# password = \"x\"\n# keep01 is the bucket\n",
		"# tags = [\n#   \"keep01\",\n# ]\n",
		"tags = {\n  Größe = \"keep01\"\n}\n",
		"\"\\U0001F600\" = \"keep01\"\n",
	} {
		for variant, input := range variants(text) {
			for view, got := range views(t, input) {
				if !strings.Contains(got, "keep01") {
					t.Errorf("%q %s %s:\n%s", text, variant, view, got)
				}
			}
		}
	}
}

// cutViews renders the kept part of a file whose rest fell past a size limit, the three
// ways that happens: diff output cut at maxDiffOutput (Git's last, partial line is dropped
// by ParseDiff, so one extra line is sent), a new file read up to maxNewFile, and generated
// configuration read up to 1 MiB.
func cutViews(t *testing.T, kept string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(kept, "\n"), "\n")
	var raw strings.Builder
	fmt.Fprintf(&raw, "@@ -0,0 +1,%d @@\n", len(lines)+50)
	for _, l := range lines {
		raw.WriteString("+" + l + "\n")
	}
	raw.WriteString("+  na") // cut in the middle of the next line
	var d GitDiff
	if err := ParseDiff([]byte(raw.String()), true, &d); err != nil {
		t.Fatal(err)
	}
	var diff, added strings.Builder
	for _, h := range d.Hunks {
		for _, l := range h.Lines {
			diff.WriteString(l.Kind + " " + l.Text + "\n")
		}
	}
	nd := GitDiff{Truncated: true}
	for _, l := range newFileHunk(kept, &nd).Lines {
		added.WriteString(l.Text + "\n")
	}
	return map[string]string{"diff overflow": diff.String(), "new file": added.String(), "import preview": imports.RedactGenerated(kept, true)}
}

// A size limit can cut between the value of a name/value pair and its secret-like name, at
// the top level as well as inside a block: every pair value before the cut is hidden. Other
// values stay visible, and the same text read whole still shows the value (the cut, not
// the text, hides it).
func TestTopLevelCutHidesPairValues(t *testing.T) {
	for name, kept := range map[string]string{
		// The rest would be: name = "DB_PASSWORD"
		"hcl attribute":       "region = \"keep01\"\nvalue = \"leak01\"\n",
		"hcl attribute, case": "region = \"keep01\"\nValue = \"leak01\"\n",
		// The rest would be: key = "API_TOKEN" (variable files, environment-style pairs)
		"tfvars key/value":   "region = \"keep01\"\nvalue = \"leak01\"\nenabled = true\n",
		"env-style heredoc":  "region = \"keep01\"\nvalue = <<EOT\nleak01\nEOT\n",
		"json top level":     "\"region\": \"keep01\",\n\"value\": \"leak01\",\n",
		"json object":        "{\n  \"region\": \"keep01\",\n  \"value\": \"leak01\",\n",
		"json env list":      "{\n  \"region\": \"keep01\",\n  \"env\": [{\"value\": \"leak01\",\n",
		"hcl block, closed":  "region = \"keep01\"\nenv {\n  value = \"leak01\"\n}\nvalue = \"leak02\"\n",
		"last line partial":  "region = \"keep01\"\ndescription = leak01\n",
	} {
		for variant, input := range variants(kept) {
			for view, got := range cutViews(t, input) {
				for _, leak := range leakMark.FindAllString(kept, -1) {
					if strings.Contains(got, leak) {
						t.Errorf("%s %s %s: %s shown:\n%s", name, variant, view, leak, got)
					}
				}
				if !strings.Contains(got, "keep01") {
					t.Errorf("%s %s %s: keep01 hidden:\n%s", name, variant, view, got)
				}
			}
		}
	}
	if got := imports.RedactGenerated("value = \"leak01\"\n", false); !strings.Contains(got, "leak01") {
		t.Errorf("whole text: an ordinary top-level value is shown, got %q", got)
	}
}
