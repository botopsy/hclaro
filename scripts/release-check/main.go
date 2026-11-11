// release-check verifies a release directory before anything is published: the checksums
// file covers every other file and matches; every archive holds exactly the expected files
// (so no tasks/, .tfvars, state, plan, credential or test file can slip in) with fixed
// ownership and no secret-looking content; and the SBOM is CycloneDX 1.5 for this version
// and lists the Go toolchain and every module recorded in each binary.
// Usage: go run ./scripts/release-check <version> <release-dir>
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Files every archive carries next to the binary (docs/RELEASING.md, Package step).
var docFiles = []string{"CHANGELOG.md", "LICENSE", "README.md", "SECURITY.md", "THIRD-PARTY-NOTICES.txt"}

// Credentials that must never appear in a release file, binary included.
var secretPattern = regexp.MustCompile(`AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,}|xox[abprs]-[0-9]{8,}-[A-Za-z0-9-]{8,}|sk-ant-[A-Za-z0-9_-]{20,}`)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/release-check <version> <release-dir>")
		os.Exit(2)
	}
	if err := check(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "release-check:", err)
		os.Exit(1)
	}
	fmt.Println("release-check: checksums, archive contents and SBOM OK")
}

func check(version, dir string) error {
	prefix := "hclaro_" + version + "_"
	if err := checkChecksums(dir, prefix+"checksums.txt"); err != nil {
		return err
	}
	sbom, err := readSBOM(filepath.Join(dir, prefix+"sbom.cdx.json"), version)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	archives := 0
	for _, e := range entries {
		name := e.Name()
		var base string
		var bins map[string][]byte
		switch {
		case strings.HasSuffix(name, ".tar.gz"):
			base = strings.TrimSuffix(name, ".tar.gz")
			bins, err = readTar(filepath.Join(dir, name), base)
		case strings.HasSuffix(name, ".zip"):
			base = strings.TrimSuffix(name, ".zip")
			bins, err = readZip(filepath.Join(dir, name), base)
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !strings.HasPrefix(base, prefix) {
			return fmt.Errorf("%s: archive name does not carry version %s", name, version)
		}
		for path, data := range bins {
			if err := sbom.covers(data); err != nil {
				return fmt.Errorf("%s: %s: %w", name, path, err)
			}
		}
		archives++
	}
	if archives == 0 {
		return errors.New("no archives found")
	}
	return nil
}

func checkChecksums(dir, sumsName string) error {
	f, err := os.Open(filepath.Join(dir, sumsName))
	if err != nil {
		return fmt.Errorf("checksums file: %w", err)
	}
	defer f.Close()
	listed := map[string]string{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		sum, name, ok := strings.Cut(lines.Text(), "  ")
		if !ok || len(sum) != 64 || name == "" || listed[name] != "" {
			return fmt.Errorf("checksums file: malformed or duplicate line %q", lines.Text())
		}
		listed[name] = sum
	}
	if err := lines.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == sumsName {
			continue
		}
		want, ok := listed[e.Name()]
		if !ok {
			return fmt.Errorf("%s is not in the checksums file", e.Name())
		}
		delete(listed, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != want {
			return fmt.Errorf("%s does not match its checksum", e.Name())
		}
	}
	for name := range listed {
		return fmt.Errorf("checksums file lists missing file %s", name)
	}
	return nil
}

// expected returns the exact set of files an archive must hold, binary included.
func expected(base string) map[string]bool {
	exe := "hclaro"
	if strings.Contains(base, "_windows_") {
		exe = "hclaro.exe"
	}
	want := map[string]bool{base + "/" + exe: true}
	for _, f := range docFiles {
		want[base+"/"+f] = true
	}
	return want
}

// verifyFile refuses unexpected names and secret-looking content and returns whether the
// file is the binary.
func verifyFile(want map[string]bool, name string, data []byte) (bool, error) {
	if !want[name] {
		return false, fmt.Errorf("unexpected file %q", name)
	}
	delete(want, name)
	if m := secretPattern.Find(data); m != nil {
		return false, fmt.Errorf("%s contains a credential-like string (%.8s…)", name, m)
	}
	return strings.HasSuffix(name, "/hclaro") || strings.HasSuffix(name, "/hclaro.exe"), nil
}

func missing(want map[string]bool) error {
	if len(want) == 0 {
		return nil
	}
	var names []string
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("missing %s", strings.Join(names, ", "))
}

func readTar(path, base string) (map[string][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	want, bins := expected(base), map[string][]byte{}
	r := tar.NewReader(gz)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Uid != 0 || h.Gid != 0 {
			return nil, fmt.Errorf("%s is not owned by 0:0", h.Name)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if h.Typeflag == tar.TypeDir && name == base {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%s is not a regular file", h.Name)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		isBin, err := verifyFile(want, name, data)
		if err != nil {
			return nil, err
		}
		if isBin {
			bins[name] = data
		}
	}
	return bins, missing(want)
}

func readZip(path, base string) (map[string][]byte, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	want, bins := expected(base), map[string][]byte{}
	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, "/")
		if f.Mode().IsDir() && name == base {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		isBin, err := verifyFile(want, name, data)
		if err != nil {
			return nil, err
		}
		if isBin {
			bins[name] = data
		}
	}
	return bins, missing(want)
}

type sbomDoc struct {
	BOMFormat   string `json:"bomFormat"`
	SpecVersion string `json:"specVersion"`
	Metadata    struct {
		Component struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"component"`
	} `json:"metadata"`
	Components []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Purl    string `json:"purl"`
	} `json:"components"`
	purls map[string]bool
}

func readSBOM(path, version string) (*sbomDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("SBOM: %w", err)
	}
	var doc sbomDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("SBOM: %w", err)
	}
	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.5" || doc.Metadata.Component.Name != "hclaro" || doc.Metadata.Component.Version != version {
		return nil, fmt.Errorf("SBOM: not a CycloneDX 1.5 document for hclaro %s", version)
	}
	doc.purls = map[string]bool{}
	for _, c := range doc.Components {
		if c.Name == "" || c.Version == "" || c.Purl == "" {
			return nil, fmt.Errorf("SBOM: component without name, version or purl: %+v", c)
		}
		doc.purls[c.Purl] = true
	}
	return &doc, nil
}

// covers checks the binary's embedded build info against the SBOM's Go components.
func (d *sbomDoc) covers(binary []byte) error {
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		return fmt.Errorf("no Go build info: %w", err)
	}
	need := []string{"pkg:golang/stdlib@" + strings.TrimPrefix(info.GoVersion, "go")}
	for _, dep := range info.Deps {
		if dep.Replace != nil {
			dep = dep.Replace
		}
		need = append(need, "pkg:golang/"+dep.Path+"@"+dep.Version)
	}
	for _, p := range need {
		if !d.purls[p] {
			return fmt.Errorf("SBOM does not list %s", p)
		}
	}
	return nil
}
