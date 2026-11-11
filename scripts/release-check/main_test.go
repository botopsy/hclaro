package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const version = "0.0.0-test"

// fake upper-cases at run time so the planted credential never appears in the test binary.
func fake(s string) string { return strings.ToUpper(s) }

// Synthetic release built from the stub binary and placeholder documents.
type release struct {
	files map[string]string // archive member (without base dir) -> content
	sbom  map[string]any
}

// bin is a tiny Go program standing in for hclaro; it carries real Go build info.
var bin []byte

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "release-check")
	if err != nil {
		panic(err)
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module stub\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", "stub")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	if bin, err = os.ReadFile(filepath.Join(dir, "stub")); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func good(t *testing.T) release {
	files := map[string]string{"hclaro": string(bin)}
	for _, f := range docFiles {
		files[f] = "synthetic " + f + "\n"
	}
	return release{files: files, sbom: map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5",
		"metadata":   map[string]any{"component": map[string]any{"name": "hclaro", "version": version}},
		"components": []map[string]any{{"name": "stdlib", "version": "x", "purl": "pkg:golang/stdlib@" + strings.TrimPrefix(runtime.Version(), "go")}},
	}}
}

func (r release) write(t *testing.T) string {
	dir := t.TempDir()
	base := "hclaro_" + version + "_linux_amd64"
	f, _ := os.Create(filepath.Join(dir, base+".tar.gz"))
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(r.files))
	for n := range r.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: base + "/" + n, Mode: 0o644, Size: int64(len(r.files[n])), Typeflag: tar.TypeReg})
		tw.Write([]byte(r.files[n]))
	}
	tw.Close()
	gz.Close()
	f.Close()

	wbase := "hclaro_" + version + "_windows_amd64"
	zf, _ := os.Create(filepath.Join(dir, wbase+".zip"))
	zw := zip.NewWriter(zf)
	for _, n := range names {
		name := n
		if n == "hclaro" {
			name = "hclaro.exe"
		}
		w, _ := zw.Create(wbase + "/" + name)
		w.Write([]byte(r.files[n]))
	}
	zw.Close()
	zf.Close()

	sbom, _ := json.Marshal(r.sbom)
	os.WriteFile(filepath.Join(dir, "hclaro_"+version+"_sbom.cdx.json"), sbom, 0o644)
	var sums strings.Builder
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), e.Name())
	}
	os.WriteFile(filepath.Join(dir, "hclaro_"+version+"_checksums.txt"), []byte(sums.String()), 0o644)
	return dir
}

func TestGoodRelease(t *testing.T) {
	if err := check(version, good(t).write(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*release)
		after  func(dir string)
		want   string
	}{
		"planted fake secret": {mutate: func(r *release) { r.files["README.md"] += "key = " + fake("akiasynthetictest000") + "\n" }, want: "credential-like"},
		"planted private key": {mutate: func(r *release) { r.files["CHANGELOG.md"] += "-----" + fake("begin rsa private key") + "-----\n" }, want: "credential-like"},
		"task folder":         {mutate: func(r *release) { r.files["tasks/M24-PROGRESS.md"] = "x" }, want: "unexpected file"},
		"tfvars":              {mutate: func(r *release) { r.files["prod.tfvars"] = "x" }, want: "unexpected file"},
		"state":               {mutate: func(r *release) { r.files["terraform.tfstate"] = "{}" }, want: "unexpected file"},
		"missing notices":     {mutate: func(r *release) { delete(r.files, "THIRD-PARTY-NOTICES.txt") }, want: "missing"},
		"sbom wrong version": {mutate: func(r *release) {
			r.sbom["metadata"] = map[string]any{"component": map[string]any{"name": "hclaro", "version": "9.9.9"}}
		}, want: "CycloneDX 1.5"},
		"sbom lacks toolchain": {mutate: func(r *release) { r.sbom["components"] = []map[string]any{} }, want: "does not list pkg:golang/stdlib"},
		"tampered archive": {after: func(dir string) {
			os.WriteFile(filepath.Join(dir, "hclaro_"+version+"_linux_amd64.tar.gz"), []byte("x"), 0o644)
		}, want: "does not match"},
		"unlisted file": {after: func(dir string) {
			os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644)
		}, want: "not in the checksums file"},
		"missing sbom": {after: func(dir string) {
			os.Remove(filepath.Join(dir, "hclaro_"+version+"_sbom.cdx.json"))
		}, want: "lists missing file"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := good(t)
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			dir := r.write(t)
			if tc.after != nil {
				tc.after(dir)
			}
			err := check(version, dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}
