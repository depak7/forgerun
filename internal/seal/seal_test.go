package seal

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	k, err := ParseKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := k.Seal([]byte("gho_secret"))
	b, _ := k.Seal([]byte("gho_secret"))
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("gho_secret")) {
		t.Error("sealing must be randomized and hide the plaintext")
	}
	if got, err := k.Open(a); err != nil || string(got) != "gho_secret" {
		t.Errorf("Open = %q, %v", got, err)
	}
	a[len(a)-1] ^= 1
	if _, err := k.Open(a); err == nil {
		t.Error("a changed ciphertext must not open")
	}
	other, _ := ParseKey(strings.Repeat("cd", 32))
	if _, err := other.Open(b); err == nil {
		t.Error("another key must not open it")
	}
}

func TestParseKey(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("ab", 16)} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) should fail", bad)
		}
	}
	if _, err := (Key{}).Seal([]byte("x")); err == nil {
		t.Error("the zero Key must refuse")
	}
}
