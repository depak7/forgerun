// Package webui serves forgerun's web app: the React build from web/,
// embedded into the binary when built with -tags webui (see Makefile).
// Without the tag it serves a placeholder, so the backend builds and runs
// without Node.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Files returns the built app, or nil when this binary was built without it.
func Files() fs.FS { return files }

// Handler serves the app in fsys at /. Paths that aren't files get
// index.html, so the app's own routes (/threads/t-…) work on reload. A nil
// fsys serves a page explaining how to build the UI.
func Handler(fsys fs.FS) http.Handler {
	if fsys == nil {
		return http.HandlerFunc(placeholder)
	}
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && exists(fsys, name) {
			// Vite puts a content hash in asset names, so they never change.
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		// A missing asset is a real 404, not the app: a stale page asking
		// for an old bundle should fail visibly.
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache") // always pick up a new build
		http.ServeFileFS(w, r, fsys, "index.html")
	})
}

func exists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

func placeholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>forgerun</title>
<body style="font-family:monospace;background:#0F1110;color:#ECEEEC;padding:40px;line-height:1.6">
<h1 style="font-weight:500">forgerun</h1>
<p>The API is running at <a style="color:#4ADE9B" href="/api/status">/api/status</a>, but this binary was built without the web UI.</p>
<p>Build it in: <code>make build</code> &nbsp;·&nbsp; or develop it live: <code>cd web &amp;&amp; npm run dev</code></p>
</body>`))
}
