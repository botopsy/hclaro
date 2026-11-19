package assist

import (
	"os"
	"strings"
	"testing"
)

// The README quotes the disclosure (AI-01.13); this keeps the copy equal to the code.
func TestReadmeQuotesTheDisclosure(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.ReplaceAll(string(b), "\r\n", "\n") // Windows checkouts may use CRLF
	if !strings.Contains(readme, "### Disclosure text (version "+DisclosureVersion+")") {
		t.Fatalf("README does not quote disclosure version %s", DisclosureVersion)
	}
	for _, l := range disclosure.Lines {
		mark := ""
		if line, ok := strings.CutPrefix(l, claudeLine); ok {
			mark, l = "*(Claude Code)* ", line
		} else if line, ok := strings.CutPrefix(l, codexLine); ok {
			mark, l = "*(OpenAI Codex)* ", line
		} else if line, ok := strings.CutPrefix(l, sendLine); ok {
			mark, l = "*(headless)* ", line
		}
		if !strings.Contains(readme, "> - "+mark+l+"\n") {
			t.Errorf("README lacks disclosure line: %s%s", mark, l)
		}
	}
}
