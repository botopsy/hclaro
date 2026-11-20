package imports

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"hclaro/internal/safefile"
)

// WriteNew creates name inside root with content. It never overwrites, truncates or follows
// a symlink: the content is written to a private temporary file in the same directory,
// synced, then hard-linked to the final name, which fails if anything already exists there
// (safefile.CreateAt). Where hard links are unavailable it falls back to an exclusive create.
// Root must be the symlink-resolved project root.
func WriteNew(root, name string, content []byte) error {
	if !fileName.MatchString(name) || filepath.Base(name) != name {
		return safefile.ErrUnsafePath
	}
	err := safefile.CreateAt(root, name, content)
	if !errors.Is(err, safefile.ErrNoLinks) {
		return err
	}
	// name is a plain file name, so its folder is root (checked by CreateAt).
	if err := exclusiveWrite(filepath.Join(root, name), content, 0o644); err != nil {
		return err
	}
	safefile.SyncDir(root)
	return nil
}

func exclusiveWrite(target string, content []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return safefile.ErrFileExists
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

// ReadGenerated reads an engine-generated configuration file for review. It must be a
// regular file directly in root; at most 1 MiB is returned.
func ReadGenerated(root, name string) (string, bool, error) {
	if !fileName.MatchString(name) {
		return "", false, safefile.ErrUnsafePath
	}
	info, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, safefile.ErrUnsafePath
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
