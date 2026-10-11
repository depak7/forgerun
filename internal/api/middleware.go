package api

import (
	"log/slog"
	"net/http"
	"time"
)

// LogRequests logs one line per request: method, path, status and duration.
func LogRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"dur", time.Since(start).Round(time.Millisecond))
	})
}

// statusWriter remembers the status code; everything else goes to the
// embedded ResponseWriter.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the original writer (for
// flushing, deadlines), which the wrapper would otherwise hide.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
