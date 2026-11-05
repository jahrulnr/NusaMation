// Package nusamation provides the version embedded in the application binary.
package nusamation

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the application version from VERSION, without surrounding whitespace.
var Version = strings.TrimSpace(versionFile)
