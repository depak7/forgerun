package api

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
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

// clientIP is the address a request came from (its TCP peer: behind a
// proxy, the proxy's).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ParseAllowFrom reads a comma-separated list of IP ranges (CIDR, or one
// address), e.g. "127.0.0.1, 100.64.0.0/10".
func ParseAllowFrom(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or range (like 100.64.0.0/10)", s)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// AllowFrom answers 403 to requests from outside allowed, before anything
// else sees them. With no ranges every address is allowed.
func AllowFrom(allowed []netip.Prefix, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, err := netip.ParseAddr(clientIP(r))
		if err == nil && slices.ContainsFunc(allowed, func(p netip.Prefix) bool { return p.Contains(ip.Unmap()) }) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}
