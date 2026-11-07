//go:build !windows

package update

import (
	"os"
	"syscall"
)

// installBinary swaps the running binary atomically. Renaming over a running
// executable is allowed on Unix — the old inode stays mapped until exec.
func installBinary(exe, staged string) error {
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	return os.Rename(staged, exe)
}

// restartSelf replaces the process image with the new binary. This works the
// same under systemd and a manual run: the PID stays, so the service manager
// sees no exit, and connected clients just see a dropped connection followed
// by a reconnect to the new version.
func restartSelf(exe, _ string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
