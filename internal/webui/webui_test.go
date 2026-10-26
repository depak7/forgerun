package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// app is a stand-in for the Vite build.
var app = fstest.MapFS{
	"index.html":          {Data: []byte(`<!doctype html><link rel="canonical" href="%SITE%/"><div id=root></div>`)},
	"assets/index-abc.js": {Data: []byte("console.log('app')")},
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesTheApp(t *testing.T) {
	h := Handler(app, "")
	for _, path := range []string{"/", "/threads/t-123", "/settings", "/tasks/new"} {
		rec := get(h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id=root`) {
			t.Errorf("%s: %d %q, want index.html", path, rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: index.html must not be cached, got %q", path, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestServesAssetsWithLongCaching(t *testing.T) {
	rec := get(Handler(app, ""), "/assets/index-abc.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache = %q", rec.Header().Get("Cache-Control"))
	}
	if rec := get(Handler(app, ""), "/assets/gone-old.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d, want 404 (not the app)", rec.Code)
	}
}

func TestOnlyReads(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(app, "").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestPlaceholderWithoutUI(t *testing.T) {
	rec := get(Handler(nil, ""), "/anything")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "make build") {
		t.Errorf("placeholder: %d %q", rec.Code, rec.Body)
	}
}

// The page names this server's own address, so link previews and search
// engines see it; without one the links stay relative.
func TestFillsInTheSite(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/threads/t-1"} {
		body := get(Handler(app, "https://forgerun.example.com/"), path).Body.String()
		if !strings.Contains(body, `href="https://forgerun.example.com/"`) || strings.Contains(body, "%SITE%") {
			t.Errorf("%s: %q", path, body)
		}
	}
	if body := get(Handler(app, ""), "/").Body.String(); !strings.Contains(body, `href="/"`) {
		t.Errorf("no site: %q", body)
	}
	// It's config, but it lands in HTML: escaped.
	if body := get(Handler(app, `https://x.example.com/"><script>`), "/").Body.String(); strings.Contains(body, "<script>") {
		t.Errorf("not escaped: %q", body)
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	h := Handler(app, "https://forgerun.example.com")
	robots := get(h, "/robots.txt")
	for _, want := range []string{"Disallow: /api/", "Disallow: /auth/", "Sitemap: https://forgerun.example.com/sitemap.xml"} {
		if !strings.Contains(robots.Body.String(), want) {
			t.Errorf("robots.txt lacks %q:\n%s", want, robots.Body)
		}
	}
	if ct := robots.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("robots.txt type %q", ct)
	}
	sm := get(h, "/sitemap.xml")
	if !strings.Contains(sm.Body.String(), "<loc>https://forgerun.example.com/</loc>") || !strings.Contains(sm.Header().Get("Content-Type"), "xml") {
		t.Errorf("sitemap: %q %q", sm.Header().Get("Content-Type"), sm.Body)
	}
	// Without an address there's no sitemap, and robots.txt doesn't point to one.
	if rec := get(Handler(app, ""), "/sitemap.xml"); rec.Code != http.StatusNotFound {
		t.Errorf("sitemap without a site: %d", rec.Code)
	}
	if body := get(Handler(app, ""), "/robots.txt").Body.String(); strings.Contains(body, "Sitemap") {
		t.Errorf("robots without a site: %q", body)
	}
}
