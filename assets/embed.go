// Package assets embeds files shipped inside the binary and seeded into the
// data directory on startup.
package assets

import (
	"embed"
	"io/fs"
)

//go:embed docs
var assets embed.FS

// Docs returns the embedded operational documentation tree.
func Docs() fs.FS {
	sub, err := fs.Sub(assets, "docs")
	if err != nil {
		panic(err)
	}
	return sub
}
