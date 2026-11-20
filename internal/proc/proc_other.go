//go:build !windows

// Package proc starts child processes in their own process group and interrupts or kills that
// group. The engine and assist runners share it.
package proc

import (
	"os"
	"os/exec"
	"syscall"
)

func PrepareInterrupt(*exec.Cmd) {}

func Interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }

// PrepareGroup starts a child (engine or assistant) in its own process group so cancellation reaches it
// and anything it started, and a terminal Ctrl+C aimed at Hclaro does not reach it directly.
func PrepareGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func InterruptGroup(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGINT) }

func KillGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}
