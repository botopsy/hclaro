//go:build windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// prepareInterrupt starts the engine in its own process group so a CTRL_BREAK reaches only
// the engine and its provider plugins, never Hclaro.
func prepareInterrupt(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// interrupt sends CTRL_BREAK, which Go programs such as Terraform and OpenTofu receive as
// os.Interrupt. It fails when Hclaro has no console; the caller then kills the process.
func interrupt(p *os.Process) error {
	if ok, _, err := generateConsoleCtrlEvent.Call(syscall.CTRL_BREAK_EVENT, uintptr(p.Pid)); ok == 0 {
		return err
	}
	return nil
}
