package agent

import "testing"

func TestNewCursorRequiresKey(t *testing.T) {
	if _, err := NewCursor(""); err == nil {
		t.Fatal("expected error for empty API key")
	}
	c, err := NewCursor("k")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "cursor" {
		t.Errorf("Name() = %q", c.Name())
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
