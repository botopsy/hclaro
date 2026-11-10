package engine

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	matrixDate   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	matrixLink   = regexp.MustCompile(`^\[[^\]]+\]\(([^)#]+)#([a-z0-9-]+)\)$`)
	matrixTest   = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	matrixFake   = regexp.MustCompile(`(?i)fake|synthetic|mock|not yet run`)
	matrixStatus = map[string]bool{"PASS": true, "FAIL": true, "NOT_TESTED": true, "BLOCKED": true}
)

// checkMatrix validates the support matrix in doc (docs/support-matrix.md). Evidence links
// resolve against docs; tests lists the Go test names that exist in the repository.
func checkMatrix(doc []byte, docs fs.FS, tests map[string]bool) []error {
	var errs []error
	bad := func(id, format string, a ...any) { errs = append(errs, fmt.Errorf(id+": "+format, a...)) }
	// Windows checkouts may convert Markdown to CRLF.
	_, table, ok := strings.Cut(strings.ReplaceAll(string(doc), "\r\n", "\n"), "\n## Matrix\n")
	if !ok {
		return []error{fmt.Errorf("no Matrix section")}
	}
	table, _, _ = strings.Cut(table, "\n## ")
	ids, rows := map[string]bool{}, 0
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ID ") || strings.HasPrefix(line, "| --- ") {
			continue
		}
		c := strings.Split(strings.Trim(line, "| "), " | ")
		if len(c) != 11 {
			bad(line, "%d cells, want 11", len(c))
			continue
		}
		rows++
		id, version, env, date, status, evidence := c[0], c[2], c[7], c[8], c[9], c[10]
		if ids[id] {
			bad(id, "duplicate ID")
		}
		ids[id] = true
		if !matrixStatus[status] {
			bad(id, "status %q", status)
		}
		for _, m := range matrixTest.FindAllStringSubmatch(env, -1) {
			if !tests[m[1]] {
				bad(id, "names %s, which does not exist", m[1])
			}
		}
		if status == "BLOCKED" && !matrixTest.MatchString(env) {
			bad(id, "BLOCKED without the test that covers the refusal")
		}
		if status != "PASS" && status != "FAIL" {
			continue
		}
		// A real result: dated, not synthetic, with evidence that names the tested version
		// and records what was run.
		if !matrixDate.MatchString(date) {
			bad(id, "%s without a date", status)
		}
		if matrixFake.MatchString(env) {
			bad(id, "%s from a synthetic environment %q", status, env)
		}
		link := matrixLink.FindStringSubmatch(evidence)
		if link == nil {
			bad(id, "%s without an evidence link", status)
			continue
		}
		b, err := fs.ReadFile(docs, link[1])
		if err != nil {
			bad(id, "evidence %s: %v", link[1], err)
			continue
		}
		section, ok := headingSection(b, link[2])
		switch {
		case !ok:
			bad(id, "evidence %s has no #%s section", link[1], link[2])
		case !strings.Contains(section, version):
			bad(id, "evidence section does not name version %s", version)
		case !strings.Contains(section, "| --- |") && !strings.Contains(section, "```"):
			bad(id, "evidence section records no results table or command log")
		}
	}
	if rows == 0 {
		errs = append(errs, fmt.Errorf("matrix has no rows"))
	}
	return errs
}

// headingSection returns the body of the Markdown heading whose GitHub anchor is anchor.
func headingSection(b []byte, anchor string) (string, bool) {
	slug := regexp.MustCompile(`[^a-z0-9 -]`)
	var out bytes.Buffer
	level := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		heading := hashes > 0 && strings.HasPrefix(line[hashes:], " ")
		if level > 0 && heading && hashes <= level {
			break
		}
		if level > 0 {
			out.WriteString(line + "\n")
		} else if heading && strings.ReplaceAll(slug.ReplaceAllString(strings.ToLower(strings.TrimSpace(line[hashes:])), ""), " ", "-") == anchor {
			level = hashes
		}
	}
	return out.String(), level > 0
}

func repoTests(t *testing.T) map[string]bool {
	names := map[string]bool{}
	decl := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, "_test.go") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range decl.FindAllSubmatch(b, -1) {
				names[string(m[1])] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestSupportMatrix(t *testing.T) {
	docs := os.DirFS("../../docs")
	doc, err := fs.ReadFile(docs, "support-matrix.md")
	if err != nil {
		t.Fatal(err)
	}
	tests := repoTests(t)
	for _, err := range checkMatrix(doc, docs, tests) {
		t.Error(err)
	}
	// The documented ranges are the ones the version check accepts.
	if !bytes.Contains(doc, []byte("**Terraform 1.5–1.17** and **OpenTofu 1.6–1.13**")) ||
		!Supported("terraform", "1.5.0") || !Supported("terraform", "1.17.0") || Supported("terraform", "1.18.0") || Supported("terraform", "1.4.0") ||
		!Supported("tofu", "1.6.0") || !Supported("tofu", "1.13.0") || Supported("tofu", "1.14.0") || Supported("tofu", "1.5.0") {
		t.Error("documented version ranges differ from engine.Supported")
	}

	// The validator refuses PASS rows that are not backed by real, recorded evidence.
	header := "\n## Matrix\n\n| ID | Engine | Version | Platform | Backend | Execution mode | Operation | Test environment | Date | Status | Evidence |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n"
	real := "[M02 real engine run](archive/verification/M02-verification.md#real-engine-run)"
	for name, row := range map[string]string{
		"no evidence":       "| X | Terraform | 1.5.5 | any | s3 | local-cli | plan | Real engine | 2026-11-10 | PASS | — |",
		"no date":           "| X | Terraform | 1.5.5 | any | s3 | local-cli | plan | Real engine | — | PASS | " + real + " |",
		"synthetic":         "| X | Terraform | 1.5.5 | any | s3 | local-cli | plan | Fake engine in CI | 2026-11-10 | PASS | " + real + " |",
		"missing file":      "| X | Terraform | 1.5.5 | any | s3 | local-cli | plan | Real engine | 2026-11-10 | PASS | [x](archive/none.md#run) |",
		"missing section":   "| X | Terraform | 1.5.5 | any | s3 | local-cli | plan | Real engine | 2026-11-10 | PASS | [x](archive/verification/M02-verification.md#no-such-run) |",
		"other version":     "| X | OpenTofu | 1.8.0 | any | local | local-cli | plan | Real engine | 2026-11-10 | PASS | " + real + " |",
		"no results":        "| X | Terraform | 1.5.5 | any | local | local-cli | plan | Real engine | 2026-11-10 | PASS | [x](archive/verification/M02-verification.md#open-items) |",
		"unknown status":    "| X | Terraform | 1.5.5 | any | local | local-cli | plan | Real engine | 2026-11-10 | CERTIFIED | — |",
		"blocked, no test":  "| X | any | any | any | cloud | remote-exec | apply | Refused | — | BLOCKED | — |",
		"blocked, bad test": "| X | any | any | any | cloud | remote-exec | apply | `TestDoesNotExist` | — | BLOCKED | — |",
		"short row":         "| X | Terraform | 1.5.5 | PASS |",
	} {
		if errs := checkMatrix([]byte(header+row+"\n"), docs, tests); len(errs) == 0 {
			t.Errorf("%s: accepted %s", name, row)
		}
	}
	if errs := checkMatrix([]byte(header+"| X | Terraform | 1.5.5 | darwin/amd64 | local | local-cli | plan | Real engine | 2026-11-05 | PASS | "+real+" |\n"), docs, tests); len(errs) != 0 {
		t.Error(errs)
	}
}
