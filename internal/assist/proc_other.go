//go:build !windows

package assist

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareGroup starts the assistant in its own process group, as the engine runner does.
func prepareGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}
