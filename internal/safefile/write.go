// Package safefile writes files without losing or following anything: project files are
// created, replaced and removed only through verified hard-link and rename steps inside a
// symlink-free project root, and Hclaro's own files are replaced atomically.
package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"hclaro/internal/ids"
)

var (
	ErrFileExists = errors.New("file already exists")
	ErrUnsafePath = errors.New("unsafe destination path")
	// ErrChanged is a replacement whose file is missing or no longer holds the reviewed text.
	ErrChanged = errors.New("file changed since it was reviewed")
	// ErrNoLinks: the folder's file system has no hard links, so a change cannot be made
	// without a window where another program's save could be overwritten. Nothing was changed.
	ErrNoLinks = errors.New("the file system does not support hard links")
)

// SafeDir returns the absolute folder of rel, a slash path inside root. Root must be its own
// symlink-resolved path (it was not swapped for a link since launch), and every folder on the
// way must exist and be a plain folder, never a symbolic link.
func SafeDir(root, rel string) (string, error) {
	if real, err := filepath.EvalSymlinks(root); err != nil || real != root {
		return "", ErrUnsafePath
	}
	if !fs.ValidPath(rel) || rel == "." || strings.ContainsAny(rel, "\\:") {
		return "", ErrUnsafePath
	}
	dir := root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return "", ErrUnsafePath
		}
	}
	return dir, nil
}

// CreateAt creates rel inside root (see SafeDir) with content: a synced temporary file is
// hard-linked to the name, which fails if anything exists there. Without hard links it fails
// with ErrNoLinks and creates nothing (no exclusive-create fallback: that could leave a
// partly written file).
func CreateAt(root, rel string, content []byte) error {
	dir, err := SafeDir(root, rel)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, path.Base(rel))
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		return ErrFileExists
	}
	tmp, err := writeTemp(dir, ".hclaro-write-*.tmp", content, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	switch err := link(tmp, target); {
	case errors.Is(err, fs.ErrExist):
		return ErrFileExists
	case err != nil:
		return ErrNoLinks
	}
	SyncDir(dir)
	return nil
}

// ReplaceAt replaces the regular file rel inside root with content, keeping its permissions,
// only while current accepts that file's bytes. A symbolic link at rel is never followed.
//
// There is no compare-and-swap rename, so a replacement never overwrites a name it has not
// verified (AI-01.12):
//  1. the file is renamed aside to AsideName(keep) in its folder (atomic: whatever another
//     program saved last is what moves);
//  2. its bytes are verified there;
//  3. the new file is hard-linked to the name, which fails if anything recreated it;
//  4. the moved file is never deleted here: it stays as a recovery artifact owned by the
//     caller's record (keep), because a program holding it open can still write into it.
//
// On a mismatch the moved file is linked back without overwriting anything. A moved file that
// holds anything but the verified bytes is reported (AsideError). Hard links are checked before
// anything moves: without them ReplaceAt fails with ErrNoLinks and changes nothing.
func ReplaceAt(root, rel string, content []byte, current func([]byte) bool, keep string) error {
	dir, target, mode, err := existing(root, rel, current, keep)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(dir, ".hclaro-write-*.tmp", content, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	hook("before-aside")
	aside, err := moveAside(dir, target, AsideName(keep))
	if err != nil {
		return err
	}
	hook("after-aside")
	if !holds(aside, current) {
		return putBack(aside, target, current)
	}
	hook("before-place")
	if err := link(tmp, target); err != nil {
		// Another program created the name after the move (its file stays), or the link failed.
		return putBack(aside, target, current)
	}
	hook("after-place")
	SyncDir(dir)
	if !holds(aside, current) {
		return &AsideError{Name: filepath.Base(aside), Written: true}
	}
	return nil
}

// RemoveAt removes the regular file rel inside root only while current accepts its bytes, with
// the same protocol as ReplaceAt: the file is moved to AsideName(keep) and kept there as the
// recovery artifact; a version another program saved is put back, never removed.
func RemoveAt(root, rel string, current func([]byte) bool, keep string) error {
	dir, target, _, err := existing(root, rel, current, keep)
	if err != nil {
		return err
	}
	hook("before-aside")
	aside, err := moveAside(dir, target, AsideName(keep))
	if err != nil {
		return err
	}
	hook("after-aside")
	if !holds(aside, current) {
		return putBack(aside, target, current)
	}
	SyncDir(dir)
	return nil
}

// existing runs the checks before a move: a safe path, a regular file current accepts (an early
// check, so a plainly changed file is never moved), a valid keep ID and working hard links.
func existing(root, rel string, current func([]byte) bool, keep string) (dir, target string, mode fs.FileMode, err error) {
	if !ids.Valid(keep) {
		return "", "", 0, ErrUnsafePath
	}
	if dir, err = SafeDir(root, rel); err != nil {
		return "", "", 0, err
	}
	target = filepath.Join(dir, path.Base(rel))
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "", 0, ErrChanged
	case err != nil || !info.Mode().IsRegular():
		return "", "", 0, ErrUnsafePath
	}
	if data, err := os.ReadFile(target); err != nil || !current(data) {
		return "", "", 0, ErrChanged
	}
	if err := linksWork(dir); err != nil {
		return "", "", 0, err
	}
	return dir, target, info.Mode().Perm(), nil
}

