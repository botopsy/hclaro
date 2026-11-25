package assist

import (
	"os"
	"strings"
	"testing"
)

// The AI assistance guide quotes the disclosure (AI-01.13); this keeps the copy equal to the code.
func TestReadmeQuotesTheDisclosure(t *testing.T) {
	b, err := os.ReadFile("../../docs/guides/ai-assistance.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.ReplaceAll(string(b), "\r\n", "\n") // Windows checkouts may use CRLF
	if !strings.Contains(readme, "### Disclosure text (version "+DisclosureVersion+")") {
		t.Fatalf("AI assistance guide does not quote disclosure version %s", DisclosureVersion)
	}
	var want []string
	for _, l := range disclosure.Lines {
		if l == providerLines {
			for _, a := range adapters {
				want = append(want, "*("+a.Name+")* "+a.Disclosure)
			}
		} else if line, ok := strings.CutPrefix(l, sendLine); ok {
			want = append(want, "*(headless)* "+line)
		} else {
			want = append(want, l)
		}
	}
	for _, l := range want {
		if !strings.Contains(readme, "> - "+l+"\n") {
			t.Errorf("AI assistance guide lacks disclosure line: %s", l)
		}
	}
}
