//go:build !windows

package browser

// execFallbackPaths returns extra absolute locations probed after the PATH
// candidates on platforms where browsers do not install to PATH. Only
// Windows needs it so far.
func execFallbackPaths() []string { return nil }
