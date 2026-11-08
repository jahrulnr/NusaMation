//go:build windows

package browser

import (
	"path/filepath"
	"testing"
)

func TestExecFallbackPaths(t *testing.T) {
	paths := execFallbackPaths()
	if len(paths) == 0 {
		t.Fatal("expected fallback paths on Windows")
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			t.Errorf("fallback path is not absolute: %s", p)
		}
	}
}
