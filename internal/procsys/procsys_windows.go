//go:build windows

package procsys

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	showWindow       = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
)

// StartInOwnGroup is a no-op on Windows: there are no POSIX process groups.
func StartInOwnGroup(cmd *exec.Cmd) {}

// HideConsole hides the process console window. The Windows logon task passes
// -hide-console so the server runs without a visible window while staying a
// child of the task (schtasks /End still stops it).
func HideConsole() {
	if hwnd, _, _ := getConsoleWindow.Call(); hwnd != 0 {
		// SW_HIDE = 0
		_, _, _ = showWindow.Call(hwnd, 0)
	}
}

// Signal hard-kills the child on Windows: POSIX signals are not supported and
// the process cannot be put in a group of its own.
func Signal(cmd *exec.Cmd, sig syscall.Signal) {
	_ = cmd.Process.Kill()
}
