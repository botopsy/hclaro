package imports

import (
	"regexp"
	"strings"

	"hclaro/internal/state"
)

var heredocOpen = regexp.MustCompile(`^<<-?\s*([A-Za-z_][A-Za-z0-9_]*)\s*$`)

// SecretLines marks the lines of HCL or JSON configuration whose content must be hidden
// because of where they are, not what they say: every line inside a block, list, call or
// heredoc opened under a secret-like name (passwords = [ … ], user_data = <<EOF … EOF),
// and the value of a name/value pair whose name is secret-like ({ name = "DB_PASSWORD",
// value = … }, in either order, on one line or several), as state.RiskyPair does for state.
// Strings (with ${…} templates) and comments are skipped. The lines must start at the top
// of the file; text that closes a block it never opened, or a string left open, cannot be
// placed, so everything from there on is marked. Line-level checks still apply on top.
func SecretLines(lines []string) []bool {
	type frame struct {
		risky bool
		key   string
		id    int
		// valueOf is the frame whose "value" this frame belongs to (value = jsonencode({ … })),
		// or -1.
		valueOf int
	}
	out := make([]bool, len(lines))
	stack := []frame{{valueOf: -1}}
	nextID := 1
	valueLines := map[int][]int{}
	pairRisky := map[int]bool{}
	heredocEnd, heredocRisky, inComment, broken := "", false, false, false
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
			}
			continue
		}
		// A secret key whose value starts on this line ("db_password":⏎ "…").
		out[i] = stack[len(stack)-1].risky || state.RiskyKey(stack[len(stack)-1].key)
		if v := stack[len(stack)-1].valueOf; v >= 0 {
			valueLines[v] = append(valueLines[v], i)
		}
		// afterKey: the line so far ends with `key =` or `"key":`, so the value (or its
		// opening bracket) may follow on the next line.
		cand, haveCand, afterKey := "", false, false
		for j := 0; j < len(line) && !broken; {
			top := &stack[len(stack)-1]
			c := line[j]
			switch {
			case inComment:
				k := strings.Index(line[j:], "*/")
				if k < 0 {
					j = len(line)
					continue
				}
				inComment, j = false, j+k+2
			case c == '"':
				s, end, ok := readString(line, j)
				if !ok {
					broken, out[i] = true, true
					continue
				}
				if (strings.EqualFold(top.key, "name") || strings.EqualFold(top.key, "key")) && state.RiskyKey(s) {
					pairRisky[top.id] = true
				}
				cand, haveCand, afterKey, j = s, true, false, end
			case c == '#' || strings.HasPrefix(line[j:], "//"):
				j = len(line)
			case strings.HasPrefix(line[j:], "/*"):
				inComment, j = true, j+2
			case strings.HasPrefix(line[j:], "<<") && heredocOpen.MatchString(line[j:]):
				heredocEnd = heredocOpen.FindStringSubmatch(line[j:])[1]
				heredocRisky = top.risky || state.RiskyKey(top.key)
				afterKey, j = false, len(line)
			case c == '{' || c == '[' || c == '(':
				key := top.key
				if key == "" && haveCand {
					key = cand // block syntax: master_password { … }
				}
				risky := top.risky || state.RiskyKey(key)
				valueOf := top.valueOf
				if valueOf < 0 && strings.EqualFold(top.key, "value") {
					valueOf = top.id
				}
				top.key, haveCand, afterKey = "", false, false
				stack = append(stack, frame{risky: risky, id: nextID, valueOf: valueOf})
				nextID++
				out[i] = out[i] || risky
				if valueOf >= 0 {
					valueLines[valueOf] = append(valueLines[valueOf], i)
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
					top.key, afterKey = cand, true
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
				cand, haveCand, afterKey, j = line[j:k], true, false, k
			case c == ' ' || c == '\t':
				j++
			default:
				haveCand = false
				j++
			}
		}
		// A newline ends an HCL attribute (a key that opened a block was already used),
		// unless the line ends right after the key.
		if !afterKey {
			stack[len(stack)-1].key = ""
		}
	}
	for id, ls := range valueLines {
		if pairRisky[id] {
			for _, l := range ls {
				out[l] = true
			}
		}
	}
	return out
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
