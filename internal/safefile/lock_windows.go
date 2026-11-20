package safefile

import (
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileFailImmediately = 1
	lockfileExclusiveLock   = 2
	errorLockViolation      = syscall.Errno(33)
)

func lockFile(f *os.File, wait bool) error {
	flags := uintptr(lockfileExclusiveLock)
	if !wait {
		flags |= lockfileFailImmediately
	}
	var ol syscall.Overlapped
	// Lock the first byte; without lockfileFailImmediately the call waits until it is free.
	if r, _, err := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol))); r == 0 {
		if err == errorLockViolation {
			return ErrLocked
		}
		return err
	}
	return nil
}
