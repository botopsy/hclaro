package imports

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	ErrFileExists = errors.New("file already exists")
	ErrUnsafePath = errors.New("unsafe destination path")
)

// WriteNew creates name inside root with content. It never overwrites, truncates or follows
// a symlink: the content is written to a private temporary file in the same directory,
// synced, then hard-linked to the final name, which fails if anything already exists there.
// Where hard links are unavailable it falls back to an exclusive create. Root must be the
// symlink-resolved project root.
func WriteNew(root, name string, content []byte) error {
	if !fileName.MatchString(name) || filepath.Base(name) != name {
		return ErrUnsafePath
	}
	// The project directory itself must not have been swapped for a symlink since launch.
	if real, err := filepath.EvalSymlinks(root); err != nil || real != root {
		return ErrUnsafePath
	}
	target := filepath.Join(root, name)
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		return ErrFileExists
	}
	tmp, err := os.CreateTemp(root, ".hclaro-import-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0o644)
	err = os.Link(tmpName, target)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrExist):
		return ErrFileExists
	default:
		if err := exclusiveWrite(target, content); err != nil {
			return err
		}
	}
	syncDir(root)
	return nil
}

func exclusiveWrite(target string, content []byte) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return ErrFileExists
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(target)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(target)
		return err
	}
	return f.Close()
}

// syncDir makes the new directory entry durable where the platform supports it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// ReadGenerated reads an engine-generated configuration file for review. It must be a
// regular file directly in root; at most 1 MiB is returned.
func ReadGenerated(root, name string) (string, bool, error) {
	if !fileName.MatchString(name) {
		return "", false, ErrUnsafePath
	}
	info, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, ErrUnsafePath
	}
	const limit = 1 << 20
	f, err := os.Open(filepath.Join(root, name))
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	b := make([]byte, limit+1)
	n, _ := io.ReadFull(f, b)
	return string(b[:min(n, limit)]), n > limit, nil
}
