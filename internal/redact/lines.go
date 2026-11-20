package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"hclaro/internal/state"
)

// The one pattern set for HCL/JSON assignments and heredoc openers, used by SecretLines,
// Generated and LineRedactor.
var (
	// assignment is `name = value` or `"name": value` at the start of a line: groups are the
	// indent, the name as written (quotes kept), the bare name, the operator with its blanks
	// and the value.
	assignment = regexp.MustCompile(`^(\s*)("?([A-Za-z0-9_.-]+)"?)(\s*[=:]\s*)(.*)$`)
	// heredoc is a heredoc opener at the end of a line: groups are the blanks after "<<" or
	// "<<-" and the anchor (which may hold dashes: <<-EO-F).
	heredoc = regexp.MustCompile(`<<-?(\s*)([A-Za-z_][A-Za-z0-9_-]*)\s*$`)
)

// Generated prepares engine-generated configuration for display. The file on disk is not
// changed. Generated HCL carries attribute values read from the provider, so values under
// secret-like names, credential-shaped or random-looking values and heredoc bodies are
// replaced. Lines inside a block under a secret-like name, and the value of a secret
// name/value pair, are replaced too (see SecretLines). truncated marks text cut at a size
// limit (see SecretLinesTruncated).
func Generated(text string, truncated bool) string {
	lines := strings.Split(text, "\n")
	forced := secretLines(lines, truncated)
	out := make([]string, 0, len(lines))
	end := ""
	for i, line := range lines {
		if end != "" {
			if strings.TrimSpace(line) == end {
				end = ""
				out = append(out, line)
			} else if strings.TrimSpace(line) != "" {
				out = append(out, "    # [redacted heredoc line]")
			}
			continue
		}
		// Generated HCL assigns with "=" only.
		if m := assignment.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[4]) == "=" {
			key, value := m[3], m[5]
			// On a forced line a quoted key that is not a secret-like name may be the
			// secret itself (a map key).
			hideKey := forced[i] && strings.HasPrefix(m[2], `"`) && !state.RiskyKey(key)
			// Generated HCL writes the anchor right after "<<" or "<<-".
			if h := heredoc.FindStringSubmatch(value); h != nil && h[1] == "" {
				end = h[2]
				if hideKey {
					line = "  # [redacted line]"
				}
				out = append(out, line)
				continue
			}
			if hideKey {
				out = append(out, "  # [redacted line]")
				continue
			}
			if (forced[i] || state.RiskyKey(key)) && !emptyValue(value) || state.LooksSecret(value) {
				out = append(out, m[1]+m[2]+m[4]+`"[redacted]"`)
				continue
			}
			out = append(out, line)
			continue
		}
		if forced[i] && !Structural(line) || state.LooksSecret(line) {
			out = append(out, "  # [redacted line]")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// Structural reports a line holding only brackets, commas and blanks.
func Structural(line string) bool { return strings.Trim(line, " \t{}[](),") == "" }

func emptyValue(v string) bool {
	v = strings.TrimSpace(v)
	return v == "null" || v == `""` || v == "{" || v == "[" || v == "[]" || v == "{}"
}

// maxLineRunes bounds one displayed line.
const maxLineRunes = 400

var (
	// quoted matches a double-quoted string literal with escapes.
	quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	// quotedValue is a value that is one string literal, with an optional trailing comma.
	quotedValue = regexp.MustCompile(`^"(?:[^"\\]|\\.)*"(,?)$`)
)

// LineRedactor redacts HCL or JSON configuration lines for display (Git diffs). It keeps state
// across the lines of one hunk to follow heredocs. It is a heuristic, not a parser: any
// literal assigned to a secret-like name, any credential-shaped literal and every heredoc
// body line (opaque JSON, YAML or shell text) is replaced. Missing sensitivity flags prove
// nothing, so the policy is the state redactor's (state.RiskyKey, state.LooksSecret).
type LineRedactor struct {
	heredocEnd string
}

// Line returns the display form of one configuration line and whether anything was
// withheld. Control characters are removed and the length is bounded.
func (r *LineRedactor) Line(line string) (string, bool) { return r.Next(line, false) }

// Next is Line for a line that SecretLines may have marked (forced): every literal and value
// on a forced line is withheld, whatever its name.
func (r *LineRedactor) Next(line string, forced bool) (string, bool) {
	line = displayable(line)
	if r.heredocEnd != "" {
		if strings.TrimSpace(line) == r.heredocEnd {
			r.heredocEnd = ""
			return line, false
		}
		return indentOf(line) + state.Redacted, true
	}
	key := ""
	// On a forced line, quoted text before ":"/"=" that is not a secret-like name may be
	// a map key or a list item that is the secret itself: it is not kept as a key.
	if m := assignment.FindStringSubmatch(line); m != nil && len(m[3]) <= 128 && !(forced && strings.Contains(m[2], `"`) && !state.RiskyKey(m[3])) {
		key = m[3]
	}
	risky := forced || key != "" && state.RiskyKey(key)
	if m := heredoc.FindStringSubmatch(line); m != nil && !strings.HasPrefix(strings.TrimSpace(line), "#") {
		r.heredocEnd = m[2]
	}
	// Under a secret-like name or on a forced line, the whole value goes, not only its
	// string literals: ["a", 1234] or "a" + var.x would otherwise show part of it. A
	// reference (var.password) names where a value comes from and is kept.
	if risky {
		_, value, ok := cutAssignment(line)
		// value is trimmed; it ends before any trailing blanks, so its last occurrence is it.
		head := line[:strings.LastIndex(line, value)]
		switch {
		case key != "" && (!ok || reference(value) && !strings.Contains(value, `"`)):
			// An opening bracket, an empty value, a heredoc or a reference: no value here.
			// A quoted index (var.m["…"]) is a value.
		case key != "" && quotedValue.MatchString(value):
			return clip(head + quotedValue.ReplaceAllString(value, `"`+state.Redacted+`"$1`)), true
		case key != "":
			return clip(head + state.Redacted), true
		case !Structural(line):
			// A list item, a comment or a bare value on a forced line.
			return indentOf(line) + state.Redacted, true
		}
		if state.Suspicious(line) {
			return indentOf(line) + state.Redacted, true
		}
		return clip(line), false
	}
	// Strings are read as SecretLines reads them, ${…} templates with nested strings
	// included, so a value inside "${"…"}" is judged as part of its string.
	redacted := false
	var b strings.Builder
	for j := 0; j < len(line); {
		if line[j] != '"' {
			b.WriteByte(line[j])
			j++
			continue
		}
		inner, end, ok := ReadString(line, j)
		if !ok {
			inner, end = line[j+1:], len(line) // unterminated: the rest of the line is its text
		}
		if inner != "" && state.LooksSecret(inner) {
			redacted = true
			b.WriteString(`"` + state.Redacted + `"`)
		} else {
			b.WriteString(line[j:end])
		}
		j = end
	}
	out := b.String()
	// A literal the plain pattern finds (a string nested in a template) is still judged on
	// its own, so nothing hidden by the older reading becomes visible.
	oldSecret := false
	for _, lit := range quoted.FindAllString(line, -1) {
		if inner := lit[1 : len(lit)-1]; inner != "" && state.LooksSecret(inner) {
			oldSecret = true
			if strings.Contains(out, inner) {
				return indentOf(line) + state.Redacted, true
			}
		}
	}
	// Credential shapes outside string literals (comments, bare words); a line whose
	// literals the plain pattern finds harmless is checked whole, as before.
	if !redacted && state.Suspicious(out) || !oldSecret && state.Suspicious(line) {
		return indentOf(line) + state.Redacted, true
	}
	return clip(out), redacted
}

// cutAssignment splits `name = value` and returns the trimmed value.
func cutAssignment(line string) (string, string, bool) {
	i := strings.IndexAny(line, "=:")
	if i < 0 {
		return "", "", false
	}
	value := strings.TrimSpace(line[i+1:])
	if value == "" || value == "{" || value == "[" || strings.HasPrefix(value, "<<") {
		return "", "", false
	}
	return line[:i], value, true
}

var referencePattern = regexp.MustCompile(`^(var|local|data|module|each|count|path|terraform)\.[A-Za-z0-9_.\[\]"-]+,?$|^[a-z][a-z0-9_]*\.[A-Za-z0-9_]+\.[A-Za-z0-9_.\[\]-]+,?$`)

// reference reports a plain reference expression such as var.password or
// aws_secretsmanager_secret.db.arn: it names where a value comes from, not the value.
func reference(value string) bool { return referencePattern.MatchString(value) }

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// HasControl reports control or format characters (bidi overrides included).
func HasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// displayable removes control and format characters (tabs are kept) and a trailing CR.
func displayable(s string) string {
	s = strings.TrimSuffix(s, "\r")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if !HasControl(strings.ReplaceAll(s, "\t", "")) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == '\t' || !HasControl(string(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxLineRunes {
		return s
	}
	return string([]rune(s)[:maxLineRunes]) + "…"
}
