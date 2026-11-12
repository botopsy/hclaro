package imports

import (
	"regexp"
	"strings"

	"hclaro/internal/state"
)

// providerRef is a provider local name with an optional alias, such as aws or aws.west.
var providerRef = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}(\.[A-Za-z_][A-Za-z0-9_-]{0,63})?$`)

// hclString quotes s as an HCL string literal. Template sequences are escaped so the value is
// never interpreted, and control characters other than tab are refused by validation before
// this is called.
func hclString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\t':
			b.WriteString(`\t`)
		case (c == '$' || c == '%') && i+1 < len(s) && s[i+1] == '{':
			// "${" becomes "$${" and "%{" becomes "%%{": literal text, not a template.
			b.WriteByte(c)
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Block renders the import block exactly as it is written to disk.
func Block(to Address, id, provider string) string {
	var b strings.Builder
	b.WriteString("import {\n")
	b.WriteString("  to = " + to.String() + "\n")
	b.WriteString("  id = " + hclString(id) + "\n")
	if provider != "" {
		b.WriteString("  provider = " + provider + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// fileContent is the complete new file: a short provenance comment and the import block.
func fileContent(to Address, id, provider string) string {
	return "# Import block prepared with Hclaro. Review it like any other configuration change.\n" +
		"# After a successful apply the block has no further effect; you may keep or remove it.\n\n" +
		Block(to, id, provider)
}

var (
	assignment = regexp.MustCompile(`^(\s*)("?[A-Za-z0-9_.-]+"?)(\s*=\s*)(.*)$`)
	heredoc    = regexp.MustCompile(`<<-?([A-Za-z_][A-Za-z0-9_-]*)\s*$`)
)

// RedactGenerated prepares engine-generated configuration for display. The file on disk is
// not changed. Generated HCL carries attribute values read from the provider, so values
// under secret-like names, credential-shaped or random-looking values and heredoc bodies
// are replaced. Lines inside a block under a secret-like name, and the value of a secret
// name/value pair, are replaced too (see SecretLines).
func RedactGenerated(text string) string {
	lines := strings.Split(text, "\n")
	forced := SecretLines(lines)
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
		if m := assignment.FindStringSubmatch(line); m != nil {
			key := strings.Trim(m[2], `"`)
			value := m[4]
			// On a forced line a quoted key that is not a secret-like name may be the
			// secret itself (a map key).
			hideKey := forced[i] && strings.HasPrefix(m[2], `"`) && !state.RiskyKey(key)
			if h := heredoc.FindStringSubmatch(value); h != nil {
				end = h[1]
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
				out = append(out, m[1]+m[2]+m[3]+`"[redacted]"`)
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
