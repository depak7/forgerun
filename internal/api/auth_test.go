package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/store/memory"
)

const password = "correct horse battery"

func authSetup(t *testing.T, opts ...func(*Config)) (http.Handler, *memory.Store) {
	t.Helper()
	h, s, _ := setup(t, append([]func(*Config){func(c *Config) {
		c.Auth = &Auth{Username: "owner", Password: password}
	}}, opts...)...)
	return h, s
}

// signIn signs in with the owner's password and returns the login cookie.
func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := as(h, nil, "POST", "/auth/login", `{"username":"owner","password":"`+password+`"}`)
	mustData(t, rec, http.StatusOK)
	for _, c := range rec.Result().Cookies() {
		if c.Name == loginCookie && c.Value != "" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("login cookie flags: %+v", c)
			}
			return c
		}
	}
	t.Fatal("no login cookie")
	return nil
}

// as makes a request with a login cookie, from forgerun's own pages.
func as(h http.Handler, c *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if c != nil {
		req.AddCookie(c)
	}
	if method != "GET" {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignIn(t *testing.T) {
	h, _ := authSetup(t)

	// Signed out: the UI learns sign-in is needed; the API refuses.
	var me meJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/me", ""), http.StatusOK), &me)
	if me.SignedIn || !me.SignIn {
		t.Errorf("signed out: %+v", me)
	}
	decode(t, do(h, "GET", "/api/threads", ""), http.StatusUnauthorized)

	c := signIn(t, h)
	json.Unmarshal(mustData(t, as(h, c, "GET", "/api/me", ""), http.StatusOK), &me)
	if !me.SignedIn || me.Login != "owner" {
		t.Errorf("signed in: %+v", me)
	}
	mustData(t, as(h, c, "GET", "/api/threads", ""), http.StatusOK)

	// Another site can't make the browser change things.
	req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{}`))
	req.AddCookie(c)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: %d", rec.Code)
	}

	// Signing out ends the login, on the server too.
	mustData(t, as(h, c, "POST", "/auth/logout", ""), http.StatusOK)
	decode(t, as(h, c, "GET", "/api/threads", ""), http.StatusUnauthorized)
}

// Wrong passwords are refused, and after a few in a row that address must
// wait, even with the right one.
func TestSignInRefusals(t *testing.T) {
	h, _ := authSetup(t)
	for _, body := range []string{
		`{"username":"owner","password":"wrong"}`,
		`{"username":"someone","password":"` + password + `"}`,
	} {
		_, apiErr := decode(t, as(h, nil, "POST", "/auth/login", body), http.StatusUnauthorized)
		if !strings.Contains(apiErr.Message, "wrong username or password") {
			t.Errorf("%s: %q", body, apiErr.Message)
		}
	}
	for range maxFailures - 2 {
		decode(t, as(h, nil, "POST", "/auth/login", `{"username":"owner","password":"wrong"}`), http.StatusUnauthorized)
	}
	_, apiErr := decode(t, as(h, nil, "POST", "/auth/login", `{"username":"owner","password":"`+password+`"}`), http.StatusTooManyRequests)
	if !strings.Contains(apiErr.Message, "too many failed sign-ins") {
		t.Errorf("locked out: %q", apiErr.Message)
	}
}

func TestFailuresExpire(t *testing.T) {
	var f failures
	now := time.Now()
	for range maxFailures {
		f.add("1.2.3.4", now)
	}
	if f.wait("1.2.3.4", now) <= 0 || f.wait("5.6.7.8", now) != 0 {
		t.Error("only the failing address waits")
	}
	if f.wait("1.2.3.4", now.Add(lockout)) != 0 {
		t.Error("the wait never ends")
	}
	f.clear("1.2.3.4")
	if f.wait("1.2.3.4", now) != 0 {
		t.Error("a sign-in doesn't clear the count")
	}
}

// Without sign-in settings nothing changes: no login needed.
func TestLocalMode(t *testing.T) {
	h, _, _ := setup(t)
	var me meJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/me", ""), http.StatusOK), &me)
	if !me.SignedIn || me.SignIn {
		t.Errorf("local mode: %+v", me)
	}
	mustData(t, do(h, "GET", "/api/snapshots", ""), http.StatusOK)
	decode(t, do(h, "POST", "/auth/login", `{}`), http.StatusBadRequest)
}

func TestRepoAccessRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		prs  fakePRs
		want string
	}{
		{"read only", fakePRs{readOnly: true}, "not push to it"},
		{"not visible", fakePRs{noRepo: true}, "doesn't show this repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setup(t, func(c *Config) { c.PRs = tc.prs })
			_, apiErr := decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"x"}`), http.StatusBadRequest)
			if !strings.Contains(apiErr.Message, tc.want) {
				t.Errorf("got %q", apiErr.Message)
			}
		})
	}
}

func TestAllowFrom(t *testing.T) {
	allow, err := ParseAllowFrom(" 127.0.0.1, 100.64.0.0/10,,::1")
	if err != nil || len(allow) != 3 {
		t.Fatalf("ParseAllowFrom = %v, %v", allow, err)
	}
	if _, err := ParseAllowFrom("tailnet"); err == nil {
		t.Error("a name isn't an address")
	}
	h := AllowFrom(allow, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for addr, want := range map[string]int{
		"127.0.0.1:5000":   http.StatusOK,
		"100.101.1.2:5000": http.StatusOK,
		"[::1]:5000":       http.StatusOK,
		"8.8.8.8:5000":     http.StatusForbidden,
		"192.168.1.9:5000": http.StatusForbidden,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", addr, rec.Code, want)
		}
	}
	open := AllowFrom(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "8.8.8.8:5000"
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("no ranges: %d, want everyone allowed", rec.Code)
	}
}
