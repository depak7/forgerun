// Package forge talks to git hosting services ("forges": GitHub now, others
// later) on behalf of forgerun's server. It runs on the host only: tokens
// never enter a VM.
package forge

import (
	"context"
	"log/slog"
)

// Token is a secret credential that can't be printed or logged by accident:
// fmt, slog and JSON all show "[redacted]". Use Reveal only where the value
// is sent to the forge.
type Token struct {
	// A pointer, not a string: if a Token sits in an unexported field of
	// another struct, fmt can't call its String method there and prints
	// the raw fields instead. A pointer prints as an address, not the value.
	v *string
}

const redacted = "[redacted]"

// NewToken wraps a secret value. An empty value gives the zero Token.
func NewToken(value string) Token {
	if value == "" {
		return Token{}
	}
	return Token{v: &value}
}

// Reveal returns the secret. Call it only to send the token to the forge.
func (t Token) Reveal() string {
	if t.v == nil {
		return ""
	}
	return *t.v
}

// IsZero reports whether there's no token.
func (t Token) IsZero() bool { return t.v == nil }

func (t Token) String() string               { return redacted } // fmt %s %v %+v
func (t Token) GoString() string             { return redacted } // fmt %#v
func (t Token) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (t Token) MarshalText() ([]byte, error) { return []byte(redacted), nil } // JSON and friends

// TokenSource gives a token allowed to push branches to repo and open pull
// requests on it. A personal access token is the same for every repo; a
// GitHub App (later) mints a short-lived token per installation.
type TokenSource interface {
	Token(ctx context.Context, repo Repo) (Token, error)
}

// StaticToken is a TokenSource that always returns one token, e.g. a
// personal access token from the environment.
type StaticToken struct{ token Token }

// NewStaticToken returns a TokenSource for a fixed token.
func NewStaticToken(t Token) StaticToken { return StaticToken{token: t} }

func (s StaticToken) Token(context.Context, Repo) (Token, error) { return s.token, nil }
