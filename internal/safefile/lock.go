package safefile

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked is TryLock finding the lock held by another process (or another open in this one).
var ErrLocked = errors.New("locked by another process")

// Lock takes an exclusive lock shared with other processes on path+".lock" and returns the
// unlock. Hclaro's configuration files are read, changed and written under it, so two processes
// sharing the folder never overwrite each other's change. It waits while another process holds it.
func Lock(path string) (unlock func(), err error) { return lock(path, true) }

// TryLock is Lock without waiting: it reports ErrLocked when the lock is held.
func TryLock(path string) (unlock func(), err error) { return lock(path, false) }

func lock(path string, wait bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, wait); err != nil {
		f.Close()
		return nil, err
	}
	// Closing the file releases the lock.
	return func() { f.Close() }, nil
}
