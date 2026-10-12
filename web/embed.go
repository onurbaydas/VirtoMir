// Package web embeds the built web UI (web/dist, produced by `npm run build`) into virtomird.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the UI files, or nil when the binary was built without them
// (dist/ only holds the .gitkeep placeholder).
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
