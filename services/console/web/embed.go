// Package web embeds the built web app (the output of ../build-web.sh). Before the first front-end build the directory
// holds only a placeholder, so `go build` works and the server answers 503 with a hint for the app shell.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built app rooted at index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
