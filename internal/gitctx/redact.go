package gitctx

import (
	"regexp"
	"strings"
	"unicode/utf8"

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
	heredocStart = regexp.MustCompile(`<<-?\s*([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	// quoted matches a double-quoted string literal with escapes.
	quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
)

// LineRedactor redacts HCL or JSON configuration lines for display. It keeps state
// across the lines of one hunk to follow heredocs. It is a heuristic, not a parser: any
// literal assigned to a secret-like name, any credential-shaped literal and every line of
// a heredoc under a secret-like name is replaced. Missing sensitivity flags prove nothing,
// so the policy is the state redactor's (state.RiskyKey, state.LooksSecret).
type LineRedactor struct {
	heredocEnd    string
	heredocSecret bool
}

// Line returns the display form of one configuration line and whether anything was
// withheld. Control characters are removed and the length is bounded.
func (r *LineRedactor) Line(line string) (string, bool) {
	line = displayable(line)
	if r.heredocEnd != "" {
		if strings.TrimSpace(line) == r.heredocEnd {
			r.heredocEnd = ""
			return line, false
		}
		if r.heredocSecret || state.LooksSecret(line) {
			return indentOf(line) + Redacted, true
		}
		return clip(line), false
	}
	key := ""
	if m := assignment.FindStringSubmatch(line); m != nil {
		key = m[1]
	}
	risky := key != "" && state.RiskyKey(key)
	if m := heredocStart.FindStringSubmatch(line); m != nil && !strings.HasPrefix(strings.TrimSpace(line), "#") {
		r.heredocEnd, r.heredocSecret = m[1], risky
	}
	redacted := false
	out := quoted.ReplaceAllStringFunc(line, func(lit string) string {
		inner := lit[1 : len(lit)-1]
		// The key itself in a JSON line ("password": "...") is a literal too.
		if strings.TrimSpace(line[:strings.Index(line, lit)]) == "" && key != "" && inner == key {
			return lit
		}
		if inner == "" {
			return lit
		}
		if risky || state.LooksSecret(inner) {
			redacted = true
			return `"` + Redacted + `"`
		}
		return lit
	})
	// An unquoted value under a secret-like name (a number, a reference) is kept only when
	// it is a reference to another object, which is not a value.
	if risky && !redacted {
		if _, value, ok := cutAssignment(out); ok && !reference(value) {
			out = out[:len(out)-len(value)] + Redacted
			redacted = true
		}
	}
	// Credential shapes outside string literals (comments, bare words).
	if !redacted && state.Suspicious(out) {
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
