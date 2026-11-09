package gitctx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// File kinds. Only HCL and JSON configuration can be diffed; variable files and the lock
// file are listed by name only.
const (
	KindHCL  = "hcl"
	KindJSON = "json"
	KindVars = "vars"
	KindLock = "lock"
)

// Kind classifies an IaC file name, or "" for other files.
func Kind(name string) string {
	base := path.Base(filepath.ToSlash(name))
	switch {
	case strings.HasSuffix(base, ".tfvars"), strings.HasSuffix(base, ".tfvars.json"):
		return KindVars
	case strings.HasSuffix(base, ".tf.json"), strings.HasSuffix(base, ".tofu.json"):
		return KindJSON
	case strings.HasSuffix(base, ".tf"), strings.HasSuffix(base, ".tofu"):
		return KindHCL
	case base == ".terraform.lock.hcl":
		return KindLock
	}
	return ""
}

// Diffable reports whether a kind's content may be shown.
func Diffable(kind string) bool { return kind == KindHCL || kind == KindJSON }

// moduleDir is a local module directory (a ./ or ../ source in the module manifest).
type moduleDir struct {
	Key string
	// Rel is the slash path relative to the scope base.
	Rel string
	Abs string
}

// Scope is what Hclaro may read for one project: the project root and the local module
// directories it calls, all inside the base (the repository top level, or the root
// outside a repository). Paths are canonical (symlinks resolved).
type Scope struct {
	Root string
	// Base is the repository top level, or Root when there is no repository.
	Base string
	// RootRel is Root relative to Base in slash form ("" when equal).
	RootRel string
	Modules []moduleDir
	DataDir string
}

const (
	maxModules   = 200
	maxScanFiles = 5000
	maxManifest  = 4 << 20
)

var (
	ErrOutsideScope = errors.New("path is outside the project scope")
	ErrNotFound     = errors.New("file not found in the project scope")
)

// dataDirOf returns the engine data directory for root from environ (TF_DATA_DIR).
func dataDirOf(root string, environ []string) string {
	dir := ".terraform"
	for _, item := range environ {
		if key, value, _ := strings.Cut(item, "="); key == "TF_DATA_DIR" && value != "" {
			dir = value
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return dir
}

// within reports whether p is base or inside it. Both must be clean absolute paths.
func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// NewScope resolves root and its local modules. top is the canonical repository top level
// or "" outside a repository; root must be inside it.
func NewScope(root, top string, environ []string) (*Scope, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	s := &Scope{Root: canonical, Base: canonical, DataDir: dataDirOf(canonical, environ)}
	if top != "" {
		if !within(top, canonical) {
			return nil, ErrOutsideScope
		}
		s.Base = top
		rel, _ := filepath.Rel(top, canonical)
		if rel != "." {
			s.RootRel = filepath.ToSlash(rel)
		}
	}
	s.Modules = s.localModules()
	return s, nil
}

// localModules reads the module manifest written by init and keeps local sources whose
// directory resolves inside the base and outside the data directory. Downloaded modules
// are copies; their source is not in this repository.
func (s *Scope) localModules() []moduleDir {
	b, err := readRegular(filepath.Join(s.DataDir, "modules", "modules.json"), maxManifest)
	if err != nil {
		return nil
	}
	var manifest struct {
		Modules []struct{ Key, Source, Dir string }
	}
	if json.Unmarshal(b, &manifest) != nil {
		return nil
	}
	dataDir, _ := filepath.EvalSymlinks(s.DataDir)
	out := []moduleDir{}
	for _, m := range manifest.Modules {
		if m.Key == "" || !(strings.HasPrefix(m.Source, "./") || strings.HasPrefix(m.Source, "../")) || filepath.IsAbs(m.Dir) || len(out) == maxModules {
			continue
		}
		abs, err := filepath.EvalSymlinks(filepath.Join(s.Root, filepath.FromSlash(m.Dir)))
		if err != nil || !within(s.Base, abs) || dataDir != "" && within(dataDir, abs) {
			continue
		}
		rel, _ := filepath.Rel(s.Base, abs)
		out = append(out, moduleDir{Key: m.Key, Rel: filepath.ToSlash(rel), Abs: abs})
	}
	return out
}

// relOf returns the slash path of abs relative to the base.
func (s *Scope) relOf(abs string) string {
	rel, _ := filepath.Rel(s.Base, abs)
	return filepath.ToSlash(rel)
}

// Place classifies a base-relative slash path: "root" inside the project root, "module"
// inside a local module directory outside the root, else "outside". The matched module
// key is returned for modules.
func (s *Scope) Place(rel string) (string, string) {
	if s.RootRel == "" || rel == s.RootRel || strings.HasPrefix(rel, s.RootRel+"/") {
		return "root", ""
	}
	for _, m := range s.Modules {
		if path.Dir(rel) == m.Rel {
			return "module", m.Key
		}
	}
	return "outside", ""
}

// hiddenOrData reports directory names never walked: hidden directories (.git, .terraform)
// and node_modules.
func hiddenOrData(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// iacFile is one IaC file found on disk in the scope.
type iacFile struct {
	Rel  string
	Abs  string
	Kind string
}

// Files lists IaC files in the root (recursively, hidden directories skipped) and in
// local module directories (not recursively), in a stable order. Symlinked files are
// included only when they resolve inside the base. It fails when the scope is too large.
func (s *Scope) Files() ([]iacFile, error) {
	seen := map[string]bool{}
	out := []iacFile{}
	add := func(abs string) error {
		kind := Kind(abs)
		if kind == "" {
			return nil
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil || !within(s.Base, real) {
			return nil
		}
		rel := s.relOf(abs)
		if seen[rel] {
			return nil
		}
		if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		seen[rel] = true
		out = append(out, iacFile{Rel: rel, Abs: abs, Kind: kind})
		if len(out) > maxScanFiles {
			return errors.New("too many files")
		}
		return nil
	}
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != s.Root && hiddenOrData(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return add(p)
	})
	if err != nil {
		return nil, err
	}
	for _, m := range s.Modules {
		if within(s.Root, m.Abs) {
			continue
		}
		entries, err := os.ReadDir(m.Abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				if err := add(filepath.Join(m.Abs, e.Name())); err != nil {
					return nil, err
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b iacFile) int { return strings.Compare(a.Rel, b.Rel) })
	return out, nil
}

// readRegular reads a regular, non-symlinked file of at most limit bytes.
func readRegular(p string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// open reads an IaC file in the scope after resolving symlinks: the target must be a
// regular file inside the base and inside the root or a local module directory.
func (s *Scope) open(abs string, limit int64) ([]byte, bool, error) {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, false, ErrNotFound
	}
	if !within(s.Base, real) {
		return nil, false, ErrOutsideScope
	}
	allowed := within(s.Root, real)
	for _, m := range s.Modules {
		allowed = allowed || filepath.Dir(real) == m.Abs
	}
	if !allowed {
		return nil, false, ErrOutsideScope
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, false, ErrNotFound
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false, ErrNotFound
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, ErrNotFound
	}
	if int64(len(b)) > limit {
		return b[:limit], true, nil
	}
	return b, false, nil
}

// ids derives opaque file IDs: an HMAC of the base-relative path under a per-process key,
// so an ID names a file only in this session and reveals nothing about the path.
type ids struct{ key []byte }

func (k ids) of(base, rel string) string {
	mac := hmac.New(sha256.New, k.key)
	mac.Write([]byte(base))
	mac.Write([]byte{0})
	mac.Write([]byte(rel))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}
