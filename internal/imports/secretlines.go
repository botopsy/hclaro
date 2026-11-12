package imports

import (
	"encoding/json"
	"regexp"
	"strings"

	"hclaro/internal/state"
)

var (
	// An HCL anchor may hold dashes (<<-EO-F).
	heredocOpen = regexp.MustCompile(`^<<-?\s*([A-Za-z_][A-Za-z0-9_-]*)\s*$`)
	// commentKey is a `name =` or `"name":` inside comment text.
	commentKey = regexp.MustCompile(`"?([A-Za-z0-9_.-]+)"?\s*[=:]`)
)

// SecretLines marks the lines of HCL or JSON configuration whose content must be hidden
// because of where they are, not what they say: every line inside a block, list, call or
// heredoc opened under a secret-like name (passwords = [ … ], user_data = <<EOF … EOF),
// every line assigning a secret-like name (also inside an inline object or after a line
// that ends with an operator), and the value of a name/value pair whose name is secret-like
// ({ name = "DB_PASSWORD", value = … }, in either order, on one line or several, also as a
// heredoc), as state.RiskyPair does for state. A block whose last label or name is
// secret-like (resource "aws_kms_key" "secrets" { … }, master_password { … }) is hidden whole.
// Strings (with ${…} templates and escapes) are read, comments are checked for secret-like assignments, and
// block comments are hidden. The lines must start at the top of the file; text that closes a
// block it never opened, or a string left open, cannot be placed, so everything from there on
// is marked. Line-level checks still apply on top.
func SecretLines(lines []string) []bool {
	type frame struct {
		risky bool
		key   string
		id    int
		// valueOf is the frame whose "value" this frame belongs to (value = jsonencode({ … })),
		// or -1.
		valueOf int
		// lastKey: the key of the statement the previous line ended, restored when a line
		// starts with an operator.
		lastKey string
	}
	out := make([]bool, len(lines))
	stack := []frame{{valueOf: -1}}
	nextID := 1
	valueLines := map[int][]int{}
	pairRisky := map[int]bool{}
	heredocEnd, heredocRisky, heredocValueOf, heredocStart, inComment, broken := "", false, -1, 0, false, false
	// cand is the last name or string read; it outlives a line break, a blank line or a
	// comment, so a key still meets a ":"/"=" written on a later line.
	cand, haveCand := "", false
	for i, line := range lines {
		if broken {
			out[i] = true
			continue
		}
		if heredocEnd != "" {
			if strings.TrimSpace(line) == heredocEnd {
				heredocEnd = ""
			} else {
				out[i] = heredocRisky
				if heredocValueOf >= 0 {
					valueLines[heredocValueOf] = append(valueLines[heredocValueOf], i)
				}
			}
			continue
		}
		top := &stack[len(stack)-1]
		// A line that starts with an operator continues the previous statement (a comment
		// start counts too, for /* … */ + "b"; a comment-only line gives the key back below).
		restored := false
		if t := strings.TrimLeft(line, " \t\ufeff"); t != "" && strings.IndexByte("+*/%&|?:.<>!=", t[0]) >= 0 && top.key == "" {
			top.key, restored = top.lastKey, true
		}
		// Inside a commented-out block, or a statement whose value continues on this line
		// ("db_password":⏎ "…", or password = "a" +⏎ "b").
		out[i] = inComment || top.risky || state.RiskyKey(top.key)
		if v := top.valueOf; v >= 0 {
			valueLines[v] = append(valueLines[v], i)
		}
		// carried: cand comes from an earlier line.
		afterKey, last, tokens, carried := false, byte(0), false, haveCand
		for j := 0; j < len(line) && !broken; {
			top := &stack[len(stack)-1]
			c := line[j]
			// last: the final token's last byte, to see whether the line ends with an operator.
			// Comments and blanks do not count; a word counts by its last byte (-1 is a value).
			if c != ' ' && c != '\t' && c != '\r' && !inComment && c != '#' && !strings.HasPrefix(line[j:], "//") && !strings.HasPrefix(line[j:], "/*") {
				tokens = true
				if !wordByte(c) {
					last = c
				}
			}
			switch {
			case inComment:
				k := strings.Index(line[j:], "*/")
				if k < 0 {
					commentSecret(line[j:], &out[i])
					j = len(line)
					continue
				}
				commentSecret(line[j:j+k], &out[i])
				inComment, j = false, j+k+2
			case c == '"':
				s, end, ok := readString(line, j)
				if !ok {
					broken, out[i] = true, true
					continue
				}
				s = unescape(line[j:end], s)
				if (strings.EqualFold(top.key, "name") || strings.EqualFold(top.key, "key")) && state.RiskyKey(s) {
					pairRisky[top.id] = true
				}
				cand, haveCand, afterKey, carried, j = s, true, false, false, end
			case c == '#' || strings.HasPrefix(line[j:], "//"):
				commentSecret(line[j:], &out[i])
				j = len(line)
			case strings.HasPrefix(line[j:], "/*"):
				inComment, j = true, j+2
			case strings.HasPrefix(line[j:], "<<"):
				m := heredocOpen.FindStringSubmatch(line[j:])
				if m == nil {
					// "<<" means nothing else in HCL or JSON: an opener HCL rejects
					// (<<EOF # c) leaves the body's extent unknown.
					broken, out[i] = true, true
					continue
				}
				heredocEnd, heredocStart, haveCand = m[1], i, false
				heredocRisky = top.risky || state.RiskyKey(top.key)
				heredocValueOf = top.valueOf
				if heredocValueOf < 0 && strings.EqualFold(top.key, "value") {
					heredocValueOf = top.id
				}
				afterKey, j, last = false, len(line), 0
			case c == '{' || c == '[' || c == '(':
				key := top.key
				if key == "" && haveCand {
					key = cand // block syntax: master_password { … } or variable "db_password" { … }
				}
				// The word just before the bracket counts too, so a key wrongly carried over
				// from the line before can only hide more.
				child := frame{id: nextID, valueOf: top.valueOf, risky: top.risky || state.RiskyKey(key) || haveCand && state.RiskyKey(cand)}
				nextID++
				if child.valueOf < 0 && strings.EqualFold(top.key, "value") {
					child.valueOf = top.id
				}
				top.key, haveCand, afterKey = "", false, false
				stack = append(stack, child)
				out[i] = out[i] || child.risky
				if child.valueOf >= 0 {
					valueLines[child.valueOf] = append(valueLines[child.valueOf], i)
				}
				j++
			case c == '}' || c == ']' || c == ')':
				if len(stack) == 1 {
					broken, out[i] = true, true
					continue
				}
				stack = stack[:len(stack)-1]
				haveCand = false
				j++
			case c == '=' || c == ':':
				if j+1 < len(line) && (line[j+1] == '=' || line[j+1] == '>') {
					haveCand, j = false, j+2
					continue
				}
				if haveCand {
					// A name carried from an earlier line may be a key or part of the
					// statement before (c ?⏎ "a"⏎ : "b"); whichever is secret-like wins, so
					// it only hides more.
					switch {
					case !carried:
						top.key = cand
					case state.RiskyKey(top.key):
					case state.RiskyKey(cand):
						top.key = cand
					case top.key == "" && state.RiskyKey(top.lastKey):
						top.key = top.lastKey
					case top.key == "":
						top.key = cand
					}
					afterKey = true
					out[i] = out[i] || state.RiskyKey(cand)
					if strings.EqualFold(cand, "value") {
						valueLines[top.id] = append(valueLines[top.id], i)
					}
				}
				haveCand = false
				j++
			case c == ',':
				top.key, haveCand = "", false
				j++
			case wordByte(c):
				k := j
				for k < len(line) && wordByte(line[k]) {
					k++
				}
				cand, haveCand, afterKey, carried, j, last = line[j:k], true, false, false, k, line[k-1]
			case c == ' ' || c == '\t' || c == '\r':
				j++
			default:
				haveCand = false
				j++
			}
		}
		// A newline ends an HCL attribute (a key that opened a block was already used),
		// unless the line ends right after the key or with an operator. A blank or
		// comment-only line changes nothing.
		top = &stack[len(stack)-1]
		if restored && !tokens {
			top.key = ""
		}
		switch {
		case !tokens:
			if top.key != "" {
				top.lastKey = top.key
			}
		case !afterKey && (last == 0 || strings.IndexByte("+-*/%&|?:<>!=", last) < 0):
			if top.key != "" {
				// The last name read was a value, not a key waiting for its ":"/"=".
				top.lastKey, haveCand = top.key, false
			}
			top.key = ""
		case top.key != "":
			haveCand = false
		}
	}
	// An unterminated heredoc's end is unknown: everything from its opener on is hidden.
	if heredocEnd != "" {
		for i := heredocStart; i < len(out); i++ {
			out[i] = true
		}
	}
	// When the rest cannot be read, a secret-like name may follow any value seen so far.
	unknown := broken || heredocEnd != ""
	for id, ls := range valueLines {
		if pairRisky[id] || unknown {
			for _, l := range ls {
				out[l] = true
			}
		}
	}
	return out
}

