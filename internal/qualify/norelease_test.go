// Package qualify holds the M27 real-engine qualification tools. This test keeps engine binaries,
// provider archives and mirrors out of the repository (M27.01).
package qualify

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoQualifyBinaries(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", "scripts/qualify", "fixtures/qualify", "internal/qualify").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f == "" {
			continue
		}
		if reason := forbidden(f, filepath.Join(root, filepath.FromSlash(f))); reason != "" {
			t.Errorf("%s is committed: %s", f, reason)
		}
	}
}

func forbidden(name, file string) string {
	base := path.Base(name)
	for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".exe", ".deb", ".rpm", ".apk", ".pkg", ".msi"} {
		if strings.HasSuffix(base, ext) {
			return "archive or package"
		}
	}
	if base == "terraform" || base == "tofu" || strings.HasPrefix(base, "terraform-provider-") {
		return "engine or provider binary name"
	}
	head := make([]byte, 4)
	if f, err := os.Open(file); err == nil {
		defer f.Close()
		_, _ = f.Read(head)
	}
	for _, magic := range [][]byte{{0x7f, 'E', 'L', 'F'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, {'M', 'Z'}, {'P', 'K', 3, 4}} {
		if bytes.HasPrefix(head, magic) {
			return "executable or zip content"
		}
	}
	return ""
}

func TestForbidden(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string][]byte{
		"terraform-provider-random_3.9.1_darwin_amd64.zip": []byte("x"),
		"tofu":  []byte("x"),
		"probe": {0xcf, 0xfa, 0xed, 0xfe, 0},
		"a.bin": {'P', 'K', 3, 4},
	}
	for name, data := range cases {
		if forbidden(name, write(name, data)) == "" {
			t.Errorf("%s was not refused", name)
		}
	}
	if r := forbidden("README.md", write("README.md", []byte("# ok"))); r != "" {
		t.Errorf("README.md refused: %s", r)
	}
}
