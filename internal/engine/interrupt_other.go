//go:build !windows

package engine

import (
	"os"
	"os/exec"
)

func prepareInterrupt(*exec.Cmd) {}

func interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }
