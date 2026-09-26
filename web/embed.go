// Package web embeds the built browser UI (web/dist) into the Go binary.
package web

import (
	"embed"
	"io/fs"
)

// dist_placeholder.txt keeps this pattern valid before the UI is built.
//
//go:embed all:dist*
var files embed.FS

// Dist returns the built UI, or nil when `make web` has not been run.
func Dist() fs.FS { return dist(files) }

func dist(fsys fs.FS) fs.FS {
	sub, err := fs.Sub(fsys, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
