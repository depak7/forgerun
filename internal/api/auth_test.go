package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/seal"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

// fakeGitHub plays github.com (OAuth) and api.github.com (/user): code
// "code-<login>" signs in as <login>.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	ids := map[string]int64{"alice": 101, "bob": 102, "depak7": 7}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			r.ParseForm()
			login := strings.TrimPrefix(r.PostForm.Get("code"), "code-")
			if r.PostForm.Get("client_secret") != "shh" || ids[login] == 0 {
				fmt.Fprint(w, `{"error":"bad_verification_code"}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"gho_%s","token_type":"bearer","scope":"repo,read:user"}`, login)
		case "/user":
			login := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer gho_")
			fmt.Fprintf(w, `{"id":%d,"login":%q,"name":"Name of %s","avatar_url":"https://a/%s.png"}`, ids[login], login, login, login)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const public = "https://forgerun.test"

func authSetup(t *testing.T, opts ...func(*Config)) (http.Handler, *memory.Store, seal.Key) {
	t.Helper()
	gh := fakeGitHub(t)
	key, _ := seal.ParseKey(strings.Repeat("ab", 32))
	h, s, _ := setup(t, append([]func(*Config){func(c *Config) {
		c.Auth = &Auth{OAuth: &forge.OAuth{ClientID: "id", ClientSecret: "shh", BaseURL: gh.URL}, GitHub: &forge.GitHub{BaseURL: gh.URL},
			PublicURL: public, Admins: []string{"DEPAK7"}, Key: key}
	}}, opts...)...)
	return h, s, key
}

// signIn goes through the sign-in as login and returns its cookie.
func signIn(t *testing.T, h http.Handler, login string) *http.Cookie {
	t.Helper()
	start := do(h, "GET", "/auth/github/login", "")
	if start.Code != http.StatusFound {
		t.Fatalf("login: %d", start.Code)
	}
	to, _ := url.Parse(start.Header().Get("Location"))
	if to.Query().Get("scope") != forge.Scopes || to.Query().Get("redirect_uri") != public+"/auth/github/callback" {
		t.Errorf("authorize URL = %s", to)
	}
	state := to.Query().Get("state")
	var stateCookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "forgerun_oauth_state" {
			stateCookie = c
		}
	}
	req := httptest.NewRequest("GET", "/auth/github/callback?code=code-"+login+"&state="+state, nil)
	req.AddCookie(stateCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d → %s", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "forgerun_login" && c.Value != "" {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
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
		req.Header.Set("Origin", public)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignInAndScoping(t *testing.T) {
	h, s, key := authSetup(t)

	// Signed out: the UI learns sign-in is needed; the API refuses.
	var me meJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/me", ""), http.StatusOK), &me)
	if me.SignedIn || !me.SignIn {
		t.Errorf("signed out: %+v", me)
	}
	if _, apiErr := decode(t, do(h, "GET", "/api/threads", ""), http.StatusUnauthorized); apiErr.Code != "unauthorized" {
		t.Errorf("signed out: code %q", apiErr.Code)
	}

	alice, bob, admin := signIn(t, h, "alice"), signIn(t, h, "bob"), signIn(t, h, "depak7")
	json.Unmarshal(mustData(t, as(h, alice, "GET", "/api/me", ""), http.StatusOK), &me)
	if !me.SignedIn || me.Login != "alice" || me.Admin || me.AvatarURL != "https://a/alice.png" {
		t.Errorf("alice: %+v", me)
	}
	json.Unmarshal(mustData(t, as(h, admin, "GET", "/api/me", ""), http.StatusOK), &me)
	if !me.Admin {
		t.Error("depak7 is an admin (case doesn't matter)")
	}

	// Her GitHub token is stored, encrypted.
	u, _ := s.GetUser(context.Background(), 1)
	if tok, err := key.Open(u.Token); err != nil || string(tok) != "gho_alice" || strings.Contains(string(u.Token), "gho_alice") {
		t.Errorf("stored token: %q, %v", tok, err)
	}

	// Each sees only their own threads; the admin sees all.
	body := `{"repo":"https://github.com/o/r","prompt":"mine"}`
	at := decodeTask(t, mustData(t, as(h, alice, "POST", "/api/tasks", body), http.StatusCreated))
	bt := decodeTask(t, mustData(t, as(h, bob, "POST", "/api/tasks", body), http.StatusCreated))
	count := func(c *http.Cookie) int {
		var threads []threadJSON
		json.Unmarshal(mustData(t, as(h, c, "GET", "/api/threads", ""), http.StatusOK), &threads)
		return len(threads)
	}
	if count(alice) != 1 || count(bob) != 1 || count(admin) != 2 {
		t.Errorf("threads: alice %d, bob %d, admin %d", count(alice), count(bob), count(admin))
	}
	// Someone else's task is "not found", for reading and for acting on it.
	decode(t, as(h, bob, "GET", "/api/tasks/"+at.ID, ""), http.StatusNotFound)
	decode(t, as(h, bob, "POST", "/api/tasks/"+at.ID+"/cancel", ""), http.StatusNotFound)
	decode(t, as(h, bob, "GET", "/api/threads/"+at.ID+"/session", ""), http.StatusNotFound)
	mustData(t, as(h, admin, "GET", "/api/tasks/"+bt.ID, ""), http.StatusOK)
	// Settings data is the admin's.
	decode(t, as(h, alice, "GET", "/api/snapshots", ""), http.StatusForbidden)
	mustData(t, as(h, admin, "GET", "/api/snapshots", ""), http.StatusOK)

	// Another site can't make her browser act.
	req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(body))
	req.AddCookie(alice)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site post: %d", rec.Code)
	}

	// Signing out ends that login only.
	mustData(t, as(h, alice, "POST", "/auth/logout", ""), http.StatusOK)
	decode(t, as(h, alice, "GET", "/api/threads", ""), http.StatusUnauthorized)
	mustData(t, as(h, bob, "GET", "/api/threads", ""), http.StatusOK)
}

