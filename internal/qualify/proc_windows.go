//go:build windows

package qualify

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// prepareStop starts Hclaro in its own process group so CTRL_BREAK reaches only Hclaro and its
// children (the F-5 mechanism; F-5 is still NOT_TESTED, so a Windows stop result is recorded,
// not assumed).
func prepareStop(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// interruptProcess sends CTRL_BREAK, which Go programs receive as os.Interrupt.
func interruptProcess(p *os.Process) error {
	if ok, _, err := generateConsoleCtrlEvent.Call(syscall.CTRL_BREAK_EVENT, uintptr(p.Pid)); ok == 0 {
		return err
	}
	return nil
}

func listProcesses() ([]process, error) {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command",
		`Get-CimInstance Win32_Process | ForEach-Object { "$($_.ProcessId) $($_.ExecutablePath) $($_.CommandLine)" }`).Output()
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

// mkfifo: the F-2 plan holder is Unix only (FEASIBILITY-CHECKLIST F-2 candidate 2).
func mkfifo(path string) error { return errors.New("named pipes are not supported on Windows") }
