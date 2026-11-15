//go:build !(darwin && cgo)

package qualify

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// peakRSS returns a running process's own peak resident set size in bytes and the method (F3):
// VmHWM from /proc/<pid>/status on Linux; -1 elsewhere (shortcut: macOS without cgo and
// Windows are not measured; add them if F3 runs there, M27.07).
func peakRSS(pid int) (int64, string) {
	if runtime.GOOS != "linux" {
		return -1, "not measured on " + runtime.GOOS
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return -1, "unreadable /proc status"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kib, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err == nil {
				return kib * 1024, "/proc/<pid>/status VmHWM, process only"
			}
		}
	}
	return -1, "no VmHWM in /proc status"
}
