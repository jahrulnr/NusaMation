//go:build windows

package procsys

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// AcquireSingleton claims the per-data-directory instance lock. Two processes
// pointing at the same data directory would open the same SQLite database and
// the same WAL, so the second one must not start. A named mutex is the
// Windows-native way to say that: the kernel owns it, it disappears when the
// last handle closes (process death included), and there is no stale lock file
// to clean up after a crash.
//
// ok=false means another process already holds the mutex; the caller exits
// quietly, which is what makes a second autostart mechanism harmless.
func AcquireSingleton(dataDir string) (release func(), ok bool, err error) {
	name, err := windows.UTF16PtrFromString(`Local\NusaMation-` + dataDirKey(dataDir))
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	// CreateMutex opens an existing mutex and still returns a valid handle, so
	// "already exists" is reported as an error rather than a nil handle.
	switch {
	case err == windows.ERROR_ALREADY_EXISTS:
		_ = windows.CloseHandle(h)
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return func() { _ = windows.CloseHandle(h) }, true, nil
}

// dataDirKey turns a data directory into a stable mutex suffix. The path is
// lower-cased because Windows paths are case-insensitive, and hashed so the
// object name stays short.
func dataDirKey(dataDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dataDir))))
	return hex.EncodeToString(sum[:8])
}
