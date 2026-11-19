//go:build darwin

package assist

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// statKeyOf identifies a file's exact on-disk version: device, inode, size, and modification and
// change times. The change time cannot be set back from user space, so a rewrite always changes
// the key and forces a new hash and self-check.
func statKeyOf(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return fmt.Sprintf("%d:%d:%d:%d.%d:%d.%d", st.Dev, st.Ino, st.Size, st.Mtimespec.Sec, st.Mtimespec.Nsec, st.Ctimespec.Sec, st.Ctimespec.Nsec), nil
}
