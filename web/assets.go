//go:build !ui

package web

import "io/fs"

// Assets returns nil in headless builds.
func Assets() fs.FS {
	return nil
}
