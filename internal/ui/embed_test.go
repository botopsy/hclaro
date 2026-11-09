package ui

import (
	"bytes"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFrontendIsSelfContained(t *testing.T) {
	assets := Assets()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(index, []byte(`id="root"`)) || !bytes.Contains(index, []byte(`<script`)) {
		t.Fatal("missing compiled application entry")
	}
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllSubmatch(index, -1)
	if len(refs) == 0 {
		t.Fatal("no embedded assets referenced")
	}
	for _, ref := range refs {
		name := string(ref[1])
		if !strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") {
			t.Fatal("external or ambiguous runtime asset")
		}
		content, err := fs.ReadFile(assets, strings.TrimPrefix(name, "/"))
		if err != nil || len(content) == 0 {
			t.Fatal("referenced asset is not embedded")
		}
		if bytes.Contains(content, []byte("DO_NOT_LEAK_HCLARO_TEST_SECRET")) {
			t.Fatal("fixture data leaked into frontend bundle")
		}
	}
}

// M19: fonts and icons are embedded, stylesheets reference only embedded files, and the design
// mockup's preview runtime never reaches the shipped UI.
func TestEmbeddedBrandAssetsAreLocal(t *testing.T) {
	assets := Assets()
	for _, name := range []string{"favicon.svg", "favicon-16.png", "favicon-32.png", "apple-touch-icon.png"} {
		if content, err := fs.ReadFile(assets, name); err != nil || len(content) == 0 {
			t.Fatalf("brand asset %s is not embedded", name)
		}
	}
	favicon, _ := fs.ReadFile(assets, "favicon.svg")
	if !bytes.Contains(favicon, []byte("#C6F432")) || !bytes.Contains(favicon, []byte("#0B1220")) {
		t.Fatal("favicon is not the hollow-cursor mark")
	}
	fonts := 0
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		base := strings.ToLower(name)
		for _, forbidden := range []string{"support.js", "react-dom.js", "cursor.dc"} {
			if strings.Contains(base, forbidden) {
				t.Fatalf("design preview runtime shipped: %s", name)
			}
		}
		if strings.HasSuffix(base, ".woff2") || strings.HasSuffix(base, ".woff") {
			fonts++
		}
		if !strings.HasSuffix(base, ".css") && !strings.HasSuffix(base, ".js") {
			return nil
		}
		content, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte("DCLogic")) || bytes.Contains(content, []byte("fonts.googleapis")) {
			t.Fatalf("%s references the design mockup runtime or a remote font", name)
		}
		if strings.HasSuffix(base, ".css") {
			for _, ref := range regexp.MustCompile(`url\(([^)]+)\)`).FindAllSubmatch(content, -1) {
				target := strings.Trim(string(ref[1]), `"'`)
				// The CSP allows font-src 'self' only; data: and remote fonts would be blocked.
				if !strings.HasPrefix(target, "/assets/") {
					t.Fatalf("stylesheet references a non-embedded asset: %s", target)
				}
				if _, err := fs.Stat(assets, strings.TrimPrefix(target, "/")); err != nil {
					t.Fatalf("stylesheet asset %s is not embedded", target)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fonts == 0 {
		t.Fatal("the brand typeface is not embedded")
	}
}
