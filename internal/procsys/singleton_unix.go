//go:build !windows

package procsys

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// AcquireSingleton claims the per-data-directory instance lock. Two processes
// pointing at the same data directory would open the same SQLite database and
// the same WAL, so the second one must not start.
//
// The lock is an exclusive flock on <dataDir>/nusamation.lock. flock is
// released by the kernel when the process dies, so a crash never leaves a lock
// the next boot has to clean up (the file itself stays, which is fine).
//
// ok=false means another process already holds the lock; the caller exits
// quietly.
func AcquireSingleton(dataDir string) (release func(), ok bool, err error) {
	path := filepath.Join(dataDir, "nusamation.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
