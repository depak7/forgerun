// Package webui serves forgerun's web app: the React build from web/,
// embedded into the binary when built with -tags webui (see Makefile).
// Without the tag it serves a placeholder, so the backend builds and runs
// without Node.
package webui

import (
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Files returns the built app, or nil when this binary was built without it.
func Files() fs.FS { return files }

// Handler serves the app in fsys at /. Known app routes (/threads/t-…,
// /tasks/new, /settings) get index.html with 200 so a browser reload works;
// unknown paths get a real 404 (not a soft 404) so crawlers and discovery
// probes (/.well-known/*, etc.) see accurate HTTP statuses. A nil fsys
// serves a page explaining how to build the UI.
//
// site is where this server is reached (FORGERUN_PUBLIC_URL, or ""). It goes
// into index.html's link-preview, canonical and JSON-LD tags, robots.txt,
// sitemap.xml and llms.txt, so search engines and link previews see this
// server's own address.
//
// analytics is a website ID for the analytics script
// (FORGERUN_ANALYTICS_WEBSITE_ID, or ""). With one, index.html loads the
// script; without one the page has no script tag at all, so a self-hosted
// server sends page views nowhere.
func Handler(fsys fs.FS, site, analytics string) http.Handler {
	if fsys == nil {
		return http.HandlerFunc(placeholder)
	}
	// Escaped once: it goes into HTML attributes and XML.
	site = html.EscapeString(strings.TrimRight(site, "/"))
	page := strings.NewReplacer("%SITE%", site, "%ANALYTICS%", analyticsTag(analytics))
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		setSecurityHeaders(w)
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch name {
		case "robots.txt":
			robots(w, site)
			return
		case "sitemap.xml":
			sitemap(w, r, site)
			return
		case "llms.txt", "llms-full.txt":
			llmsTxt(w, site)
			return
		case "index.md":
			w.Header().Set("Vary", "Accept")
			llmsTxt(w, site)
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
		// Missing static assets and unknown discovery paths must be real 404s,
		// never a 200 HTML shell (which creates soft 404s and breaks ARD/Well-Known checks).
		if strings.HasPrefix(name, "assets/") || !isAppRoute(name) {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Robots-Tag", "noindex")
			serveIndex(w, fsys, page, http.StatusNotFound)
			return
		}
		w.Header().Set("Vary", "Accept")
		if name == "" && prefersMarkdown(r.Header.Get("Accept")) {
			llmsTxt(w, site)
			return
		}
		w.Header().Set("Cache-Control", "no-cache") // always pick up a new build
		serveIndex(w, fsys, page, http.StatusOK)
	})
}

// analyticsTag is the script tag that loads the analytics script for
// website id, or "" without one.
func analyticsTag(id string) string {
	if id = strings.TrimSpace(id); id == "" {
		return ""
	}
	return `<script defer src="https://cloud.umami.is/script.js" data-website-id="` + html.EscapeString(id) + `"></script>`
}

// serveIndex sends index.html with page's placeholders filled in.
func serveIndex(w http.ResponseWriter, fsys fs.FS, page *strings.Replacer, status int) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "index.html is missing from the build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = page.WriteString(w, string(index))
}

// isAppRoute reports whether clean relative path name is a valid client-side
// route in the React router (/, /tasks/new, /threads/:id, /settings).
func isAppRoute(name string) bool {
	if name == "" || name == "tasks/new" || name == "settings" {
		return true
	}
	if rest, ok := strings.CutPrefix(name, "threads/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return true
	}
	return false
}

