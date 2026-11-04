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
