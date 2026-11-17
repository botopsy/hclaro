//go:build windows

package assist

import (
	"os"
	"os/exec"
	"syscall"
)

// shortcut: Windows kills only the assistant process, not processes it started; Claude Code
// 2.1.326 starts none with tools off. Use a job object if a provider ever does.
func prepareGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killGroup(p *os.Process) error { return p.Kill() }
