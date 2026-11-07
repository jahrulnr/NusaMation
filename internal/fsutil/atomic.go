// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic writes data to dir/name atomically: a temp file in the same
// directory, then rename. On POSIX the rename is atomic; on Windows os.Rename
// cannot replace an existing file, so the destination is removed first — a
// brief non-atomic gap that is acceptable for the ephemeral files this
// serves (pick JSON, conversation meta).
func WriteFileAtomic(dir, name string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	dst := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		_ = os.Remove(dst)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
