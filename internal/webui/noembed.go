//go:build !webui

package webui

import "io/fs"

// Built without -tags webui: no app inside; Handler serves a placeholder.
var files fs.FS
