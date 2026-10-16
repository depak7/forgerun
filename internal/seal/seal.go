// Package seal encrypts small secrets (users' GitHub tokens) before they
// are stored, with AES-256-GCM and a key the server is given.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Key encrypts and decrypts. The zero Key is unusable.
type Key struct{ aead cipher.AEAD }

// ParseKey reads a 32-byte key written as 64 hex characters (openssl rand
// -hex 32) or as base64.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	raw, err := hex.DecodeString(s)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(s)
	}
	if err != nil || len(raw) != 32 {
		return Key{}, errors.New("the key must be 32 bytes, as 64 hex characters (openssl rand -hex 32) or base64")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return Key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Key{}, err
	}
	return Key{aead: aead}, nil
}

// Seal encrypts plaintext. Each call uses a fresh random nonce, stored in
// front of the ciphertext.
func (k Key) Seal(plaintext []byte) ([]byte, error) {
	if k.aead == nil {
		return nil, errors.New("seal: no key")
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts what Seal produced. It fails if the data was changed or
// sealed with another key.
func (k Key) Open(sealed []byte) ([]byte, error) {
	if k.aead == nil {
		return nil, errors.New("seal: no key")
	}
	n := k.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("seal: data too short")
	}
	out, err := k.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("seal: can't decrypt (wrong key, or the data was changed): %w", err)
	}
	return out, nil
}
