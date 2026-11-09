package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSet(t *testing.T) {
	ctx := context.Background()
	s := NewSet(StaticCredentials{"CURSOR_API_KEY": "k"}, Builtin()...)
	if ids := s.IDs(); !slices.Equal(ids, []string{"claude-code", "codex", "cursor", "opencode", "pi"}) {
		t.Errorf("IDs = %v", ids)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("unknown agent: %v", err)
	}

	cursor, _ := s.Get("cursor")
	secrets, err := s.Secrets(ctx, cursor, "", false)
	if err != nil || len(secrets) != 1 || secrets[0].Value != "k" {
		t.Errorf("cursor secrets = %+v, %v", secrets, err)
	}
	// Claude Code is available, but its credential isn't configured: only
	// its tasks fail, with a message naming the variable to set.
	err = s.Check(ctx, "claude-code")
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "set CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("claude-code without a token: %v", err)
	}
	if err := s.Check(ctx, "cursor"); err != nil {
		t.Errorf("cursor: %v", err)
	}
	if err := NewSet(nil, &Cursor{}).Check(ctx, "cursor"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("no credential source: %v", err)
	}
}

// Environment credentials are read when needed, not once at startup, so a
// key set later is used by the next task.
func TestEnvCredentialsAreReadWhenNeeded(t *testing.T) {
	ctx := context.Background()
	s := NewSet(EnvCredentials{}, &ClaudeCode{})
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if err := s.Check(ctx, "claude-code"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("unset: %v", err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "later")
	a, _ := s.Get("claude-code")
	if secrets, err := s.Secrets(ctx, a, "", false); err != nil || secrets[0].Value != "later" {
		t.Errorf("after setting it: %+v, %v", secrets, err)
	}
}

// A model that runs without the agent's key (a free model) gets no error
// when the key is missing, and still gets the key when it's set: a later
// turn in the same VM may use a model that needs it.
func TestSecretsForKeylessModels(t *testing.T) {
	ctx := context.Background()
	oc := &OpenCode{}
	none := NewSet(StaticCredentials{}, oc)
	if secrets, err := none.Secrets(ctx, oc, "", true); err != nil || len(secrets) != 0 {
		t.Errorf("keyless, no key: %+v, %v", secrets, err)
	}
	if _, err := none.Secrets(ctx, oc, "", false); !errors.Is(err, ErrNoCredential) {
		t.Errorf("needs a key, no key: %v", err)
	}
	if _, err := NewSet(nil, oc).Secrets(ctx, oc, "", true); err != nil {
		t.Errorf("keyless, no credential source: %v", err)
	}
	keyed := NewSet(StaticCredentials{"OPENCODE_API_KEY": "k"}, oc)
	if secrets, err := keyed.Secrets(ctx, oc, "", true); err != nil || len(secrets) != 1 || secrets[0].Value != "k" {
		t.Errorf("keyless, key set: %+v, %v", secrets, err)
	}
}

// A Providers agent needs its model's provider's key; the others it has go
// along too, for a later turn on another provider.
func TestSecretsForProviders(t *testing.T) {
	ctx := context.Background()
	pi := &Pi{}
	s := NewSet(StaticCredentials{"OPENROUTER_API_KEY": "or"}, pi)

	secrets, err := s.Secrets(ctx, pi, "openrouter/openai/gpt-5.5", false)
	if err != nil || len(secrets) != 1 || secrets[0].EnvVar != "OPENROUTER_API_KEY" || secrets[0].Value != "or" {
		t.Errorf("openrouter model: %+v, %v", secrets, err)
	}
	_, err = s.Secrets(ctx, pi, "openai/gpt-5.5", false)
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "pi's openai models: set OPENAI_API_KEY") {
		t.Errorf("openai model without its key: %v", err)
	}
	if _, err := s.Secrets(ctx, pi, "nope/model", false); err == nil || errors.Is(err, ErrNoCredential) {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := s.Secrets(ctx, pi, "openai/gpt-5.5", true); err != nil {
		t.Errorf("keyless: %v", err)
	}

	// Check is about any key; CheckModel about the model's.
	if err := s.Check(ctx, "pi"); err != nil {
		t.Errorf("Check with one key: %v", err)
	}
	if err := s.CheckModel(ctx, "pi", "anthropic/claude-opus-5-5"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("CheckModel without its key: %v", err)
	}
	err = NewSet(StaticCredentials{}, pi).Check(ctx, "pi")
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY, OPENAI_API_KEY or OPENROUTER_API_KEY") {
		t.Errorf("no keys: %v", err)
	}
}
