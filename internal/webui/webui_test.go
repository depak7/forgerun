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
	"index.html":          {Data: []byte("<!doctype html><div id=root></div>")},
	"assets/index-abc.js": {Data: []byte("console.log('app')")},
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesTheApp(t *testing.T) {
	h := Handler(app)
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
	rec := get(Handler(app), "/assets/index-abc.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache = %q", rec.Header().Get("Cache-Control"))
	}
	if rec := get(Handler(app), "/assets/gone-old.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d, want 404 (not the app)", rec.Code)
	}
}

func TestOnlyReads(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(app).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestPlaceholderWithoutUI(t *testing.T) {
	rec := get(Handler(nil), "/anything")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "make build") {
		t.Errorf("placeholder: %d %q", rec.Code, rec.Body)
	}
}
