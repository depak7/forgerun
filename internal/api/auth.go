package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/seal"
	"github.com/depak7/forgerun/internal/store"
)

// Auth is sign-in with GitHub. Without it (nil) the server is in local
// mode: nobody signs in, and every request acts as the admin.
type Auth struct {
	OAuth     *forge.OAuth
	GitHub    *forge.GitHub // reads the signed-in user's profile
	PublicURL string        // where browsers reach forgerun, e.g. https://forgerun.example.com
	Admins    []string      // GitHub logins that may see everything and change settings
	Key       seal.Key      // encrypts users' GitHub tokens at rest
}

const (
	loginCookie = "forgerun_login"
	stateCookie = "forgerun_oauth_state"
	loginFor    = 30 * 24 * time.Hour
)

// viewer is who a request acts for.
type viewer struct {
	ID    int64 // 0 in local mode
	Login string
	Admin bool
}

type viewerKey struct{}

func viewerOf(r *http.Request) viewer {
	v, _ := r.Context().Value(viewerKey{}).(viewer)
	return v
}

// owner is the owner filter for lists: the viewer's own tasks, or
// everyone's for an admin.
func (v viewer) owner() int64 {
	if v.Admin {
		return 0
	}
	return v.ID
}

// canSee reports whether the viewer may see a task.
func (v viewer) canSee(rec store.Record) bool { return v.Admin || (v.ID != 0 && rec.Owner == v.ID) }

// withViewer resolves who's signed in and guards the API: in sign-in mode,
// every /api/ request but /api/me needs a login, and requests that change
// something must come from forgerun's own pages.
func (s *Server) withViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerKey{}, viewer{Login: "local", Admin: true})))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "requests that change something must come from forgerun's own pages")
			return
		}
		v, ok := s.signedIn(r)
		if !ok && strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/me" {
			writeError(w, http.StatusUnauthorized, "sign in with GitHub first")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerKey{}, v)))
	})
}

// sameOrigin checks a browser's Origin (or, failing that, Referer) against
// forgerun's public address. Together with SameSite cookies this stops
// other sites from making a signed-in browser change things.
func (s *Server) sameOrigin(r *http.Request) bool {
	want, err := url.Parse(s.auth.PublicURL)
	if err != nil {
		return false
	}
	from := r.Header.Get("Origin")
	if from == "" {
		from = r.Header.Get("Referer")
	}
	if from == "" {
		return true // not a browser (curl, scripts): the cookie is what's checked
	}
	got, err := url.Parse(from)
	return err == nil && got.Scheme == want.Scheme && got.Host == want.Host
}

func (s *Server) signedIn(r *http.Request) (viewer, bool) {
	c, err := r.Cookie(loginCookie)
	if err != nil || c.Value == "" {
		return viewer{}, false
	}
	u, err := s.store.LoginUser(r.Context(), loginID(c.Value))
	if err != nil {
		if !errors.Is(err, store.ErrUserNotFound) {
			s.log.Error("reading a login", "err", err)
		}
		return viewer{}, false
	}
	return viewer{ID: u.ID, Login: u.Login, Admin: s.isAdmin(u.Login)}, true
}

func (s *Server) isAdmin(login string) bool {
	return slices.ContainsFunc(s.auth.Admins, func(a string) bool { return strings.EqualFold(a, login) })
}

// loginID is what the database keeps for a login cookie: its hash, so the
// table can't be used to sign in.
func loginID(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) callbackURL() string {
	return strings.TrimRight(s.auth.PublicURL, "/") + "/auth/github/callback"
}
func (s *Server) secure() bool { return strings.HasPrefix(s.auth.PublicURL, "https://") }

// githubLogin serves GET /auth/github/login: off to GitHub to approve.
func (s *Server) githubLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state := randomToken()
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/auth/", MaxAge: 600,
		HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.auth.OAuth.AuthorizeURL(s.callbackURL(), state), http.StatusFound)
}

// githubCallback serves GET /auth/github/callback: GitHub sends the browser
// back with a code, which becomes the user's token and a login.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	fail := func(msg string) { http.Redirect(w, r, "/?signin_error="+url.QueryEscape(msg), http.StatusFound) }
	if e := r.URL.Query().Get("error"); e != "" {
		fail("GitHub said: " + e)
		return
	}
	c, err := r.Cookie(stateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		fail("the sign-in expired or didn't start here; try again")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth/", MaxAge: -1})

	ctx := r.Context()
	tok, err := s.auth.OAuth.Exchange(ctx, r.URL.Query().Get("code"), s.callbackURL())
	if err != nil {
		s.log.Warn("GitHub sign-in failed", "err", err)
		fail("GitHub didn't accept the sign-in; try again")
		return
	}
	who, err := s.auth.GitHub.WhoAmI(ctx, tok)
	if err != nil {
		s.log.Warn("reading the signed-in GitHub user", "err", err)
		fail("couldn't read your GitHub profile; try again")
		return
	}
	sealed, err := s.auth.Key.Seal([]byte(tok.Reveal()))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.store.SaveUser(ctx, store.User{GitHubID: who.ID, Login: who.Login, Name: who.Name, AvatarURL: who.AvatarURL, Token: sealed})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cookie := randomToken()
	if err := s.store.CreateLogin(ctx, loginID(cookie), u.ID, time.Now().Add(loginFor)); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: cookie, Path: "/", MaxAge: int(loginFor.Seconds()),
		HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
	s.log.Info("signed in", "user", u.Login, "admin", s.isAdmin(u.Login))
	http.Redirect(w, r, "/", http.StatusFound)
}

