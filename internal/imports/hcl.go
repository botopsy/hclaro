package imports

import (
	"regexp"
	"strings"
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
