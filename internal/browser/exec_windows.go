//go:build windows

package browser

import (
	"os"
	"path/filepath"
)

// execFallbackPaths lists well-known browser install locations probed when no
// PATH candidate matches. Chrome-family browsers install under Program Files
// or LOCALAPPDATA and never add themselves to PATH on Windows. It is a
// variable so tests can isolate resolution from whatever is installed on the
// machine running them (Edge ships with Windows).
var execFallbackPaths = func() []string {
	var dirs []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if dir := os.Getenv(env); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	rels := []string{
		`Google\Chrome\Application\chrome.exe`,
		`Chromium\Application\chrome.exe`,
		`Microsoft\Edge\Application\msedge.exe`,
		`Google\Chrome SxS\Application\chrome.exe`,
	}
	var out []string
	for _, dir := range dirs {
		for _, rel := range rels {
			out = append(out, filepath.Join(dir, rel))
		}
	}
	return out
}
