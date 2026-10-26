// Package webui serves forgerun's web app: the React build from web/,
// embedded into the binary when built with -tags webui (see Makefile).
// Without the tag it serves a placeholder, so the backend builds and runs
// without Node.
package webui

import (
	"bytes"
	"html"
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
//
// site is where this server is reached (FORGERUN_PUBLIC_URL, or ""). It goes
// into index.html's link-preview and canonical tags, robots.txt and
// sitemap.xml, so search engines and link previews see this server's own
// address.
func Handler(fsys fs.FS, site string) http.Handler {
	if fsys == nil {
		return http.HandlerFunc(placeholder)
	}
	// Escaped once: it goes into HTML attributes and XML.
	site = html.EscapeString(strings.TrimRight(site, "/"))
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch name {
		case "robots.txt":
			robots(w, site)
			return
		case "sitemap.xml":
			sitemap(w, r, site)
			return
		case "index.html":
			name = "" // the page, with the site filled in
		}
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
		page, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "index.html is missing from the build", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(bytes.ReplaceAll(page, []byte("%SITE%"), []byte(site)))
	})
}

// robots lets crawlers read the pages but not the API or the sign-in flow.
func robots(w http.ResponseWriter, site string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := "User-agent: *\nAllow: /\nDisallow: /api/\nDisallow: /auth/\n"
	if site != "" {
		body += "Sitemap: " + site + "/sitemap.xml\n"
	}
	_, _ = w.Write([]byte(body))
}

// sitemap lists the one public page. It needs an absolute address, so
// without a site there is none.
func sitemap(w http.ResponseWriter, r *http.Request, site string) {
	if site == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>` + site + `/</loc></url></urlset>
`))
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