func TestSignInRefusals(t *testing.T) {
	h, _, _ := authSetup(t)
	// No state cookie (the sign-in didn't start here), or a bad code.
	rec := do(h, "GET", "/auth/github/callback?code=code-alice&state=x", "")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "signin_error") {
		t.Errorf("forged callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	start := do(h, "GET", "/auth/github/login", "")
	to, _ := url.Parse(start.Header().Get("Location"))
	req := httptest.NewRequest("GET", "/auth/github/callback?code=code-mallory&state="+to.Query().Get("state"), nil)
	for _, c := range start.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Location"), "signin_error") {
		t.Errorf("bad code: %s", rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "forgerun_login" && c.Value != "" {
			t.Error("a failed sign-in set a login cookie")
		}
	}
}

// Without sign-in settings nothing changes: no login, everyone is the admin.
func TestLocalMode(t *testing.T) {
	h, _, _ := setup(t)
	var me meJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/me", ""), http.StatusOK), &me)
	if !me.SignedIn || !me.Admin || me.SignIn {
		t.Errorf("local mode: %+v", me)
	}
	mustData(t, do(h, "GET", "/api/snapshots", ""), http.StatusOK)
}

// Signed-in users act on GitHub with their own token: repo checks, the
// repo list, pull request checks and pushes.
func TestUserTokensReachGitHub(t *testing.T) {
	var tokens []string
	var published []task.PublishRequest
	prs := fakePRs{open: true, tokens: &tokens, got: &published, pub: task.Published{Branch: "forgerun/x"}}
	h, s, _ := authSetup(t, func(c *Config) { c.PRs = prs })
	alice := signIn(t, h, "alice")

	var repos []repoJSON
	json.Unmarshal(mustData(t, as(h, alice, "GET", "/api/repos", ""), http.StatusOK), &repos)
	if len(repos) != 1 || repos[0].FullName != "o/r" || !repos[0].Private {
		t.Errorf("repos = %+v", repos)
	}
	tk := decodeTask(t, mustData(t, as(h, alice, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"x"}`), http.StatusCreated))
	s.Start(context.Background(), tk.ID)
	s.Finish(context.Background(), tk.ID, store.Outcome{State: task.Succeeded, Diff: "+a\n", BaseCommit: "b"})
	mustData(t, as(h, alice, "POST", "/api/threads/"+tk.ID+"/push", `{}`), http.StatusOK)
	if len(published) != 1 || published[0].Token.Reveal() != "gho_alice" {
		t.Errorf("push token = %q", published[0].Token.Reveal())
	}
	for _, tok := range tokens {
		if tok != "gho_alice" {
			t.Errorf("GitHub was called with %q, want alice's token", tok)
		}
	}
	if len(tokens) < 2 {
		t.Errorf("GitHub calls: %v", tokens)
	}
}

func TestRepoAccessRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		prs  fakePRs
		want string
	}{
		{"read only", fakePRs{readOnly: true}, "not push to it"},
		{"not visible", fakePRs{noRepo: true}, "doesn't show you this repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := authSetup(t, func(c *Config) { c.PRs = tc.prs })
			bob := signIn(t, h, "bob")
			_, apiErr := decode(t, as(h, bob, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"x"}`), http.StatusBadRequest)
			if !strings.Contains(apiErr.Message, tc.want) {
				t.Errorf("got %q", apiErr.Message)
			}
		})
	}
}
