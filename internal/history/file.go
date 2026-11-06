package history

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExportFormat returns the export format for a file name: json or markdown.
func ExportFormat(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json", nil
	case ".md", ".markdown":
		return "markdown", nil
	}
	return "", errors.New("the export file name must end in .json or .md")
}

// ValidateExportPath checks a path given on the command line for a history export: it must
// name a new .json or .md file in an existing directory that is not a Terraform/OpenTofu
// data directory or a Git directory. Existing files and symbolic links are never replaced.
func ValidateExportPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("the export path is empty or invalid")
	}
	if _, err := ExportFormat(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("the export path cannot be resolved")
	}
	dir := filepath.Dir(abs)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", errors.New("the export directory does not exist")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.New("the export directory cannot be resolved")
	}
	for _, part := range strings.Split(filepath.ToSlash(real), "/") {
		if part == ".terraform" || part == ".git" {
			return "", errors.New("refusing to write an export into a .terraform or .git directory")
		}
	}
	target := filepath.Join(real, filepath.Base(abs))
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New("the export file already exists; choose a new file name")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("the export path cannot be checked")
	}
	return target, nil
}

// WriteExport writes a report to a new file with owner-only permissions. It never replaces
// an existing file.
func WriteExport(path string, r Report) error {
	target, err := ValidateExportPath(path)
	if err != nil {
		return err
	}
	format, _ := ExportFormat(target)
	var body []byte
	if format == "json" {
		if body, err = json.MarshalIndent(r, "", "  "); err != nil {
			return err
		}
		body = append(body, '\n')
	} else {
		body = []byte(r.Markdown())
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("the export file cannot be created")
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		_ = os.Remove(target)
		return errors.New("the export file cannot be written")
	}
	return f.Close()
}
