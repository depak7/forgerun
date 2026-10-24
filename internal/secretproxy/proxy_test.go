package secretproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// upstream is a fake API: it echoes the Authorization header and the query.
func upstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("Authorization")+"|"+r.URL.RawQuery)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setup starts a proxy whose upstream and tunnels all lead to srv, and a
// listener with one secret for api.test.
func setup(t *testing.T, srv *httptest.Server) (*Proxy, *Listener, Secret) {
	t.Helper()
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	toSrv := func(string, string) (net.Conn, error) { return net.Dial("tcp", srv.Listener.Addr().String()) }
	p.Upstream = &http.Transport{
		DialContext:     func(context.Context, string, string) (net.Conn, error) { return toSrv("", "") },
		TLSClientConfig: &tls.Config{RootCAs: certPool(srv), ServerName: "example.com"},
	}
	p.Dial = toSrv
	sec := Secret{Placeholder: Placeholder(), Value: "real-key", Hosts: []string{"api.test"}}
	l, err := p.Listen("127.0.0.1:0", []Secret{sec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return p, l, sec
}

func certPool(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

// client is a sandbox's HTTP client: the proxy as HTTPS_PROXY, trusting
// the proxy's CA (and, for tunnels, the real server's certificate).
func client(l *Listener, p *Proxy, srv *httptest.Server) *http.Client {
	pool := certPool(srv)
	pool.AppendCertsFromPEM(p.CAPEM())
	proxyURL, _ := url.Parse("http://" + l.Addr().String())
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

func get(t *testing.T, c *http.Client, url, auth string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", auth)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestSwapsThePlaceholder(t *testing.T) {
	srv := upstream(t)
	p, l, sec := setup(t, srv)
	got := get(t, client(l, p, srv), "https://api.test/v1?key="+sec.Placeholder, "Bearer "+sec.Placeholder)
	if got != "Bearer real-key|key=real-key" {
		t.Errorf("upstream saw %q", got)
	}
}

// A host with no secret is a plain tunnel: the client checks the real
// server's own certificate (example.com), which would fail had the proxy
// intercepted, and the placeholder goes through untouched.
func TestOtherHostsAreTunnelled(t *testing.T) {
	srv := upstream(t)
	p, l, sec := setup(t, srv)
	got := get(t, client(l, p, srv), "https://example.com/", "Bearer "+sec.Placeholder)
	if got != "Bearer "+sec.Placeholder+"|" {
		t.Errorf("upstream saw %q", got)
	}
}

// Another sandbox's listener has its own secrets, never these.
func TestListenersDontShareSecrets(t *testing.T) {
	srv := upstream(t)
	p, _, sec := setup(t, srv)
	other, err := p.Listen("127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// For the other sandbox, api.test has no secret: a plain tunnel to the
	// real server, whose certificate isn't for api.test.
	req, _ := http.NewRequest(http.MethodGet, "https://api.test/", nil)
	req.Header.Set("Authorization", "Bearer "+sec.Placeholder)
	if resp, err := client(other, p, srv).Do(req); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), "real-key") {
			t.Fatalf("the other sandbox got the key: %q", b)
		}
	}
}

// Answers are relayed as they stream, not when they end.
func TestStreams(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "second\n")
	}))
	defer srv.Close()
	p, l, sec := setup(t, srv)
	req, _ := http.NewRequest(http.MethodGet, "https://api.test/stream", nil)
	req.Header.Set("Authorization", sec.Placeholder)
	resp, err := client(l, p, srv).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	close(release)
	if err != nil || line != "first\n" {
		t.Errorf("first chunk: %q, %v", line, err)
	}
}

// A plain-HTTP request is passed on and never gets a key.
func TestPlainHTTPGetsNoKey(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer plain.Close()
	srv := upstream(t)
	p, l, sec := setup(t, srv)
	got := get(t, client(l, p, srv), plain.URL, sec.Placeholder)
	if got != sec.Placeholder {
		t.Errorf("plain http got %q", got)
	}
}

func TestHostMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern, host string
		want          bool
	}{
		{"api.anthropic.com", "api.anthropic.com", true},
		{"api.anthropic.com", "API.Anthropic.com", true},
		{"api.anthropic.com", "evil-api.anthropic.com", false},
		{"*.cursor.sh", "api2.cursor.sh", true},
		{"*.cursor.sh", "cursor.sh", false},
		{"*.cursor.sh", "evilcursor.sh", false},
	} {
		if got := hostMatches(tc.pattern, tc.host); got != tc.want {
			t.Errorf("hostMatches(%q, %q) = %v", tc.pattern, tc.host, got)
		}
	}
}

func TestPlaceholdersDiffer(t *testing.T) {
	a, b := Placeholder(), Placeholder()
	if a == b || !strings.HasPrefix(a, "forgerun-secret-") || len(a) != len("forgerun-secret-")+32 {
		t.Errorf("%q, %q", a, b)
	}
}
