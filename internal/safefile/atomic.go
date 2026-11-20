package safefile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// WriteAtomic replaces name in dir with data, owner-only (see WriteAtomicIn).
func WriteAtomic(dir, name, prefix string, data []byte) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	return WriteAtomicIn(r, name, prefix, data)
}

// WriteAtomicIn replaces name in r with data: a new temporary file prefix+<random>+".tmp" in
// name's folder (exclusive create, 0600) is written, synced and renamed over name. Rename
// replaces a link at name itself, never its target, and os.Root never leaves r. A crash
// before the rename leaves the old file and a temporary file the caller sweeps by prefix;
// the folder is not synced, so a power loss can bring back the previous version.
func WriteAtomicIn(r *os.Root, name, prefix string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(name), prefix+rand.Text()+".tmp")
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// ProjectKey derives a stable store key from a project root without keeping the path; salt
// separates the stores (cost history, saved conversations).
func ProjectKey(salt, root string) string {
	h := sha256.Sum256([]byte(salt + "\x00" + root))
	return hex.EncodeToString(h[:8])
}
