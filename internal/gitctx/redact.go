package gitctx

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"hclaro/internal/imports"
	"hclaro/internal/state"
)

// Redacted replaces a withheld literal in a diff line.
const Redacted = "[redacted]"

// maxLineRunes bounds one displayed line.
const maxLineRunes = 400

// SuspiciousText reports credential-shaped text (see state.Suspicious).
func SuspiciousText(s string) bool { return state.Suspicious(s) }

var (
	// assignment matches `name =`, `"name" :` and similar at the start of a line.
	assignment = regexp.MustCompile(`^\s*"?([A-Za-z0-9_.-]{1,128})"?\s*[=:]`)
	// heredocStart matches the opening of a heredoc at the end of a line.
	heredocStart = regexp.MustCompile(`<<-?\s*([A-Za-z_][A-Za-z0-9_-]*)\s*$`)
	// quoted matches a double-quoted string literal with escapes.
	quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	// quotedValue is a value that is one string literal, with an optional trailing comma.
	quotedValue = regexp.MustCompile(`^"(?:[^"\\]|\\.)*"(,?)$`)
)

// LineRedactor redacts HCL or JSON configuration lines for display. It keeps state
// across the lines of one hunk to follow heredocs. It is a heuristic, not a parser: any
// literal assigned to a secret-like name, any credential-shaped literal and every heredoc
// body line (opaque JSON, YAML or shell text) is replaced. Missing sensitivity flags prove
// nothing, so the policy is the state redactor's (state.RiskyKey, state.LooksSecret).
type LineRedactor struct {
	heredocEnd string
}

// Line returns the display form of one configuration line and whether anything was
// withheld. Control characters are removed and the length is bounded.
func (r *LineRedactor) Line(line string) (string, bool) { return r.line(line, false) }

// line is Line for a line that imports.SecretLines marked (forced): every literal and value
// on it is withheld, whatever its name.
func (r *LineRedactor) line(line string, forced bool) (string, bool) {
	line = displayable(line)
	if r.heredocEnd != "" {
		if strings.TrimSpace(line) == r.heredocEnd {
			r.heredocEnd = ""
			return line, false
		}
		return indentOf(line) + Redacted, true
	}
	key := ""
	// On a forced line, quoted text before ":"/"=" that is not a secret-like name may be
	// a map key or a list item that is the secret itself: it is not kept as a key.
	if m := assignment.FindStringSubmatch(line); m != nil && !(forced && strings.Contains(m[0], `"`) && !state.RiskyKey(m[1])) {
		key = m[1]
	}
	risky := forced || key != "" && state.RiskyKey(key)
	if m := heredocStart.FindStringSubmatch(line); m != nil && !strings.HasPrefix(strings.TrimSpace(line), "#") {
		r.heredocEnd = m[1]
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
			return clip(head + quotedValue.ReplaceAllString(value, `"`+Redacted+`"$1`)), true
		case key != "":
			return clip(head + Redacted), true
		case !imports.Structural(line):
			// A list item, a comment or a bare value on a forced line.
			return indentOf(line) + Redacted, true
		}
		if state.Suspicious(line) {
			return indentOf(line) + Redacted, true
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
		inner, end, ok := imports.ReadString(line, j)
		if !ok {
			inner, end = line[j+1:], len(line) // unterminated: the rest of the line is its text
		}
		if inner != "" && state.LooksSecret(inner) {
			redacted = true
			b.WriteString(`"` + Redacted + `"`)
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
				return indentOf(line) + Redacted, true
			}
		}
	}
	// Credential shapes outside string literals (comments, bare words); a line whose
	// literals the plain pattern finds harmless is checked whole, as before.
	if !redacted && state.Suspicious(out) || !oldSecret && state.Suspicious(line) {
		return indentOf(line) + Redacted, true
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

// displayable removes control and format characters (tabs are kept) and a trailing CR.
func displayable(s string) string {
	s = strings.TrimSuffix(s, "\r")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if !hasControl(strings.ReplaceAll(s, "\t", "")) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == '\t' || !hasControl(string(r)) {
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
