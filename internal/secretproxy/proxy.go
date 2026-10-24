// Package secretproxy keeps API keys out of sandboxes whose runtime has no
// key-swapping proxy of its own (gVisor). A sandbox gets placeholders in
// place of its keys and this proxy as its HTTPS_PROXY. For a host that a
// key is meant for, the proxy terminates TLS with a certificate from its
// own CA (which the sandbox is told to trust), puts the real key where the
// placeholder is, and sends the request on to the real host. Every other
// connection is a plain tunnel the proxy can't read, and never gets a key.
//
// Each sandbox has its own listener, on its own network, so a connection
// can only ever get the keys of the sandbox it came from.
package secretproxy

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Secret is one key: the placeholder the sandbox sees, the real value, and
// the hosts it may be sent to: exact ("api.anthropic.com") or a wildcard
// for subdomains ("*.cursor.sh").
type Secret struct {
	Placeholder string
	Value       string
	Hosts       []string
}

// Placeholder returns a new random placeholder, so no sandbox can guess
// another's.
func Placeholder() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "forgerun-secret-" + hex.EncodeToString(b)
}

// Proxy is the CA and the settings shared by every sandbox's listener.
type Proxy struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
	leaves sync.Map // host → *tls.Certificate

	// Upstream sends intercepted requests on; nil = HTTP/1.1 with the
	// system's roots. Dial opens tunnels; nil = net.Dialer. (Tests replace both.)
	Upstream http.RoundTripper
	Dial     func(network, addr string) (net.Conn, error)
	Log      *slog.Logger
}

// New makes a proxy with a fresh CA. The CA's key lives only in memory: a
// restarted worker makes a new one, and sandboxes get the new certificate.
func New() (*Proxy, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "forgerun sandbox CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Proxy{caCert: cert, caKey: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// CAPEM is the CA certificate, for sandboxes to trust.
func (p *Proxy) CAPEM() []byte { return p.caPEM }

// Listener serves one sandbox.
type Listener struct {
	p       *Proxy
	ln      net.Listener
	secrets []Secret
	wg      sync.WaitGroup
}

// Listen serves a sandbox on addr (its network's gateway, e.g.
// "172.30.0.1:0"), with that sandbox's secrets only.
func (p *Proxy) Listen(addr string, secrets []Secret) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	l := &Listener{p: p, ln: ln, secrets: secrets}
	l.wg.Add(1)
	go l.serve()
	return l, nil
}

// Addr is where the listener is, for the sandbox's HTTPS_PROXY.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Close stops the listener; connections already open are dropped as their
// sandbox goes away.
func (l *Listener) Close() error {
	err := l.ln.Close()
	l.wg.Wait()
	return err
}

func (l *Listener) serve() {
	defer l.wg.Done()
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		go l.handle(c)
	}
}

func (l *Listener) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		l.forwardPlain(c, req) // plain http://: passed on as it is, never with a key
		return
	}
	host, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\n\r\n")
		return
	}
	mine := l.secretsFor(host)
	if len(mine) == 0 {
		l.tunnel(c, br, req.Host)
		return
	}
	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	l.intercept(c, host, req.Host, mine)
}

// secretsFor are the secrets that may be sent to host.
func (l *Listener) secretsFor(host string) []Secret {
	var out []Secret
	for _, s := range l.secrets {
		for _, h := range s.Hosts {
			if hostMatches(h, host) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func hostMatches(pattern, host string) bool {
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

// tunnel connects the sandbox to addr byte for byte.
func (l *Listener) tunnel(c net.Conn, br *bufio.Reader, addr string) {
	up, err := l.p.dial("tcp", addr)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{})
	go func() {
		io.Copy(up, br) // what the sandbox already sent after CONNECT is in br
		if tc, ok := up.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		close(done)
	}()
	io.Copy(c, up)
	<-done
}

// intercept reads the sandbox's requests to host over TLS, swaps its
// placeholders for the real keys, and relays each answer as it streams.
func (l *Listener) intercept(c net.Conn, host, addr string, secrets []Secret) {
	tc := tls.Server(c, &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return l.p.leaf(host) },
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
	})
	if err := tc.Handshake(); err != nil {
		l.p.log().Debug("secret proxy: TLS handshake", "host", host, "err", err)
		return
	}
	br := bufio.NewReader(tc)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		swap(req, secrets)
		req.URL.Scheme, req.URL.Host, req.RequestURI = "https", addr, ""
		resp, err := l.p.upstream().RoundTrip(req)
		if err != nil {
			l.p.log().Info("secret proxy: upstream", "host", host, "err", err)
			io.WriteString(tc, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		err = resp.Write(tc) // streams the body as it arrives
		resp.Body.Close()
		if err != nil || resp.Close {
			return
		}
	}
}

// swap puts the real keys where their placeholders are: in headers and in
// the URL's query (some APIs take ?key=).
func swap(req *http.Request, secrets []Secret) {
	for k, vs := range req.Header {
		for i, v := range vs {
			for _, s := range secrets {
				v = strings.ReplaceAll(v, s.Placeholder, s.Value)
			}
			req.Header[k][i] = v
		}
	}
	if q := req.URL.RawQuery; q != "" {
		for _, s := range secrets {
			q = strings.ReplaceAll(q, s.Placeholder, s.Value)
		}
		req.URL.RawQuery = q
	}
}

// forwardPlain passes a plain-HTTP proxy request on unchanged.
func (l *Listener) forwardPlain(c net.Conn, req *http.Request) {
	if req.URL.Host == "" {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\n\r\n")
		return
	}
	req.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer resp.Body.Close()
	resp.Write(c)
}

// leaf is a certificate for host, signed by the CA; made once per host.
func (p *Proxy) leaf(host string) (*tls.Certificate, error) {
	if c, ok := p.leaves.Load(host); ok {
		return c.(*tls.Certificate), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, 30),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, p.caCert.Raw}, PrivateKey: key}
	actual, _ := p.leaves.LoadOrStore(host, c)
	return actual.(*tls.Certificate), nil
}

func (p *Proxy) upstream() http.RoundTripper {
	if p.Upstream != nil {
		return p.Upstream
	}
	return defaultUpstream
}

// defaultUpstream speaks HTTP/1.1, as the sandbox side does, so answers
// can be relayed as they are.
var defaultUpstream = &http.Transport{
	Proxy:               nil,
	TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	MaxIdleConnsPerHost: 4,
	IdleConnTimeout:     90 * time.Second,
}

func (p *Proxy) dial(network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(network, addr)
	}
	return (&net.Dialer{Timeout: 15 * time.Second}).Dial(network, addr)
}

func (p *Proxy) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		panic(errors.New("secretproxy: no randomness: " + err.Error()))
	}
	return n
}