// AsideName is the name of the recovery artifact of record id, in the folder of the file it
// replaced. The prefix and the 32-hex ID are Hclaro's ownership marks; it never ends in .tf or
// .tofu, so neither the engines nor Hclaro read it as configuration.
func AsideName(id string) string { return ".hclaro-aside-" + id + ".tmp" }

// linksWork reports ErrNoLinks unless dir supports hard links (probed with a private file).
func linksWork(dir string) error {
	probe, err := writeTemp(dir, ".hclaro-probe-*.tmp", nil, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(probe)
	if err := link(probe, probe+".link"); err != nil {
		return ErrNoLinks
	}
	return os.Remove(probe + ".link")
}

// ErrKeptAside: a moved file holds something other than the verified bytes, or could not be put
// back under its name; AsideError names it. Nothing was lost.
var ErrKeptAside = errors.New("another program's version was kept aside")

// AsideError reports a kept file; Written tells whether Hclaro's own write took place.
type AsideError struct {
	Name    string
	Written bool
}

func (e *AsideError) Error() string { return ErrKeptAside.Error() + ": " + e.Name }
func (e *AsideError) Unwrap() error { return ErrKeptAside }

// hook is set by tests only, to change the file between the steps of the protocol; link is
// replaced by tests to emulate a file system without hard links.
var (
	hook = func(string) {}
	link = os.Link
)

func writeTemp(dir, pattern string, content []byte, mode fs.FileMode) (string, error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, werr := tmp.Write(content)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(name)
		return "", err
	}
	_ = os.Chmod(name, mode)
	return name, nil
}

// moveAside renames target to name in dir. The name is first claimed exclusively, so the
// rename only ever replaces Hclaro's own empty placeholder.
func moveAside(dir, target, name string) (string, error) {
	aside := filepath.Join(dir, name)
	f, err := os.OpenFile(aside, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	if err := os.Rename(target, aside); err != nil {
		os.Remove(aside)
		return "", err
	}
	return aside, nil
}

// holds reports whether the moved file is a regular file whose bytes current accepts.
func holds(aside string, current func([]byte) bool) bool {
	info, err := os.Lstat(aside)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(aside)
	return err == nil && current(data)
}

// putBack returns the moved file to target without overwriting anything and reports ErrChanged.
// Linked back, the moved name is only a second name of the restored file and is removed. When
// target was taken again the moved file stays as the recovery artifact; it is reported unless
// it holds only the verified bytes. A failed restore leaves it in place and reports it.
func putBack(aside, target string, current func([]byte) bool) error {
	info, err := os.Lstat(aside)
	if err == nil && info.Mode().IsRegular() {
		err = link(aside, target)
	} else if _, terr := os.Lstat(target); err == nil && errors.Is(terr, fs.ErrNotExist) {
		err = os.Rename(aside, target) // a link swapped in: renaming never follows it
		if err == nil {
			return ErrChanged
		}
	} else {
		err = fs.ErrExist
	}
	switch {
	case err == nil:
		os.Remove(aside)
		return ErrChanged
	case errors.Is(err, fs.ErrExist) && holds(aside, current):
		return ErrChanged
	}
	return &AsideError{Name: filepath.Base(aside)}
}

// SyncDir makes the new directory entry durable where the platform supports it.
func SyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
