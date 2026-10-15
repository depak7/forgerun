//go:build webui

package webui

import (
	"embed"
	"io/fs"
)

// dist is written by `npm run build` in web/ (vite.config.ts sets outDir).
//
//go:embed all:dist
var dist embed.FS

var files = mustSub(dist, "dist")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
