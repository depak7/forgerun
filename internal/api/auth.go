package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/forge"
)

// Auth is the owner's sign-in: a username and password from the
// environment. Without it (nil) the server is in local mode: nobody signs
// in, and every request is allowed.
type Auth struct {
	Username, Password string
	failures           failures
}

const (
	loginCookie = "forgerun_login"
	loginFor    = 30 * 24 * time.Hour
)

// viewer is how a request signed in.
type viewer struct {
	ByToken bool // with a personal token, not a browser login
}

type viewerKey struct{}

func viewerOf(r *http.Request) viewer {
	v, _ := r.Context().Value(viewerKey{}).(viewer)
	return v
}

// withViewer guards the API: with sign-in on, every /api/ request but
// /api/me needs a login or a personal token, and requests that change
// something with a login must come from forgerun's own pages. (A token
// isn't sent by browsers on their own, so it needs no such check.)
func (s *Server) withViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if !s.validToken(r, tok) {
				writeError(w, http.StatusUnauthorized, "that token isn't valid: it may have been revoked")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerKey{}, viewer{ByToken: true})))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "requests that change something must come from forgerun's own pages")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/me" && !s.signedIn(r) {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin checks a browser's Origin (or, failing that, Referer) against
// the host it asked for. Together with SameSite cookies this stops other
// sites from making a signed-in browser change things.
func sameOrigin(r *http.Request) bool {
	from := r.Header.Get("Origin")
	if from == "" {
		from = r.Header.Get("Referer")
	}
	if from == "" {
		return true // not a browser (curl, scripts): the cookie is what's checked
	}
	got, err := url.Parse(from)
	return err == nil && got.Host == r.Host
}

func (s *Server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(loginCookie)
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := s.store.ValidLogin(r.Context(), hashOf(c.Value))
	if err != nil {
		s.log.Error("reading a login", "err", err)
	}
	return ok
}

// hashOf is what the database keeps for a login cookie or a token: its
// hash, so the table can't be used to sign in.
func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// secure reports whether the browser reached forgerun over HTTPS (itself,
// or through a proxy that says so), so cookies are sent over HTTPS only.
func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// login serves POST /auth/login {"username", "password"}: on a match, the
// browser gets a login cookie.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusBadRequest, "this server has no sign-in (local mode)")
		return
	}
	ip := clientIP(r)
	if wait := s.auth.failures.wait(ip, time.Now()); wait > 0 {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed sign-ins: try again in %d minutes", int(wait.Minutes())+1))
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !s.auth.matches(req.Username, req.Password) {
		s.auth.failures.add(ip, time.Now())
		s.log.Warn("failed sign-in", "ip", ip)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.auth.failures.clear(ip)
	cookie := randomToken()
	if err := s.store.CreateLogin(r.Context(), hashOf(cookie), time.Now().Add(loginFor)); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: cookie, Path: "/", MaxAge: int(loginFor.Seconds()),
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode})
	s.log.Info("signed in", "ip", ip)
	writeData(w, http.StatusOK, struct{}{})
}

// matches compares a username and password with the owner's, in constant
// time: both are checked even when the first differs.
func (a *Auth) matches(username, password string) bool {
	same := func(got, want string) int {
		g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
		return subtle.ConstantTimeCompare(g[:], w[:])
	}
	return same(username, a.Username)&same(password, a.Password) == 1
}

// logout serves POST /auth/logout.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if c, err := r.Cookie(loginCookie); err == nil {
			if err := s.store.DeleteLogin(r.Context(), hashOf(c.Value)); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode})
	}
	writeData(w, http.StatusOK, struct{}{})
}

// meJSON is whether the browser is signed in.
type meJSON struct {
	SignedIn bool   `json:"signed_in"`
	SignIn   bool   `json:"sign_in"` // the server has sign-in (false = local mode)
	Login    string `json:"login"`
}

