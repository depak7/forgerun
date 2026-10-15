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
	if ids := s.IDs(); !slices.Equal(ids, []string{"claude-code", "cursor"}) {
		t.Errorf("IDs = %v", ids)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("unknown agent: %v", err)
	}

	cursor, _ := s.Get("cursor")
	secrets, err := s.Secrets(ctx, cursor)
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
	if secrets, err := s.Secrets(ctx, a); err != nil || secrets[0].Value != "later" {
		t.Errorf("after setting it: %+v, %v", secrets, err)
	}
}
