//go:build !windows

package qualify

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func prepareStop(cmd *exec.Cmd) {}

// interruptProcess sends SIGINT, as Ctrl+C in a terminal does.
func interruptProcess(p *os.Process) error { return p.Signal(syscall.SIGINT) }

// listProcesses prints "pid command line" per process. macOS ps is setuid, which the
// sandbox-exec profile refuses to execute, so macOS uses pgrep (-lf prints the full command
// line there; procps pgrep needs -a for that).
func listProcesses() ([]process, error) {
	args := []string{"-a", "."}
	if runtime.GOOS == "darwin" {
		args = []string{"-lf", "."}
	}
	out, err := exec.Command("/usr/bin/pgrep", args...).Output()
	if err != nil {
		return nil, err
	}
	var procs []process
	for _, line := range strings.Split(string(out), "\n") {
		pid, cmd, ok := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); ok && err == nil {
			procs = append(procs, process{n, strings.TrimSpace(cmd)})
		}
	}
	return procs, nil
}

// mkfifo creates the named pipe the F-2 plan lock holder blocks on.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
