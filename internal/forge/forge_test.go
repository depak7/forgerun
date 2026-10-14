package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const secret = "github_pat_SECRET123"

// The token must never show up in output, however it's printed or logged,
// including when it's buried in an unexported field of another struct.
func TestTokenIsRedacted(t *testing.T) {
	tok := NewToken(secret)
	type config struct {
		name  string
		token Token // unexported: fmt can't call Token.String here
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("cfg", "token", tok, "cfg", config{"x", tok})
	js, _ := json.Marshal(struct{ Token Token }{tok})

	outputs := map[string]string{
		"%s":          fmt.Sprintf("%s", tok),
		"%v":          fmt.Sprintf("%v", tok),
		"%+v":         fmt.Sprintf("%+v", tok),
		"%#v":         fmt.Sprintf("%#v", tok),
		"nested %+v":  fmt.Sprintf("%+v", config{"x", tok}),
		"nested %#v":  fmt.Sprintf("%#v", config{"x", tok}),
		"pointer %+v": fmt.Sprintf("%+v", &config{"x", tok}),
		"error":       fmt.Errorf("using %v", tok).Error(),
		"slog":        logged.String(),
		"json":        string(js),
	}
	for how, out := range outputs {
		if strings.Contains(out, secret) {
			t.Errorf("%s leaks the token: %s", how, out)
		}
	}
	if tok.Reveal() != secret {
		t.Errorf("Reveal = %q", tok.Reveal())
	}
	if !NewToken("").IsZero() || tok.IsZero() {
		t.Error("IsZero wrong")
	}
}

func TestParseRepo(t *testing.T) {
	good := map[string]Repo{
		"https://github.com/depak7/playground":        {"github.com", "depak7", "playground"},
		"https://github.com/depak7/playground.git":    {"github.com", "depak7", "playground"},
		" https://GitHub.com/depak7/playground.git/ ": {"github.com", "depak7", "playground"},
		"https://github.com/Owner/Repo.Name":          {"github.com", "Owner", "Repo.Name"},
	}
	for in, want := range good {
		got, err := ParseRepo(in)
		if err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://github.com/a/b",     // not https
		"git@github.com:a/b.git",    // ssh
		"https://github.com/a",      // no name
		"https://github.com/a/b/c",  // too deep
		"https://github.com//b",     // empty owner
		"https://github.com/a/.git", // empty name
		"",
	} {
		if _, err := ParseRepo(in); err == nil {
			t.Errorf("ParseRepo(%q) accepted", in)
		}
	}
	if _, err := ParseRepo("https://gitlab.com/a/b"); !errors.Is(err, ErrUnsupportedHost) {
		t.Errorf("gitlab: got %v, want ErrUnsupportedHost", err)
	}
	if s := (Repo{"github.com", "a", "b"}).String(); s != "github.com/a/b" {
		t.Errorf("String = %q", s)
	}
}

// fakeGitHub answers /user like GitHub does.
func fakeGitHub(t *testing.T, status int, body, expiry string) *GitHub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		if expiry != "" {
			w.Header().Set("GitHub-Authentication-Token-Expiration", expiry)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return &GitHub{BaseURL: srv.URL}
}

func TestWhoAmI(t *testing.T) {
	ctx := context.Background()
	id, err := fakeGitHub(t, 200, `{"login":"depak7"}`, "2026-10-21 07:52:48 UTC").WhoAmI(ctx, NewToken(secret))
	if err != nil || id.Login != "depak7" || !id.Expires.Equal(time.Date(2026, 10, 21, 7, 52, 48, 0, time.UTC)) {
		t.Errorf("WhoAmI = %+v, %v", id, err)
	}
	if id, err := fakeGitHub(t, 200, `{"login":"depak7"}`, "").WhoAmI(ctx, NewToken(secret)); err != nil || !id.Expires.IsZero() {
		t.Errorf("no expiry header: %+v, %v", id, err)
	}
	_, err = fakeGitHub(t, 200, `{"login":"x"}`, "").WhoAmI(ctx, NewToken("wrong"))
	if !errors.Is(err, ErrBadToken) || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("wrong token: %v", err)
	}
	if _, err := fakeGitHub(t, 500, `{"message":"boom"}`, "").WhoAmI(ctx, NewToken(secret)); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("server error: %v", err)
	}
}
