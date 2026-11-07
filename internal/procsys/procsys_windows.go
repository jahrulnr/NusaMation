//go:build windows

package procsys

import (
	"os/exec"
	"syscall"
)

// StartInOwnGroup is a no-op on Windows: there are no POSIX process groups.
func StartInOwnGroup(cmd *exec.Cmd) {}

// Signal hard-kills the child on Windows: POSIX signals are not supported and
// the process cannot be put in a group of its own.
func Signal(cmd *exec.Cmd, sig syscall.Signal) {
	_ = cmd.Process.Kill()
}
