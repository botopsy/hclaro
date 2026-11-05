package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
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
//   - configuration, variable and lock files under root (hidden directories skipped);
//   - from the engine data directory (.terraform or TF_DATA_DIR): the backend configuration,
//     the selected workspace, the module manifest and installed module sources;
//   - environment variables the engine reads for planning: TF_VAR_*, TF_WORKSPACE,
//     TF_DATA_DIR, TF_CLI_ARGS, TF_CLI_ARGS_plan and TF_CLI_CONFIG_FILE;
//   - the given labels such as engine, version, workspace and selected variable files.
//
// Provider plugin binaries are covered only through .terraform.lock.hcl checksums.
// An empty result means a walk exceeded its limits.
func Fingerprint(root string, environ []string, labels ...string) string {
	return FingerprintExcept(root, environ, nil, labels...)
}

// FingerprintExcept is Fingerprint with root-level files in skip left out. Import plans use
// it to tell an engine-generated configuration file apart from other edits made while the
// plan ran.
func FingerprintExcept(root string, environ []string, skip map[string]bool, labels ...string) string {
	h := sha256.New()
	field := func(b []byte) {
		h.Write(b)
		h.Write([]byte{0})
	}
	for _, label := range labels {
		field([]byte(label))
	}
	env := []string{}
	dataDir := ".terraform"
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		switch {
		case strings.HasPrefix(key, "TF_VAR_"), key == "TF_WORKSPACE", key == "TF_CLI_ARGS", key == "TF_CLI_ARGS_plan", key == "TF_CLI_CONFIG_FILE":
			env = append(env, item)
		case key == "TF_DATA_DIR":
			env = append(env, item)
			if value != "" {
				dataDir = value
			}
		}
	}
	sort.Strings(env)
	field([]byte("env"))
	for _, item := range env {
		field([]byte(item))
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(root, dataDir)
	}
	budget := &walkBudget{skip: skip}
	if hashTree(h, root, root, budget) != nil {
		return ""
	}
	field([]byte("data"))
	for _, name := range []string{"terraform.tfstate", "environment", filepath.Join("modules", "modules.json")} {
		if err := hashFile(h, dataDir, filepath.Join(dataDir, name), budget); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ""
		}
	}
	if err := hashTree(h, dataDir, filepath.Join(dataDir, "modules"), budget); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

type walkBudget struct {
	files int
	bytes int64
	// skip names root-relative, slash-separated paths that are not hashed.
	skip map[string]bool
}

func configFile(name string) bool {
	for _, suffix := range []string{".tf", ".tf.json", ".tofu", ".tofu.json", ".tfvars", ".tfvars.json"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return name == ".terraform.lock.hcl"
}

// hashTree hashes configuration files below dir, skipping hidden directories and
// node_modules. Paths are recorded relative to base.
func hashTree(h hash.Hash, base, dir string, budget *walkBudget) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !configFile(d.Name()) {
			return nil
		}
		if rel, err := filepath.Rel(base, path); err == nil && budget.skip[filepath.ToSlash(rel)] {
			return nil
		}
		return hashFile(h, base, path, budget)
	})
}

func hashFile(h hash.Hash, base, path string, budget *walkBudget) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	budget.files++
	budget.bytes += info.Size()
	if budget.files > maxFingerprintFiles || budget.bytes > maxFingerprintBytes {
		return errLimit
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rel, _ := filepath.Rel(base, path)
	h.Write([]byte(filepath.ToSlash(rel)))
	h.Write([]byte{0})
	_, err = io.Copy(h, io.LimitReader(f, info.Size()))
	h.Write([]byte{0})
	return err
}

var errLimit = errors.New("fingerprint limits exceeded")
