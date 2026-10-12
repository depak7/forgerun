package agent

import (
	"slices"
	"testing"
)

func TestNewCursorRequiresKey(t *testing.T) {
	if _, err := NewCursor(""); err == nil {
		t.Fatal("expected error for empty API key")
	}
	c, err := NewCursor("k")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID() != "cursor" {
		t.Errorf("ID() = %q", c.ID())
	}
}

func TestCursorKeyGoesThroughSecrets(t *testing.T) {
	c, _ := NewCursor("k")
	s := c.Secrets()
	if len(s) != 1 || s[0].EnvVar != "CURSOR_API_KEY" || s[0].Value != "k" {
		t.Fatalf("Secrets() = %+v, want one CURSOR_API_KEY entry", s)
	}
	if len(s[0].AllowHosts) == 0 {
		t.Error("CURSOR_API_KEY has no allowed hosts; the proxy would never send it")
	}
}

func TestCursorArgs(t *testing.T) {
	base := []string{"-p", "fix it", "--force", "--trust", "--output-format", "text"}
	tests := []struct {
		modelID string
		want    []string
	}{
		{"", base},
		{"auto", base},
		{"claude-opus-5-5-high-fast", append(slices.Clone(base), "--model", "claude-opus-5-5-high-fast")},
	}
	for _, tc := range tests {
		got := cursorArgs(Request{Prompt: "fix it", ModelID: tc.modelID})
		if !slices.Equal(got, tc.want) {
			t.Errorf("model %q: got %v, want %v", tc.modelID, got, tc.want)
		}
	}
}
