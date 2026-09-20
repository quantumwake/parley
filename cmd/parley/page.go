package main

import (
	"embed"
	"io/fs"
)

// The console page is built by `make console` into console/dist and
// embedded here, so the binary carries its own viewer.
//
//go:embed all:dist
var pageFS embed.FS

func consolePage() fs.FS {
	sub, err := fs.Sub(pageFS, "dist")
	if err != nil {
		return nil
	}

	return sub
}