// prefersMarkdown reports whether an Accept header asks for text/markdown
// and does not simultaneously prefer text/html (like a standard browser).
func prefersMarkdown(accept string) bool {
	a := strings.ToLower(accept)
	return strings.Contains(a, "text/markdown") && !strings.Contains(a, "text/html")
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

// robots lets search and AI crawlers read the public landing page and llms.txt,
// while disallowing the API and sign-in flow. Includes RFC 9309 named groups
// and Content-Signal declarations.
func robots(w http.ResponseWriter, site string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	const signal = "Content-Signal: search=yes, ai-input=yes, ai-train=yes\n"
	const rules = "Allow: /\nDisallow: /api/\nDisallow: /auth/\n"
	var b strings.Builder
	b.WriteString("User-agent: *\n" + signal + rules)
	for _, agent := range []string{
		"OAI-SearchBot",
		"ChatGPT-User",
		"GPTBot",
		"Claude-SearchBot",
		"Claude-User",
		"ClaudeBot",
		"PerplexityBot",
		"Perplexity-User",
		"Google-Extended",
		"Applebot-Extended",
	} {
		b.WriteString("\nUser-agent: " + agent + "\n" + signal + rules)
	}
	if site != "" {
		b.WriteString("\nSitemap: " + site + "/sitemap.xml\n")
	}
	_, _ = w.Write([]byte(b.String()))
}

// sitemap lists the public landing page with a W3C Datetime lastmod.
// It needs an absolute address, so without a site there is none.
func sitemap(w http.ResponseWriter, r *http.Request, site string) {
	if site == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>` + site + `/</loc><lastmod>2026-11-04</lastmod></url></urlset>
`))
}

// llmsTxt serves a structured Markdown summary of forgerun following the
// llmstxt.org specification and Lighthouse Agentic Browsing rules.
func llmsTxt(w http.ResponseWriter, site string) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	home := "/"
	if site != "" {
		home = site + "/"
	}
	body := `# forgerun

> Open-source, self-hosted coding agent runner. Run Claude Code, Cursor CLI, OpenCode, and Pi on your GitHub repositories — every thread in its own isolated microVM (via microsandbox/libkrun) or gVisor sandbox, with host-side secret proxying and warm disk snapshots.

forgerun is a single Go binary with an embedded React web UI and JSON API. You point it at a GitHub repository, pick a coding agent and model, and give it a task. The agent installs dependencies, builds, and runs tests inside its own sandboxed VM. API keys never enter the sandbox (a host-side TLS proxy swaps placeholders for real keys only when talking to the agent's official API), and nothing reaches GitHub until you review the diff and press push.

## Key Capabilities

- **Warm-start VM snapshots (26.8s -> 2.8s):** Saving a session's VM disk snapshot preserves cloned repositories and installed dependencies so subsequent threads start in ~2.8 seconds (measured on expressjs/express).
- **Zero-trust secret isolation:** Sandboxes only receive placeholder environment variables (` + "`$MSB_...`" + ` or ` + "`FORGERUN_PH_...`" + `). A host-side proxy injects the real API key exclusively for the agent's allowed upstream host.
- **Hardware or user-space isolation:** Uses hardware-virtualized microVMs via microsandbox (libkrun) on Apple Silicon macOS and Linux with KVM, or gVisor (` + "`runsc`" + `) user-space kernel sandboxes on standard cloud VMs without nested virtualization.
- **Pluggable multi-agent support:** Built-in adapters for ` + "`cursor`" + ` (Cursor CLI), ` + "`claude-code`" + ` (Claude Code using your Pro/Max subscription ` + "`CLAUDE_CODE_OAUTH_TOKEN`" + `), ` + "`opencode`" + ` (including free keyless models), and ` + "`pi`" + ` (OpenRouter, OpenAI, Anthropic).
- **Split orchestrator & outbound workers:** Run the lightweight web UI + Postgres orchestrator on any small VPS (` + "`forgerun server`" + `) and connect outbound workers (` + "`forgerun worker`" + `) from your Mac or home Linux server over HTTPS with zero inbound ports open.

## Documentation & Links

- [forgerun Web UI & Live Demo](` + home + `): Interactive web interface, 30-second video demo, and one-line installer.
- [GitHub Repository](https://github.com/depak7/forgerun): Source code (Go & TypeScript/React), Apache-2.0 license, issues, and releases.
- [One-Line Installer Script](https://raw.githubusercontent.com/depak7/forgerun/main/install.sh): Automated setup for macOS (Apple Silicon) and Linux (KVM or gVisor) with systemd/launchd service registration.
- [Deployment Guide](https://github.com/depak7/forgerun/blob/main/docs/deploying.md): How to run the orchestrator on a VPS with Caddy automatic HTTPS, GitHub OAuth sign-in, and remote workers.
- [Architecture & Benchmarks](https://github.com/depak7/forgerun/blob/main/docs/experiments.md): Detailed engineering experiments on microVM snapshots, TLS secret proxying, gVisor startup latency (0.34s), and git patch publishing.

## Quick Start

Install on macOS (Apple Silicon) or Linux (KVM or Docker + gVisor):

` + "```sh\ncurl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh\n```" + `

Check system readiness and virtualization diagnostics at any time:

` + "```sh\nforgerun doctor\n```" + `

## Optional

- [Apache-2.0 License](https://github.com/depak7/forgerun/blob/main/LICENSE): Free for personal and commercial self-hosting.
`
	_, _ = w.Write([]byte(body))
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
