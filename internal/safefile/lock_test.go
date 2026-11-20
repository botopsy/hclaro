package safefile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockExcludesOtherHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second holder got the lock: %v", err)
	}
	unlock()
	again, err := TryLock(path)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	again()
}