// logout serves POST /auth/logout.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if c, err := r.Cookie(loginCookie); err == nil {
			if err := s.store.DeleteLogin(r.Context(), loginID(c.Value)); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
	}
	writeData(w, http.StatusOK, struct{}{})
}

// meJSON is who's signed in.
type meJSON struct {
	SignedIn  bool   `json:"signed_in"`
	SignIn    bool   `json:"sign_in"` // the server uses GitHub sign-in (false = local mode)
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Admin     bool   `json:"admin"`
}

// me serves GET /api/me. It answers 200 when signed out too, so the UI can
// show the sign-in page.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	if s.auth == nil {
		writeData(w, http.StatusOK, meJSON{SignedIn: true, Login: v.Login, Admin: true})
		return
	}
	if v.ID == 0 {
		writeData(w, http.StatusOK, meJSON{SignIn: true})
		return
	}
	u, err := s.store.GetUser(r.Context(), v.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, meJSON{SignedIn: true, SignIn: true, Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL, Admin: v.Admin})
}

// adminOnly wraps handlers only an admin may use.
func adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !viewerOf(r).Admin {
			writeError(w, http.StatusForbidden, "only an admin can see this")
			return
		}
		h(w, r)
	}
}

// userToken is the signed-in user's GitHub token, decrypted. In local mode
// it's the zero Token: the server's own GITHUB_TOKEN is used.
func (s *Server) userToken(r *http.Request) (forge.Token, error) {
	return s.tokenOf(r.Context(), viewerOf(r).ID)
}

// tokenOf is a user's GitHub token; the zero Token for user 0 (local mode).
func (s *Server) tokenOf(ctx context.Context, userID int64) (forge.Token, error) {
	return s.auth.Token(ctx, s.store, userID)
}

// Token is a user's GitHub token, decrypted. Without sign-in (a nil Auth),
// or for user 0, it's the zero Token: the server's own is used.
func (a *Auth) Token(ctx context.Context, st store.Store, userID int64) (forge.Token, error) {
	if a == nil || userID == 0 {
		return forge.Token{}, nil
	}
	u, err := st.GetUser(ctx, userID)
	if err != nil {
		return forge.Token{}, err
	}
	if u.Token == nil {
		return forge.Token{}, errors.New("no GitHub token on file: sign out and in again")
	}
	raw, err := a.Key.Open(u.Token)
	if err != nil {
		return forge.Token{}, err
	}
	return forge.NewToken(string(raw)), nil
}

// checkPush refuses a repo the user can't push to, in their terms.
func (s *Server) checkPush(w http.ResponseWriter, r *http.Request, repo string) bool {
	tok, err := s.userToken(r)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	acc, err := s.prs.Access(r.Context(), repo, tok)
	switch {
	case errors.Is(err, forge.ErrNoRepo):
		writeError(w, http.StatusBadRequest, "GitHub doesn't show you this repo: check the URL, or that it's shared with you")
		return false
	case err != nil:
		writeError(w, http.StatusBadGateway, "checking the repo on GitHub: "+err.Error())
		return false
	case !acc.Push:
		writeError(w, http.StatusBadRequest, "you can read this repo but not push to it, so forgerun couldn't push the changes; fork it and use your fork")
		return false
	}
	return true
}

// repoJSON is a repo the user can push to.
type repoJSON struct {
	FullName string `json:"full_name"`
	URL      string `json:"url"`
	Private  bool   `json:"private"`
}

// listRepos serves GET /api/repos: the repos the user can push to, most
// recently pushed first.
func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	out := []repoJSON{}
	if s.prs == nil {
		writeData(w, http.StatusOK, out)
		return
	}
	tok, err := s.userToken(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	repos, err := s.prs.Repos(r.Context(), tok)
	if err != nil {
		writeError(w, http.StatusBadGateway, "listing your repos on GitHub: "+err.Error())
		return
	}
	for _, re := range repos {
		out = append(out, repoJSON{FullName: re.FullName, URL: re.URL, Private: re.Private})
	}
	writeData(w, http.StatusOK, out)
}
