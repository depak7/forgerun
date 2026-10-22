package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
)

// disabledStore reports every agent as disabled.
type disabledStore struct{ store.Store }

func (d disabledStore) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	a, err := d.Store.GetAgent(ctx, id)
	a.Enabled = false
	return a, err
}

func TestCheckAgent(t *testing.T) {
	ctx := context.Background()
	known := memory.New(models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", IsDefault: true, AgentDefault: true})

	if err := checkAgent(ctx, known, "cursor"); err != nil {
		t.Errorf("known agent: %v", err)
	}
	if err := checkAgent(ctx, memory.New(), "cursor"); err == nil || !strings.Contains(err.Error(), "not in the agents table") {
		t.Errorf("missing agent: got %v", err)
	}
	if err := checkAgent(ctx, disabledStore{known}, "cursor"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled agent: got %v", err)
	}
}

func TestCheckModels(t *testing.T) {
	ctx := context.Background()
	cursor := memory.New(
		models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", IsDefault: true, AgentDefault: true, ZDR: true},
		models.Variant{ID: "opus-medium", AgentID: "cursor", Family: "opus", Effort: "medium", IsDefault: true, ZDR: true},
		models.Variant{ID: "fable-high", AgentID: "cursor", Family: "fable", Effort: "high", IsDefault: true},
	)
	tests := []struct {
		name         string
		db           store.Store
		defaultModel string
		requireZDR   bool
		want         string // effective default; "" means startup must fail
		wantErr      string
	}{
		{"agent default", cursor, "", false, "auto", ""},
		{"server default", cursor, "opus", false, "opus-medium", ""},
		{"non-ZDR default allowed", cursor, "fable", false, "fable-high", ""},
		{"typo in -model", cursor, "opsu", false, "", `-model "opsu"`},
		{"-require-zdr removes the chosen default", cursor, "fable", true, "", `-model "fable"`},
		{"agent without a default", memory.New(models.Variant{ID: "x", AgentID: "cursor", Family: "x", IsDefault: true}), "", false, "", "no usable default model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := checkModels(ctx, tc.db, "cursor", tc.defaultModel, tc.requireZDR)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("got %q, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGitHubTokens(t *testing.T) {
	const secret = "github_pat_SECRET123"
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	fake := func(expiry string) *forge.GitHub {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"Bad credentials"}`)
				return
			}
			w.Header().Set("GitHub-Authentication-Token-Expiration", expiry)
			fmt.Fprint(w, `{"login":"depak7"}`)
		}))
		t.Cleanup(srv.Close)
		return &forge.GitHub{BaseURL: srv.URL}
	}
	tests := []struct {
		name, value, expiry string
		wantSource, wantErr bool
		wantLog             []string
	}{
		{"no token stops startup", "", "", false, true, nil},
		{"valid token", secret, "2026-10-21 07:52:48 UTC", true, false, []string{"as=depak7", "token_expires=2026-10-21"}},
		{"expires soon", secret, "2026-10-15 07:52:48 UTC", true, false, []string{"level=WARN", "expires soon"}},
		{"rejected token stops startup", "github_pat_WRONG", "", false, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logged, nil))
			src, login, err := githubTokens(context.Background(), tc.value, fake(tc.expiry), now, log)
			if src != nil && login != "depak7" {
				t.Errorf("login = %q, want depak7", login)
			}
			if (err != nil) != tc.wantErr || (src != nil) != tc.wantSource {
				t.Fatalf("source %v, err %v", src, err)
			}
			for _, w := range tc.wantLog {
				if !strings.Contains(logged.String(), w) {
					t.Errorf("log lacks %q:\n%s", w, logged.String())
				}
			}
			if strings.Contains(logged.String(), secret) || (err != nil && strings.Contains(err.Error(), "WRONG")) {
				t.Error("token leaked into the log or error")
			}
			if src != nil {
				tok, _ := src.Token(context.Background(), forge.Repo{})
				if tok.Reveal() != secret {
					t.Error("source returns the wrong token")
				}
			}
		})
	}
}

func TestSignInSettings(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if a, err := signIn(env(nil), log); a != nil || err != nil {
		t.Errorf("no client ID: local mode, got %v, %v", a, err)
	}
	full := map[string]string{"GITHUB_OAUTH_CLIENT_ID": "id", "GITHUB_OAUTH_CLIENT_SECRET": "s",
		"FORGERUN_SECRET_KEY": strings.Repeat("ab", 32), "FORGERUN_ADMINS": " depak7 , other ", "FORGERUN_PUBLIC_URL": "https://f.example/"}
	a, err := signIn(env(full), log)
	if err != nil || a.PublicURL != "https://f.example" || len(a.Admins) != 2 || a.Admins[0] != "depak7" {
		t.Errorf("full settings: %+v, %v", a, err)
	}
	for _, missing := range []string{"GITHUB_OAUTH_CLIENT_SECRET", "FORGERUN_SECRET_KEY"} {
		kv := maps.Clone(full)
		delete(kv, missing)
		if _, err := signIn(env(kv), log); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: %v", missing, err)
		}
	}
	kv := maps.Clone(full)
	kv["FORGERUN_PUBLIC_URL"] = "forgerun.example"
	if _, err := signIn(env(kv), log); err == nil {
		t.Error("a public URL without a scheme should be refused")
	}
}

// Without -addr, serve stays on this machine, unless the host assigns a
// port (PORT): then it listens on every interface.
func TestDefaultAddr(t *testing.T) {
	env := func(port string) func(string) string {
		return func(k string) string {
			if k == "PORT" {
				return port
			}
			return ""
		}
	}
	if got := defaultAddr(env("")); got != "127.0.0.1:8080" {
		t.Errorf("no PORT: %q", got)
	}
	if got := defaultAddr(env("31337")); got != "0.0.0.0:31337" {
		t.Errorf("PORT=31337: %q", got)
	}
}
