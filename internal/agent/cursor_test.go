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
