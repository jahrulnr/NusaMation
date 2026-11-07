//go:build !windows

// Package procsys isolates process-group and signal differences between Unix
// and Windows so callers can start and signal child processes portably.
package procsys

import (
	"os/exec"
	"syscall"
)

// StartInOwnGroup puts the child in its own process group so a signal can be
// delivered to the whole tree (grandchildren included), not just the parent.
func StartInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Signal sends sig to the child's process group when the child was started in
// its own group; otherwise it signals the child directly.
func Signal(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	} else {
		_ = cmd.Process.Signal(sig)
	}
}