// me serves GET /api/me. It answers 200 when signed out too, so the UI can
// show the sign-in page.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.auth == nil:
		writeData(w, http.StatusOK, meJSON{SignedIn: true, Login: "local"})
	case viewerOf(r).ByToken || s.signedIn(r):
		writeData(w, http.StatusOK, meJSON{SignedIn: true, SignIn: true, Login: s.auth.Username})
	default:
		writeData(w, http.StatusOK, meJSON{SignIn: true})
	}
}

// failures counts failed sign-ins by IP. A password, unlike a token, can be
// guessed, so after maxFailures in a row an IP waits out lockout.
type failures struct {
	mu sync.Mutex
	by map[string]failure
}

type failure struct {
	n    int
	last time.Time
}

const (
	maxFailures = 5
	lockout     = 15 * time.Minute
)

// wait is how long ip must wait before trying again; 0 if it needn't.
func (f *failures) wait(ip string, now time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.by[ip]
	if e.n < maxFailures {
		return 0
	}
	return max(0, lockout-now.Sub(e.last))
}

func (f *failures) add(ip string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[string]failure{}
	}
	for k, e := range f.by { // forget the ones whose lockout is over
		if now.Sub(e.last) >= lockout {
			delete(f.by, k)
		}
	}
	e := f.by[ip]
	e.n++
	e.last = now
	f.by[ip] = e
}

func (f *failures) clear(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.by, ip)
}

// checkPush refuses a repo the server's token can't push to.
func (s *Server) checkPush(w http.ResponseWriter, r *http.Request, repo string) bool {
	acc, err := s.prs.Access(r.Context(), repo)
	switch {
	case errors.Is(err, forge.ErrNoRepo):
		writeError(w, http.StatusBadRequest, "GitHub doesn't show this repo to GITHUB_TOKEN: check the URL, or that it's shared with the token's account")
		return false
	case err != nil:
		writeError(w, http.StatusBadGateway, "checking the repo on GitHub: "+err.Error())
		return false
	case !acc.Push:
		writeError(w, http.StatusBadRequest, "GITHUB_TOKEN can read this repo but not push to it, so forgerun couldn't push the changes; fork it and use your fork")
		return false
	}
	return true
}

// checkBranch makes sure a branch a thread is to start from exists ("" is
// the default branch, which always does).
func (s *Server) checkBranch(w http.ResponseWriter, r *http.Request, repo, branch string) bool {
	if branch == "" {
		return true
	}
	ok, err := s.prs.HasBranch(r.Context(), repo, branch)
	switch {
	case err != nil:
		writeError(w, http.StatusBadGateway, "checking the branch on GitHub: "+err.Error())
		return false
	case !ok:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("there's no branch %q in %s", branch, repo))
		return false
	}
	return true
}

// listBranches serves GET /api/repos/branches?repo=…: the repo's default
// branch and up to 100 of its branches, to start a thread from.
func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if err := validateRepo(repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := struct {
		Default  string   `json:"default"`
		Branches []string `json:"branches"`
	}{Branches: []string{}}
	if s.prs == nil {
		writeData(w, http.StatusOK, out)
		return
	}
	def, names, err := s.prs.Branches(r.Context(), repo)
	switch {
	case errors.Is(err, forge.ErrNoRepo):
		writeError(w, http.StatusBadRequest, "GitHub doesn't show this repo to GITHUB_TOKEN: check the URL, or that it's shared with the token's account")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, "listing the repo's branches on GitHub: "+err.Error())
		return
	}
	out.Default, out.Branches = def, append(out.Branches, names...)
	writeData(w, http.StatusOK, out)
}

// repoJSON is a repo forgerun can push to.
type repoJSON struct {
	FullName string `json:"full_name"`
	URL      string `json:"url"`
	Private  bool   `json:"private"`
}

// listRepos serves GET /api/repos: the repos GITHUB_TOKEN can push to, most
// recently pushed first.
func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	out := []repoJSON{}
	if s.prs == nil {
		writeData(w, http.StatusOK, out)
		return
	}
	repos, err := s.prs.Repos(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "listing your repos on GitHub: "+err.Error())
		return
	}
	for _, re := range repos {
		out = append(out, repoJSON{FullName: re.FullName, URL: re.URL, Private: re.Private})
	}
	writeData(w, http.StatusOK, out)
}
