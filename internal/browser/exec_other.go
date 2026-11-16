//go:build !windows

package browser

// execFallbackPaths returns extra absolute locations probed after the PATH
// candidates on platforms where browsers do not install to PATH. Only
// Windows needs it so far. It is a variable so tests can isolate resolution
// from whatever is installed on the machine running them.
var execFallbackPaths = func() []string { return nil }