// commentSecret marks a comment that assigns a secret-like name (commented-out code).
func commentSecret(text string, mark *bool) {
	for _, m := range commentKey.FindAllStringSubmatch(text, -1) {
		if state.RiskyKey(m[1]) {
			*mark = true
		}
	}
}

// unescape decodes the escapes of a string literal ("p\u0061ssword") so names compare as
// written; a literal that is not plain JSON (a template with nested quotes) stays as read.
// When decoding changes it, both forms are kept, joined by a NUL, so a name counts as
// secret-like if either does: decoding "sec\ret" turns \r into a control character and
// loses the letter the raw form keeps.
func unescape(literal, raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var s string
	if json.Unmarshal([]byte(literal), &s) != nil || s == raw {
		return raw
	}
	return raw + "\x00" + s
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// readString reads the double-quoted literal starting at line[start], skipping escapes and
// ${…}/%{…} templates (which may hold nested strings). ok is false when it does not end on
// this line.
func readString(line string, start int) (string, int, bool) {
	for k := start + 1; k < len(line); k++ {
		switch {
		case line[k] == '\\':
			k++
		case line[k] == '"':
			return line[start+1 : k], k + 1, true
		case (line[k] == '$' || line[k] == '%') && k+1 < len(line) && line[k+1] == '{':
			depth := 1
			for k += 2; k < len(line) && depth > 0; k++ {
				switch line[k] {
				case '\\':
					k++ // an escaped quote in a JSON-encoded template: ${var.m[\"k\"]}
				case '{':
					depth++
				case '}':
					depth--
				case '"':
					_, end, ok := readString(line, k)
					if !ok {
						return "", 0, false
					}
					k = end - 1
				}
			}
			if depth > 0 {
				return "", 0, false
			}
			k--
		}
	}
	return "", 0, false
}
