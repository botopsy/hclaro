package tfpolicy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Bounds for the profile-selectable plan options.
const (
	MaxParallelism        = 256
	MaxLockTimeoutSeconds = 3600
)

var varFileSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidVarFilePath accepts a variable file reference relative to the project root:
// slash-separated, at most five segments, no hidden or dot segments, no node_modules folder
// (the plan fingerprint skips both, so a file there would not be covered), no leading dash,
// and a .tfvars or .tfvars.json name. It never accepts an absolute path or a flag.
func ValidVarFilePath(path string) bool {
	if path == "" || len(path) > 512 || strings.Contains(path, `\`) {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 5 {
		return false
	}
	for _, part := range parts {
		if !varFileSegment.MatchString(part) || part == "node_modules" {
			return false
		}
	}
	return strings.HasSuffix(path, ".tfvars") || strings.HasSuffix(path, ".tfvars.json")
}

// AutoLoaded reports variable files the engines load on their own from the root module.
func AutoLoaded(path string) bool {
	if strings.Contains(path, "/") {
		return false
	}
	return path == "terraform.tfvars" || path == "terraform.tfvars.json" || strings.HasSuffix(path, ".auto.tfvars") || strings.HasSuffix(path, ".auto.tfvars.json")
}

// VarFile is a resolved variable file reference: the reference as listed or stored, the
// canonical root-relative path passed to the engine, and display metadata. File contents are
// never read here.
type VarFile struct {
	Ref        string `json:"ref"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	Linked     bool   `json:"linked"`
}

// VarFileError explains why a reference cannot be used.
type VarFileError struct {
	Code    string `json:"code"`
	Ref     string `json:"ref"`
	Message string `json:"message"`
}

func (e *VarFileError) Error() string { return e.Message }

// ResolveVarFile resolves ref inside root. The reference and its canonical target must both
// be valid variable file paths inside the canonical root, so a symbolic link cannot point
// outside the root, into a hidden folder or at a non-variable file.
func ResolveVarFile(root, ref string) (VarFile, error) {
	fail := func(code, message string) (VarFile, error) {
		return VarFile{}, &VarFileError{code, ref, message}
	}
	if !ValidVarFilePath(ref) || AutoLoaded(ref) {
		return fail("var_file_invalid", ref+" is not a selectable variable file path.")
	}
	// The root is compared in canonical form, so a root reached through a link (for example
	// /var on macOS) does not make every file look outside it.
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	} else {
		return fail("var_file_unreadable", "The project root cannot be resolved.")
	}
	full := filepath.Join(root, filepath.FromSlash(ref))
	link, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("var_file_missing", ref+" was not found in the project root. Restore it, or remove it from the profile.")
	}
	if err != nil {
		return fail("var_file_unreadable", ref+" cannot be read. Check its permissions.")
	}
	resolved, err := filepath.EvalSymlinks(full)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("var_file_missing", ref+" is a link to a file that no longer exists. Restore it, or remove it from the profile.")
	}
	if err != nil {
		return fail("var_file_unreadable", ref+" cannot be resolved. Check its permissions.")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return fail("var_file_outside_root", ref+" resolves outside the project root through a symbolic link, so it was refused.")
	}
	rel = filepath.ToSlash(rel)
	if !ValidVarFilePath(rel) || AutoLoaded(rel) {
		return fail("var_file_outside_root", ref+" resolves to "+rel+", which is not a selectable variable file (hidden folder, unusual name or automatically loaded file).")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return fail("var_file_not_regular", ref+" is not a regular file.")
	}
	return VarFile{Ref: ref, Path: rel, Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Linked: link.Mode()&fs.ModeSymlink != 0}, nil
}

// VarFiles lists the variable files below root that a plan can select or a profile approve:
// .tfvars and .tfvars.json files at most five levels deep, skipping hidden folders,
// node_modules and files the engine loads automatically. Linked files are listed only when
// they resolve to a valid file inside the root. The walk is bounded; truncated reports
// whether it stopped. Files directly in the root are listed first, so a large folder can never
// crowd them out. An unreadable root is an error; unreadable folders below it are skipped.
func VarFiles(root string) (files []VarFile, truncated bool, err error) {
	files = []VarFile{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return []VarFile{}, false, err
	}
	for _, e := range entries {
		if len(files) >= 500 {
			return files, true, nil
		}
		if e.IsDir() || !ValidVarFilePath(e.Name()) || AutoLoaded(e.Name()) {
			continue
		}
		if f, err := ResolveVarFile(root, e.Name()); err == nil {
			files = append(files, f)
		}
	}
	visited := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		visited++
		if visited > 20_000 || len(files) >= 500 {
			truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || strings.Count(rel, "/") >= 4) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.Contains(rel, "/") || !ValidVarFilePath(rel) || AutoLoaded(rel) {
			return nil
		}
		if f, err := ResolveVarFile(root, rel); err == nil {
			files = append(files, f)
		}
		return nil
	})
	if err != nil {
		return []VarFile{}, false, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Ref < files[j].Ref })
	return files, truncated, nil
}
