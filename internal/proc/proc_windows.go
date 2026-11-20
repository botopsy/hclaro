//go:build windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// PrepareInterrupt starts the engine in its own process group so a CTRL_BREAK reaches only
// the engine and its provider plugins, never Hclaro.
func PrepareInterrupt(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Interrupt sends CTRL_BREAK, which Go programs such as Terraform and OpenTofu receive as
// os.Interrupt. It fails when Hclaro has no console; the caller then kills the process.
func Interrupt(p *os.Process) error {
	if ok, _, err := generateConsoleCtrlEvent.Call(syscall.CTRL_BREAK_EVENT, uintptr(p.Pid)); ok == 0 {
		return err
	}
	return nil
}

// PrepareGroup already applies: the child gets its own process group.
func PrepareGroup(cmd *exec.Cmd) { PrepareInterrupt(cmd) }

func InterruptGroup(p *os.Process) error { return Interrupt(p) }

// shortcut: Windows kills only the child, not processes it started; Claude Code 2.1.326 starts
// none with tools off. Use a job object if a provider ever does.
func KillGroup(p *os.Process) error { return p.Kill() }
