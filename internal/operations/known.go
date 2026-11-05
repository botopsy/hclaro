package operations

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	minKnownValue = 4
	maxKnownFile  = 1 << 20
	maxKnown      = 10_000
)

// known holds input values the engine may echo in diagnostics: TF_VAR_* environment values
// and string values from the variable files a plan loads. Diagnostics are free text, so any
// occurrence is replaced before the heuristic line checks run. Values are kept only for the
// duration of one plan and never returned.
type known struct{ values []string }

// collectKnown reads the selected variable files plus the files the engine loads on its own
// (terraform.tfvars and *.auto.tfvars, with .json variants) from the project root.
func collectKnown(root string, selected []string, environ []string) *known {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) >= minKnownValue && len(set) < maxKnown {
			set[v] = true
		}
	}
	for _, item := range environ {
		if key, value, ok := strings.Cut(item, "="); ok && strings.HasPrefix(key, "TF_VAR_") {
			add(value)
			// A TF_VAR value may itself be an HCL or JSON literal such as a map or list.
			for _, v := range quoted(value) {
				add(v)
			}
		}
	}
	names := append([]string{}, selected...)
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			n := e.Name()
			if n == "terraform.tfvars" || n == "terraform.tfvars.json" || strings.HasSuffix(n, ".auto.tfvars") || strings.HasSuffix(n, ".auto.tfvars.json") {
				names = append(names, n)
			}
		}
	}
	for _, name := range names {
		b, err := readBounded(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var values []string
		if strings.HasSuffix(name, ".json") {
			values = jsonStrings(b)
		} else {
			values = append(quoted(string(b)), heredocs(string(b))...)
		}
		for _, v := range values {
			add(v)
		}
	}
	k := &known{values: make([]string, 0, len(set))}
	for v := range set {
		k.values = append(k.values, v)
	}
	// Longest first, so a value containing another is replaced whole.
	sort.Slice(k.values, func(i, j int) bool { return len(k.values[i]) > len(k.values[j]) })
	return k
}

func (k *known) scrub(text string) string {
	if k == nil {
		return text
	}
	for _, v := range k.values {
		if strings.Contains(text, v) {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	return text
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxKnownFile))
}

// quoted returns the contents of every double-quoted HCL string literal, unescaped. It
// over-collects (map keys are included), which only makes redaction broader.
func quoted(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] == '#' || s[i] == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if s[i] != '"' {
			continue
		}
		var b strings.Builder
		for i++; i < len(s) && s[i] != '"' && s[i] != '\n'; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			b.WriteByte(s[i])
		}
		out = append(out, b.String())
	}
	return out
}

// heredocs returns each non-empty line of <<MARKER and <<-MARKER bodies.
func heredocs(s string) []string {
	var out []string
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		at := strings.Index(lines[i], "<<")
		if at < 0 {
			continue
		}
		marker := strings.TrimSpace(strings.TrimPrefix(lines[i][at+2:], "-"))
		if marker == "" || strings.ContainsAny(marker, " \"") {
			continue
		}
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != marker; i++ {
			if line := strings.TrimSpace(lines[i]); line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

func jsonStrings(b []byte) []string {
	var doc any
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var out []string
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 64 {
			return
		}
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, x := range t {
				walk(x, depth+1)
			}
		case map[string]any:
			for _, x := range t {
				walk(x, depth+1)
			}
		}
	}
	walk(doc, 0)
	return out
}
