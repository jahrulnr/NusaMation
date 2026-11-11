//go:build !windows

package chat

import "os/exec"

func hideReferenceProcessWindow(*exec.Cmd) {}
