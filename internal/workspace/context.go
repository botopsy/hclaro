package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var varFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// VarFiles lists optional variable files directly inside the project root. Files the
// engine loads automatically are excluded, as are symlinks and unusual names.
func VarFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []string{}
	}
	files := []string{}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !varFilePattern.MatchString(name) {
			continue
		}
		if !strings.HasSuffix(name, ".tfvars") && !strings.HasSuffix(name, ".tfvars.json") {
			continue
		}
		if name == "terraform.tfvars" || name == "terraform.tfvars.json" || strings.HasSuffix(name, ".auto.tfvars") || strings.HasSuffix(name, ".auto.tfvars.json") {
			continue
		}
		files = append(files, name)
		if len(files) == 100 {
			break
		}
	}
	sort.Strings(files)
	return files
}

const (
	maxFingerprintFiles = 10_000
	maxFingerprintBytes = 256 << 20
)

// Fingerprint hashes the inputs that change a plan without reading secrets into the result:
// configuration, variable and lock files under root (hidden directories skipped), plus the
// given labels such as engine, version, workspace and selected variable files.
// Environment variables are not included. An empty result means the walk exceeded its limits.
func Fingerprint(root string, labels ...string) string {
	h := sha256.New()
	for _, label := range labels {
		h.Write([]byte(label))
		h.Write([]byte{0})
	}
	files, total := 0, int64(0)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !d.Type().IsRegular() || !(strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu") || strings.HasSuffix(name, ".tofu.json") || strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tfvars.json") || name == ".terraform.lock.hcl") {
			return nil
		}
		files++
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if files > maxFingerprintFiles || total > maxFingerprintBytes {
			return errLimit
		}
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		_, err = io.Copy(h, io.LimitReader(f, info.Size()))
		h.Write([]byte{0})
		return err
	})
	if err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

var errLimit = fs.ErrInvalid
