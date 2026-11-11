//go:build windows

package chat

import (
	"os/exec"
	"syscall"
)

func hideReferenceProcessWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
