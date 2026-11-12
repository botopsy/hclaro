package gitctx

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fullDiff builds Git's full-context diff of old -> new for one changed line, the shape
// fullContext produces.
func fullDiff(lines []string, changed int, replacement string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/main.tf b/main.tf\n--- a/main.tf\n+++ b/main.tf\n@@ -1,%d +1,%d @@ leaked section\n", len(lines), len(lines))
	for i, l := range lines {
		if i == changed {
			b.WriteString("-" + l + "\n+" + replacement + "\n")
		} else {
			b.WriteString(" " + l + "\n")
		}
	}
	return b.String()
}

// Secrets whose name is on another line than the value, with the name far outside the
// displayed context. Each leaked before: the redactor saw one line at a time and Git sent
// only three lines of context.
func TestMultilineSecretsHiddenInDiff(t *testing.T) {
	pad := []string{"  # one", "  # two", "  # three", "  # four", "  # five"}
	cases := map[string][]string{
		"list":      {"  passwords = [", `    "hunter2",`},
		"object":    {"  master_password = {", `    value = "hunter2"`},
		"block":     {"  master_password {", `    value = "hunter2"`},
		"call":      {"  db_password = join(\"\", [", `    "hunter2",`},
		"json":      {`  "db_password": [`, `    "hunter2"`},
		"heredoc":   {"  user_data = <<EOF", "export DB_PASS=hunter2"},
		"pair":      {"  environment = [", "    {", `      name  = "DB_PASSWORD"`, `      value = "hunter2"`},
		"pairafter": {"  env {", `    value = "hunter2"`},
		"jsonpair":  {"  container_definitions = jsonencode([", `    { "name": "DB_PASSWORD",`, `      "value": "hunter2" },`},
	}
	for name, head := range cases {
		lines := append([]string{`resource "x" "y" {`}, head[:len(head)-1]...)
		lines = append(lines, pad...)
		lines = append(lines, head[len(head)-1])
		if name == "pairafter" {
			lines = append(lines, pad...)
			lines = append(lines, `    name = "DB_PASSWORD"`)
		}
		lines = append(lines, "  # changed", "  ]", "}")
		changed := len(lines) - 3
		if name == "pairafter" {
			changed = 2 + len(pad) // the pad line just after the value
		}
		var d GitDiff
		if err := ParseDiff([]byte(fullDiff(lines, changed, "  # edited")), false, &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "leaked section") {
			t.Errorf("%s leaked:\n%s", name, b)
		}
		if d.Added != 1 || d.Removed != 1 || len(d.Hunks) != 1 {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// A one-line pair and ordinary pairs.
	lines := []string{`env = [{ name = "DB_PASSWORD", value = "hunter2" }]`, `log = [{ name = "LOG_LEVEL", value = "info" }]`, "# x"}
	var d GitDiff
	if err := ParseDiff([]byte(fullDiff(lines, 2, "# y")), false, &d); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "hunter2") || !strings.Contains(string(b), `\"info\"`) {
		t.Errorf("one-line pair:\n%s", b)
	}
}

// Only displayContext lines around a change are shown, with correct line numbers, and
// ordinary values stay visible.
func TestDiffCutToContext(t *testing.T) {
	lines := []string{}
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf(`  tag%d = "v%d"`, i, i))
	}
	raw := fullDiff(lines, 9, `  tag10 = "new"`)
	raw = strings.Replace(raw, ` `+lines[24]+"\n", "-"+lines[24]+"\n", 1)
	raw = strings.Replace(raw, "@@ -1,30 +1,30 @@", "@@ -1,30 +1,29 @@", 1)
	var d GitDiff
	if err := ParseDiff([]byte(raw), false, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Hunks) != 2 || d.Added != 1 || d.Removed != 2 || d.Redacted != 0 {
		t.Fatalf("%+v", d)
	}
	h0, h1 := d.Hunks[0], d.Hunks[1]
	if h0.OldStart != 7 || h0.OldLines != 7 || h0.NewStart != 7 || h0.NewLines != 7 || h0.Lines[0].Text != `  tag7 = "v7"` || h0.Section != "" {
		t.Fatalf("first %+v", h0)
	}
	if h1.OldStart != 22 || h1.OldLines != 7 || h1.NewStart != 22 || h1.NewLines != 6 {
		t.Fatalf("second %+v", h1)
	}
}

// Without full context (a hunk that does not start at line 1) the enclosing block is
// unknown, so every value is withheld.
func TestPartialContextWithheld(t *testing.T) {
	var d GitDiff
	err := ParseDiff([]byte("@@ -8,3 +8,3 @@ x\n \"hunter2\",\n-  a = 1\n+  a = 2\n bare hunter2\n"), false, &d)
	b, _ := json.Marshal(d)
	if err != nil || strings.Contains(string(b), "hunter2") || strings.Contains(string(b), `= 2`) {
		t.Fatalf("%v %s", err, b)
	}
}

// End to end with real Git: a heredoc body far from its opener.
func TestDiffHeredocWithRealGit(t *testing.T) {
	s, _, _ := service(t)
	dir := repo(t)
	body := "resource \"aws_instance\" \"web\" {\n  user_data = <<EOF\n#!/bin/sh\nl1\nl2\nl3\nl4\nexport DB_PASS=hunter2\nl6\nl7\nl8\nEOF\n}\n"
	write(t, filepath.Join(dir, "web.tf"), body)
	fixture(t, dir, "add", "web.tf")
	fixture(t, dir, "commit", "-q", "-m", "web")
	write(t, filepath.Join(dir, "web.tf"), strings.Replace(body, "l7", "l7-changed", 1))
	c := s.Status(context.Background(), dir)
	for _, f := range c.Files {
		if f.Path != "web.tf" {
			continue
		}
		d, err := s.Diff(context.Background(), dir, f.ID)
		b, _ := json.Marshal(d)
		if err != nil || d.Added != 1 || strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "l3") {
			t.Fatalf("%v %s", err, b)
		}
		return
	}
	t.Fatal("web.tf not in status")
}
