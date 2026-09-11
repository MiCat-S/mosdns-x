//go:build ui

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Assets returns the production frontend rooted at web/dist.
func Assets() fs.FS {
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
