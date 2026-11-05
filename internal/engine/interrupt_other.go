//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

func prepareInterrupt(*exec.Cmd) {}

func interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }

// prepareGroup starts the engine in its own process group so cancellation reaches the engine
// and anything it started, and a terminal Ctrl+C aimed at Hclaro does not reach it directly.
func prepareGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interruptGroup(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGINT) }

func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}
